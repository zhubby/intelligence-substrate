package platform

// A signing work item transforms an exact successful unsigned build. It never
// accepts a repository command or caller-selected credentials.
func androidSigningTask(task Resource) bool {
	return Has([]string{"android-apk", "android-aab"}, S(task.Data, "signingMode"))
}

func controlledSigningTask(task Resource) bool {
	return androidSigningTask(task) || Has([]string{"macos-pkg", "ios-ipa", "windows-msix"}, S(task.Data, "signingMode"))
}

func controlledSigningMode(target string) string {
	if target == "windows" {
		return "windows-msix"
	}
	if target == "macos" {
		return "macos-pkg"
	}
	if target == "ios-appstore" {
		return "ios-ipa"
	}
	return androidSigningMode(target)
}

func androidSigningMode(target string) string {
	if target == "android-google-play" {
		return "android-aab"
	}
	if androidAPKTarget(target) {
		return "android-apk"
	}
	return ""
}

func androidAPKTarget(target string) bool {
	return Has([]string{"android-huawei", "android-xiaomi", "android-oppo", "android-vivo"}, target)
}

func validateSigningTask(m M) error {
	if err := validateWindowsSigningTask(m); err != nil {
		return err
	}
	macOS := S(m, "target") == "macos"
	iOS := S(m, "target") == "ios-appstore"
	if _, exists := m["artifactStage"]; exists {
		if S(m, "artifactStage") != "unsigned" || S(m, "kind") != "build" || controlledSigningMode(S(m, "target")) == "" || S(m, "role") != "developer" || ((macOS || iOS) && S(m, "runtime") != "darwin") || (S(m, "target") == "windows" && S(m, "runtime") != "windows") {
			return invalid("无签名阶段仅用于受支持目标的原生开发构建")
		}
	}
	if _, exists := m["signingMode"]; !exists {
		for _, key := range []string{"inputArtifactFrom", "expectedSigner", "expectedApplicationSigner", "expectedTeamId", "expectedProvisioningProfile"} {
			if _, exists := m[key]; exists {
				return invalid("签名字段只能用于受控签名转换")
			}
		}
		return nil
	}
	if S(m, "signingMode") == "" || S(m, "signingMode") != controlledSigningMode(S(m, "target")) || S(m, "kind") != "build" || S(m, "role") != "release" || !digestPattern.MatchString(S(m, "expectedSigner")) || !IDPattern.MatchString(S(m, "inputArtifactFrom")) || !Has([]string{"linux", "darwin", "windows"}, S(m, "runtime")) {
		return invalid("需要明确的签名目标、发布角色、输入及证书")
	}
	if macOS {
		if S(m, "runtime") != "darwin" || !digestPattern.MatchString(S(m, "expectedApplicationSigner")) || !macOSTeamPattern.MatchString(S(m, "expectedTeamId")) {
			return invalid("macOS签名需要原生节点、应用证书及Developer ID团队")
		}
	} else if iOS {
		if S(m, "runtime") != "darwin" || !macOSTeamPattern.MatchString(S(m, "expectedTeamId")) || !digestPattern.MatchString(S(m, "expectedProvisioningProfile")) {
			return invalid("iOS签名需要原生节点、Distribution团队及准确App Store profile摘要")
		}
		if _, exists := m["expectedApplicationSigner"]; exists {
			return invalid("iOS只接受一份批准的Distribution应用证书")
		}
	} else {
		for _, key := range []string{"expectedApplicationSigner", "expectedTeamId"} {
			if _, exists := m[key]; exists {
				return invalid("非macOS签名不接受Apple身份字段")
			}
		}
	}
	if !iOS {
		if _, exists := m["expectedProvisioningProfile"]; exists {
			return invalid("App Store profile仅用于iOS签名")
		}
	}
	for _, key := range []string{"command", "outputPath", "artifactStage", "artifactId", "sourceMode", "sourceFrom", "branch"} {
		if _, exists := m[key]; exists {
			return invalid("签名转换不能运行仓库命令或自行选择制品与源码：" + key)
		}
	}
	return nil
}

func signingOnlyWorker(worker Resource) bool {
	roles, capabilities := Strings(worker.Data["roles"]), Strings(worker.Data["capabilities"])
	return worker.Kind == "worker" && !B(worker.Data, "trustedQuality") && len(roles) == 1 && roles[0] == "release" && len(capabilities) == 1 && capabilities[0] == "build"
}

