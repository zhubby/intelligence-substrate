package platform

import (
	"context"
	"math"
	"time"

	"github.com/intelligence-substrate/platform/internal/webpreview"
)

func previewView(r Resource) Resource {
	if r.Kind != "preview" || r.Status != "active" {
		return r
	}
	deadline, err := time.Parse(time.RFC3339Nano, S(r.Data, "expiresAt"))
	if err != nil || !deadline.After(time.Now()) {
		r.Status = "expired"
	}
	return r
}

func (s *Service) previewCommand(t *Tx, a Actor, cmd string, in M) (Resource, error) {
	if a.Kind != "human" && a.Kind != "agent" {
		return Resource{}, denied()
	}
	if err := validateText(S(in, "key"), "请求 key", 160); err != nil {
		return Resource{}, err
	}
	if err := validateText(S(in, "reason"), "预览操作原因", 2000); err != nil {
		return Resource{}, err
	}
	if cmd == "previews.stop" {
		r, err := s.scoped(t, a, S(in, "id"), "preview", "developer")
		if err != nil {
			return r, err
		}
		scope := "preview.stop:" + r.ID
		if previous, ok, e := t.Once(scope, S(in, "key"), in); e != nil || ok {
			if e != nil {
				return r, e
			}
			current, e := t.Get(previous.ID)
			return previewView(current), e
		}
		if N(in, "expectedRevision") != float64(r.Revision) {
			return r, conflict("预览版本已变化，请刷新后重试")
		}
		r.Status = "stopped"
		r.Revision++
		r.Data["stoppedBy"], r.Data["stoppedAt"], r.Data["stopReason"] = a.ID, now(), S(in, "reason")
		if err = t.Put(r); err != nil {
			return r, err
		}
		return r, t.Remember(scope, S(in, "key"), in, r)
	}
	artifact, err := s.scoped(t, a, S(in, "artifactId"), "artifact", "developer")
	if err != nil {
		return artifact, err
	}
	scope := "preview.create:" + artifact.ID
	if previous, ok, e := t.Once(scope, S(in, "key"), in); e != nil || ok {
		if e != nil {
			return Resource{}, e
		}
		current, e := t.Get(previous.ID)
		return previewView(current), e
	}
	g, err := t.Get(artifact.GoalID)
	if err != nil || g.Kind != "goal" {
		return Resource{}, missing()
	}
	if N(in, "expectedRequirementRevision") != float64(g.Revision) {
		return Resource{}, conflict("需求版本已变化，请刷新后创建预览")
	}
	if err = approved(t, g); err != nil {
		return Resource{}, err
	}
	if err = checkRevision(g, artifact); err != nil {
		return Resource{}, err
	}
	if S(artifact.Data, "type") != "package" || S(artifact.Data, "target") != "web" || !Has(Strings(g.Data["targets"]), "web") {
		return Resource{}, invalid("开发预览需要当前需求的 Web package 制品")
	}
	sourceKind, buildRunID, err := previewSource(t, artifact)
	if err != nil {
		return Resource{}, err
	}
	if err = validateText(S(in, "title"), "预览名称", 200); err != nil {
		return Resource{}, err
	}
	ttl := float64(60)
	if _, exists := in["ttlMinutes"]; exists {
		ttl = N(in, "ttlMinutes")
	}
	if ttl < 5 || ttl > 240 || math.Trunc(ttl) != ttl {
		return Resource{}, invalid("预览期限须为 5–240 的整数分钟")
	}
	previews, err := t.List(g.ProjectID, "preview", "")
	if err != nil {
		return Resource{}, err
	}
	active := 0
	for _, preview := range previews {
		if previewView(preview).Status == "active" {
			active++
		}
	}
	if active >= 10 {
		return Resource{}, conflict("每项目最多保留 10 个活动预览，请先停止不再使用的预览")
	}
	bundle, err := webpreview.Read(s.ArtifactPath(S(artifact.Data, "digest")), S(artifact.Data, "digest"), int64(N(artifact.Data, "size")))
	if err != nil {
		return Resource{}, invalid(err.Error())
	}
	if bundle.Runtime() != nil && !s.PreviewRuntimeAvailable {
		return Resource{}, fail("unconfigured", "完整应用预览需要配置独立执行节点，请联系管理员", 409)
	}
	r := resource("preview", S(in, "title"), g.ProjectID, g.ID, M{
		"artifactId": artifact.ID, "repositoryId": S(artifact.Data, "repositoryId"), "commit": S(artifact.Data, "commit"),
		"digest": S(artifact.Data, "digest"), "size": N(artifact.Data, "size"), "requirementRevision": g.Revision,
		"fileCount": bundle.FileCount, "createdBy": a.ID, "reason": S(in, "reason"),
		"sourceKind": sourceKind, "buildRunId": buildRunID,
		"expiresAt": time.Now().Add(time.Duration(ttl) * time.Minute).UTC().Format(time.RFC3339Nano),
	})
	r.Data["binding"] = previewBinding(r)
	if runtime := bundle.Runtime(); runtime != nil {
		r.Data["runtimeProfile"], r.Data["appMode"], r.Data["temporaryData"] = runtime.Profile, runtime.Mode, true
	}
	if err = t.Put(r); err != nil {
		return r, err
	}
	return r, t.Remember(scope, S(in, "key"), in, r)
}

