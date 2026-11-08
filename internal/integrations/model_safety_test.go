package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestModelAuthorizationCancellationPreservesContextError(t *testing.T) {
	for _, mode := range []string{"cancelled", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			m, actor, goal, calls := modelFixture(t, true)
			// Hold the sole connection so authorization finishes only after the
			// caller's context ends. No provider or paid model is contacted.
			tx, err := m.Service.Store.DB.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			ctx, cancel := context.WithCancel(context.Background())
			if mode == "deadline" {
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			}
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, e := m.Chat(ctx, actor, p.M{"goalId": goal.ID, "messages": []any{p.M{"role": "user", "content": "hello"}}})
				result <- e
			}()
			if mode == "cancelled" {
				cancel()
			}
			<-ctx.Done()
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err = <-result; !errors.Is(err, ctx.Err()) {
				t.Fatal("authorization hid cancellation as an access failure", mode, err)
			}
			charges, err := m.Service.Store.List(goal.ProjectID, "charge", goal.ID)
			if err != nil || *calls != 0 || len(charges) != 0 {
				t.Fatal("cancelled authorization reserved budget/contacted a provider", err, *calls, charges)
			}
		})
	}
}

func TestModelPlanCannotRebindToRequirementChangedDuringGeneration(t *testing.T) {
	m, actor, goal, _ := modelFixture(t, true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := m.Service.Command(r.Context(), actor, "goals.revise", p.M{"id": goal.ID, "reason": "Business baseline changed while planning", "outcome": "Deliver the revised booking workflow"}); err != nil {
			t.Error(err)
		}
		if _, err := m.Service.Command(r.Context(), actor, "goals.approve", p.M{"id": goal.ID}); err != nil {
			t.Error(err)
		}
		plan := `{"summary":"Plan for the old portal requirement","tasks":[{"id":"analysis","title":"Analyze old portal","kind":"analysis","role":"analysis","runtime":"linux"}]}`
		_ = json.NewEncoder(w).Encode(p.M{"id": "chatcmpl-stale-plan", "model": "test-model", "choices": []any{p.M{"finish_reason": "stop", "message": p.M{"content": plan}}}, "usage": p.M{"prompt_tokens": 10, "completion_tokens": 30}})
	}))
	defer server.Close()
	m.BaseURL, m.Client = server.URL, server.Client()
	_, err := m.GeneratePlan(context.Background(), actor, planInput(goal, "old-context"))
	var fault *p.Fault
	if !errors.As(err, &fault) || fault.Code != "conflict" {
		t.Fatalf("old model plan rebound to a different approved requirement: %v", err)
	}
	plans, err := m.Service.Store.List(goal.ProjectID, "plan", goal.ID)
	if err != nil || len(plans) != 0 {
		t.Fatal("stale plan was persisted", err, plans)
	}
	charges, _ := m.Service.Store.List(goal.ProjectID, "charge", goal.ID)
	if len(charges) != 1 || charges[0].Status != "settled" {
		t.Fatal("rejecting a stale plan erased actual model usage", charges)
	}
}

func TestModelRejectsNonfiniteConfiguredPrices(t *testing.T) {
	for _, price := range []string{"NaN", "+Inf", "-Inf", "1e300"} {
		t.Run(price, func(t *testing.T) {
			t.Setenv("IS_MODEL_BASE_URL", "https://models.example.test/v1")
			t.Setenv("IS_MODEL_NAME", "test-model")
			t.Setenv("IS_MODEL_INPUT_USD_PER_MILLION", price)
			t.Setenv("IS_MODEL_OUTPUT_USD_PER_MILLION", "1")
			if ModelFromEnv(nil) != nil {
				t.Fatal("unbounded price enabled a model integration")
			}
		})
	}
}

