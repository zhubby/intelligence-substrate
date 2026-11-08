package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/intelligence-substrate/platform/internal/source"
)

func workspaceTask(task Resource, run Resource) error {
	if !Has([]string{"code", "design"}, S(task.Data, "kind")) || len(Items(task.Data["command"])) != 0 || len(Strings(task.Data["children"])) != 0 || messageTaskRecipe(task.Data) != messageTaskRecipe(Obj(run.Data["task"])) {
		return conflict("工作区保存需要当前未变更的无固定命令开发或设计工作项")
	}
	if N(task.Data, "messageWaitCount") >= 3 || N(task.Data, "messageStepsUsed") >= 23 {
		return conflict("工作项已达到协作等待或累计步骤上限")
	}
	return nil
}

func workspaceWorker(t *Tx, a Actor, task Resource) error {
	worker, e := t.Get(a.WorkerID)
	if e != nil || !Has(Strings(worker.Data["capabilities"]), S(task.Data, "kind")) || S(worker.Data, "runtime") != S(task.Data, "runtime") {
		return denied()
	}
	return nil
}

func (s *Service) checkpointSource(t *Tx, task Resource, run Resource, input M) error {
	if e := s.validateWaitSource(t, run.ProjectID, input); e != nil {
		return e
	}
	for _, key := range []string{"repositoryId", "commit"} {
		if expected := S(task.Data, key); expected != "" && expected != S(input, key) {
			return conflict("工作区必须保留批准工作项的准确源码")
		}
	}
	if previous := Obj(run.Data["messageContinuation"]); len(previous) > 0 && Hash(previous["source"]) != Hash(input) {
		return conflict("工作区不能切换原等待源码")
	}
	if S(task.Data, "kind") == "code" {
		binding, e := taskSourceBinding(task)
		if e != nil {
			return e
		}
		if Hash(binding) != Hash(Obj(run.Data["task"])["sourceBinding"]) || S(binding, "repositoryId") != S(input, "repositoryId") || S(binding, "commit") != S(input, "commit") {
			return conflict("工作区与执行源码绑定不一致")
		}
		repo, e := t.Get(S(binding, "repositoryId"))
		if e != nil {
			return e
		}
		if e = s.sourceTargetUnchanged(repo, binding); e != nil {
			return e
		}
	}
	return nil
}

func (s *Service) checkpointAuthority(t *Tx, a Actor, id string, in M) (run Resource, task Resource, err error) {
	a, err = t.CurrentActor(a)
	if err != nil {
		return
	}
	if a.Kind != "worker" {
		err = denied()
		return
	}
	if len(in) != 3 {
		err = invalid("工作区元数据仅包含 leaseToken、repositoryId 和 commit")
		return
	}
	for _, key := range []string{"leaseToken", "repositoryId", "commit"} {
		if _, ok := in[key].(string); !ok {
			err = invalid("工作区元数据必须为字符串")
			return
		}
	}
	run, task, err = s.messageRun(t, a, M{"runId": id, "leaseToken": S(in, "leaseToken")})
	if err != nil {
		return
	}
	if err = workspaceTask(task, run); err != nil {
		return
	}
	if err = workspaceWorker(t, a, task); err != nil {
		return
	}
	if err = s.checkpointSource(t, task, run, M{"repositoryId": S(in, "repositoryId"), "commit": S(in, "commit")}); err != nil {
		return
	}
	_, _, err = messageTaskAllowance(t, task, run.ID)
	if err != nil {
		return
	}
	ops, e := t.List(run.ProjectID, "source_commit", run.GoalID)
	if e != nil {
		err = e
		return
	}
	for _, op := range ops {
		if S(op.Data, "runId") == run.ID {
			err = conflict("已提交源码，不能改为工作区等待；请核对原结果")
			return
		}
	}
	return
}

