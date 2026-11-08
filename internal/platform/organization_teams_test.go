package platform

import (
	"context"
	"strings"
	"testing"
)

type teamFixture struct {
	s     *Service
	a     Actor
	p     Resource
	user  Actor
	team  Resource
	grant Resource
}

func organizationFixture(t *testing.T) teamFixture {
	t.Helper()
	s, a, p := fixture(t)
	registerMembershipIdentity(t, s, a.ID, true, false)
	user := Actor{ID: "team-person", Name: "Team person", Kind: "human"}
	registerMembershipIdentity(t, s, user.ID, false, false)
	team := must(t, s, a, "teams.create", M{"title": "Application team", "description": "Enterprise delivery", "memberIds": []string{user.ID}, "key": "create-team"})
	grant := must(t, s, a, "teams.grant", M{"projectId": p.ID, "teamId": team.ID, "expectedTeamRevision": team.Revision, "roles": []string{"developer"}, "reason": "Approved delivery team", "key": "grant-team"})
	return teamFixture{s, a, p, user, team, grant}
}

// This fails if effective authority still reads only direct members, or if
// inherited project roles cross project boundaries or become platform admin.
func TestOrganizationTeamProjectAuthorityAndDirectUnion(t *testing.T) {
	f := organizationFixture(t)
	ctx := context.Background()
	if e := f.s.CanAccess(ctx, f.user, f.p.ID, "developer"); e != nil {
		t.Fatal("team member cannot develop", e)
	}
	if e := f.s.CanAccess(ctx, f.user, f.p.ID, "owner"); e == nil {
		t.Fatal("developer became owner")
	}
	other := must(t, f.s, f.a, "projects.create", M{"title": "Unrelated project"})
	if e := f.s.CanAccess(ctx, f.user, other.ID); e == nil {
		t.Fatal("team leaked across projects")
	}
	if _, e := f.s.Command(ctx, f.user, "projects.create", M{"title": "Unauthorized"}); e == nil {
		t.Fatal("team became enterprise admin")
	}
	member := must(t, f.s, f.a, "members.grant", M{"projectId": f.p.ID, "identityId": f.user.ID, "roles": []string{"viewer"}})
	must(t, f.s, f.a, "members.revoke", M{"id": member.ID, "expectedRevision": member.Revision, "reason": "Remove direct access only"})
	if e := f.s.CanAccess(ctx, f.user, f.p.ID, "developer"); e != nil {
		t.Fatal("direct removal erased team authority", e)
	}
	items, e := f.s.Workspace(ctx, f.user, f.p.ID)
	if e != nil {
		t.Fatal(e)
	}
	var projected bool
	var inherited bool
	for _, item := range items {
		if item.ID == f.p.ID {
			projected = Has(Strings(item.Data["effectiveRoles"]), "developer")
		}
		if item.ID == f.grant.ID {
			inherited = B(item.Data, "effectiveForCurrentActor")
		}
	}
	if !projected || !inherited {
		t.Fatal("workbench lost server-derived effective roles")
	}
	stored, e := f.s.Store.Get(f.grant.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, exists := stored.Data["effectiveForCurrentActor"]; exists {
		t.Fatal("derived authority persisted to shared resource")
	}
}

// Removing a team member must delete only this issuer's credentials in projects
// actually bound to this team, including a cached token whose issuer stays owner
// through a direct grant. Renaming and exact replay must not revoke them again.
func TestOrganizationTeamMembershipRevokesAffectedDelegationAtomically(t *testing.T) {
	f := organizationFixture(t)
	ctx := context.Background()
	must(t, f.s, f.a, "members.grant", M{"projectId": f.p.ID, "identityId": f.user.ID, "roles": []string{"owner"}})
	other := must(t, f.s, f.a, "projects.create", M{"title": "Other"})
	membershipToken(t, f.s, "affected", f.user.ID, f.p.ID)
	membershipToken(t, f.s, "other-project", f.user.ID, other.ID)
	membershipToken(t, f.s, "other-issuer", f.a.ID, f.p.ID)
	input := M{"id": f.team.ID, "expectedRevision": 1, "title": "Renamed team", "description": "Metadata only", "memberIds": []string{f.user.ID}, "reason": "Rename", "key": "rename"}
	renamed := must(t, f.s, f.a, "teams.update", input)
	var n int
	if e := f.s.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE hash='affected'`).Scan(&n); e != nil {
		t.Fatal(e)
	}
	if n != 1 {
		t.Fatal("metadata update revoked delegation")
	}
	input = M{"id": f.team.ID, "expectedRevision": renamed.Revision, "title": renamed.Title, "description": "Metadata only", "memberIds": []string{}, "reason": "Department transfer", "key": "remove-person"}
	updated := must(t, f.s, f.a, "teams.update", input)
	if e := f.s.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE hash='affected'`).Scan(&n); e != nil {
		t.Fatal(e)
	}
	if n != 0 {
		t.Fatal("team change retained old delegated access")
	}
	if e := f.s.Store.DB.QueryRow(`SELECT count(*) FROM credentials`).Scan(&n); e != nil {
		t.Fatal(e)
	}
	if n != 2 {
		t.Fatal("unrelated credentials were revoked", n)
	}
	if e := f.s.CanAccess(ctx, f.user, f.p.ID, "owner"); e != nil {
		t.Fatal("direct owner was removed", e)
	}
	membershipToken(t, f.s, "new-delegation", f.user.ID, f.p.ID)
	replay := must(t, f.s, f.a, "teams.update", input)
	if e := f.s.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE hash='new-delegation'`).Scan(&n); e != nil {
		t.Fatal(e)
	}
	if replay.Revision != updated.Revision || n != 1 {
		t.Fatal("exact replay mutated state/credentials")
	}
	input["title"] = "Conflicting key"
	if _, e := f.s.Command(ctx, f.a, "teams.update", input); e == nil {
		t.Fatal("conflicting replay accepted")
	}
	history, e := f.s.Store.List("", "team_revision", "")
	if e != nil || len(history) != 3 {
		t.Fatal("immutable team history missing", len(history), e)
	}
}

func TestOrganizationTeamLifecycleAndLastDirectOwner(t *testing.T) {
	f := organizationFixture(t)
	ctx := context.Background()
	must(t, f.s, f.a, "teams.grant", M{"projectId": f.p.ID, "teamId": f.team.ID, "expectedTeamRevision": 1, "expectedRevision": f.grant.Revision, "roles": []string{"owner"}, "reason": "Team leads delivery", "key": "team-owner"})
	members, _ := f.s.Store.List(f.p.ID, "member", "")
	for _, member := range members {
		if S(member.Data, "identityId") == f.a.ID {
			if _, e := f.s.Command(ctx, f.a, "members.revoke", M{"id": member.ID, "expectedRevision": member.Revision, "reason": "Team owner is enough"}); e == nil {
				t.Fatal("team replaced last direct recovery owner")
			}
		}
	}
	disabled := must(t, f.s, f.a, "teams.disable", M{"id": f.team.ID, "expectedRevision": 1, "reason": "Suspend team", "key": "disable"})
	if e := f.s.CanAccess(ctx, f.user, f.p.ID); e == nil {
		t.Fatal("disabled team retained access")
	}
	enabled := must(t, f.s, f.a, "teams.enable", M{"id": f.team.ID, "expectedRevision": disabled.Revision, "reason": "Resume team", "key": "enable"})
	if e := f.s.CanAccess(ctx, f.user, f.p.ID, "owner"); e != nil {
		t.Fatal("explicitly enabled team did not regain roles", e)
	}
	if enabled.Revision != 3 {
		t.Fatal("team lifecycle history lost")
	}
	if e := f.s.Store.Update(ctx, func(tx *Tx) error { return tx.DisableIdentity(f.a, f.user.ID) }); e != nil {
		t.Fatal(e)
	}
	if e := f.s.CanAccess(ctx, f.user, f.p.ID); e == nil {
		t.Fatal("disabled identity retained inherited access")
	}
	audits, e := f.s.Store.Audits(f.p.ID)
	if e != nil {
		t.Fatal(e)
	}
	foundDisabled := false
	for _, audit := range audits {
		if audit.Action == "teams.access.changed" && S(audit.Detail, "reason") == "identity_disabled" && Has(Strings(audit.Detail["affectedIdentityIds"]), f.user.ID) {
			foundDisabled = true
		}
	}
	if !foundDisabled {
		t.Fatal("inherited-only identity disable missing project audit")
	}
	stored, _ := f.s.Store.Get(f.team.ID)
	if !Has(Strings(stored.Data["memberIds"]), f.user.ID) {
		t.Fatal("identity disable erased team history")
	}
}

func TestOrganizationTeamRejectsMalformedOrUnauthorizedChanges(t *testing.T) {
	for _, mode := range []string{"nonadmin", "agent", "worker", "missing-member", "disabled-member", "duplicate-member", "nonstring-member", "missing-members", "null-members", "over-limit", "stale", "missing-revision", "missing-reason", "missing-key", "description", "wrong-kind"} {
		t.Run(mode, func(t *testing.T) {
			f := organizationFixture(t)
			a := f.a
			in := M{"id": f.team.ID, "expectedRevision": 1, "title": "Changed", "description": "", "memberIds": []string{f.user.ID}, "reason": "Team change", "key": "invalid"}
			switch mode {
			case "nonadmin":
				a = f.user
			case "agent", "worker":
				a = Actor{ID: "delegated", Kind: mode, Admin: true, ProjectID: f.p.ID, Roles: []string{"owner"}}
			case "missing-member":
				in["memberIds"] = []string{"absent"}
			case "disabled-member":
				registerMembershipIdentity(t, f.s, "disabled", false, true)
				in["memberIds"] = []string{"disabled"}
			case "duplicate-member":
				in["memberIds"] = []string{f.user.ID, f.user.ID}
			case "nonstring-member":
				in["memberIds"] = []any{12}
			case "missing-members":
				delete(in, "memberIds")
			case "null-members":
				in["memberIds"] = nil
			case "over-limit":
				in["memberIds"] = make([]string, 1001)
			case "stale":
				in["expectedRevision"] = 3
			case "missing-revision":
				delete(in, "expectedRevision")
			case "missing-reason":
				delete(in, "reason")
			case "missing-key":
				delete(in, "key")
			case "description":
				in["description"] = strings.Repeat("x", 4001)
			case "wrong-kind":
				in["id"] = f.p.ID
			}
			if _, e := f.s.Command(context.Background(), a, "teams.update", in); e == nil {
				t.Fatal("invalid team change accepted", mode)
			}
			stored, _ := f.s.Store.Get(f.team.ID)
			if stored.Revision != 1 {
				t.Fatal("rejected request changed team")
			}
		})
	}
}

func TestOrganizationTeamAuditFailureRollsBackMembershipHistoryAndTokens(t *testing.T) {
	f := organizationFixture(t)
	membershipToken(t, f.s, "rollback-team-token", f.user.ID, f.p.ID)
	if _, e := f.s.Store.DB.Exec(`CREATE TRIGGER team_audit_failure BEFORE INSERT ON audit WHEN json_extract(NEW.body,'$.action')='teams.access.changed' BEGIN SELECT RAISE(FAIL,'injected team audit failure'); END`); e != nil {
		t.Fatal(e)
	}
	_, e := f.s.Command(context.Background(), f.a, "teams.update", M{"id": f.team.ID, "expectedRevision": 1, "title": f.team.Title, "memberIds": []string{}, "reason": "Transfer", "key": "rollback"})
	if e == nil || !strings.Contains(e.Error(), "injected team audit failure") {
		t.Fatal("expected injected audit failure", e)
	}
	stored, _ := f.s.Store.Get(f.team.ID)
	if stored.Revision != 1 || !Has(Strings(stored.Data["memberIds"]), f.user.ID) {
		t.Fatal("audit failure retained team mutation")
	}
	history, _ := f.s.Store.List("", "team_revision", "")
	if len(history) != 1 {
		t.Fatal("audit failure retained new history")
	}
	var n int
	if e := f.s.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE hash='rollback-team-token'`).Scan(&n); e != nil {
		t.Fatal(e)
	}
	if n != 1 {
		t.Fatal("audit failure lost token")
	}
	if e := f.s.Store.DB.QueryRow(`SELECT count(*) FROM idempotency WHERE key='rollback'`).Scan(&n); e != nil {
		t.Fatal(e)
	}
	if n != 0 {
		t.Fatal("audit failure retained replay key")
	}
}

