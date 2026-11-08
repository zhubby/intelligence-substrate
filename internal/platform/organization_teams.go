package platform

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func teamAdministrator(a Actor) error {
	if a.Kind != "human" || !a.Admin {
		return denied()
	}
	return nil
}

func teamMemberIDs(value any) ([]string, error) {
	switch value.(type) {
	case []string, []any:
	default:
		return nil, invalid("团队成员必须是完整的账户 ID 列表")
	}
	items := Items(value)
	if len(items) > 1000 {
		return nil, invalid("每个团队最多 1000 位成员")
	}
	ids := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		id, ok := item.(string)
		if !ok || !IDPattern.MatchString(id) || seen[id] {
			return nil, invalid("团队成员必须是互不重复的有效账户 ID")
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (t *Tx) enabledTeamMembers(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return []string{}, nil
	}
	marks, args := make([]string, len(ids)), make([]any, len(ids))
	for i, id := range ids {
		marks[i], args[i] = fmt.Sprintf("$%d", i+1), id
	}
	rows, e := t.tx.Query(`SELECT id,body FROM identities WHERE id IN (`+strings.Join(marks, ",")+`)`, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id, body string
		if e = rows.Scan(&id, &body); e != nil {
			return nil, e
		}
		var identity identityState
		if e = json.Unmarshal([]byte(body), &identity); e != nil {
			return nil, e
		}
		if identity.ID == id && !identity.Disabled {
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result, rows.Err()
}

func (t *Tx) validateTeamMembers(ids []string) error {
	enabled, e := t.enabledTeamMembers(ids)
	if e != nil {
		return e
	}
	if len(enabled) != len(ids) {
		return invalid("团队成员必须是已注册且启用的账户；请移除失效账户后重试")
	}
	return nil
}

func teamRequest(in M) error {
	if e := validateText(S(in, "key"), "请求标识", 128); e != nil {
		return e
	}
	return noSecrets(in)
}

func (s *Service) teamCommand(t *Tx, a Actor, command string, in M) (Resource, error) {
	if command == "teams.grant" || command == "teams.revoke" {
		return s.teamGrantCommand(t, a, command, in)
	}
	if e := teamAdministrator(a); e != nil {
		return Resource{}, e
	}
	if e := teamRequest(in); e != nil {
		return Resource{}, e
	}
	var previous Resource
	var e error
	if command != "teams.create" {
		previous, e = t.Get(S(in, "id"))
		if e != nil || previous.Kind != "team" || previous.ProjectID != "" {
			return Resource{}, missing()
		}
		if e = validateText(S(in, "reason"), "变更原因", 2048); e != nil {
			return Resource{}, e
		}
	}
	payload := M{"command": command, "id": previous.ID, "expectedRevision": N(in, "expectedRevision"), "reason": S(in, "reason")}
	if command == "teams.create" || command == "teams.update" {
		if e = validateText(S(in, "title"), "团队名称", 160); e != nil {
			return Resource{}, e
		}
		if value, exists := in["description"]; exists {
			if text, ok := value.(string); !ok || len(text) > 4000 {
				return Resource{}, invalid("团队说明最多 4000 字节")
			}
		}
		ids, e := teamMemberIDs(in["memberIds"])
		if e != nil {
			return Resource{}, e
		}
		payload["title"], payload["description"], payload["memberIds"] = S(in, "title"), S(in, "description"), ids
	}
	scope := command + ":" + previous.ID
	if command == "teams.create" {
		scope += a.ID
	}
	if existing, ok, e := t.Once(scope, S(in, "key"), payload); e != nil || ok {
		return existing, e
	}
	if previous.ID != "" {
		if e = membershipRevision(in, previous.Revision, true); e != nil {
			return Resource{}, e
		}
	}
	next := previous
	if command == "teams.create" {
		next = resource("team", S(in, "title"), "", "", M{"createdBy": a.ID})
	}
	next.Data = copyM(next.Data)
	switch command {
	case "teams.create", "teams.update":
		ids := Strings(payload["memberIds"])
		if e = t.validateTeamMembers(ids); e != nil {
			return Resource{}, e
		}
		next.Title = S(in, "title")
		next.Data["description"], next.Data["memberIds"] = S(in, "description"), ids
	case "teams.disable":
		next.Status = "disabled"
	case "teams.enable":
		if e = t.validateTeamMembers(Strings(previous.Data["memberIds"])); e != nil {
			return Resource{}, e
		}
		next.Status = "active"
	default:
		return Resource{}, missing()
	}
	changed := previous.ID == "" || previous.Title != next.Title || previous.Status != next.Status || Hash(previous.Data) != Hash(next.Data)
	if changed {
		if previous.ID != "" {
			next.Revision++
		}
		next.Data["updatedBy"], next.Data["reason"] = a.ID, S(in, "reason")
		if e = t.Put(next); e != nil {
			return Resource{}, e
		}
		history := resource("team_revision", next.Title, "", "", M{"teamId": next.ID, "teamRevision": next.Revision, "snapshot": M{"title": next.Title, "status": next.Status, "description": next.Data["description"], "memberIds": next.Data["memberIds"]}, "changedBy": a.ID, "reason": S(in, "reason")})
		history.ID, history.Status, history.Revision = "team_revision_"+Hash([]any{next.ID, next.Revision}), "recorded", next.Revision
		history.Data["binding"] = Hash(history.Data)
		if e = t.Put(history); e != nil {
			return Resource{}, e
		}
		if e = t.changedTeamAuthority(a, previous, next); e != nil {
			return Resource{}, e
		}
	}
	if e = t.Remember(scope, S(in, "key"), payload, next); e != nil {
		return Resource{}, e
	}
	return next, nil
}

func (t *Tx) changedTeamAuthority(a Actor, before, after Resource) error {
	old, next := []string{}, []string{}
	if before.Status == "active" {
		old = Strings(before.Data["memberIds"])
	}
	if after.Status == "active" {
		next = Strings(after.Data["memberIds"])
	}
	affected := []string{}
	for _, id := range old {
		if !Has(next, id) {
			affected = append(affected, id)
		}
	}
	for _, id := range next {
		if !Has(old, id) {
			affected = append(affected, id)
		}
	}
	grants, e := t.List("", "team_grant", "")
	if e != nil {
		return e
	}
	for _, grant := range grants {
		if grant.Status != "active" || S(grant.Data, "teamId") != after.ID {
			continue
		}
		for _, id := range affected {
			if e = t.revokeProjectDelegation(id, grant.ProjectID); e != nil {
				return e
			}
		}
		if e = t.Audit(a, "teams.access.changed", grant, M{"teamId": after.ID, "teamRevision": after.Revision, "affectedIdentityIds": affected, "reason": S(after.Data, "reason")}); e != nil {
			return e
		}
	}
	return nil
}
