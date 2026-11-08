package platform

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func registerMembershipIdentity(t *testing.T, s *Service, id string, admin, disabled bool) {
	t.Helper()
	body, _ := json.Marshal(M{"id": id, "email": id + "@example.test", "name": id, "admin": admin, "disabled": disabled})
	if _, err := s.Store.DB.Exec(`INSERT INTO identities(id,email,body) VALUES($1,$2,$3)`, id, id+"@example.test", string(body)); err != nil {
		t.Fatal(err)
	}
}

func membershipFixture(t *testing.T) (*Service, Actor, Resource, Actor, Resource) {
	t.Helper()
	s, admin, project := fixture(t)
	registerMembershipIdentity(t, s, admin.ID, true, false)
	other := Actor{ID: "member-owner", Kind: "human", Name: "Member owner"}
	registerMembershipIdentity(t, s, other.ID, false, false)
	m := must(t, s, admin, "members.grant", M{"projectId": project.ID, "identityId": other.ID, "roles": []string{"owner"}})
	return s, admin, project, other, m
}

func membershipToken(t *testing.T, s *Service, id, issuer, project string) {
	t.Helper()
	body, _ := json.Marshal(Actor{ID: id, Kind: "agent", ProjectID: project, Roles: []string{"developer"}})
	if _, err := s.Store.DB.Exec(`INSERT INTO credentials(hash,identity_id,kind,expires_at,body) VALUES($1,$2,'token',$3,$4)`, id, issuer, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), string(body)); err != nil {
		t.Fatal(err)
	}
}

func TestMembershipGrantValidatesIdentityRolesAndRevision(t *testing.T) {
	for _, scenario := range []string{"missing-identity", "disabled-identity", "unknown-project", "nonstring-role", "duplicate-role", "unknown-role", "stale-revision"} {
		t.Run(scenario, func(t *testing.T) {
			s, admin, project, other, m := membershipFixture(t)
			in := M{"projectId": project.ID, "identityId": other.ID, "roles": []string{"viewer"}, "expectedRevision": m.Revision}
			switch scenario {
			case "missing-identity":
				in["identityId"] = "does-not-exist"
			case "disabled-identity":
				registerMembershipIdentity(t, s, "disabled-member", false, true)
				in["identityId"] = "disabled-member"
			case "unknown-project":
				in["projectId"] = "project_missing"
			case "nonstring-role":
				in["roles"] = []any{"viewer", 12}
			case "duplicate-role":
				in["roles"] = []string{"viewer", "viewer"}
			case "unknown-role":
				in["roles"] = []string{"administrator"}
			case "stale-revision":
				in["expectedRevision"] = m.Revision + 1
			}
			if _, err := s.Command(context.Background(), admin, "members.grant", in); err == nil {
				t.Fatal("invalid membership grant accepted")
			}
		})
	}
}

func TestMembershipRoleChangesRevokeOnlyAffectedDelegation(t *testing.T) {
	s, admin, project, other, m := membershipFixture(t)
	second := must(t, s, admin, "projects.create", M{"title": "Other project"})
	membershipToken(t, s, "affected", other.ID, project.ID)
	membershipToken(t, s, "other-project", other.ID, second.ID)
	membershipToken(t, s, "other-issuer", admin.ID, project.ID)
	updated := must(t, s, admin, "members.grant", M{"projectId": project.ID, "identityId": other.ID, "roles": []string{"viewer"}, "expectedRevision": m.Revision})
	var count int
	if err := s.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE hash='affected'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("role change retained delegated credential", count, err)
	}
	if err := s.Store.DB.QueryRow(`SELECT count(*) FROM credentials`).Scan(&count); err != nil || count != 2 {
		t.Fatal("unrelated credentials revoked", count, err)
	}
	if updated.ID != m.ID || updated.Revision != m.Revision+1 {
		t.Fatal("membership history lost")
	}
	membershipToken(t, s, "same-roles", other.ID, project.ID)
	unchanged := must(t, s, admin, "members.grant", M{"projectId": project.ID, "identityId": other.ID, "roles": []string{"viewer"}, "expectedRevision": updated.Revision})
	if err := s.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE hash='same-roles'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("identical role set revoked credentials", count, err)
	}
	if unchanged.Revision != updated.Revision {
		t.Fatal("identical role grant changed revision")
	}
}

