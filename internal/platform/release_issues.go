package platform

// releaseBinding preserves the original format for projects without remediation.
func releaseBinding(r Resource) M {
	binding := M{"goalId": r.GoalID, "revision": revision(r), "commit": S(r.Data, "commit"), "artifacts": Items(r.Data["artifacts"]), "planId": S(r.Data, "planId")}
	for _, key := range []string{"qualityPolicies", "storeMetadata", "releaseIssues"} {
		if len(Items(r.Data[key])) > 0 {
			binding[key] = r.Data[key]
		}
	}
	return binding
}

// The caller persists the target and issue in the same transaction. Channel
// observations are evidence of rejection, not evidence of a successful release.
func (s *Service) recordReleaseRejection(tx *Tx, r Resource, target M, actor string) error {
	if id := S(target, "rejectionIssueId"); id != "" {
		previous, err := tx.Get(id)
		if err != nil {
			return err
		}
		if S(Obj(previous.Data["source"]), "materialRevisionId") == S(target, "materialRevisionId") {
			return conflict("拒审候选已有修复问题，不能重复提交")
		}
	}
	pkg, err := tx.Get(S(target, "artifactId"))
	if err != nil {
		return err
	}
	if S(r.Data, "binding") != Hash(releaseBinding(r)) || pkg.Kind != "artifact" || pkg.ProjectID != r.ProjectID || pkg.GoalID != r.GoalID || S(pkg.Data, "type") != "package" || S(pkg.Data, "commit") != S(r.Data, "commit") {
		return conflict("拒审来源与批准候选不一致")
	}
	history := Items(target["observedHistory"])
	source := copyM(M{"releaseId": r.ID, "releaseBinding": S(r.Data, "binding"), "requirementRevision": revision(r), "planId": S(r.Data, "planId"), "target": S(target, "target"), "artifactId": pkg.ID, "artifactDigest": S(pkg.Data, "digest"), "repositoryId": S(pkg.Data, "repositoryId"), "commit": S(pkg.Data, "commit"), "externalId": S(target, "externalId"), "reason": S(target, "reason"), "observation": history[len(history)-1]})
	for _, key := range []string{"operationKey", "materialRevisionId", "publicationBinding"} {
		if S(target, key) != "" {
			source[key] = target[key]
		}
	}
	if digest := releaseMetadataDigest(r, S(target, "target")); digest != "" {
		source["metadataDigest"] = digest
	}
	issue := resource("release_issue", "渠道拒审："+S(target, "target"), r.ProjectID, r.GoalID, M{"source": source, "binding": Hash(source), "requirementRevision": revision(r)})
	issue.Status = "open"
	if err = tx.Put(issue); err != nil {
		return err
	}
	target["rejectionIssueId"] = issue.ID
	target["rejectionIssueIds"] = append(Items(target["rejectionIssueIds"]), issue.ID)
	return tx.Audit(Actor{ID: actor}, "release_issues.opened", issue, M{"releaseId": r.ID, "target": S(target, "target"), "sourceBinding": S(issue.Data, "binding")})
}

