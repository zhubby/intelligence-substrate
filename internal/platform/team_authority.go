package platform

import (
	"context"
	"sort"
)

// ProjectRoles is evaluated inside the caller's governed transaction. Delegated
// actors keep their explicitly scoped roles; only humans inherit team grants.
func (t *Tx) ProjectRoles(a Actor, project string) ([]string, error) {
	if a.ProjectID != "" {
		return a.Roles, nil
	}
	members, e := t.List(project, "member", "")
	if e != nil {
		return nil, e
	}
	roles := []string{}
	for _, member := range members {
		if S(member.Data, "identityId") == a.ID && member.Status == "active" {
			roles = append(roles, Strings(member.Data["roles"])...)
		}
	}
	if a.Kind != "human" {
		return roles, nil
	}
	identity, e := t.identityState(a.ID)
	if e != nil {
		if isUnavailableIdentity(e) {
			// Existing trusted in-process callers can have direct membership without
			// a persisted identity. Such callers never acquire inherited authority.
			return roles, nil
		}
		return nil, e
	}
	if identity.Disabled {
		return []string{}, nil
	}
	grants, e := t.List(project, "team_grant", "")
	if e != nil {
		return nil, e
	}
	for _, grant := range grants {
		if grant.Status != "active" {
			continue
		}
		team, e := t.Get(S(grant.Data, "teamId"))
		if e != nil {
			return nil, e
		}
		if team.Kind != "team" || team.ProjectID != "" || team.Status != "active" || !Has(Strings(team.Data["memberIds"]), a.ID) {
			continue
		}
		inherited, e := memberRoles(grant.Data["roles"])
		if e != nil {
			return nil, e
		}
		for _, role := range inherited {
			if !Has(roles, role) {
				roles = append(roles, role)
			}
		}
	}
	sort.Strings(roles)
	return roles, nil
}

func (s *Service) Teams(ctx context.Context, a Actor, project string) (result []Resource, err error) {
	result = []Resource{}
	err = s.Store.Update(ctx, func(t *Tx) error {
		var e error
		a, e = t.CurrentActor(a)
		if e != nil {
			return e
		}
		if project == "" {
			if e = teamAdministrator(a); e != nil {
				return e
			}
		} else {
			if e = human(a); e != nil {
				return e
			}
			if e = t.RequireProjectAccess(a, project, "owner"); e != nil {
				return e
			}
			p, e := t.Get(project)
			if e != nil || p.Kind != "project" {
				return missing()
			}
		}
		teams, e := t.List("", "team", "")
		if e != nil {
			return e
		}
		for _, team := range teams {
			enabled, e := t.enabledTeamMembers(Strings(team.Data["memberIds"]))
			if e != nil {
				return e
			}
			team.Data = M{"description": S(team.Data, "description"), "memberCount": len(enabled), "recordedMemberCount": len(Strings(team.Data["memberIds"]))}
			result = append(result, team)
		}
		return nil
	})
	return
}

func (t *Tx) projectTeamView(grant Resource, a Actor) (Resource, error) {
	team, e := t.Get(S(grant.Data, "teamId"))
	if e != nil {
		return Resource{}, e
	}
	enabled, e := t.enabledTeamMembers(Strings(team.Data["memberIds"]))
	if e != nil {
		return Resource{}, e
	}
	grant.Title = team.Title
	grant.Data = copyM(grant.Data)
	grant.Data["teamStatus"], grant.Data["currentTeamRevision"], grant.Data["memberCount"] = team.Status, team.Revision, len(enabled)
	grant.Data["effectiveForCurrentActor"] = a.Kind == "human" && a.ProjectID == "" && grant.Status == "active" && team.Status == "active" && Has(enabled, a.ID)
	return grant, nil
}

// Global offboarding retains historical team membership but records the lost
// authority in each bound project, including people with no direct membership.
func (t *Tx) auditDisabledTeamIdentity(a Actor, identity string) error {
	grants, e := t.List("", "team_grant", "")
	if e != nil {
		return e
	}
	for _, grant := range grants {
		if grant.Status != "active" {
			continue
		}
		team, e := t.Get(S(grant.Data, "teamId"))
		if e != nil {
			return e
		}
		if team.Kind != "team" || team.Status != "active" || !Has(Strings(team.Data["memberIds"]), identity) {
			continue
		}
		if e = t.Audit(a, "teams.access.changed", grant, M{"teamId": team.ID, "teamRevision": team.Revision, "affectedIdentityIds": []string{identity}, "reason": "identity_disabled"}); e != nil {
			return e
		}
	}
	return nil
}