func TestMembershipRevokeRetainsHistoryAndExplicitRegrant(t *testing.T) {
	s, admin, project, other, m := membershipFixture(t)
	membershipToken(t, s, "old-delegation", other.ID, project.ID)
	r := must(t, s, admin, "members.revoke", M{"id": m.ID, "expectedRevision": m.Revision, "reason": "Department transfer"})
	if r.Status != "revoked" || S(r.Data, "revocationReason") != "Department transfer" || S(r.Data, "revokedBy") != admin.ID || r.Revision != m.Revision+1 {
		t.Fatal("revocation history missing", r)
	}
	if err := s.CanAccess(context.Background(), other, project.ID); err == nil {
		t.Fatal("removed member still accesses project")
	}
	rejoined := must(t, s, admin, "members.grant", M{"projectId": project.ID, "identityId": other.ID, "roles": []string{"developer"}, "expectedRevision": r.Revision})
	if rejoined.ID != m.ID || rejoined.Status != "active" || rejoined.Revision != r.Revision+1 {
		t.Fatal("explicit regrant lost history", rejoined)
	}
	var n int
	if err := s.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE hash='old-delegation'`).Scan(&n); err != nil || n != 0 {
		t.Fatal("regrant resurrected token", n, err)
	}
}

func TestMembershipRevokeRequiresHumanCurrentRevisionAndReason(t *testing.T) {
	for _, scenario := range []string{"agent", "viewer", "stale", "no-revision", "empty-reason", "long-reason", "wrong-kind"} {
		t.Run(scenario, func(t *testing.T) {
			s, admin, project, other, m := membershipFixture(t)
			in := M{"id": m.ID, "expectedRevision": m.Revision, "reason": "Transfer"}
			actor := admin
			switch scenario {
			case "agent":
				actor = Actor{ID: "delegated", Kind: "agent", ProjectID: project.ID, Roles: []string{"owner"}}
			case "viewer":
				must(t, s, admin, "members.grant", M{"projectId": project.ID, "identityId": other.ID, "roles": []string{"viewer"}})
				actor = other
			case "stale":
				in["expectedRevision"] = m.Revision + 1
			case "no-revision":
				delete(in, "expectedRevision")
			case "empty-reason":
				in["reason"] = " "
			case "long-reason":
				in["reason"] = strings.Repeat("x", 2049)
			case "wrong-kind":
				in["id"] = project.ID
			}
			if _, err := s.Command(context.Background(), actor, "members.revoke", in); err == nil {
				t.Fatal("unsafe member revocation accepted")
			}
		})
	}
}

func TestMembershipLastRegisteredOwnerCannotDisappear(t *testing.T) {
	for _, action := range []string{"demote", "revoke"} {
		t.Run(action, func(t *testing.T) {
			s, admin, project, other, m := membershipFixture(t)
			members, err := s.Store.List(project.ID, "member", "")
			if err != nil {
				t.Fatal(err)
			}
			var creator Resource
			for _, member := range members {
				if S(member.Data, "identityId") == admin.ID {
					creator = member
				}
			}
			must(t, s, other, "members.revoke", M{"id": creator.ID, "expectedRevision": creator.Revision, "reason": "Ownership transferred"})
			input := M{"projectId": project.ID, "identityId": other.ID, "roles": []string{"viewer"}}
			command := "members.grant"
			if action == "revoke" {
				command = "members.revoke"
				input = M{"id": m.ID, "expectedRevision": m.Revision, "reason": "Remove last owner"}
			}
			if _, err = s.Command(context.Background(), admin, command, input); err == nil {
				t.Fatal("last registered owner disappeared")
			}
		})
	}
}

func TestMembershipAuditFailureRollsBackRoleAndCredentials(t *testing.T) {
	s, admin, project, other, m := membershipFixture(t)
	membershipToken(t, s, "rollback-token", other.ID, project.ID)
	if _, err := s.Store.DB.Exec(`CREATE TRIGGER membership_audit_failure BEFORE INSERT ON audit BEGIN SELECT RAISE(FAIL, 'injected audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Command(context.Background(), admin, "members.grant", M{"projectId": project.ID, "identityId": other.ID, "roles": []string{"viewer"}}); err == nil {
		t.Fatal("fixture did not reject audit")
	}
	stored, err := s.Store.Get(m.ID)
	if err != nil || stored.Revision != m.Revision || !Has(Strings(stored.Data["roles"]), "owner") {
		t.Fatal("failed audit retained changed membership", stored, err)
	}
	var n int
	if err = s.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE hash='rollback-token'`).Scan(&n); err != nil || n != 1 {
		t.Fatal("failed audit lost credential", n, err)
	}
}