func (s *Service) checkpointDenied(ctx context.Context, a Actor, id, action string, err error) {
	var fault *Fault
	if !errors.As(err, &fault) {
		return
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = s.Store.Update(auditCtx, func(t *Tx) error {
		run, e := t.Get(id)
		if e != nil {
			run = Resource{ID: id, ProjectID: a.ProjectID}
		}
		return t.Audit(a, "denied:"+action, run, M{"code": fault.Code})
	})
}

// UploadWorkspaceCheckpoint saves private, non-adopted workspace bytes. It never
// changes a Git branch or registers an artifact, design or quality result.
func (s *Service) UploadWorkspaceCheckpoint(ctx context.Context, a Actor, id string, in M, body io.Reader) (Resource, error) {
	return s.uploadWorkspaceCheckpoint(ctx, a, id, in, body, "")
}

func (s *Service) uploadWorkspaceCheckpoint(ctx context.Context, a Actor, id string, in M, body io.Reader, stepID string) (result Resource, err error) {
	authority := func(t *Tx) (Resource, Resource, error) {
		if stepID == "" {
			return s.checkpointAuthority(t, a, id, in)
		}
		return s.agentCheckpointAuthority(t, a, stepID, S(in, "leaseToken"))
	}
	if stepID == "" {
		defer func() { s.checkpointDenied(ctx, a, id, "workspace.checkpoint", err) }()
	}
	err = s.Store.Update(ctx, func(t *Tx) error { _, _, e := authority(t); return e })
	if err != nil {
		return
	}
	root, e := os.MkdirTemp("", "is-checkpoint-")
	if e != nil {
		return result, e
	}
	defer os.RemoveAll(root)
	tree := filepath.Join(root, "tree")
	if e = os.Mkdir(tree, 0700); e != nil {
		return result, e
	}
	archive, e := os.OpenFile(filepath.Join(root, "workspace.tar"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		return result, e
	}
	defer archive.Close()
	entries, digest, e := source.Extract(io.TeeReader(body, archive), tree)
	if e != nil {
		return result, invalid("工作区归档无效：" + e.Error())
	}
	for _, entry := range entries {
		for _, part := range strings.Split(entry.Name, "/") {
			if strings.EqualFold(part, ".intelligence-artifact") {
				return result, invalid("工作区不能包含控制器制品路径")
			}
		}
	}
	if e = archive.Sync(); e != nil {
		return result, e
	}
	info, e := archive.Stat()
	if e != nil {
		return result, e
	}
	err = s.Store.Update(ctx, func(t *Tx) error {
		run, task, e := authority(t)
		if e != nil {
			return e
		}
		data := M{"version": 1, "runId": run.ID, "taskId": task.ID, "planId": S(run.Data, "planId"), "requirementRevision": revision(run), "taskRecipe": messageTaskRecipe(task.Data), "kind": S(task.Data, "kind"), "source": M{"repositoryId": S(in, "repositoryId"), "commit": S(in, "commit")}, "sourceBinding": Obj(task.Data["sourceBinding"]), "digest": digest, "size": info.Size(), "fileCount": len(entries)}
		if S(task.Data, "sourceMode") == "integrate" {
			binding, e := taskSourceBinding(task)
			if e != nil {
				return e
			}
			prep, e := integrationPreparation(t, run, binding)
			if e != nil {
				return e
			}
			decisions := Obj(run.Data["integrationResolutions"])
			if Hash(decisions) != S(run.Data, "integrationResolutionDigest") {
				return conflict("集成决定摘要无效")
			}
			data["integration"] = M{"id": prep.ID, "workspaceCommit": S(prep.Data, "workspaceCommit"), "conflicts": Strings(prep.Data["conflicts"]), "resolutions": decisions, "resolutionDigest": Hash(decisions)}
		}
		if S(task.Data, "kind") == "design" {
			if e = s.validateDesignCheckpoint(ctx, t, Obj(data["source"]), tree, entries); e != nil {
				return e
			}
		}
		scope := "workspace-checkpoint:" + run.ID
		kind, identity := "workspace_checkpoint", run.ID
		if stepID != "" {
			data["agentStepId"] = stepID
			scope, kind, identity = "agent-checkpoint:"+stepID, "agent_checkpoint", stepID
		}
		previous, exists, e := t.Once(scope, run.ID, data)
		if e != nil {
			return e
		}
		if exists {
			result = previous
			return s.VerifyArtifact(previous)
		}
		dest := s.ArtifactPath(digest)
		if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
			return e
		}
		// Copy rather than rename an open file so the same path works on Windows.
		if _, e = archive.Seek(0, io.SeekStart); e != nil {
			return e
		}
		storedDigest, storedSize, e := s.uploadContent(archive, source.MaxBytes)
		if e != nil {
			return e
		}
		if storedDigest != digest || storedSize != info.Size() {
			return conflict("工作区存储字节与已验证归档不一致")
		}
		stored, e := os.OpenFile(dest, os.O_RDWR, 0)
		if e != nil {
			return e
		}
		syncErr := stored.Sync()
		closeErr := stored.Close()
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		if directory, e := os.Open(filepath.Dir(dest)); e == nil {
			_ = directory.Sync()
			directory.Close()
		}
		checkpoint := resource(kind, task.Title, run.ProjectID, run.GoalID, copyM(data))
		checkpoint.ID = kind + "_" + Hash([]string{identity})
		checkpoint.Status = "saved"
		checkpoint.Data["binding"] = Hash(data)
		if e = t.Put(checkpoint); e != nil {
			return e
		}
		if e = t.Remember(scope, run.ID, data, checkpoint); e != nil {
			return e
		}
		action := "workspace.checkpoint"
		if stepID != "" {
			action = "agent.checkpoint"
		}
		if e = t.Audit(a, action, checkpoint, M{"digest": digest, "fileCount": len(entries)}); e != nil {
			return e
		}
		result = checkpoint
		return nil
	})
	return
}

func (s *Service) validateDesignCheckpoint(ctx context.Context, t *Tx, input M, root string, entries []source.Entry) error {
	original, e := os.MkdirTemp("", "is-design-base-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(original)
	base := []source.Entry{}
	if S(input, "repositoryId") != "" {
		repo, e := t.Get(S(input, "repositoryId"))
		if e != nil {
			return e
		}
		path, e := s.sourceArchive(ctx, repo, S(input, "commit"))
		if e != nil {
			return e
		}
		defer os.Remove(path)
		file, e := os.Open(path)
		if e != nil {
			return e
		}
		base, _, e = source.Extract(file, original)
		file.Close()
		if e != nil {
			return e
		}
	}
	files := map[string]source.Entry{}
	for _, entry := range entries {
		files[entry.Name] = entry
	}
	for _, entry := range base {
		got, ok := files[entry.Name]
		if !ok {
			return conflict("设计工作区不能删除原始源码：" + entry.Name)
		}
		before, e := checkpointFileHash(filepath.Join(original, filepath.FromSlash(entry.Name)))
		if e != nil {
			return e
		}
		after, e := checkpointFileHash(filepath.Join(root, filepath.FromSlash(entry.Name)))
		if e != nil {
			return e
		}
		if got.Executable == entry.Executable && got.Size == entry.Size && before == after {
			delete(files, entry.Name)
		} else if entry.Executable {
			return conflict("设计工作区不能覆盖可执行源码")
		}
	}
	for _, entry := range files {
		if entry.Executable || entry.Size < 1 || entry.Size > 2<<20 || !Has([]string{".html", ".htm"}, strings.ToLower(filepath.Ext(entry.Name))) {
			return conflict("设计工作区只能新增或修改非可执行 HTML（1 B–2 MiB）")
		}
	}
	return nil
}

func checkpointFileHash(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), e
}

func (s *Service) validateWorkspaceCheckpoint(t *Tx, checkpoint, task Resource, input M) error {
	return s.validateSavedWorkspaceCheckpoint(t, checkpoint, task, input, "workspace_checkpoint")
}

func (s *Service) validateSavedWorkspaceCheckpoint(t *Tx, checkpoint, task Resource, input M, kind string) error {
	data := copyM(checkpoint.Data)
	delete(data, "binding")
	if checkpoint.Kind != kind || checkpoint.Status != "saved" || N(data, "version") != 1 || S(checkpoint.Data, "binding") != Hash(data) || checkpoint.ProjectID != task.ProjectID || checkpoint.GoalID != task.GoalID || revision(checkpoint) != revision(task) || S(data, "taskId") != task.ID || S(data, "planId") != S(task.Data, "planId") || S(data, "kind") != S(task.Data, "kind") || S(data, "taskRecipe") != messageTaskRecipe(task.Data) || Hash(data["source"]) != Hash(input) || Hash(data["sourceBinding"]) != Hash(Obj(task.Data["sourceBinding"])) {
		return conflict("工作区记录与当前工作项或源码不一致")
	}
	origin, e := t.Get(S(data, "runId"))
	if e != nil || origin.Kind != "run" || origin.ProjectID != task.ProjectID || origin.GoalID != task.GoalID || S(origin.Data, "taskId") != task.ID || S(origin.Data, "planId") != S(data, "planId") || revision(origin) != revision(task) || messageTaskRecipe(Obj(origin.Data["task"])) != S(data, "taskRecipe") {
		return conflict("工作区原执行绑定无效")
	}
	ops, e := t.List(task.ProjectID, "source_commit", task.GoalID)
	if e != nil {
		return e
	}
	for _, op := range ops {
		if S(op.Data, "runId") == origin.ID {
			return conflict("工作区原执行已提交源码，需要核对")
		}
	}
	if S(task.Data, "sourceMode") == "integrate" {
		binding, e := taskSourceBinding(task)
		if e != nil {
			return e
		}
		prep, e := integrationPreparation(t, origin, binding)
		if e != nil {
			return e
		}
		integration := M{"id": prep.ID, "workspaceCommit": S(prep.Data, "workspaceCommit"), "conflicts": Strings(prep.Data["conflicts"]), "resolutions": Obj(origin.Data["integrationResolutions"]), "resolutionDigest": S(origin.Data, "integrationResolutionDigest")}
		if kind == "agent_checkpoint" {
			// A later unconfirmed resolve may have changed the old run. Its
			// reconciled abandonment must restore the protected, confirmed
			// decisions with the exact saved bytes, not inherit the later state.
			saved := Obj(data["integration"])
			integration["resolutions"], integration["resolutionDigest"] = Obj(saved["resolutions"]), S(saved, "resolutionDigest")
		}
		if Hash(integration) != Hash(data["integration"]) || S(integration, "resolutionDigest") != Hash(integration["resolutions"]) {
			return conflict("集成决定已在工作区保存后变化")
		}
	} else if len(Obj(data["integration"])) > 0 {
		return conflict("工作区包含未批准的集成状态")
	}
	if S(task.Data, "kind") == "code" {
		binding, e := taskSourceBinding(task)
		if e != nil {
			return e
		}
		repo, e := t.Get(S(binding, "repositoryId"))
		if e != nil {
			return e
		}
		if e = s.sourceTargetUnchanged(repo, binding); e != nil {
			return e
		}
	}
	return s.VerifyArtifact(checkpoint)
}

// WorkspaceCheckpoint returns an already-open, hash-verified file, authorized
// only for the exact active continuation. Lease tokens never enter URLs/state.
func (s *Service) WorkspaceCheckpoint(ctx context.Context, a Actor, id, lease string) (file *os.File, checkpoint Resource, err error) {
	defer func() { s.checkpointDenied(ctx, a, id, "workspace.restore", err) }()
	err = s.Store.Update(ctx, func(t *Tx) error {
		current, e := t.CurrentActor(a)
		if e != nil {
			return e
		}
		a = current
		if a.Kind != "worker" {
			return denied()
		}
		run, task, e := s.messageRun(t, a, M{"runId": id, "leaseToken": lease})
		if e != nil {
			return e
		}
		if e = workspaceWorker(t, a, task); e != nil {
			return e
		}
		continuation := Obj(run.Data["messageContinuation"])
		wait, e := t.Get(S(continuation, "waitId"))
		if e != nil {
			return e
		}
		if e = s.validMessageWait(t, wait, task); e != nil {
			return e
		}
		checkpoint, e = t.Get(S(continuation, "checkpointId"))
		if e != nil {
			return e
		}
		if S(wait.Data, "checkpointId") != checkpoint.ID || S(continuation, "checkpointDigest") != S(checkpoint.Data, "digest") || S(wait.Data, "checkpointDigest") != S(checkpoint.Data, "digest") {
			return conflict("执行不持有准确工作区继续记录")
		}
		if e = s.validateWorkspaceCheckpoint(t, checkpoint, task, Obj(wait.Data["source"])); e != nil {
			return e
		}
		file, e = os.Open(s.ArtifactPath(S(checkpoint.Data, "digest")))
		if e != nil {
			return e
		}
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(file, source.MaxBytes+1))
		if e == nil && (n != int64(N(checkpoint.Data, "size")) || hex.EncodeToString(h.Sum(nil)) != S(checkpoint.Data, "digest")) {
			e = conflict("工作区完整性验证失败")
		}
		if e == nil {
			_, e = file.Seek(0, io.SeekStart)
		}
		if e != nil {
			file.Close()
			file = nil
			return e
		}
		return nil
	})
	if err != nil && file != nil {
		file.Close()
		file = nil
	}
	return
}

