package platform

// Repair records are server-owned consistency bindings, not signatures. Every
// signing attempt keeps its own durable intent and operation key.
func signingRepairRecipe(data M) string {
	m := copyM(data)
	for _, key := range []string{"attempts", "reason", "sourceBinding", "sourceConflict", "sourceRecoveryId", "sourceRecoveryIds", "signingInput", "repositoryId", "commit", "artifactId", "qualityDispatchId"} {
		delete(m, key)
	}
	if S(data, "signingMode") == "macos-pkg" {
		for _, key := range macOSSigningExecutionFields {
			delete(m, key)
		}
	}
	return Hash(m)
}

// Android version 1 records stay byte-for-byte compatible. Native version 2
// records use a separate collection; a defect can never mix the authorities.
func signingRepairField(mode string) string {
	if Has([]string{"android-apk", "android-aab"}, mode) {
		return "androidSigningRepairs"
	}
	return "signingRepairs"
}

func signingRepairItems(defect Resource) ([]any, error) {
	legacy, native := Items(defect.Data["androidSigningRepairs"]), Items(defect.Data["signingRepairs"])
	if len(legacy) > 0 && len(native) > 0 {
		return nil, conflict("签名修复不能混用不同版本的权威记录")
	}
	if len(native) > 0 {
		return native, nil
	}
	return legacy, nil
}

func signingRepairDigest(record M) string {
	m := copyM(record)
	delete(m, "binding")
	delete(m, "continuation")
	return Hash(m)
}

func signingRepairRecord(defect Resource, id string) (M, int, error) {
	items, err := signingRepairItems(defect)
	if err != nil {
		return nil, 0, err
	}
	if len(items) == 0 || len(items) > 2 || id == "" {
		return nil, 0, conflict("签名修复缺少有界权威记录")
	}
	var found M
	index := 0
	for i, item := range items {
		r := Obj(item)
		if len(Items(defect.Data["signingRepairs"])) > 0 {
			if N(r, "version") != 2 || !Has([]string{"macos-pkg", "ios-ipa", "windows-msix"}, S(r, "signingMode")) || controlledSigningMode(S(r, "target")) != S(r, "signingMode") {
				return nil, 0, conflict("签名修复记录的版本、模式或目标无效")
			}
		} else if N(r, "version") != 1 {
			return nil, 0, conflict("Android签名修复记录版本无效")
		}
		if S(r, "id") == id {
			if found != nil || N(r, "round") != float64(i+1) || S(r, "binding") != signingRepairDigest(r) {
				return nil, 0, conflict("签名修复记录不唯一或摘要已变化")
			}
			found, index = r, i+1
		}
	}
	if found == nil {
		return nil, 0, conflict("签名修复记录不存在")
	}
	return found, index, nil
}

// Compare original root tasks to the approved plan, including its normalized
// defaults. Repository/commit may be derived from successful source ancestors;
// explicitly declared source selectors must still match the approved recipe.
func approvedSigningRepairRecipe(plan, g, task Resource) error {
	var recipe M
	for _, item := range Items(plan.Data["tasks"]) {
		m := Obj(item)
		if S(m, "id") == S(task.Data, "logicalId") {
			recipe = copyM(m)
		}
	}
	if recipe == nil || S(task.Data, "signingRepairId") != "" || S(task.Data, "defectId") != "" {
		return conflict("签名修复必须使用原批准的构建与签名配方")
	}
	recipe["planId"], recipe["requirementRevision"], recipe["logicalId"] = plan.ID, g.Revision, S(recipe, "id")
	recipe["maxAttempts"] = 3
	if N(recipe, "timeoutSeconds") == 0 {
		recipe["timeoutSeconds"] = 900
	}
	if N(recipe, "budgetCents") == 0 {
		recipe["budgetCents"] = int64(N(g.Data, "budgetCents")) / int64(len(Items(plan.Data["tasks"])))
	}
	if signingRepairRecipe(recipe) != signingRepairRecipe(task.Data) || (S(recipe, "repositoryId") != "" && S(recipe, "repositoryId") != S(task.Data, "repositoryId")) {
		return conflict("原构建或签名配方已偏离批准方案")
	}
	return nil
}

