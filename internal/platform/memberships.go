package platform

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
)

// IdentityBody is read inside the same transaction as authorization changes.
// Keep the complete document so disabling an identity preserves its IdP binding.
func (t *Tx) IdentityBody(id string) (string, error) {
	var body string
	err := t.tx.QueryRow(`SELECT body FROM identities WHERE id=$1`, id).Scan(&body)
	return body, err
}

type identityState struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Admin    bool   `json:"admin"`
	Disabled bool   `json:"disabled"`
}

func (t *Tx) identityState(id string) (identityState, error) {
	body, err := t.IdentityBody(id)
	var identity identityState
	if err != nil {
		return identity, err
	}
	if err = json.Unmarshal([]byte(body), &identity); err != nil {
		return identity, err
	}
	if identity.ID != id {
		return identity, denied()
	}
	return identity, nil
}

func memberRoles(value any) ([]string, error) {
	items := Items(value)
	if len(items) == 0 {
		return nil, invalid("成员角色不能为空")
	}
	roles := make([]string, 0, len(items))
	for _, item := range items {
		role, ok := item.(string)
		if !ok || !Has(Roles, role) || Has(roles, role) {
			return nil, invalid("角色必须是互不重复的有效角色列表")
		}
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles, nil
}

func sameRoles(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, role := range a {
		if !Has(b, role) {
			return false
		}
	}
	return true
}

func membershipRevision(in M, current int, required bool) error {
	_, exists := in["expectedRevision"]
	if !exists && !required {
		return nil
	}
	if !exists || current < 1 || N(in, "expectedRevision") != float64(current) {
		return conflict("成员版本已变化，请刷新后重试")
	}
	return nil
}

func (t *Tx) protectLastOwner(member Resource) error {
	if member.Status != "active" || !Has(Strings(member.Data["roles"]), "owner") {
		return nil
	}
	members, err := t.List(member.ProjectID, "member", "")
	if err != nil {
		return err
	}
	for _, candidate := range members {
		if candidate.ID == member.ID || candidate.Status != "active" || !Has(Strings(candidate.Data["roles"]), "owner") {
			continue
		}
		identity, err := t.identityState(S(candidate.Data, "identityId"))
		if err == nil && !identity.Disabled {
			return nil
		}
		// Missing/invalid identity records do not qualify as replacement owners.
		// Database failures must still abort rather than authorize the change.
		if err != nil && !isUnavailableIdentity(err) {
			return err
		}
	}
	return conflict("项目必须保留至少一位已注册且启用的负责人，请先移交所有权")
}

func isUnavailableIdentity(err error) bool {
	var fault *Fault
	var syntax *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	return errors.Is(err, sql.ErrNoRows) || errors.As(err, &fault) || errors.As(err, &syntax) || errors.As(err, &typeError)
}

func (t *Tx) revokeProjectDelegation(identity, project string) error {
	rows, err := t.tx.Query(`SELECT hash,body FROM credentials WHERE identity_id=$1 AND kind IN ('token','preview')`, identity)
	if err != nil {
		return err
	}
	var hashes []string
	for rows.Next() {
		var hash, body string
		if err = rows.Scan(&hash, &body); err != nil {
			rows.Close()
			return err
		}
		var actor Actor
		if err = json.Unmarshal([]byte(body), &actor); err != nil {
			rows.Close()
			return err
		}
		if actor.ProjectID == project {
			hashes = append(hashes, hash)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, hash := range hashes {
		if err = t.RevokeCredential(hash); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) grantMember(t *Tx, a Actor, in M) (Resource, error) {
	project, identityID := S(in, "projectId"), S(in, "identityId")
	if err := human(a); err != nil {
		return Resource{}, err
	}
	if err := s.allowed(t, a, project, "owner"); err != nil {
		return Resource{}, err
	}
	p, err := t.Get(project)
	if err != nil || p.Kind != "project" {
		return Resource{}, missing()
	}
	identity, err := t.identityState(identityID)
	if err != nil || identity.Disabled {
		return Resource{}, invalid("成员必须是已注册且启用的账户")
	}
	roles, err := memberRoles(in["roles"])
	if err != nil {
		return Resource{}, err
	}
	members, err := t.List(project, "member", "")
	if err != nil {
		return Resource{}, err
	}
	m := resource("member", identity.Name, project, "", M{"identityId": identityID, "roles": roles})
	for _, existing := range members {
		if S(existing.Data, "identityId") != identityID {
			continue
		}
		if err = membershipRevision(in, existing.Revision, false); err != nil {
			return Resource{}, err
		}
		if existing.Status == "active" && sameRoles(roles, Strings(existing.Data["roles"])) {
			return existing, nil
		}
		if !Has(roles, "owner") {
			if err = t.protectLastOwner(existing); err != nil {
				return Resource{}, err
			}
		}
		m = existing
		m.Status = "active"
		m.Revision++
		m.Data["roles"] = roles
		m.Data["grantedBy"], m.Data["grantedAt"] = a.ID, now()
		if err = t.revokeProjectDelegation(identityID, project); err != nil {
			return Resource{}, err
		}
		return m, t.Put(m)
	}
	if err = membershipRevision(in, 0, false); err != nil {
		return Resource{}, err
	}
	m.Data["grantedBy"], m.Data["grantedAt"] = a.ID, now()
	return m, t.Put(m)
}

func (s *Service) revokeMember(t *Tx, a Actor, in M) (Resource, error) {
	if err := human(a); err != nil {
		return Resource{}, err
	}
	m, err := s.scoped(t, a, S(in, "id"), "member", "owner")
	if err != nil {
		return Resource{}, err
	}
	if err = membershipRevision(in, m.Revision, true); err != nil {
		return Resource{}, err
	}
	if err = validateText(S(in, "reason"), "移除原因", 2048); err != nil {
		return Resource{}, err
	}
	if m.Status != "active" {
		return Resource{}, conflict("成员已经移除，请刷新成员列表")
	}
	if err = t.protectLastOwner(m); err != nil {
		return Resource{}, err
	}
	return t.revokeMembership(a, m, S(in, "reason"))
}

func (t *Tx) revokeMembership(a Actor, m Resource, reason string) (Resource, error) {
	m.Status = "revoked"
	m.Revision++
	m.Data["revokedBy"], m.Data["revokedAt"], m.Data["revocationReason"] = a.ID, now(), reason
	if err := t.revokeProjectDelegation(S(m.Data, "identityId"), m.ProjectID); err != nil {
		return Resource{}, err
	}
	return m, t.Put(m)
}