func (s *Service) continueWorkspace(t *Tx, wait Resource, task Resource, run *Resource) error {
	if S(wait.Data, "checkpointId") == "" {
		return nil
	}
	checkpoint, e := t.Get(S(wait.Data, "checkpointId"))
	if e != nil {
		return e
	}
	if e = s.validateWorkspaceCheckpoint(t, checkpoint, task, Obj(wait.Data["source"])); e != nil {
		return e
	}
	integration := Obj(checkpoint.Data["integration"])
	return s.continueSavedWorkspace(t, integration, task, run)
}

func (s *Service) continueSavedWorkspace(t *Tx, integration M, task Resource, run *Resource) error {
	if S(task.Data, "sourceMode") != "integrate" {
		if len(integration) > 0 {
			return conflict("工作区不能引入未批准的集成")
		}
		return nil
	}
	if len(integration) == 0 || S(integration, "resolutionDigest") != Hash(Obj(integration["resolutions"])) {
		return conflict("集成工作区缺少准确冲突决定")
	}
	run.Data["integrationId"], run.Data["integrationWorkspaceCommit"], run.Data["integrationConflicts"] = S(integration, "id"), S(integration, "workspaceCommit"), Strings(integration["conflicts"])
	run.Data["integrationResolutions"], run.Data["integrationResolutionDigest"] = copyM(Obj(integration["resolutions"])), S(integration, "resolutionDigest")
	binding, e := taskSourceBinding(task)
	if e != nil {
		return e
	}
	_, e = integrationPreparation(t, *run, binding)
	return e
}