func TestOrganizationTeamInheritedHumanReceivesCollaboration(t *testing.T) {
	f := organizationFixture(t)
	request := must(t, f.s, f.a, "messages.send", M{"projectId": f.p.ID, "recipient": f.user.ID, "type": "request", "key": "team-message", "payload": M{"text": "Review the implementation"}})
	claimed := must(t, f.s, f.user, "messages.claim", M{"projectId": f.p.ID})
	if claimed.ID != request.ID {
		t.Fatal("inherited human did not receive collaboration", claimed)
	}
	must(t, f.s, f.user, "messages.ack", M{"id": claimed.ID, "leaseToken": claimed.Data["leaseToken"]})
	must(t, f.s, f.a, "teams.disable", M{"id": f.team.ID, "expectedRevision": 1, "reason": "Suspend communication", "key": "disable-messaging"})
	if _, e := f.s.Command(context.Background(), f.a, "messages.send", M{"projectId": f.p.ID, "recipient": f.user.ID, "type": "request", "key": "disabled-team-message", "payload": M{"text": "Should be rejected"}}); e == nil {
		t.Fatal("disabled inherited recipient still accepted")
	}
}

func TestOrganizationTeamIdentityDisableAuditRollsBackOffboarding(t *testing.T) {
	f := organizationFixture(t)
	membershipToken(t, f.s, "offboard-team-token", f.user.ID, f.p.ID)
	if _, e := f.s.Store.DB.Exec(`CREATE TRIGGER disabled_team_audit_failure BEFORE INSERT ON audit WHEN json_extract(NEW.body,'$.action')='teams.access.changed' BEGIN SELECT RAISE(FAIL,'injected team offboarding audit failure'); END`); e != nil {
		t.Fatal(e)
	}
	e := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.DisableIdentity(f.a, f.user.ID) })
	if e == nil || !strings.Contains(e.Error(), "injected team offboarding audit failure") {
		t.Fatal("expected offboarding audit failure", e)
	}
	if e = f.s.CanAccess(context.Background(), f.user, f.p.ID, "developer"); e != nil {
		t.Fatal("failed audit retained identity disable", e)
	}
	var n int
	if e = f.s.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE hash='offboard-team-token'`).Scan(&n); e != nil || n != 1 {
		t.Fatal("failed offboarding lost credential", n, e)
	}
}
