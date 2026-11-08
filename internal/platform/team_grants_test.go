package platform

import (
	"context"
	"strings"
	"testing"
)

func TestOrganizationTeamGrantRevisionsReplayAndRoleUnion(t *testing.T) {
	f := organizationFixture(t)
	ctx := context.Background()
	second := must(t, f.s, f.a, "teams.create", M{"title": "Quality team", "memberIds": []string{f.user.ID}, "key": "quality-team"})
	must(t, f.s, f.a, "teams.grant", M{"projectId": f.p.ID, "teamId": second.ID, "expectedTeamRevision": 1, "roles": []string{"quality"}, "reason": "Independent duties", "key": "quality"})
	if e := f.s.CanAccess(ctx, f.user, f.p.ID, "quality"); e != nil {
		t.Fatal(e)
	}
	input := M{"projectId": f.p.ID, "teamId": f.team.ID, "expectedTeamRevision": 1, "expectedRevision": 1, "roles": []string{"viewer"}, "reason": "Reduce roles", "key": "reduce"}
	updated := must(t, f.s, f.a, "teams.grant", input)
	if e := f.s.CanAccess(ctx, f.user, f.p.ID, "developer"); e == nil {
		t.Fatal("old team roles survived")
	}
	if e := f.s.CanAccess(ctx, f.user, f.p.ID, "quality"); e != nil {
		t.Fatal("other team lost authority", e)
	}
	membershipToken(t, f.s, "fresh-after-reduce", f.user.ID, f.p.ID)
	if replay := must(t, f.s, f.a, "teams.grant", input); replay.Revision != updated.Revision {
		t.Fatal("grant replay mutated")
	}
	var n int
	if e := f.s.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE hash='fresh-after-reduce'`).Scan(&n); e != nil || n != 1 {
		t.Fatal("replay revoked fresh token", n, e)
	}
	bad := copyM(input)
	bad["roles"] = []string{"owner"}
	if _, e := f.s.Command(ctx, f.a, "teams.grant", bad); e == nil {
		t.Fatal("conflicting grant replay accepted")
	}
	revoke := M{"id": updated.ID, "expectedRevision": updated.Revision, "reason": "Exit project", "key": "revoke-team"}
	revoked := must(t, f.s, f.a, "teams.revoke", revoke)
	if replay := must(t, f.s, f.a, "teams.grant", input); replay.Status != "revoked" || replay.Revision != revoked.Revision {
		t.Fatal("replay resurrected old grant")
	}
	if replay := must(t, f.s, f.a, "teams.revoke", revoke); replay.Revision != revoked.Revision {
		t.Fatal("revoke replay mutated")
	}
	must(t, f.s, f.a, "teams.grant", M{"projectId": f.p.ID, "teamId": f.team.ID, "expectedTeamRevision": 1, "expectedRevision": revoked.Revision, "roles": []string{"developer"}, "reason": "Explicit regrant", "key": "regrant"})
	if e := f.s.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE hash='fresh-after-reduce'`).Scan(&n); e != nil || n != 0 {
		t.Fatal("regrant resurrected delegation", n, e)
	}
	history, e := f.s.Store.List(f.p.ID, "team_grant_revision", "")
	if e != nil || len(history) != 5 {
		t.Fatal("grant history missing", len(history), e)
	}
	for _, item := range history {
		if N(item.Data, "grantRevision") < 1 || S(item.Data, "binding") == "" {
			t.Fatal("unbound history")
		}
	}
}

func TestOrganizationTeamGrantRejectsStaleAndUnauthorizedInputs(t *testing.T) {
	for _, mode := range []string{"nonowner", "agent", "worker", "missing-revision", "stale-grant", "stale-team", "disabled-team", "empty-roles", "admin-role", "duplicate-roles", "no-reason", "no-key", "wrong-kind"} {
		t.Run(mode, func(t *testing.T) {
			f := organizationFixture(t)
			a := f.a
			in := M{"projectId": f.p.ID, "teamId": f.team.ID, "expectedTeamRevision": 1, "expectedRevision": 1, "roles": []string{"viewer"}, "reason": "Change", "key": "invalid"}
			switch mode {
			case "nonowner":
				a = f.user
			case "agent", "worker":
				a = Actor{ID: "delegate", Kind: mode, ProjectID: f.p.ID, Roles: []string{"owner"}}
			case "missing-revision":
				delete(in, "expectedRevision")
			case "stale-grant":
				in["expectedRevision"] = 20
			case "stale-team":
				in["expectedTeamRevision"] = 20
			case "disabled-team":
				must(t, f.s, f.a, "teams.disable", M{"id": f.team.ID, "expectedRevision": 1, "reason": "Suspend", "key": "suspend"})
				in["expectedTeamRevision"] = 2
			case "empty-roles":
				in["roles"] = []string{}
			case "admin-role":
				in["roles"] = []string{"admin"}
			case "duplicate-roles":
				in["roles"] = []string{"owner", "owner"}
			case "no-reason":
				delete(in, "reason")
			case "no-key":
				delete(in, "key")
			case "wrong-kind":
				in["teamId"] = f.p.ID
			}
			if _, e := f.s.Command(context.Background(), a, "teams.grant", in); e == nil {
				t.Fatal("invalid grant accepted")
			}
			stored, e := f.s.Store.Get(f.grant.ID)
			if e != nil || stored.Revision != 1 || stored.Status != "active" {
				t.Fatal("rejected grant mutated", e)
			}
		})
	}
}

func TestOrganizationTeamGrantAuditRollbackAndLastOwnerDisable(t *testing.T) {
	f := organizationFixture(t)
	ctx := context.Background()
	must(t, f.s, f.a, "teams.grant", M{"projectId": f.p.ID, "teamId": f.team.ID, "expectedTeamRevision": 1, "expectedRevision": 1, "roles": []string{"owner"}, "reason": "Own delivery", "key": "own"})
	if e := f.s.Store.Update(ctx, func(tx *Tx) error { return tx.DisableIdentity(f.a, f.a.ID) }); e == nil {
		t.Fatal("inherited owner replaced last direct owner on disable")
	}
	membershipToken(t, f.s, "grant-rollback-token", f.user.ID, f.p.ID)
	if _, e := f.s.Store.DB.Exec(`CREATE TRIGGER grant_audit_failure BEFORE INSERT ON audit WHEN json_extract(NEW.body,'$.action')='teams.revoke' BEGIN SELECT RAISE(FAIL,'injected grant audit failure'); END`); e != nil {
		t.Fatal(e)
	}
	_, e := f.s.Command(ctx, f.a, "teams.revoke", M{"id": f.grant.ID, "expectedRevision": 2, "reason": "Exit", "key": "rollback-grant"})
	if e == nil || !strings.Contains(e.Error(), "injected grant audit failure") {
		t.Fatal("missing injected failure", e)
	}
	stored, e := f.s.Store.Get(f.grant.ID)
	if e != nil || stored.Revision != 2 || stored.Status != "active" {
		t.Fatal("failed audit retained grant mutation", e)
	}
	var n int
	if e = f.s.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE hash='grant-rollback-token'`).Scan(&n); e != nil || n != 1 {
		t.Fatal("failed audit removed credential", n, e)
	}
	if e = f.s.Store.DB.QueryRow(`SELECT count(*) FROM idempotency WHERE key='rollback-grant'`).Scan(&n); e != nil || n != 0 {
		t.Fatal("failed audit retained replay", n, e)
	}
	history, e := f.s.Store.List(f.p.ID, "team_grant_revision", "")
	if e != nil || len(history) != 2 {
		t.Fatal("failed audit retained history", len(history), e)
	}
}