func TestModelConfiguredDefaultOutputCapControlsPlanning(t *testing.T) {
	m, actor, goal, _ := modelFixture(t, true)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input p.M
		_ = json.NewDecoder(r.Body).Decode(&input)
		if p.N(input, "max_tokens") != 8192 {
			t.Errorf("planner ignored configured output budget: %v", input["max_tokens"])
		}
		plan := `{"summary":"Configured plan","tasks":[{"id":"analysis","title":"Analyze goal","kind":"analysis","role":"analysis","runtime":"linux"}]}`
		_ = json.NewEncoder(w).Encode(p.M{"choices": []any{p.M{"finish_reason": "stop", "message": p.M{"content": plan}}}, "usage": p.M{"prompt_tokens": 10, "completion_tokens": 30}})
	}))
	defer server.Close()
	for key, value := range map[string]string{"IS_MODEL_BASE_URL": server.URL, "IS_MODEL_NAME": "test", "IS_MODEL_INPUT_USD_PER_MILLION": "1", "IS_MODEL_OUTPUT_USD_PER_MILLION": "1", "IS_MODEL_DEFAULT_MAX_TOKENS": "8192"} {
		t.Setenv(key, value)
	}
	configured := ModelFromEnv(m.Service)
	if configured == nil {
		t.Fatal("valid configured output cap rejected")
	}
	configured.Client = server.Client()
	if _, err := configured.GeneratePlan(context.Background(), actor, planInput(goal, "configured-cap")); err != nil {
		t.Fatal(err)
	}
	charges, _ := m.Service.Store.List(goal.ProjectID, "charge", goal.ID)
	if len(charges) != 1 || p.N(charges[0].Data, "outputBound") != 8192 {
		t.Fatal("configured output cap was not budgeted", charges)
	}
}

func TestModelRejectsInvalidConfiguredDefaultOutputCap(t *testing.T) {
	for _, value := range []string{"0", "-1", "8193", "1.5", "NaN", "invalid"} {
		t.Run(value, func(t *testing.T) {
			for key, setting := range map[string]string{"IS_MODEL_BASE_URL": "https://models.example.test/v1", "IS_MODEL_NAME": "test", "IS_MODEL_INPUT_USD_PER_MILLION": "1", "IS_MODEL_OUTPUT_USD_PER_MILLION": "1", "IS_MODEL_DEFAULT_MAX_TOKENS": value} {
				t.Setenv(key, setting)
			}
			if ModelFromEnv(nil) != nil {
				t.Fatal("invalid default output cap enabled model access")
			}
		})
	}
}

func TestModelOperatorOptionsReachProviderWithoutOverridingBudget(t *testing.T) {
	m, actor, goal, _ := modelFixture(t, true)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input p.M
		_ = json.NewDecoder(r.Body).Decode(&input)
		if p.S(p.Obj(input["thinking"]), "type") != "disabled" || p.N(input, "max_tokens") != 32 {
			t.Errorf("operator options missing or changed requested budget: %v", input)
		}
		_ = json.NewEncoder(w).Encode(p.M{"choices": []any{p.M{"finish_reason": "stop", "message": p.M{"content": "ok"}}}, "usage": p.M{"prompt_tokens": 1, "completion_tokens": 1}})
	}))
	defer server.Close()
	for key, value := range map[string]string{"IS_MODEL_BASE_URL": server.URL, "IS_MODEL_NAME": "test", "IS_MODEL_INPUT_USD_PER_MILLION": "1", "IS_MODEL_OUTPUT_USD_PER_MILLION": "1", "IS_MODEL_OPTIONS_JSON": `{"thinking":{"type":"disabled"}}`} {
		t.Setenv(key, value)
	}
	configured := ModelFromEnv(m.Service)
	if configured == nil {
		t.Fatal("valid operator options rejected")
	}
	configured.Client = server.Client()
	if _, err := configured.Chat(context.Background(), actor, p.M{"goalId": goal.ID, "max_tokens": 32, "messages": []any{p.M{"role": "user", "content": "hello"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestModelOptionsCannotReplaceAuthorityOrTokenBudget(t *testing.T) {
	for _, value := range []string{`{"max_tokens":1000000}`, `{"model":"other"}`, `{"messages":[]}`, `{"stream":true}`, `{"thinking":{"type":"disabled","secret":"value"}}`, `{"thinking":"disabled"}`, `null`, `[]`, `{"reasoning_effort":42}`} {
		t.Run(value, func(t *testing.T) {
			for key, setting := range map[string]string{"IS_MODEL_BASE_URL": "https://models.example.test/v1", "IS_MODEL_NAME": "test", "IS_MODEL_INPUT_USD_PER_MILLION": "1", "IS_MODEL_OUTPUT_USD_PER_MILLION": "1", "IS_MODEL_OPTIONS_JSON": value} {
				t.Setenv(key, setting)
			}
			if ModelFromEnv(nil) != nil {
				t.Fatal("operator options replaced controlled request fields")
			}
		})
	}
}

func TestModelRejectsInvalidOutputLimitBeforeReservation(t *testing.T) {
	for _, limit := range []any{0, -1, 1.5, "32", nil, math.NaN(), math.Inf(1)} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			m, a, goal, calls := modelFixture(t, true)
			if _, err := m.Chat(context.Background(), a, p.M{"goalId": goal.ID, "messages": []any{p.M{"role": "user", "content": "hello"}}, "max_tokens": limit}); err == nil {
				t.Fatal("invalid output cap reached the provider")
			}
			charges, _ := m.Service.Store.List(goal.ProjectID, "charge", goal.ID)
			if *calls != 0 || len(charges) != 0 {
				t.Fatal("invalid output cap created a charge or network request")
			}
		})
	}
}

func TestModelIncompleteOutputCannotBecomeAgentAction(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		choice p.M
	}{
		{"truncated", p.M{"finish_reason": "length", "message": p.M{"content": `{"action":"finish","summary":"not complete"}`}}},
		{"filtered", p.M{"finish_reason": "content_filter", "message": p.M{"content": `{"action":"finish"}`}}},
		{"refused", p.M{"finish_reason": "stop", "message": p.M{"content": `{"action":"finish"}`, "refusal": "Cannot comply"}}},
		{"empty", p.M{"finish_reason": "stop", "message": p.M{"content": ""}}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			m, a, goal, _ := modelFixture(t, true)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(p.M{"choices": []any{scenario.choice}, "usage": p.M{"prompt_tokens": 10, "completion_tokens": 16}})
			}))
			defer server.Close()
			m.BaseURL, m.Client = server.URL, server.Client()
			out, err := m.Chat(context.Background(), a, p.M{"goalId": goal.ID, "messages": []any{p.M{"role": "user", "content": "hello"}}, "max_tokens": 32})
			if err == nil || out != nil {
				t.Fatal("incomplete/refused content was returned to the agent")
			}
			charges, _ := m.Service.Store.List(goal.ProjectID, "charge", goal.ID)
			if len(charges) != 1 || charges[0].Status != "settled" || p.N(charges[0].Data, "chargedCents") != 1 {
				t.Fatal("known provider usage was lost on incomplete output", charges)
			}
		})
	}
}