func (s *Service) startSigningRepair(t *Tx, run, test, artifact, plan Resource, repair *Resource, defect *Resource) error {
	mode := S(artifact.Data, "signingMode")
	if mode == "" || mode != controlledSigningMode(S(artifact.Data, "target")) {
		return nil
	}
	if S(test.Data, "qualityPolicyId") == "" {
		return conflict("签名包自动修复需要明确批准的自动质量绑定")
	}
	if err := s.validateQualityTask(t, test); err != nil {
		return err
	}
	g, err := t.Get(run.GoalID)
	if err != nil {
		return err
	}
	signedRun, err := t.Get(S(artifact.Data, "runId"))
	if err != nil {
		return err
	}
	sign, err := t.Get(S(signedRun.Data, "taskId"))
	if err != nil {
		return err
	}
	unsigned, err := t.Get(S(Obj(sign.Data["signingInput"]), "producerTaskId"))
	if err != nil {
		return err
	}
	if previous := S(sign.Data, "signingRepairId"); previous != "" {
		record, _, e := signingRepairRecord(*defect, previous)
		if e != nil {
			return e
		}
		sign, err = t.Get(S(record, "rootSigningTaskId"))
		if err != nil {
			return err
		}
		unsigned, err = t.Get(S(record, "rootUnsignedTaskId"))
		if err != nil {
			return err
		}
	}
	for _, root := range []Resource{unsigned, sign} {
		if root.ProjectID != run.ProjectID || root.GoalID != run.GoalID || revision(root) != revision(run) || root.Status != "succeeded" || S(root.Data, "planId") != plan.ID {
			return conflict("签名修复原配方来源不属于当前批准目标")
		}
		if err = approvedSigningRepairRecipe(plan, g, root); err != nil {
			return err
		}
	}
	if !controlledSigningTask(sign) || S(sign.Data, "signingMode") != mode || S(unsigned.Data, "artifactStage") != "unsigned" || len(Strings(unsigned.Data["command"])) == 0 || S(unsigned.Data, "outputPath") == "" || S(sign.Data, "inputArtifactFrom") != S(unsigned.Data, "logicalId") || S(sign.Data, "target") != S(artifact.Data, "target") || S(unsigned.Data, "target") != S(sign.Data, "target") {
		return conflict("签名修复缺少原批准的无签名构建与签名转换")
	}
	items, err := signingRepairItems(*defect)
	if err != nil {
		return err
	}
	if len(items) >= 2 || len(items) != int(N(defect.Data, "repairAttempts")) {
		return conflict("签名修复轮次不一致或达到上限")
	}
	id := NewID("signrepair")
	repair.Data["signingRepairId"] = id
	record := M{"version": 1, "id": id, "round": len(items) + 1, "projectId": run.ProjectID, "goalId": run.GoalID, "planId": plan.ID, "requirementRevision": revision(run), "planTasksDigest": Hash(plan.Data["tasks"]), "policyId": S(test.Data, "qualityPolicyId"), "policyDigest": S(test.Data, "qualityPolicyDigest"), "failedTestTaskId": test.ID, "failedTestRecipeDigest": signingRepairRecipe(test.Data), "failedRunId": run.ID, "failedArtifactId": artifact.ID, "failedArtifactDigest": Hash(artifact.Data), "rootUnsignedTaskId": unsigned.ID, "rootUnsignedRecipeDigest": signingRepairRecipe(unsigned.Data), "rootSigningTaskId": sign.ID, "rootSigningRecipeDigest": signingRepairRecipe(sign.Data), "repairTaskId": repair.ID, "repairRecipeDigest": signingRepairRecipe(repair.Data), "expectedSigner": S(sign.Data, "expectedSigner")}
	if !androidSigningTask(sign) {
		record["version"], record["signingMode"], record["target"] = 2, mode, S(sign.Data, "target")
		for _, key := range []string{"expectedApplicationSigner", "expectedTeamId", "expectedProvisioningProfile", "expectedWindowsPublisher", "expectedWindowsPackage", "expectedWindowsVersion"} {
			if value, exists := sign.Data[key]; exists {
				record[key] = value
			}
		}
	}
	record["binding"] = signingRepairDigest(record)
	defect.Data[signingRepairField(mode)] = append(items, record)
	return nil
}

