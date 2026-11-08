package platform

func (s *Service) teamGrantCommand(t *Tx, a Actor, command string, in M) (Resource, error) {
	if e := human(a); e != nil {
		return Resource{}, e
	}
	if e := teamRequest(in); e != nil {
		return Resource{}, e
	}
	if e := validateText(S(in, "reason"), "授权变更原因", 2048); e != nil {
		return Resource{}, e
	}
	var grant Resource
	var e error
	project, teamID := S(in, "projectId"), S(in, "teamId")
	if command == "teams.revoke" {
		grant, e = s.scoped(t, a, S(in, "id"), "team_grant", "owner")
		if e != nil {
			return Resource{}, e
		}
		project, teamID = grant.ProjectID, S(grant.Data, "teamId")
	} else {
		if e = s.allowed(t, a, project, "owner"); e != nil {
			return Resource{}, e
		}
		p, e := t.Get(project)
		if e != nil || p.Kind != "project" {
			return Resource{}, missing()
		}
		all, e := t.List(project, "team_grant", "")
		if e != nil {
			return Resource{}, e
		}
		for _, item := range all {
			if S(item.Data, "teamId") == teamID {
				grant = item
			}
		}
	}
	team, e := t.Get(teamID)
	if e != nil || team.Kind != "team" || team.ProjectID != "" {
		return Resource{}, missing()
	}
	payload := M{"command": command, "projectId": project, "teamId": team.ID, "expectedRevision": N(in, "expectedRevision"), "reason": S(in, "reason")}
	if command == "teams.grant" {
		roles, e := memberRoles(in["roles"])
		if e != nil {
			return Resource{}, e
		}
		payload["roles"], payload["expectedTeamRevision"] = roles, N(in, "expectedTeamRevision")
	}
	scope := command + ":" + project + ":" + team.ID
	if existing, ok, e := t.Once(scope, S(in, "key"), payload); e != nil || ok {
		return existing, e
	}
	if grant.ID != "" {
		if e = membershipRevision(in, grant.Revision, true); e != nil {
			return Resource{}, e
		}
	} else if N(in, "expectedRevision") != 0 {
		return Resource{}, conflict("团队授权版本已变化，请刷新后重试")
	}
	if command == "teams.grant" {
		if team.Status != "active" || N(in, "expectedTeamRevision") != float64(team.Revision) {
			return Resource{}, conflict("团队已停用或版本变化，请重新审阅成员后授权")
		}
		if grant.ID != "" && grant.Status == "active" && sameRoles(Strings(grant.Data["roles"]), Strings(payload["roles"])) {
			if e = t.Remember(scope, S(in, "key"), payload, grant); e != nil {
				return Resource{}, e
			}
			return grant, nil
		}
	} else if grant.Status != "active" {
		return Resource{}, conflict("团队授权已经撤销，请刷新后重试")
	}
	if grant.ID == "" {
		grant = resource("team_grant", team.Title, project, "", M{"teamId": team.ID})
		grant.ID = "team_grant_" + Hash([]string{project, team.ID})
	} else {
		grant.Revision++
		grant.Data = copyM(grant.Data)
	}
	grant.Status = "revoked"
	if command == "teams.grant" {
		grant.Status = "active"
		grant.Data["roles"], grant.Data["teamRevisionAtGrant"] = payload["roles"], team.Revision
	}
	grant.Data["changedBy"], grant.Data["reason"], grant.Data["changedAt"] = a.ID, S(in, "reason"), now()
	if e = t.Put(grant); e != nil {
		return Resource{}, e
	}
	for _, id := range Strings(team.Data["memberIds"]) {
		if e = t.revokeProjectDelegation(id, project); e != nil {
			return Resource{}, e
		}
	}
	history := resource("team_grant_revision", team.Title, project, "", M{"grantId": grant.ID, "teamId": team.ID, "grantRevision": grant.Revision, "snapshot": copyM(grant.Data), "status": grant.Status})
	history.ID, history.Status, history.Revision = "team_grant_revision_"+Hash([]any{grant.ID, grant.Revision}), "recorded", grant.Revision
	history.Data["binding"] = Hash(history.Data)
	if e = t.Put(history); e != nil {
		return Resource{}, e
	}
	if e = t.Remember(scope, S(in, "key"), payload, grant); e != nil {
		return Resource{}, e
	}
	return grant, nil
}