func TestModelUnknownUsageBlocksOutputAndKeepsReservation(t *testing.T) {
	for _, usage := range []p.M{{}, {"prompt_tokens": -1, "completion_tokens": 1}, {"prompt_tokens": 1.5, "completion_tokens": 1}, {"prompt_tokens": 10, "completion_tokens": 33}} {
		m, a, goal, _ := modelFixture(t, true)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(p.M{"choices": []any{p.M{"finish_reason": "stop", "message": p.M{"content": `{"action":"finish"}`}}}, "usage": usage})
		}))
		m.BaseURL, m.Client = server.URL, server.Client()
		out, err := m.Chat(context.Background(), a, p.M{"goalId": goal.ID, "messages": []any{p.M{"role": "user", "content": "hello"}}, "max_tokens": 32})
		server.Close()
		if err == nil || out != nil {
			t.Fatal("unverified usage was returned to the agent")
		}
		charges, _ := m.Service.Store.List(goal.ProjectID, "charge", goal.ID)
		for _, charge := range charges {
			if charge.Status != "uncertain" || p.N(charge.Data, "chargedCents") != p.N(charge.Data, "reservedCents") {
				t.Fatal("unknown usage released reserved budget", charge)
			}
		}
	}
}

func TestModelPlanSeesOnlyCurrentProjectContext(t *testing.T) {
	m, a, goal, _ := modelFixture(t, true)
	ctx := context.Background()
	command := func(name string, input p.M) p.Resource {
		t.Helper()
		out, err := m.Service.Command(ctx, a, name, input)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	repository := command("repositories.create", p.M{"projectId": goal.ProjectID, "title": "current-repository"})
	command("knowledge.create", p.M{"projectId": goal.ProjectID, "title": "Approved build recipe", "content": "current-project-build-recipe", "source": "local:recipe-v1"})
	command("workers.register", p.M{"projectId": goal.ProjectID, "title": "Linux developer", "runtime": "linux", "roles": []any{"developer"}, "capabilities": []any{"code", "build"}})
	other := command("projects.create", p.M{"title": "Other project"})
	command("knowledge.create", p.M{"projectId": other.ID, "title": "Foreign knowledge", "content": "foreign-project-information", "source": "local:private"})
	sawContext := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in p.M
		_ = json.NewDecoder(r.Body).Decode(&in)
		body, _ := json.Marshal(in["messages"])
		sawContext = strings.Contains(string(body), repository.ID) && strings.Contains(string(body), "current-project-build-recipe") && strings.Contains(string(body), "Linux developer")
		if strings.Contains(string(body), "foreign-project-information") {
			t.Error("planner received another project's context")
		}
		plan := `{"summary":"Current project plan","risk":"Requires independent validation","tasks":[{"id":"analysis","title":"Analyze current goal","kind":"analysis","role":"analysis","runtime":"linux"}]}`
		_ = json.NewEncoder(w).Encode(p.M{"id": "chatcmpl-plan", "model": "test-model", "choices": []any{p.M{"finish_reason": "stop", "message": p.M{"content": plan}}}, "usage": p.M{"prompt_tokens": 10, "completion_tokens": 30}})
	}))
	defer server.Close()
	m.BaseURL, m.Client = server.URL, server.Client()
	plan, err := m.GeneratePlan(ctx, a, planInput(goal, "project-context"))
	if err != nil {
		t.Fatal(err)
	}
	if !sawContext {
		t.Fatal("planner could not discover current repositories, knowledge and workers")
	}
	if plan.Status != "proposed" {
		t.Fatal("model plan bypassed human approval", plan.Status)
	}
}