func (s *Service) validateReleaseIssueSource(tx *Tx, issue Resource) error {
	source := Obj(issue.Data["source"])
	r, err := tx.Get(S(source, "releaseId"))
	if err != nil {
		return err
	}
	target := releaseTarget(r, S(source, "target"))
	if issue.Kind != "release_issue" || r.Kind != "release" || issue.ProjectID != r.ProjectID || issue.GoalID != r.GoalID || S(issue.Data, "binding") != Hash(source) || S(source, "releaseBinding") != S(r.Data, "binding") || S(r.Data, "binding") != Hash(releaseBinding(r)) || revision(issue) != revision(r) || revision(issue) != int(N(source, "requirementRevision")) || S(source, "planId") != S(r.Data, "planId") || target == nil || (S(target, "rejectionIssueId") != issue.ID && !Has(Strings(target["rejectionIssueIds"]), issue.ID)) || S(target, "artifactId") != S(source, "artifactId") {
		return conflict("拒审问题来源绑定无法核验")
	}
	observation := Obj(source["observation"])
	found := false
	for _, entry := range Items(target["observedHistory"]) {
		if S(Obj(entry), "to") == "rejected" && Hash(entry) == Hash(observation) {
			found = true
		}
	}
	if !found || S(observation, "reason") != S(source, "reason") || S(observation, "externalId") != S(source, "externalId") {
		return conflict("拒审问题缺少准确渠道观察")
	}
	pkg, err := tx.Get(S(source, "artifactId"))
	if err != nil {
		return err
	}
	if pkg.Kind != "artifact" || pkg.ProjectID != r.ProjectID || pkg.GoalID != r.GoalID || revision(pkg) != revision(r) || S(pkg.Data, "type") != "package" || S(pkg.Data, "target") != S(source, "target") || S(pkg.Data, "digest") != S(source, "artifactDigest") || S(pkg.Data, "commit") != S(source, "commit") || S(pkg.Data, "commit") != S(r.Data, "commit") || S(pkg.Data, "repositoryId") != S(source, "repositoryId") {
		return conflict("拒审问题的准确制品无法核验")
	}
	bound := false
	for _, entry := range Items(r.Data["artifacts"]) {
		m := Obj(entry)
		if S(m, "id") == pkg.ID && S(m, "target") == S(source, "target") && S(m, "digest") == S(source, "artifactDigest") {
			bound = true
		}
	}
	if !bound {
		return conflict("拒审制品不属于原候选")
	}
	return s.VerifyArtifact(pkg)
}

func (s *Service) triageReleaseIssue(tx *Tx, a Actor, in M) (Resource, error) {
	if err := human(a); err != nil {
		return Resource{}, err
	}
	issue, err := s.scoped(tx, a, S(in, "id"), "release_issue", "product", "architect", "release")
	if err != nil {
		return issue, err
	}
	for key := range in {
		if !Has([]string{"id", "expectedRevision", "expectedBinding", "key", "strategy", "instructions"}, key) {
			return issue, invalid("修复要求包含不支持的字段：" + key)
		}
	}
	if err = validateText(S(in, "key"), "幂等键", 160); err != nil {
		return issue, err
	}
	if err = s.validateReleaseIssueSource(tx, issue); err != nil {
		return issue, err
	}
	scope := "release-issue-triage:" + issue.ID + ":" + a.ID
	if previous, ok, err := tx.Once(scope, S(in, "key"), in); err != nil || ok {
		return previous, err
	}
	if issue.Status == "resolved" || N(in, "expectedRevision") != float64(issue.Revision) || S(in, "expectedBinding") != S(issue.Data, "binding") {
		return issue, conflict("拒审问题或修复要求已变化，请重新读取")
	}
	strategy := S(in, "strategy")
	if !Has([]string{"new-build", "store-text", "store-media"}, strategy) {
		return issue, invalid("修复策略必须为新构建、App Store 文字或截图资料修订")
	}
	if Has([]string{"store-text", "store-media"}, strategy) {
		source := Obj(issue.Data["source"])
		if S(source, "target") != "ios-appstore" || !digestPattern.MatchString(S(source, "operationKey")) || !digestPattern.MatchString(S(source, "metadataDigest")) || S(source, "externalId") == "" {
			return issue, conflict("资料修订需要 App Store 准确原操作与批准资料")
		}
	}
	if err = validateText(S(in, "instructions"), "修复要求", 12000); err != nil {
		return issue, err
	}
	if err = noSecrets(in); err != nil {
		return issue, err
	}
	g, err := tx.Get(issue.GoalID)
	if err != nil {
		return issue, err
	}
	if !Has(Strings(g.Data["targets"]), S(Obj(issue.Data["source"]), "target")) {
		return issue, conflict("原拒审渠道已移出当前需求范围，保留历史问题")
	}
	if err = approved(tx, g); err != nil {
		return issue, err
	}
	issue.Revision++
	triage := copyM(M{"sourceBinding": S(issue.Data, "binding"), "revision": issue.Revision, "requirementRevision": g.Revision, "strategy": strategy, "instructions": S(in, "instructions"), "actorId": a.ID, "at": now()})
	issue.Data["triage"], issue.Data["triageBinding"] = triage, Hash(triage)
	issue.Data["decisions"] = append(Items(issue.Data["decisions"]), triage)
	issue.Status = "triaged"
	if err = tx.Put(issue); err != nil {
		return issue, err
	}
	return issue, tx.Remember(scope, S(in, "key"), in, issue)
}
