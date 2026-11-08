package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/intelligence-substrate/platform/internal/source"
)

// CommitSnapshot replaces a feature tree. A durable prepared operation and
// producer record precede the branch CAS, since Git and SQL cannot commit together.
func (s *Service) CommitSnapshot(ctx context.Context, a Actor, id string, in M, body io.Reader) (result Resource, err error) {
	defer func() {
		var fault *Fault
		if errors.As(err, &fault) {
			auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = s.Store.Update(auditCtx, func(t *Tx) error {
				repo, e := t.Get(id)
				if e != nil {
					repo = Resource{ID: id, ProjectID: a.ProjectID}
				}
				return t.Audit(a, "denied:repositories.snapshot", repo, M{"code": fault.Code})
			})
		}
	}()
	// Check authority before accepting potentially large or slow request bodies.
	var repo Resource
	err = s.Store.Update(ctx, func(t *Tx) error { var e error; repo, e = s.snapshotAuthority(t, a, id, in); return e })
	if err != nil {
		return result, err
	}
	root, e := os.MkdirTemp("", "is-source-commit-")
	if e != nil {
		return result, e
	}
	defer os.RemoveAll(root)
	entries, digest, e := source.Extract(body, root)
	if e != nil {
		return result, invalid("源码快照无效：" + e.Error())
	}
	binding := M{"branch": S(in, "branch"), "baseCommit": S(in, "baseCommit"), "expectedHead": S(in, "expectedHead"), "message": S(in, "message"), "runId": S(in, "runId"), "digest": digest}
	// Preserve the exact legacy idempotency digest when metadata is absent.
	if S(in, "integrationId") != "" {
		binding["integrationId"], binding["resolutionDigest"] = S(in, "integrationId"), S(in, "resolutionDigest")
	}
	scope, key := "source:"+a.ID+":"+id, S(in, "key")
	var op Resource
	err = s.Store.Update(ctx, func(t *Tx) error {
		var e error
		repo, e = s.snapshotAuthority(t, a, id, in)
		if e != nil {
			return e
		}
		parents := []string{S(in, "baseCommit")}
		if e = s.validateBlueprintArchive(t, a, in, digest); e != nil {
			return e
		}
		if S(in, "integrationId") != "" {
			parents, e = s.integrationSnapshot(t, a, in, root, entries)
			if e != nil {
				return e
			}
		}
		var found bool
		op, found, e = t.Once(scope, key, binding)
		if e != nil || found {
			return e
		}
		if e = s.snapshotBase(repo, in); e != nil {
			return e
		}
		tree, e := s.sourceTree(ctx, repo, root, entries)
		if e != nil {
			return e
		}
		oldTree, e := s.Git(repo, "rev-parse", S(in, "baseCommit")+"^{tree}")
		if e != nil {
			return e
		}
		if tree == oldTree && S(in, "integrationId") == "" {
			return conflict("源码快照没有变更")
		}
		if e = s.snapshotWriteScope(ctx, t, a, repo, in, tree); e != nil {
			var violation *sourceWriteScopeViolation
			if errors.As(e, &violation) {
				// No commit, operation, change or ref has been created. Only this
				// exact rejection authorizes editing before another submission.
				return fail("source_write_scope_rejected", e.Error(), 409)
			}
			return e
		}
		args := []string{"commit-tree", tree}
		for _, parent := range parents {
			args = append(args, "-p", parent)
		}
		args = append(args, "-m", S(in, "message"))
		commit, e := s.rawGit(ctx, repo, "", nil, args...)
		if e != nil {
			return e
		}
		op = resource("source_commit", S(in, "message"), repo.ProjectID, "", copyM(binding))
		op.Status = "prepared"
		op.Data["repositoryId"] = repo.ID
		op.Data["authorId"] = a.ID
		op.Data["commit"] = strings.TrimSpace(string(commit))
		op.Data["fileCount"] = len(entries)
		if S(in, "integrationId") != "" {
			op.Data["parents"] = parents
		}
		if a.Kind == "worker" {
			run, _ := t.Get(S(in, "runId"))
			op.GoalID = run.GoalID
			op.Data["requirementRevision"] = revision(run)
		}
		change := resource("change", op.Title, repo.ProjectID, op.GoalID, M{"repositoryId": id, "branch": S(in, "branch"), "commit": S(op.Data, "commit"), "authorId": a.ID, "sourceOperationId": op.ID, "runId": S(in, "runId")})
		change.Status = "prepared"
		op.Data["changeId"] = change.ID
		// Keep the prepared candidate reachable across GC and process interruption.
		if _, e = s.rawGit(ctx, repo, "", nil, "update-ref", "refs/intelligence/source/"+op.ID, S(op.Data, "commit"), strings.Repeat("0", len(S(op.Data, "commit")))); e != nil {
			return e
		}
		if e = t.Put(change); e != nil {
			return e
		}
		if e = t.Put(op); e != nil {
			return e
		}
		if e = t.Remember(scope, key, binding, op); e != nil {
			return e
		}
		return t.Audit(a, "repositories.snapshot.prepare", op, M{"commit": S(op.Data, "commit"), "digest": digest})
	})
	if err != nil {
		return result, err
	}
	err = s.Store.Update(ctx, func(t *Tx) error {
		var e error
		repo, e = s.snapshotAuthority(t, a, id, in)
		if e != nil {
			return e
		}
		op, _, e = t.Once(scope, key, binding)
		if e != nil {
			return e
		}
		if op.Status == "committed" {
			result = op
			return nil
		}
		if op.Status != "prepared" {
			return conflict("源码提交需要人工核对")
		}
		candidate := S(op.Data, "commit")
		if e = s.snapshotWriteScope(ctx, t, a, repo, in, candidate); e != nil {
			return e
		}
		head, _ := s.Git(repo, "rev-parse", "--verify", "refs/heads/"+S(in, "branch"))
		if head != candidate {
			if e = s.snapshotBase(repo, in); e != nil {
				return e
			}
			old := S(in, "expectedHead")
			if old == "" {
				old = strings.Repeat("0", len(candidate))
			}
			if _, e = s.rawGit(ctx, repo, "", nil, "update-ref", "refs/heads/"+S(in, "branch"), candidate, old); e != nil {
				return conflict("分支已变化，需要读取最新提交后再编辑")
			}
		}
		// If SQL fails here, the original prepared record survives and an exact retry
		// recognizes the candidate ref. Never roll back or overwrite a newer branch.
		op.Status = "committed"
		if e = t.Put(op); e != nil {
			return e
		}
		change, e := t.Get(S(op.Data, "changeId"))
		if e != nil {
			return e
		}
		change.Status = "committed"
		if e = t.Put(change); e != nil {
			return e
		}
		repo.Data["lastCommit"], repo.Data["lastBranch"] = candidate, S(in, "branch")
		if e = t.Put(repo); e != nil {
			return e
		}
		if e = t.Audit(a, "repositories.snapshot", op, M{"commit": candidate, "digest": digest}); e != nil {
			return e
		}
		result, e = t.Get(op.ID)
		return e
	})
	return result, err
}

