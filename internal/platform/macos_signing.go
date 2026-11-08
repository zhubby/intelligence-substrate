package platform

import (
	"regexp"
	"time"
)

var macOSTeamPattern = regexp.MustCompile(`^[A-Z0-9]{10}$`)
var macOSNotaryPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func macOSSigningTask(task Resource) bool { return S(task.Data, "signingMode") == "macos-pkg" }

var macOSOperationFields = []string{"repositoryId", "commit", "signingInputDigest", "operationKey", "certificateSHA256", "applicationCertificateSHA256", "teamId", "notarizationId", "payloadDigest", "requestBinding"}

// Persisted observations of one signing operation, never part of its approved
// recipe. A fresh derived signing task must start without any of these fields.
var macOSSigningExecutionFields = []string{"assignedWorkerId", "signingPhase", "signingPolls", "signingNextCheckAt", "signingDeadline", "signingOperation", "signingActiveSeconds", "notarizationId"}

func macOSOperation(result M) M {
	op := M{}
	for _, key := range macOSOperationFields {
		op[key] = result[key]
	}
	return op
}

func matchMacOSOperation(task Resource, result M) error {
	if old := Obj(task.Data["signingOperation"]); len(old) != 0 && Hash(old) != Hash(macOSOperation(result)) {
		return conflict("公证查询必须对应原签名操作、请求、PKG和公证编号")
	}
	return nil
}

// Waiting releases the lease without declaring a successful build. Only the
// final stapled package may become the source producer or schedule quality.
func (s *Service) waitMacOSSignature(t *Tx, run, task *Resource, result M) error {
	if !macOSSigningTask(*task) || len(result) != len(macOSOperationFields)+1 || !B(result, "signingPending") {
		return invalid("公证等待需要准确且完整的受控回执")
	}
	in, err := s.validateSigningRun(t, *run, *task)
	if err != nil {
		return err
	}
	if S(result, "repositoryId") != S(in, "repositoryId") || S(result, "commit") != S(in, "commit") || S(result, "signingInputDigest") != S(in, "digest") || S(result, "operationKey") != S(in, "operationKey") || S(result, "certificateSHA256") != S(in, "expectedSigner") || S(result, "applicationCertificateSHA256") != S(in, "expectedApplicationSigner") || S(result, "teamId") != S(in, "expectedTeamId") || !macOSNotaryPattern.MatchString(S(result, "notarizationId")) || !digestPattern.MatchString(S(result, "payloadDigest")) || !digestPattern.MatchString(S(result, "requestBinding")) {
		return conflict("公证等待回执与准确签名输入不一致")
	}
	if err = matchMacOSOperation(*task, result); err != nil {
		return err
	}
	if _, err = macOSSigningAllowance(*task); err != nil {
		return err
	}
	task.Data["signingOperation"] = macOSOperation(result)
	task.Data["signingPhase"], task.Data["assignedWorkerId"] = "status", S(run.Data, "workerId")
	task.Data["signingNextCheckAt"] = time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339Nano)
	task.Status, run.Status = "awaiting_signature", "waiting_signature"
	task.Data["reason"] = "Apple公证处理中，已释放签名节点，等待查询原操作"
	return nil
}

func macOSSigningAllowance(task Resource) (time.Duration, error) {
	deadline, err := time.Parse(time.RFC3339Nano, S(task.Data, "signingDeadline"))
	if err != nil || !deadline.After(time.Now()) || N(task.Data, "signingPolls") >= 288 {
		return 0, conflict("公证等待达到截止或查询上限，需要人工核对")
	}
	limit := N(task.Data, "timeoutSeconds")
	if limit <= 0 || limit > 86400 {
		limit = 900
	}
	remaining := time.Duration((limit - N(task.Data, "signingActiveSeconds")) * float64(time.Second))
	if remaining <= 0 {
		return 0, conflict("签名与公证查询已达到活动执行时间上限")
	}
	if until := time.Until(deadline); until < remaining {
		remaining = until
	}
	return remaining, nil
}

func recordMacOSSigningTime(task *Resource, run Resource) {
	if !macOSSigningTask(*task) {
		return
	}
	started, err := time.Parse(time.RFC3339Nano, run.CreatedAt)
	if err == nil {
		seconds := time.Since(started).Seconds()
		if seconds > 0 {
			task.Data["signingActiveSeconds"] = N(task.Data, "signingActiveSeconds") + seconds
		}
	}
}

func (s *Service) refreshMacOSSignatures(t *Tx, project string) error {
	tasks, err := t.List(project, "task", "")
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if !macOSSigningTask(task) || task.Status != "awaiting_signature" {
			continue
		}
		_, err = macOSSigningAllowance(task)
		if err == nil {
			worker, e := t.Get(S(task.Data, "assignedWorkerId"))
			if e != nil || !signingWorker(worker, task) {
				err = conflict("原公证节点已撤权，需要人工核对")
			}
		}
		if err == nil {
			err = s.bindSigningInput(t, &task)
		}
		if err != nil {
			task.Status, task.Data["reason"] = "blocked", err.Error()
		} else {
			due, e := time.Parse(time.RFC3339Nano, S(task.Data, "signingNextCheckAt"))
			if e != nil {
				task.Status, task.Data["reason"] = "blocked", "公证查询时间无效，需要人工核对"
			} else if !due.After(time.Now()) {
				task.Status = "pending"
			}
		}
		if e := t.Put(task); e != nil {
			return e
		}
	}
	return nil
}