func (s *Service) validateSigningRepair(t *Tx, task Resource) error {
	id := S(task.Data, "signingRepairId")
	if id == "" && S(task.Data, "defectId") == "" {
		return nil
	}
	defect, err := t.Get(S(task.Data, "defectId"))
	if err != nil {
		return err
	}
	if id == "" {
		items, e := signingRepairItems(defect)
		if e != nil {
			return e
		}
		if len(items) > 0 {
			return conflict("签名修复工作项缺少权威记录关联")
		}
		return nil
	}
	r, round, err := signingRepairRecord(defect, id)
	if err != nil {
		return err
	}
	g, err := t.Get(task.GoalID)
	if err != nil {
		return err
	}
	if defect.Kind != "defect" || defect.ProjectID != task.ProjectID || defect.GoalID != task.GoalID || revision(defect) != revision(task) || S(r, "projectId") != task.ProjectID || S(r, "goalId") != task.GoalID || S(r, "planId") != S(task.Data, "planId") || N(r, "requirementRevision") != float64(revision(task)) || checkRevision(g, task) != nil || S(g.Data, "activePlanId") != S(r, "planId") {
		return conflict("签名修复需求、方案或权限范围已变化")
	}
	policy, err := t.Get(S(r, "policyId"))
	if err != nil {
		return err
	}
	if _, err = s.validQualityPolicy(t, policy, g); err != nil {
		return err
	}
	plan, err := t.Get(S(r, "planId"))
	if err != nil || Hash(plan.Data["tasks"]) != S(r, "planTasksDigest") || S(policy.Data, "binding") != S(r, "policyDigest") {
		return conflict("签名修复批准配方或质量绑定已变化")
	}
	for _, pair := range [][2]string{{"rootUnsignedTaskId", "rootUnsignedRecipeDigest"}, {"rootSigningTaskId", "rootSigningRecipeDigest"}, {"repairTaskId", "repairRecipeDigest"}, {"failedTestTaskId", "failedTestRecipeDigest"}} {
		other, e := t.Get(S(r, pair[0]))
		if e != nil || other.Kind != "task" || other.ProjectID != task.ProjectID || other.GoalID != task.GoalID || revision(other) != revision(task) || S(other.Data, "planId") != plan.ID || signingRepairRecipe(other.Data) != S(r, pair[1]) {
			return conflict("签名修复原配方、修复配方或失败验证已变化")
		}
		if pair[0] == "rootSigningTaskId" && ((N(r, "version") == 1 && !androidSigningTask(other)) || (N(r, "version") == 2 && (S(r, "signingMode") != S(other.Data, "signingMode") || S(r, "target") != S(other.Data, "target")))) {
			return conflict("签名修复记录与原批准签名目标不一致")
		}
	}
	failed, err := t.Get(S(r, "failedArtifactId"))
	if err != nil || Hash(failed.Data) != S(r, "failedArtifactDigest") {
		return conflict("签名修复失败制品来源已变化")
	}
	failedRun, err := t.Get(S(r, "failedRunId"))
	if err != nil || failedRun.Status != "failed" || S(failedRun.Data, "taskId") != S(r, "failedTestTaskId") || failedRun.ProjectID != task.ProjectID || failedRun.GoalID != task.GoalID || revision(failedRun) != revision(task) {
		return conflict("签名修复缺少准确失败执行")
	}
	test, _ := t.Get(S(r, "failedTestTaskId"))
	if S(test.Data, "artifactId") != failed.ID || S(test.Data, "qualityPolicyId") != policy.ID || S(test.Data, "qualityPolicyDigest") != S(r, "policyDigest") || signingRepairRecipe(Obj(failedRun.Data["task"])) != S(r, "failedTestRecipeDigest") {
		return conflict("签名修复失败执行没有绑定原质量策略和准确制品")
	}
	// Enforce a strictly earlier round before recursively validating a previous
	// signed artifact. Two rounds bound the complete provenance walk.
	signedRun, err := t.Get(S(failed.Data, "runId"))
	if err != nil {
		return err
	}
	producer, err := t.Get(S(signedRun.Data, "taskId"))
	if err != nil {
		return err
	}
	if previous := S(producer.Data, "signingRepairId"); previous != "" {
		_, earlier, e := signingRepairRecord(defect, previous)
		if e != nil || earlier != round-1 || S(producer.Data, "defectId") != defect.ID {
			return conflict("签名修复来源轮次循环或不连续")
		}
	} else if round != 1 {
		return conflict("签名修复没有使用上一轮失败包")
	}
	if err = s.validateQualityTask(t, test); err != nil {
		return err
	}
	if task.ID == S(r, "repairTaskId") {
		if S(task.Data, "repositoryId") != S(failed.Data, "repositoryId") || S(task.Data, "commit") != S(failed.Data, "commit") {
			return conflict("开发修复必须从准确失败制品源码开始")
		}
		return nil
	}
	c := Obj(r["continuation"])
	content := copyM(c)
	delete(content, "binding")
	if len(c) == 0 || S(c, "binding") != Hash(content) {
		return conflict("签名修复缺少准确构建与签名延续")
	}
	key := ""
	for _, k := range []string{"unsigned", "signing", "verification"} {
		if S(c, k+"TaskId") == task.ID {
			key = k
		}
	}
	if key == "" || signingRepairRecipe(task.Data) != S(c, key+"RecipeDigest") {
		return conflict("签名修复延续配方与权威记录不一致")
	}
	repair, _ := t.Get(S(r, "repairTaskId"))
	runs, err := t.List(task.ProjectID, "run", task.GoalID)
	if err != nil {
		return err
	}
	source, err := s.sourceProducer(t, repair, runs)
	if err != nil || repair.Status != "succeeded" || S(source, "runId") != S(c, "repairRunId") || S(source, "commit") != S(c, "commit") || S(source, "repositoryId") != S(c, "repositoryId") {
		return conflict("签名修复延续没有使用可信修复执行的准确提交")
	}
	if (S(task.Data, "repositoryId") != "" && S(task.Data, "repositoryId") != S(c, "repositoryId")) || (S(task.Data, "commit") != "" && S(task.Data, "commit") != S(c, "commit")) {
		return conflict("签名修复构建、签名或验证源码已变化")
	}
	if key == "unsigned" && (S(task.Data, "repositoryId") == "" || S(task.Data, "commit") == "") {
		return conflict("无签名修复构建缺少准确源码")
	}
	return nil
}