func (s *Service) snapshotAuthority(t *Tx, a Actor, id string, in M) (Resource, error) {
	repo, e := s.scoped(t, a, id, "repository", "developer")
	if e != nil {
		return repo, e
	}
	if a.ID == "" || (a.Kind != "human" && a.Kind != "worker") {
		return repo, denied()
	}
	if !IDPattern.MatchString(S(in, "key")) {
		return repo, invalid("需要 1–96 字符的有效幂等键")
	}
	if !validBranch(S(in, "branch")) || S(in, "branch") == "main" {
		return repo, invalid("请使用有效的非主分支")
	}
	if !CommitPattern.MatchString(S(in, "baseCommit")) || (S(in, "expectedHead") != "" && !CommitPattern.MatchString(S(in, "expectedHead"))) {
		return repo, invalid("需要完整的基础提交和预期分支提交")
	}
	if e = validateText(S(in, "message"), "提交说明", 1000); e != nil {
		return repo, e
	}
	if a.Kind == "worker" {
		input := copyM(in)
		input["id"] = id
		if e = s.validWorkerRun(t, a, "repositories.commit", input); e != nil {
			return repo, e
		}
		if e = s.workerSourceBinding(t, input, true); e != nil {
			return repo, e
		}
		if S(in, "key") != S(in, "runId") {
			return repo, conflict("源码提交幂等键必须绑定当前执行")
		}
		run, _ := t.Get(S(in, "runId"))
		if S(Obj(run.Data["task"]), "sourceMode") == "integrate" {
			_, _, _, binding, err := s.integrationAuthority(t, a, in)
			if err != nil {
				return repo, err
			}
			prep, err := integrationPreparation(t, run, binding)
			if err != nil {
				return repo, err
			}
			if S(in, "integrationId") != prep.ID || S(in, "resolutionDigest") != S(run.Data, "integrationResolutionDigest") {
				return repo, conflict("源码提交需要当前集成准备和冲突决定")
			}
		} else if S(in, "integrationId") != "" || S(in, "resolutionDigest") != "" {
			return repo, denied()
		}
	} else if S(in, "integrationId") != "" || S(in, "resolutionDigest") != "" {
		return repo, denied()
	}
	return repo, nil
}
func (s *Service) workerSourceBinding(t *Tx, in M, snapshot ...bool) error {
	run, e := t.Get(S(in, "runId"))
	if e != nil {
		return e
	}
	task, e := t.Get(S(run.Data, "taskId"))
	if e != nil {
		return e
	}
	if S(task.Data, "kind") != "code" {
		return denied()
	}
	if e = s.AgentProgressGate(t, task); e != nil {
		return e
	}
	if S(task.Data, "sourceMode") == "integrate" && (len(snapshot) == 0 || !snapshot[0]) {
		return conflict("集成任务必须通过受控源码快照提交")
	}
	branch := S(task.Data, "branch")
	if branch == "" {
		branch = "agent/" + run.GoalID
	}
	if b := Obj(task.Data["sourceBinding"]); len(b) > 0 {
		branch = S(b, "targetBranch")
	}
	if S(in, "branch") != branch {
		return denied()
	}
	if base := S(task.Data, "commit"); base != "" && base != S(in, "baseCommit") {
		return conflict("源码基础提交必须与批准的任务一致")
	}
	if _, bound := task.Data["sourceBinding"]; bound {
		binding, e := taskSourceBinding(task)
		if e != nil {
			return e
		}
		if Hash(Obj(run.Data["task"])["sourceBinding"]) != Hash(binding) || !sameWritePaths(task.Data, Obj(run.Data["task"])) || S(in, "expectedHead") != S(binding, "expectedHead") {
			return conflict("源码提交必须使用领取时绑定的目标分支状态")
		}
	}
	return nil
}
func (s *Service) snapshotBase(repo Resource, in M) error {
	head, _ := s.Git(repo, "rev-parse", "--verify", "refs/heads/"+S(in, "branch"))
	if head != S(in, "expectedHead") || (head != "" && head != S(in, "baseCommit")) {
		return conflict("分支已变化，需要读取最新提交后再编辑")
	}
	actual, e := s.Git(repo, "rev-parse", "--verify", S(in, "baseCommit")+"^{commit}")
	if e != nil || actual != S(in, "baseCommit") {
		return invalid("基础提交不存在")
	}
	return nil
}