func TestModelChargeRetainsProviderProvenance(t *testing.T) {
	m, a, goal, _ := modelFixture(t, true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(p.M{"id": "chatcmpl-proof", "model": "actual-model-version", "choices": []any{p.M{"finish_reason": "stop", "message": p.M{"content": "ok"}}}, "usage": p.M{"prompt_tokens": 20, "completion_tokens": 30}})
	}))
	defer server.Close()
	m.BaseURL, m.Client, m.InputUSD, m.OutputUSD = server.URL, server.Client(), 1000, 2000
	if _, err := m.Chat(context.Background(), a, p.M{"goalId": goal.ID, "messages": []any{p.M{"role": "user", "content": "hello"}}, "max_tokens": 64}); err != nil {
		t.Fatal(err)
	}
	charges, _ := m.Service.Store.List(goal.ProjectID, "charge", goal.ID)
	if len(charges) != 1 || p.S(charges[0].Data, "providerResponseId") != "chatcmpl-proof" || p.S(charges[0].Data, "providerModel") != "actual-model-version" || p.N(charges[0].Data, "chargedCents") != 8 || p.S(charges[0].Data, "outcome") != "completed" {
		t.Fatal("charge lacks actual provider binding or accurate cost", charges)
	}
}

func TestModelOversizeBodyCannotHideBehindWhitespace(t *testing.T) {
	m, a, goal, _ := modelFixture(t, true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}` + strings.Repeat(" ", 2<<20)))
	}))
	defer server.Close()
	m.BaseURL, m.Client = server.URL, server.Client()
	if out, err := m.Chat(context.Background(), a, p.M{"goalId": goal.ID, "messages": []any{p.M{"role": "user", "content": "hello"}}}); err == nil || out != nil {
		t.Fatal("oversized response was accepted after silent truncation")
	}
}

func TestModelAdmissionPersistsAcrossInstancesAndCancellationDoesNotCharge(t *testing.T) {
	m, actor, goal, _ := modelFixture(t, true)
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(p.M{"id": "chatcmpl-admission", "model": "test", "choices": []any{p.M{"finish_reason": "stop", "message": p.M{"content": "ok"}}}, "usage": p.M{"prompt_tokens": 1, "completion_tokens": 1}})
	}))
	defer server.Close()
	for key, value := range map[string]string{"IS_MODEL_BASE_URL": server.URL, "IS_MODEL_NAME": "test", "IS_MODEL_INPUT_USD_PER_MILLION": "1", "IS_MODEL_OUTPUT_USD_PER_MILLION": "1", "IS_MODEL_MIN_INTERVAL_SECONDS": "0.2"} {
		t.Setenv(key, value)
	}
	first := ModelFromEnv(m.Service)
	if first == nil {
		t.Fatal("valid explicit admission policy rejected")
	}
	first.Client = server.Client()
	input := p.M{"goalId": goal.ID, "messages": []any{p.M{"role": "user", "content": "hello"}}}
	if _, err := first.Chat(context.Background(), actor, input); err != nil {
		t.Fatal(err)
	}
	otherStore, err := p.Open(m.Service.Store.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer otherStore.Close()
	second := ModelFromEnv(p.NewService(otherStore))
	second.Client = server.Client()
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = second.Chat(short, actor, input); err == nil {
		t.Fatal("new model instance ignored durable provider admission")
	}
	charges, _ := m.Service.Store.List(goal.ProjectID, "charge", goal.ID)
	if calls != 1 || len(charges) != 1 {
		t.Fatal("cancelled queued call contacted provider or reserved budget")
	}
	if _, err = second.Chat(context.Background(), actor, input); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("admitted call did not reach provider")
	}
}

func TestModelQueuedAdmissionRechecksPolicyAndRequirement(t *testing.T) {
	for _, change := range []string{"policy", "requirement"} {
		t.Run(change, func(t *testing.T) {
			m, actor, goal, _ := modelFixture(t, true)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_ = json.NewEncoder(w).Encode(p.M{"choices": []any{p.M{"finish_reason": "stop", "message": p.M{"content": "ok"}}}, "usage": p.M{"prompt_tokens": 1, "completion_tokens": 1}})
			}))
			defer server.Close()
			m.BaseURL, m.Client, m.MinInterval = server.URL, server.Client(), 200*time.Millisecond
			input := p.M{"goalId": goal.ID, "messages": []any{p.M{"role": "user", "content": "hello"}}}
			if _, err := m.Chat(context.Background(), actor, input); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			go func() { _, err := m.Chat(ctx, actor, input); result <- err }()
			// The first call has durably closed admission. Change authority
			// while the second call is queued, before that gate can reopen.
			time.Sleep(10 * time.Millisecond)
			command, in := "projects.policy", p.M{"id": goal.ProjectID, "allowExternalModels": false}
			if change == "requirement" {
				command, in = "goals.revise", p.M{"id": goal.ID, "reason": "Change queued execution baseline", "outcome": "Changed scope"}
			}
			if _, err := m.Service.Command(ctx, actor, command, in); err != nil {
				t.Fatal(err)
			}
			var fault *p.Fault
			if err := <-result; !errors.As(err, &fault) || (fault.Code != "model_policy" && fault.Code != "approval_required") {
				t.Fatalf("queued call ignored revoked authority: %v", err)
			}
			charges, _ := m.Service.Store.List(goal.ProjectID, "charge", goal.ID)
			if calls != 1 || len(charges) != 1 {
				t.Fatal("revoked queued call reserved budget or contacted the provider")
			}
		})
	}
}

func TestPostgresModelAdmissionCoordinatesIndependentStores(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	store, err := p.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	other, err := p.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	svc := p.NewService(store)
	actor := p.Actor{ID: "model-pg-owner", Kind: "human", Admin: true}
	ctx := context.Background()
	project, err := svc.Command(ctx, actor, "projects.create", p.M{"title": "PostgreSQL provider admission", "allowExternalModels": true})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := svc.Command(ctx, actor, "goals.create", p.M{"projectId": project.ID, "title": "Admission acceptance", "outcome": "Coordinate model budget and provider admission", "criteria": []any{p.M{"id": "admission", "text": "No duplicate provider admission"}}, "targets": []any{"web"}, "budget": 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Command(ctx, actor, "goals.approve", p.M{"id": goal.ID}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(p.M{"choices": []any{p.M{"finish_reason": "stop", "message": p.M{"content": "ok"}}}, "usage": p.M{"prompt_tokens": 1, "completion_tokens": 1}})
	}))
	defer server.Close()
	first := &Model{Service: svc, BaseURL: server.URL, Name: "pg-admission", External: true, InputUSD: 1, OutputUSD: 1, MinInterval: 200 * time.Millisecond, Client: server.Client()}
	second := *first
	second.Service = p.NewService(other)
	input := p.M{"goalId": goal.ID, "messages": []any{p.M{"role": "user", "content": "hello"}}}
	if _, err = first.Chat(ctx, actor, input); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err = second.Chat(short, actor, input); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("independent PostgreSQL control plane bypassed the gate: %v", err)
	}
	charges, _ := store.List(project.ID, "charge", goal.ID)
	if calls != 1 || len(charges) != 1 {
		t.Fatal("queued PostgreSQL request contacted provider or created a charge")
	}
	if _, err = second.Chat(ctx, actor, input); err != nil || calls != 2 {
		t.Fatal("PostgreSQL admission did not resume", err, calls)
	}
}