func (s *Service) afterSigningRepair(t *Tx, run, task Resource, result M, defect *Resource) error {
	r, _, err := signingRepairRecord(*defect, S(task.Data, "signingRepairId"))
	if err != nil {
		return err
	}
	if _, exists := r["continuation"]; exists {
		return conflict("签名修复延续已生成")
	}
	unsigned, err := t.Get(S(r, "rootUnsignedTaskId"))
	if err != nil {
		return err
	}
	sign, err := t.Get(S(r, "rootSigningTaskId"))
	if err != nil {
		return err
	}
	test, err := t.Get(S(r, "failedTestTaskId"))
	if err != nil {
		return err
	}
	reset := func(original Resource, prefix, title string) Resource {
		data := copyM(original.Data)
		for _, key := range []string{"sourceBinding", "sourceConflict", "sourceFrom", "sourceMode", "signingInput", "artifactId", "qualityDispatchId", "requiresArtifactFrom", "repositoryId", "commit", "branch", "reason"} {
			delete(data, key)
		}
		if macOSSigningTask(original) {
			for _, key := range macOSSigningExecutionFields {
				delete(data, key)
			}
		}
		logical := NewID(prefix)
		data["id"], data["logicalId"], data["attempts"] = logical, logical, 0
		data["defectId"], data["signingRepairId"] = defect.ID, S(r, "id")
		next := resource("task", title+original.Title, task.ProjectID, task.GoalID, data)
		next.Status = "pending"
		return next
	}
	build := reset(unsigned, "repairunsigned", "修复后无签名构建：")
	build.Data["repositoryId"], build.Data["commit"] = S(result, "repositoryId"), S(result, "commit")
	build.Data["dependsOn"] = []string{S(task.Data, "logicalId")}
	signed := reset(sign, "repairsign", "修复后受控签名：")
	signed.Data["dependsOn"], signed.Data["inputArtifactFrom"] = []string{S(build.Data, "logicalId")}, S(build.Data, "logicalId")
	verify := reset(test, "reverify", "签名包重新验证：")
	verify.Data["dependsOn"], verify.Data["requiresArtifactFrom"] = []string{S(signed.Data, "logicalId")}, signed.ID
	c := M{"repairRunId": run.ID, "repositoryId": S(result, "repositoryId"), "commit": S(result, "commit"), "unsignedTaskId": build.ID, "unsignedRecipeDigest": signingRepairRecipe(build.Data), "signingTaskId": signed.ID, "signingRecipeDigest": signingRepairRecipe(signed.Data), "verificationTaskId": verify.ID, "verificationRecipeDigest": signingRepairRecipe(verify.Data)}
	c["binding"] = Hash(c)
	r["continuation"] = c
	for _, next := range []Resource{build, signed, verify} {
		if err = t.Put(next); err != nil {
			return err
		}
	}
	defect.Status = "awaiting_verification"
	defect.Data["repairCommit"], defect.Data["buildTaskId"], defect.Data["signingTaskId"], defect.Data["verificationTaskId"] = S(result, "commit"), build.ID, signed.ID, verify.ID
	return t.Put(*defect)
}