// rawGit disables repository hooks and external config; stdin blobs bypass all
// attributes, line-ending conversions, encodings and clean/smudge filters.
func (s *Service) rawGit(ctx context.Context, repo Resource, index string, input io.Reader, args ...string) ([]byte, error) {
	timeout, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	prefix := []string{"--git-dir", s.RepoPath(repo), "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsync=committed", "-c", "user.name=Intelligence Substrate", "-c", "user.email=platform@localhost"}
	cmd := exec.CommandContext(timeout, "git", append(prefix, args...)...)
	cmd.Env = gitEnvironment()
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	cmd.Stdin = input
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, e := cmd.Output()
	if e != nil {
		return nil, fmt.Errorf("source Git operation failed: %w: %s", e, stderr.String())
	}
	return out, nil
}
func (s *Service) sourceTree(ctx context.Context, repo Resource, root string, entries []source.Entry) (string, error) {
	// Index location is private and never taken from a source-controlled file.
	indexDir, e := os.MkdirTemp("", "is-git-index-")
	if e != nil {
		return "", e
	}
	defer os.RemoveAll(indexDir)
	index := filepath.Join(indexDir, "index")
	var info bytes.Buffer
	for _, entry := range entries {
		path, e := source.Path(root, entry.Name)
		if e != nil {
			return "", e
		}
		f, e := os.Open(path)
		if e != nil {
			return "", e
		}
		hash, e := s.rawGit(ctx, repo, "", f, "hash-object", "-w", "--stdin")
		f.Close()
		if e != nil {
			return "", e
		}
		mode := 100644
		if entry.Executable {
			mode = 100755
		}
		info.WriteString(strconv.Itoa(mode) + " " + strings.TrimSpace(string(hash)) + "\t" + entry.Name + "\x00")
	}
	if _, e = s.rawGit(ctx, repo, index, nil, "read-tree", "--empty"); e != nil {
		return "", e
	}
	if _, e = s.rawGit(ctx, repo, index, &info, "update-index", "-z", "--index-info"); e != nil {
		return "", e
	}
	tree, e := s.rawGit(ctx, repo, index, nil, "write-tree")
	return strings.TrimSpace(string(tree)), e
}