func signingWorker(worker, task Resource) bool {
	return signingOnlyWorker(worker) && worker.ProjectID == task.ProjectID && worker.Status != "disabled" && S(worker.Data, "runtime") == S(task.Data, "runtime")
}

func (s *Service) resolveSigningInput(t *Tx, task Resource) (M, error) {
	if err := s.validateSigningRepair(t, task); err != nil {
		return nil, err
	}
	if !controlledSigningTask(task) {
		return nil, conflict("缺少受控签名模式")
	}
	if err := validateSigningTask(task.Data); err != nil {
		return nil, err
	}
	g, err := t.Get(task.GoalID)
	if err != nil || checkRevision(g, task) != nil || S(g.Data, "activePlanId") != S(task.Data, "planId") || approved(t, g) != nil {
		return nil, conflict("签名需求或批准方案已变化")
	}
	plan, err := t.Get(S(task.Data, "planId"))
	if err != nil || plan.Kind != "plan" || plan.Status != "active" || plan.ProjectID != task.ProjectID || plan.GoalID != task.GoalID {
		return nil, conflict("签名需要当前批准方案")
	}
	items, err := t.List(task.ProjectID, "task", task.GoalID)
	if err != nil {
		return nil, err
	}
	byLogical := map[string]Resource{}
	for _, item := range items {
		if S(item.Data, "planId") == plan.ID && revision(item) == revision(task) {
			id := S(item.Data, "logicalId")
			if _, exists := byLogical[id]; exists {
				return nil, conflict("签名输入存在重复工作项")
			}
			byLogical[id] = item
		}
	}
	reachable, visiting := map[string]bool{}, map[string]bool{}
	var walk func(string) error
	walk = func(id string) error {
		if visiting[id] {
			return conflict("签名依赖包含循环")
		}
		if reachable[id] {
			return nil
		}
		other, ok := byLogical[id]
		if !ok || other.ID == task.ID || other.Status != "succeeded" {
			return conflict("签名依赖尚未成功完成")
		}
		visiting[id] = true
		for _, dep := range Strings(other.Data["dependsOn"]) {
			if err := walk(dep); err != nil {
				return err
			}
		}
		visiting[id], reachable[id] = false, true
		return nil
	}
	for _, id := range Strings(task.Data["dependsOn"]) {
		if err := walk(id); err != nil {
			return nil, err
		}
	}
	producer, ok := byLogical[S(task.Data, "inputArtifactFrom")]
	if !ok || !reachable[S(task.Data, "inputArtifactFrom")] || S(producer.Data, "kind") != "build" || S(producer.Data, "artifactStage") != "unsigned" || S(producer.Data, "target") != S(task.Data, "target") {
		return nil, conflict("签名输入必须是当前依赖祖先的无签名构建")
	}
	runs, err := t.List(task.ProjectID, "run", task.GoalID)
	if err != nil {
		return nil, err
	}
	source, err := s.sourceProducer(t, producer, runs)
	if err != nil {
		return nil, err
	}
	producerRun, err := t.Get(S(source, "runId"))
	if err != nil {
		return nil, err
	}
	artifact, err := t.Get(S(Obj(producerRun.Data["result"]), "artifactId"))
	if err != nil || artifact.Kind != "artifact" || artifact.ProjectID != task.ProjectID || artifact.GoalID != task.GoalID || revision(artifact) != revision(task) || S(artifact.Data, "type") != "unsigned-package" || S(artifact.Data, "target") != S(task.Data, "target") || S(artifact.Data, "runId") != producerRun.ID || S(artifact.Data, "producerId") != "worker:"+S(producerRun.Data, "workerId") || S(artifact.Data, "commit") != S(source, "commit") || S(artifact.Data, "repositoryId") != S(source, "repositoryId") {
		return nil, conflict("无签名制品的来源或版本不匹配")
	}
	if err = s.VerifyArtifact(artifact); err != nil {
		return nil, err
	}
	binding, err := taskSourceBinding(task)
	if err != nil || S(binding, "commit") != S(artifact.Data, "commit") || S(binding, "repositoryId") != S(artifact.Data, "repositoryId") {
		return nil, conflict("签名源码必须与实际无签名制品一致")
	}
	in := M{"version": 1, "taskId": task.ID, "projectId": task.ProjectID, "goalId": task.GoalID, "planId": plan.ID, "requirementRevision": revision(task), "operationKey": "sign-" + task.ID, "artifactId": artifact.ID, "artifactDigest": S(artifact.Data, "digest"), "artifactSize": N(artifact.Data, "size"), "repositoryId": S(artifact.Data, "repositoryId"), "commit": S(artifact.Data, "commit"), "target": S(task.Data, "target"), "expectedSigner": S(task.Data, "expectedSigner"), "producerTaskId": producer.ID, "producerRunId": producerRun.ID, "sourceBindingDigest": S(binding, "digest")}
	if S(task.Data, "signingMode") == "macos-pkg" {
		in["expectedApplicationSigner"], in["expectedTeamId"] = task.Data["expectedApplicationSigner"], task.Data["expectedTeamId"]
	}
	if S(task.Data, "signingMode") == "ios-ipa" {
		in["expectedTeamId"], in["expectedProvisioningProfile"] = task.Data["expectedTeamId"], task.Data["expectedProvisioningProfile"]
	}
	if S(task.Data, "signingMode") == "windows-msix" {
		for _, key := range windowsSigningFields {
			in[key] = task.Data[key]
		}
	}
	in["digest"] = Hash(in)
	return in, nil
}

