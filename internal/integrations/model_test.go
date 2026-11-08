package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	axapi "github.com/google/ax/pkg/apis/v1alpha1"
	"gopkg.in/yaml.v3"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func modelFixture(t *testing.T, external bool) (*Model, p.Actor, p.Resource, *int) {
	t.Helper()
	store, e := p.Open(filepath.Join(t.TempDir(), "model.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	svc := p.NewService(store)
	a := p.Actor{ID: "owner", Kind: "human", Admin: true}
	project, e := svc.Command(context.Background(), a, "projects.create", p.M{"title": "Model integration", "allowExternalModels": external})
	if e != nil {
		t.Fatal(e)
	}
	g, e := svc.Command(context.Background(), a, "goals.create", p.M{"projectId": project.ID, "title": "Goal", "outcome": "Build a portal", "criteria": []any{p.M{"id": "x", "text": "Works"}}, "targets": []any{"web"}, "budget": 100})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Command(context.Background(), a, "goals.approve", p.M{"id": g.ID}); e != nil {
		t.Fatal(e)
	}
	calls := new(int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p.M{"choices": []any{p.M{"message": p.M{"content": "ok"}}}, "usage": p.M{"prompt_tokens": -1000000, "completion_tokens": -1000000}})
	}))
	t.Cleanup(server.Close)
	return &Model{Service: svc, BaseURL: server.URL, Name: "test-model", External: true, InputUSD: 1, OutputUSD: 1, Client: server.Client()}, a, g, calls
}

func TestModelFromEnvReadsPrivateAPIKeyFile(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "model-key")
	if err := os.WriteFile(keyFile, []byte(" file-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"IS_MODEL_BASE_URL":               "https://models.example.test/v1",
		"IS_MODEL_NAME":                   "file-model",
		"IS_MODEL_INPUT_USD_PER_MILLION":  "1",
		"IS_MODEL_OUTPUT_USD_PER_MILLION": "1",
		"IS_MODEL_API_KEY_FILE":           keyFile,
		"IS_MODEL_API_KEY":                "inline-key-must-not-win",
	} {
		t.Setenv(key, value)
	}
	configured := ModelFromEnv(nil)
	if configured == nil || configured.Key != "file-key" {
		t.Fatalf("private API key file was not authoritative: %#v", configured)
	}
}

func TestModelFromEnvRejectsUnsafeAPIKeyFile(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "model-key")
	if err := os.WriteFile(keyFile, []byte("file-key"), 0640); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"IS_MODEL_BASE_URL":               "https://models.example.test/v1",
		"IS_MODEL_NAME":                   "file-model",
		"IS_MODEL_INPUT_USD_PER_MILLION":  "1",
		"IS_MODEL_OUTPUT_USD_PER_MILLION": "1",
		"IS_MODEL_API_KEY_FILE":           keyFile,
		"IS_MODEL_API_KEY":                "inline-key-must-not-fallback",
	} {
		t.Setenv(key, value)
	}
	if configured := ModelFromEnv(nil); configured != nil {
		t.Fatal("unsafe API key file enabled model configuration")
	}
}
func TestModelPolicyPreventsExternalRequest(t *testing.T) {
	m, a, g, calls := modelFixture(t, false)
	if _, e := m.Chat(context.Background(), a, p.M{"goalId": g.ID, "messages": []any{p.M{"role": "user", "content": "hello"}}}); e == nil {
		t.Fatal("project policy did not stop external model")
	}
	if *calls != 0 {
		t.Fatal("model was called before policy check")
	}
}
func TestMalformedUsageNeverRefundsBudget(t *testing.T) {
	m, a, g, _ := modelFixture(t, true)
	_, e := m.Chat(context.Background(), a, p.M{"goalId": g.ID, "messages": []any{p.M{"role": "user", "content": "hello"}}, "max_tokens": 16})
	if e == nil {
		t.Fatal("unverified usage must block the response")
	}
	g, e = m.Service.Store.Get(g.ID)
	if e != nil {
		t.Fatal(e)
	}
	if p.N(g.Data, "spentCents") < 1 {
		t.Fatalf("invalid provider usage reduced budget: %+v", g.Data)
	}
	charges, _ := m.Service.Store.List(g.ProjectID, "charge", g.ID)
	if len(charges) != 1 || charges[0].Status != "uncertain" {
		t.Fatal("invalid usage was not retained for reconciliation")
	}
}
func TestHumanModelCallCannotAttributeToForeignRun(t *testing.T) {
	m, a, g, calls := modelFixture(t, true)
	if _, e := m.Chat(context.Background(), a, p.M{"goalId": g.ID, "runId": "another-project-run", "messages": []any{p.M{"role": "user", "content": "hello"}}}); e == nil {
		t.Fatal("human attributed charges to unchecked run")
	}
	if *calls != 0 {
		t.Fatal("invalid run attribution reached provider")
	}
}
func TestModelBudgetRejectsBeforeNetwork(t *testing.T) {
	m, a, g, calls := modelFixture(t, true)
	m.InputUSD = 1000000
	if _, e := m.Chat(context.Background(), a, p.M{"goalId": g.ID, "messages": []any{p.M{"role": "user", "content": "hello"}}}); e == nil {
		t.Fatal("oversized cost reservation accepted")
	}
	if *calls != 0 {
		t.Fatal("unfunded model request reached provider")
	}
}
func TestAXManifestRejectsUnpinnedOrInvalidDigest(t *testing.T) {
	for _, image := range []string{"runner:latest", "runner@sha256:abc", "runner@sha256:"} {
		if _, e := (AX{Image: image}).Manifest(p.Resource{ID: "run_x"}, "https://source.test/repo.git"); e == nil {
			t.Fatalf("unsafe AX image accepted: %s", image)
		}
	}
}
func TestAXManifestMatchesPinnedUpstreamSchema(t *testing.T) {
	body, e := (AX{Image: "runner@sha256:" + strings.Repeat("a", 64), Atespace: "test"}).Manifest(p.Resource{ID: "run_test", Data: p.M{"task": p.M{}}}, "")
	if e != nil {
		t.Fatal(e)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	var workspace axapi.Workspace
	if e = decoder.Decode(&workspace); e != nil {
		t.Fatalf("workspace does not match pinned AX schema: %v", e)
	}
	var task axapi.Task
	if e = decoder.Decode(&task); e != nil {
		t.Fatalf("task does not match pinned AX schema: %v", e)
	}
	if task.GetSpec().GetWorkspace().GetName() != workspace.GetMetadata().GetName() || task.GetSpec().GetWorkspace().GetPath() != "/workspace" || !task.GetSpec().GetDebug() || len(task.GetSpec().GetCommand()) == 0 {
		t.Fatal("guest transport prerequisites missing")
	}
	var extra any
	if e = decoder.Decode(&extra); e != io.EOF {
		t.Fatal("unexpected extra AX resource")
	}
}
