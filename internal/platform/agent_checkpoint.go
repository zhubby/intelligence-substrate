package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"github.com/intelligence-substrate/platform/internal/source"
)

func (s *Service) agentCheckpointAuthority(t *Tx, a Actor, id, lease string) (Resource, Resource, error) {
	step, e := t.Get(id)
	if e != nil {
		return step, Resource{}, e
	}
	if step.Kind != "agent_step" || !Has([]string{"prepared", "completed"}, step.Status) {
		return step, Resource{}, conflict("操作不接受工作区保存")
	}
	run, task, e := s.agentProgressAuthority(t, a, M{"runId": S(step.Data, "runId"), "leaseToken": lease})
	if e != nil {
		return run, task, e
	}
	if step.ProjectID != task.ProjectID || step.GoalID != task.GoalID || S(step.Data, "taskId") != task.ID || S(step.Data, "taskRecipe") != messageTaskRecipe(task.Data) || S(step.Data, "actionDigest") != Hash(step.Data["action"]) || S(task.Data, "kind") == "analysis" {
		return run, task, denied()
	}
	if e = s.checkpointSource(t, task, run, Obj(step.Data["source"])); e != nil {
		return run, task, e
	}
	ops, e := t.List(task.ProjectID, "source_commit", task.GoalID)
	if e != nil {
		return run, task, e
	}
	for _, op := range ops {
		if S(op.Data, "runId") == run.ID {
			return run, task, conflict("原执行已提交源码，不能保存普通进度")
		}
	}
	return run, task, nil
}

func (s *Service) AgentResumeCheckpoint(ctx context.Context, a Actor, id, lease string) (file *os.File, checkpoint Resource, err error) {
	defer func() { s.checkpointDenied(ctx, a, id, "agent.restore", err) }()
	err = s.Store.Update(ctx, func(t *Tx) error {
		current, e := t.CurrentActor(a)
		if e != nil {
			return e
		}
		run, task, e := s.agentProgressAuthority(t, current, M{"runId": id, "leaseToken": lease})
		if e != nil {
			return e
		}
		resume, e := s.agentResume(t, task)
		if e != nil {
			return e
		}
		if len(resume) == 0 || Hash(resume) != Hash(Obj(run.Data["agentResume"])) || S(task.Data, "kind") == "analysis" {
			return conflict("此执行不持有准确 Agent 工作区恢复记录")
		}
		checkpoint, e = t.Get(S(resume, "checkpointId"))
		if e != nil {
			return e
		}
		file, e = os.Open(s.ArtifactPath(S(checkpoint.Data, "digest")))
		if e != nil {
			return e
		}
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(file, source.MaxBytes+1))
		if e == nil && (n != int64(N(checkpoint.Data, "size")) || hex.EncodeToString(h.Sum(nil)) != S(checkpoint.Data, "digest")) {
			e = conflict("Agent 恢复归档完整性验证失败")
		}
		if e == nil {
			_, e = file.Seek(0, io.SeekStart)
		}
		return e
	})
	if err != nil && file != nil {
		file.Close()
		file = nil
	}
	return
}

func (s *Service) UploadAgentCheckpoint(ctx context.Context, a Actor, id, lease string, body io.Reader) (result Resource, err error) {
	defer func() { s.checkpointDenied(ctx, a, id, "agent.checkpoint", err) }()
	var run Resource
	var input M
	e := s.Store.Update(ctx, func(t *Tx) error {
		var err error
		run, _, err = s.agentCheckpointAuthority(t, a, id, lease)
		if err != nil {
			return err
		}
		step, err := t.Get(id)
		if err != nil {
			return err
		}
		input = copyM(Obj(step.Data["source"]))
		input["leaseToken"] = lease
		return nil
	})
	if e != nil {
		return Resource{}, e
	}
	return s.uploadWorkspaceCheckpoint(ctx, a, run.ID, input, body, id)
}

func (s *Service) validateAgentCheckpoint(t *Tx, checkpoint, task Resource, input M) error {
	if S(checkpoint.Data, "agentStepId") == "" {
		return conflict("普通进度工作区缺少原操作")
	}
	return s.validateSavedWorkspaceCheckpoint(t, checkpoint, task, input, "agent_checkpoint")
}