func (s *Service) bindSigningInput(t *Tx, task *Resource) error {
	if !controlledSigningTask(*task) {
		return nil
	}
	in, err := s.resolveSigningInput(t, *task)
	if err != nil {
		return err
	}
	if old, exists := task.Data["signingInput"]; exists && Hash(old) != Hash(in) {
		return conflict("已绑定的签名输入已变化，需要人工核对")
	}
	task.Data["signingInput"] = in
	return nil
}

func (s *Service) validateSigningRun(t *Tx, run, task Resource) (M, error) {
	in, err := s.resolveSigningInput(t, task)
	if err != nil {
		return nil, err
	}
	if Hash(in) != Hash(task.Data["signingInput"]) || Hash(in) != Hash(Obj(run.Data["task"])["signingInput"]) || S(run.Data, "taskId") != task.ID || run.ProjectID != task.ProjectID || run.GoalID != task.GoalID || revision(run) != revision(task) || S(run.Data, "planId") != S(task.Data, "planId") {
		return nil, conflict("签名执行与领取时的准确输入不一致")
	}
	worker, err := t.Get(S(run.Data, "workerId"))
	if err != nil || !signingWorker(worker, task) {
		return nil, denied()
	}
	return in, nil
}

func (s *Service) bindAndroidArtifact(t *Tx, a Actor, in M, artifact *Resource) error {
	for _, key := range append(append([]string{}, windowsSigningFields...), "certificateSHA1") {
		if _, exists := in[key]; exists {
			return invalid("签名制品来源只能由控制面推导：" + key)
		}
	}
	for _, key := range []string{"signingInput", "signingInputDigest", "inputArtifactId", "inputArtifactDigest", "expectedSigner", "expectedApplicationSigner", "expectedTeamId", "expectedProvisioningProfile", "certificateSHA256", "profileSHA256", "applicationCertificateSHA256", "notarizationId", "notarized", "signingReceipt", "signingMode"} {
		if _, exists := in[key]; exists {
			return invalid("签名制品来源只能由控制面推导：" + key)
		}
	}
	if a.Kind != "worker" {
		if S(artifact.Data, "type") == "unsigned-package" {
			return denied()
		}
		return nil
	}
	run, err := t.Get(S(in, "runId"))
	if err != nil {
		return err
	}
	task, err := t.Get(S(run.Data, "taskId"))
	if err != nil {
		return err
	}
	unsigned := S(task.Data, "artifactStage") == "unsigned"
	if unsigned || S(artifact.Data, "type") == "unsigned-package" {
		if !unsigned || S(task.Data, "kind") != "build" || S(artifact.Data, "type") != "unsigned-package" || S(task.Data, "target") != S(artifact.Data, "target") {
			return conflict("无签名构建只能上传 unsigned-package")
		}
		artifact.Data["artifactStage"] = "unsigned"
	}
	if !controlledSigningTask(task) {
		return nil
	}
	binding, err := s.validateSigningRun(t, run, task)
	if err != nil {
		return err
	}
	if S(artifact.Data, "type") != "package" || S(artifact.Data, "target") != S(binding, "target") || S(artifact.Data, "repositoryId") != S(binding, "repositoryId") || S(artifact.Data, "commit") != S(binding, "commit") || S(artifact.Data, "digest") == S(binding, "artifactDigest") {
		return conflict("签后制品必须是同一输入的新制品")
	}
	artifact.Data["signingMode"] = S(task.Data, "signingMode")
	artifact.Data["signingInputDigest"] = S(binding, "digest")
	artifact.Data["inputArtifactId"] = S(binding, "artifactId")
	artifact.Data["inputArtifactDigest"] = S(binding, "artifactDigest")
	artifact.Data["expectedSigner"] = S(binding, "expectedSigner")
	if S(task.Data, "signingMode") == "macos-pkg" {
		artifact.Data["expectedApplicationSigner"], artifact.Data["expectedTeamId"] = binding["expectedApplicationSigner"], binding["expectedTeamId"]
	}
	if S(task.Data, "signingMode") == "ios-ipa" {
		artifact.Data["expectedTeamId"], artifact.Data["expectedProvisioningProfile"] = binding["expectedTeamId"], binding["expectedProvisioningProfile"]
	}
	if S(task.Data, "signingMode") == "windows-msix" {
		for _, key := range windowsSigningFields {
			artifact.Data[key] = binding[key]
		}
	}
	return nil
}

