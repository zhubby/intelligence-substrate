package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestPlanProviderReceivesChosenDesignAndProposalKeepsIt(t *testing.T) {
	m, owner, g, _ := modelFixture(t, true)
	design := discoveryCommand(t, m, owner, "designs.propose", p.M{"goalId": g.ID, "expectedRequirementRevision": 1, "key": "design", "title": "Portal design", "summary": "A single portal", "decisions": "EXACT-SELECTED-DESIGN: show request status first", "criteria": []any{"x"}, "reason": "Business review"})
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var input p.M
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		body, _ := json.Marshal(input["messages"])
		if !strings.Contains(string(body), "EXACT-SELECTED-DESIGN") || !strings.Contains(string(body), design.ID) {
			t.Error("provider did not receive selected design snapshot", string(body))
		}
		discoveryReply(w, planReply)
	}))
	defer provider.Close()
	m.BaseURL, m.Client = provider.URL, provider.Client()
	in := planInput(g, "chosen")
	in["designId"], in["expectedDesignBinding"] = design.ID, design.Data["binding"]
	plan, err := m.GeneratePlan(context.Background(), owner, in)
	if err != nil || p.S(p.Obj(plan.Data["design"]), "id") != design.ID || plan.Status != "proposed" || calls != 1 {
		t.Fatal(plan, err, calls)
	}
	if replay, err := m.GeneratePlan(context.Background(), owner, in); err != nil || replay.ID != plan.ID || calls != 1 {
		t.Fatal("selected-design replay repeated inference", replay, err, calls)
	}
}
