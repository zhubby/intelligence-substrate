package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func httpCostGoal(t *testing.T, h *Handler, a p.Actor, project p.Resource) p.Resource {
	t.Helper()
	return authorityCommand(t, h, a, "goals.create", p.M{"projectId": project.ID, "title": "Accounting fixture", "outcome": "Trace delivery costs", "criteria": []any{p.M{"id": "accounting", "text": "Costs are traceable"}}, "targets": []any{"web"}, "budget": 100})
}

func httpExpenseInput(g p.Resource, reference string, amount int) p.M {
	return p.M{"goalId": g.ID, "title": "Synthetic expense " + reference, "category": "build", "amountCents": amount, "currency": "USD", "incurredAt": "2026-01-02T03:04:05Z", "reference": reference, "note": "Synthetic provider allocation, not a real invoice", "key": reference}
}

// Catch double-counting goal.spentCents, counting reserved money as settled,
// silently rounding old invalid rows, or losing explicit zero-cost provenance.
func TestDeliveryCostHTTPReportSeparatesEstimatesPendingAndExternalRecords(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	a := identityActor(identity)
	project := authorityCommand(t, h, a, "projects.create", p.M{"title": "Delivery accounting"})
	g := httpCostGoal(t, h, a, project)
	otherGoal := httpCostGoal(t, h, a, project)
	fixture := []struct {
		status string
		data   p.M
	}{
		{"settled", p.M{"chargedCents": 100, "pricingBasis": "configured-estimate", "leaseToken": "synthetic-token-to-hide"}},
		{"settled", p.M{"chargedCents": 200, "reconciledBy": a.ID}},
		{"uncertain", p.M{"chargedCents": 300, "reservedCents": 600}},
		{"reserved", p.M{"reservedCents": 400}},
		{"settled", p.M{"chargedCents": 1.5}},
		{"unknown-fixture-state", p.M{"chargedCents": 999}},
	}
	if err := h.Service.Store.Update(context.Background(), func(tx *p.Tx) error {
		for _, row := range fixture {
			r := p.Resource{ID: p.NewID("cost_charge"), Kind: "charge", ProjectID: project.ID, GoalID: g.ID, Title: "Synthetic model response", Status: row.status, Revision: 1, CreatedAt: "2026-01-02T03:04:05Z", Data: row.data}
			if err := tx.Put(r); err != nil {
				return err
			}
		}
		g.Data["spentCents"] = 999999 // A cache is not a second cost source.
		return tx.Put(g)
	}); err != nil {
		t.Fatal(err)
	}
	post := func(in p.M) p.Resource {
		t.Helper()
		response := req(h, "POST", "/api/v1/commands/expenses.record", in, cookie, csrf)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		var r p.Resource
		if err := json.Unmarshal(response.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	post(httpExpenseInput(g, "build-1", 1234))
	zero := httpExpenseInput(g, "device-explicit-zero", 0)
	zero["category"] = "device"
	post(zero)
	voided := post(httpExpenseInput(g, "incorrect-allocation", 500))
	if response := req(h, "POST", "/api/v1/commands/expenses.void", p.M{"id": voided.ID, "key": "void", "reason": "Not incurred by this goal"}, cookie, csrf); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	post(httpExpenseInput(otherGoal, "another-goal", 900))
	route := "/api/v1/projects/" + project.ID + "/costs?goalId=" + g.ID
	response := req(h, "GET", route, nil, cookie, "")
	if response.Code != 200 {
		t.Fatal("cost report unavailable", response.Code, response.Body.String())
	}
	var report p.M
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]float64{"modelBudgetCents": 10000, "modelEstimatedCents": 100, "modelReconciledCents": 200, "modelPendingCents": 700, "externalRecordedCents": 1234, "recordedTotalCents": 1534, "exposureCents": 2234} {
		if p.N(report, key) != want {
			t.Fatalf("%s: got %v, want %v", key, report[key], want)
		}
	}
	byCategory := p.Obj(report["externalByCategory"])
	if amount, exists := byCategory["device"]; !exists || amount != float64(0) {
		t.Fatal("explicit zero lost", byCategory)
	}
	if _, exists := byCategory["storage"]; exists {
		t.Fatal("unrecorded category was invented as zero", byCategory)
	}
	if len(p.Items(report["unmeasurable"])) != 2 || strings.Join(p.Strings(report["unreportedCategories"]), ",") != "storage,channel,labor" || len(p.Items(report["expenses"])) != 3 || p.B(report, "complete") {
		t.Fatal("cost provenance or missing coverage was hidden", report)
	}
	if bytes.Contains(response.Body.Bytes(), []byte("synthetic-token-to-hide")) {
		t.Fatal("cost report bypassed resource sanitization")
	}
	all := req(h, "GET", "/api/v1/projects/"+project.ID+"/costs", nil, cookie, "")
	if all.Code != 200 || json.Unmarshal(all.Body.Bytes(), &report) != nil || p.N(report, "recordedTotalCents") != 2434 || p.N(report, "modelBudgetCents") != 20000 {
		t.Fatal("project aggregation lost another goal or included it twice", all.Code, all.Body.String())
	}
}

func TestDeliveryCostHTTPAuthenticationScopeAndCurrentMembership(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	a := identityActor(identity)
	project := authorityCommand(t, h, a, "projects.create", p.M{"title": "Private accounting"})
	g := httpCostGoal(t, h, a, project)
	other := authorityCommand(t, h, a, "projects.create", p.M{"title": "Other project"})
	otherGoal := httpCostGoal(t, h, a, other)
	route := "/api/v1/projects/" + project.ID + "/costs?goalId=" + g.ID
	if response := req(h, "GET", route, nil, nil, ""); response.Code != 401 {
		t.Fatal("anonymous report accepted", response.Code)
	}
	if response := req(h, "GET", "/api/v1/projects/"+project.ID+"/costs?goalId="+otherGoal.ID, nil, cookie, ""); response.Code != 403 {
		t.Fatal("cross-project goal accepted", response.Code)
	}
	in := httpExpenseInput(g, "csrf-cost", 10)
	if response := req(h, "POST", "/api/v1/commands/expenses.record", in, cookie, ""); response.Code != 403 {
		t.Fatal("expense write bypassed CSRF", response.Code)
	}
	// Public Agent tokens have the fixed developer role. The platform test
	// separately proves that even an owner-role Agent fails the human gate.
	delegated := req(h, "POST", "/api/v1/tokens", p.M{"projectId": project.ID}, cookie, csrf)
	if delegated.Code != 201 {
		t.Fatal(delegated.Body.String())
	}
	var token p.M
	if err := json.Unmarshal(delegated.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/v1/commands/expenses.record", strings.NewReader(string(mustCostJSON(t, in))))
	request.Header.Set("Authorization", "Bearer "+p.S(token, "token"))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	if recorder.Code != 403 {
		t.Fatal("Agent token bypassed accounting authorization", recorder.Code, recorder.Body.String())
	}
	viewer, err := h.Auth.CreateIdentity("cost-viewer@example.test", "Accounting observer", "synthetic-viewer-password-2026", false)
	if err != nil {
		t.Fatal(err)
	}
	member := authorityCommand(t, h, a, "members.grant", p.M{"projectId": project.ID, "identityId": viewer.ID, "roles": []string{"viewer"}})
	logged := req(h, "POST", "/api/v1/auth/login", p.M{"email": viewer.Email, "password": "synthetic-viewer-password-2026"}, nil, "")
	if logged.Code != 200 {
		t.Fatal(logged.Body.String())
	}
	viewerCookie := logged.Result().Cookies()[0]
	if response := req(h, "GET", route, nil, viewerCookie, ""); response.Code != 200 {
		t.Fatal("current member cannot inspect costs", response.Code, response.Body.String())
	}
	if response := req(h, "GET", "/api/v1/projects/"+other.ID+"/costs", nil, viewerCookie, ""); response.Code != 403 {
		t.Fatal("member saw another project's costs", response.Code)
	}
	authorityCommand(t, h, a, "members.revoke", p.M{"id": member.ID, "expectedRevision": member.Revision, "reason": "Observer moved to another team"})
	if response := req(h, "GET", route, nil, viewerCookie, ""); response.Code != 403 {
		t.Fatal("removed membership retained report access", response.Code)
	}
}

func mustCostJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