func (s *Service) validateSignedResult(t *Tx, run, task, artifact Resource, result M) error {
	binding, err := s.validateSigningRun(t, run, task)
	if err != nil {
		return err
	}
	if S(result, "signingInputDigest") != S(binding, "digest") || S(result, "operationKey") != S(binding, "operationKey") || S(result, "certificateSHA256") != S(binding, "expectedSigner") || !digestPattern.MatchString(S(result, "payloadDigest")) || !digestPattern.MatchString(S(result, "requestBinding")) || S(artifact.Data, "signingMode") != S(task.Data, "signingMode") || S(artifact.Data, "signingInputDigest") != S(binding, "digest") || S(artifact.Data, "inputArtifactId") != S(binding, "artifactId") || S(artifact.Data, "inputArtifactDigest") != S(binding, "artifactDigest") || S(artifact.Data, "expectedSigner") != S(binding, "expectedSigner") {
		return conflict("签名回执与准确输入、输出或批准证书不一致")
	}
	if S(task.Data, "signingMode") == "macos-pkg" {
		notarized, ok := result["notarized"].(bool)
		if !ok || !notarized || S(result, "applicationCertificateSHA256") != S(binding, "expectedApplicationSigner") || S(result, "teamId") != S(binding, "expectedTeamId") || !macOSNotaryPattern.MatchString(S(result, "notarizationId")) || S(artifact.Data, "expectedApplicationSigner") != S(binding, "expectedApplicationSigner") || S(artifact.Data, "expectedTeamId") != S(binding, "expectedTeamId") {
			return conflict("macOS签后回执缺少准确双证书、团队或成功公证")
		}
		if err = matchMacOSOperation(task, result); err != nil {
			return err
		}
	}
	if S(task.Data, "signingMode") == "ios-ipa" {
		if S(result, "teamId") != S(binding, "expectedTeamId") || S(result, "profileSHA256") != S(binding, "expectedProvisioningProfile") || S(artifact.Data, "expectedTeamId") != S(binding, "expectedTeamId") || S(artifact.Data, "expectedProvisioningProfile") != S(binding, "expectedProvisioningProfile") {
			return conflict("iOS签后回执缺少准确Distribution团队或App Store profile")
		}
	}
	if S(task.Data, "signingMode") == "windows-msix" {
		return validateWindowsSigningResult(binding, artifact.Data, result)
	}
	return nil
}

func (s *Service) validateSigningArtifact(t *Tx, artifact Resource) error {
	run, err := t.Get(S(artifact.Data, "runId"))
	if err != nil {
		if S(artifact.Data, "signingMode") == "" {
			return nil
		}
		return conflict("签名制品缺少实际执行")
	}
	task, err := t.Get(S(run.Data, "taskId"))
	if err != nil {
		if S(artifact.Data, "signingMode") == "" {
			return nil
		}
		return conflict("签名制品缺少实际工作项")
	}
	if !controlledSigningTask(task) && S(artifact.Data, "signingMode") == "" {
		return nil
	}
	if !controlledSigningTask(task) || run.Status != "succeeded" || task.Status != "succeeded" || S(Obj(run.Data["result"]), "artifactId") != artifact.ID {
		return conflict("签名制品需要已确认成功的原始签名执行")
	}
	return s.validateTaskResult(t, Actor{ID: "worker:" + S(run.Data, "workerId"), Kind: "worker", WorkerID: S(run.Data, "workerId")}, run, task, Obj(run.Data["result"]))
}