func previewSource(t *Tx, artifact Resource) (string, string, error) {
	id := S(artifact.Data, "runId")
	if id == "" {
		return "registered", "", nil
	}
	run, err := t.Get(id)
	if err != nil || run.Kind != "run" || run.Status != "succeeded" || run.ProjectID != artifact.ProjectID || run.GoalID != artifact.GoalID || revision(run) != revision(artifact) || S(Obj(run.Data["result"]), "artifactId") != artifact.ID || S(artifact.Data, "producerId") != "worker:"+S(run.Data, "workerId") {
		return "", "", conflict("构建尚未完成或输出与当前制品不匹配")
	}
	task, err := t.Get(S(run.Data, "taskId"))
	if err != nil || task.Kind != "task" || S(task.Data, "kind") != "build" {
		return "", "", conflict("开发预览需要可核对的构建来源")
	}
	return "worker-build", run.ID, nil
}

func previewBinding(r Resource) string {
	return Hash(M{"projectId": r.ProjectID, "goalId": r.GoalID, "artifactId": S(r.Data, "artifactId"), "repositoryId": S(r.Data, "repositoryId"), "commit": S(r.Data, "commit"), "digest": S(r.Data, "digest"), "size": N(r.Data, "size"), "requirementRevision": revision(r)})
}

func (s *Service) currentPreview(t *Tx, id string) (Resource, Resource, error) {
	r, err := t.Get(id)
	if err != nil || r.Kind != "preview" || previewView(r).Status != "active" {
		return Resource{}, Resource{}, missing()
	}
	g, err := t.Get(r.GoalID)
	if err != nil || g.Kind != "goal" || g.ProjectID != r.ProjectID || checkRevision(g, r) != nil || approved(t, g) != nil || S(r.Data, "binding") != previewBinding(r) {
		return Resource{}, Resource{}, missing()
	}
	artifact, err := t.Get(S(r.Data, "artifactId"))
	if err != nil || artifact.Kind != "artifact" || artifact.ProjectID != r.ProjectID || artifact.GoalID != r.GoalID || S(artifact.Data, "type") != "package" || S(artifact.Data, "target") != "web" {
		return Resource{}, Resource{}, missing()
	}
	for _, key := range []string{"digest", "size", "commit", "repositoryId", "requirementRevision"} {
		if Hash(artifact.Data[key]) != Hash(r.Data[key]) {
			return Resource{}, Resource{}, missing()
		}
	}
	return r, artifact, nil
}

// PreviewRuntimeCurrent is an internal maintenance check, not an Agent tool.
// The artifact digest in the immutable binding also covers its runtime manifest.
func (s *Service) PreviewRuntimeCurrent(ctx context.Context, id, binding string) (bool, error) {
	valid := false
	err := s.Store.Update(ctx, func(t *Tx) error {
		r, _, err := s.currentPreview(t, id)
		if err != nil {
			return nil
		}
		valid = S(r.Data, "binding") == binding && S(r.Data, "runtimeProfile") == "flutter-go-v1"
		return nil
	})
	return valid, err
}
