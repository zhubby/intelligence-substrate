package platform

func releaseIssueEntry(issue, g Resource) (M, error) {
	source, triage := Obj(issue.Data["source"]), Obj(issue.Data["triage"])
	if issue.ProjectID != g.ProjectID || issue.GoalID != g.ID || issue.Status == "open" || S(issue.Data, "binding") != Hash(source) || len(triage) == 0 || S(triage, "sourceBinding") != S(issue.Data, "binding") || S(issue.Data, "triageBinding") != Hash(triage) || S(triage, "strategy") != "new-build" || int(N(triage, "revision")) != issue.Revision || int(N(triage, "requirementRevision")) != g.Revision {
		return nil, conflict("当前范围的拒审问题需要有权限的人确认修复要求，再重新规划")
	}
	return copyM(M{"id": issue.ID, "source": source, "sourceBinding": S(issue.Data, "binding"), "triage": triage, "triageBinding": S(issue.Data, "triageBinding")}), nil
}

// Stable planning data deliberately excludes status, candidate IDs and progress.
// A removed channel stays in history, and does not silently become resolved.
func releaseIssueSnapshot(tx *Tx, g Resource) ([]any, error) {
	issues, err := tx.List(g.ProjectID, "release_issue", g.ID)
	if err != nil {
		return nil, err
	}
	entries := []any{}
	for _, issue := range issues {
		if issue.Status == "resolved" || !Has(Strings(g.Data["targets"]), S(Obj(issue.Data["source"]), "target")) {
			continue
		}
		entry, err := releaseIssueEntry(issue, g)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if len(entries) > 100 {
		return nil, conflict("当前目标超过 100 条未解决拒审问题，需要人工整理范围")
	}
	return entries, nil
}

func (s *Service) validatePlanReleaseIssues(tx *Tx, g Resource, raw any) error {
	current, err := releaseIssueSnapshot(tx, g)
	if err != nil {
		return err
	}
	if Hash(Items(raw)) != Hash(current) {
		return conflict("拒审修复上下文已变化，请按当前问题重新规划")
	}
	for _, entry := range current {
		issue, err := tx.Get(S(Obj(entry), "id"))
		if err != nil {
			return err
		}
		if err = s.validateReleaseIssueSource(tx, issue); err != nil {
			return err
		}
	}
	return nil
}

func candidateReleaseIssues(tx *Tx, plan Resource) ([]any, error) {
	entries := []any{}
	for _, entry := range Items(plan.Data["releaseIssues"]) {
		issue, err := tx.Get(S(Obj(entry), "id"))
		if err != nil {
			return nil, err
		}
		if issue.Status != "resolved" {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func (s *Service) releaseIssueGate(tx *Tx, r, g, plan Resource) error {
	entries := Items(r.Data["releaseIssues"])
	planned := map[string]any{}
	for _, entry := range Items(plan.Data["releaseIssues"]) {
		planned[S(Obj(entry), "id")] = entry
	}
	if len(entries) > 0 {
		approvals, err := tx.List(g.ProjectID, "approval", g.ID)
		if err != nil {
			return err
		}
		approved := false
		for _, approval := range approvals {
			if approval.Status != "stale" && S(approval.Data, "type") == "plan" && S(approval.Data, "planId") == plan.ID && S(approval.Data, "binding") == S(plan.Data, "binding") && Hash(Items(approval.Data["releaseIssues"])) == Hash(Items(plan.Data["releaseIssues"])) {
				approved = true
			}
		}
		if !approved {
			return conflict("修复候选缺少绑定拒审问题的方案批准")
		}
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		id := S(Obj(entry), "id")
		if seen[id] || Hash(planned[id]) != Hash(entry) {
			return conflict("修复候选与批准方案的问题快照不一致")
		}
		seen[id] = true
		issue, err := tx.Get(id)
		if err != nil {
			return err
		}
		current, err := releaseIssueEntry(issue, g)
		if err != nil {
			return err
		}
		if Hash(entry) != Hash(current) || (issue.Status == "resolved" && S(Obj(issue.Data["resolution"]), "releaseId") != r.ID) {
			return conflict("修复要求或解决版本已变化，请重新创建候选")
		}
		if err = s.validateReleaseIssueSource(tx, issue); err != nil {
			return err
		}
		source := Obj(issue.Data["source"])
		target := releaseTarget(r, S(source, "target"))
		if target == nil || r.ID == S(source, "releaseId") || S(r.Data, "commit") == S(source, "commit") {
			return conflict("拒审修复必须使用新的后继提交和候选版本")
		}
		pkg, err := tx.Get(S(target, "artifactId"))
		if err != nil {
			return err
		}
		if S(pkg.Data, "repositoryId") != S(source, "repositoryId") || pkg.ID == S(source, "artifactId") || S(pkg.Data, "digest") == S(source, "artifactDigest") {
			return conflict("拒审修复需要原仓库产生的新制品字节")
		}
		repo, err := tx.Get(S(source, "repositoryId"))
		if err != nil {
			return err
		}
		if _, err = s.Git(repo, "merge-base", "--is-ancestor", S(source, "commit"), S(r.Data, "commit")); err != nil {
			return conflict("修复提交必须是原拒审提交的后继")
		}
	}
	issues, err := tx.List(g.ProjectID, "release_issue", g.ID)
	if err != nil {
		return err
	}
	for _, issue := range issues {
		source := Obj(issue.Data["source"])
		if issue.Status != "resolved" && Has(Strings(g.Data["targets"]), S(source, "target")) && S(source, "releaseId") != r.ID && !seen[issue.ID] {
			return conflict("还有拒审问题未纳入当前批准方案，请先确认修复要求并重新规划")
		}
	}
	return nil
}

// Called only after releases.verify has validated the independent, exact-target
// publication evidence. Publisher observations and Agent tools cannot call it.
func (s *Service) resolveReleaseIssues(tx *Tx, r Resource, target string, evidence Resource) error {
	for _, entry := range Items(r.Data["releaseIssues"]) {
		if S(Obj(Obj(entry)["source"]), "target") != target {
			continue
		}
		issue, err := tx.Get(S(Obj(entry), "id"))
		if err != nil {
			return err
		}
		if issue.Status == "resolved" {
			continue
		}
		pkg, err := tx.Get(S(releaseTarget(r, target), "artifactId"))
		if err != nil {
			return err
		}
		issue.Status = "resolved"
		issue.Data["resolution"] = M{"releaseId": r.ID, "releaseBinding": S(r.Data, "binding"), "artifactId": pkg.ID, "artifactDigest": S(pkg.Data, "digest"), "commit": S(r.Data, "commit"), "evidenceId": evidence.ID, "externalId": S(evidence.Data, "externalId"), "actorId": S(evidence.Data, "actorId"), "at": now()}
		if err = tx.Put(issue); err != nil {
			return err
		}
		if err = tx.Audit(Actor{ID: S(evidence.Data, "actorId")}, "release_issues.resolved", issue, Obj(issue.Data["resolution"])); err != nil {
			return err
		}
	}
	return nil
}
