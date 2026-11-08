package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/intelligence-substrate/platform/internal/source"
)

const maxIntegrationParents = 8
const maxIntegrationConflicts = 100

// Preparation is controller-owned state. It can outlive a lease; conflict
// decisions are owned by a single run and are never inherited by a retry.
func (s *Service) integrationAuthority(t *Tx, a Actor, in M) (run, task, repo Resource, binding M, err error) {
	if a.Kind != "worker" {
		err = denied()
		return
	}
	if err = s.validWorkerRun(t, a, "sources.prepare", in); err != nil {
		return
	}
	run, err = t.Get(S(in, "runId"))
	if err != nil {
		return
	}
	task, err = t.Get(S(run.Data, "taskId"))
	if err != nil {
		return
	}
	if task.Status != "running" || task.ProjectID != run.ProjectID || task.GoalID != run.GoalID ||
		S(task.Data, "kind") != "code" || S(task.Data, "sourceMode") != "integrate" ||
		S(task.Data, "planId") != S(run.Data, "planId") || revision(task) != revision(run) {
		err = denied()
		return
	}
	g, e := t.Get(run.GoalID)
	if e != nil {
		err = e
		return
	}
	if S(g.Data, "activePlanId") != S(run.Data, "planId") {
		err = conflict("集成执行不属于当前批准方案")
		return
	}
	binding, err = taskSourceBinding(task)
	if err != nil {
		return
	}
	parents := Strings(binding["integrationParents"])
	if Hash(Obj(run.Data["task"])["sourceBinding"]) != Hash(binding) || len(parents) == 0 || len(parents) > maxIntegrationParents {
		err = conflict("集成输入与当前执行绑定不一致")
		return
	}
	repo, err = t.Get(S(binding, "repositoryId"))
	if err == nil && (repo.Kind != "repository" || repo.ProjectID != run.ProjectID) {
		err = denied()
	}
	return
}

func (s *Service) integrationCommand(ctx context.Context, t *Tx, a Actor, command string, in M) (Resource, error) {
	run, task, repo, binding, e := s.integrationAuthority(t, a, in)
	if e != nil {
		return run, e
	}
	// Decisions freeze on the first prepared source submission, preserving
	// exact replay even after a lost acknowledgement.
	ops, err := t.List(run.ProjectID, "source_commit", run.GoalID)
	if err != nil {
		return run, err
	}
	for _, op := range ops {
		if S(op.Data, "runId") == run.ID {
			return run, conflict("已提交源码，请确认原提交结果")
		}
	}
	if command == "sources.prepare" {
		prepBinding := M{"bindingDigest": S(binding, "digest")}
		scope, key := "integration:"+task.ID, S(binding, "digest")
		prep, found, err := t.Once(scope, key, prepBinding)
		if err != nil {
			return run, err
		}
		if !found {
			prep = resource("integration", "集成："+task.Title, run.ProjectID, run.GoalID, M{
				"taskId": task.ID, "planId": S(run.Data, "planId"), "requirementRevision": revision(run),
				"repositoryId": repo.ID, "bindingDigest": S(binding, "digest"), "parents": Strings(binding["integrationParents"]),
			})
			prep.Status = "prepared"
			if e = s.prepareIntegration(ctx, repo, binding, &prep); e != nil {
				return run, e
			}
			// The operation may have outlasted a lease. Check again before SQL
			// persistence; no output branch has been changed by preparation.
			if _, _, _, _, e = s.integrationAuthority(t, a, in); e != nil {
				return run, e
			}
			if e = t.Put(prep); e != nil {
				return run, e
			}
			if e = t.Remember(scope, key, prepBinding, prep); e != nil {
				return run, e
			}
		}
		if S(run.Data, "integrationId") == "" {
			run.Data["integrationId"] = prep.ID
			run.Data["integrationWorkspaceCommit"] = S(prep.Data, "workspaceCommit")
			run.Data["integrationConflicts"] = Strings(prep.Data["conflicts"])
			run.Data["integrationResolutions"] = M{}
			run.Data["integrationResolutionDigest"] = Hash(M{})
		}
		return run, t.Put(run)
	}
	prep, e := integrationPreparation(t, run, binding)
	if e != nil {
		return run, e
	}
	items := Items(in["resolutions"])
	if len(items) == 0 || len(items) > maxIntegrationConflicts {
		return run, invalid("需要 1–100 个冲突处理决定")
	}
	decisions, seen := copyM(Obj(run.Data["integrationResolutions"])), map[string]bool{}
	for _, item := range items {
		m := Obj(item)
		path, _ := m["path"].(string)
		if !Has(Strings(prep.Data["conflicts"]), path) || seen[path] {
			return run, invalid("只能处理准备记录中的冲突路径，且不能重复")
		}
		seen[path] = true
		for key := range m {
			if !Has([]string{"path", "digest", "executable", "deleted"}, key) {
				return run, invalid("冲突决定包含未知字段")
			}
		}
		if deleted, exists := m["deleted"]; exists {
			if deleted != true || len(m) != 2 {
				return run, invalid("删除决定仅包含 path 和 deleted=true")
			}
			decisions[path] = M{"deleted": true}
		} else {
			mode, valid := m["executable"].(bool)
			digest := S(m, "digest")
			if !valid || !digestPattern.MatchString(digest) || len(m) != 3 {
				return run, invalid("文件决定需要 SHA256 和布尔 executable")
			}
			decisions[path] = M{"digest": digest, "executable": mode}
		}
	}
	run.Data["integrationResolutions"] = decisions
	run.Data["integrationResolutionDigest"] = Hash(decisions)
	return run, t.Put(run)
}

func integrationPreparation(t *Tx, run Resource, binding M) (Resource, error) {
	prep, e := t.Get(S(run.Data, "integrationId"))
	if e != nil || prep.Kind != "integration" || prep.Status != "prepared" || prep.ProjectID != run.ProjectID || prep.GoalID != run.GoalID ||
		S(prep.Data, "taskId") != S(run.Data, "taskId") || S(prep.Data, "planId") != S(run.Data, "planId") || revision(prep) != revision(run) ||
		S(prep.Data, "repositoryId") != S(binding, "repositoryId") || S(prep.Data, "bindingDigest") != S(binding, "digest") ||
		Hash(prep.Data["parents"]) != Hash(binding["integrationParents"]) || S(prep.Data, "workspaceCommit") != S(run.Data, "integrationWorkspaceCommit") {
		return prep, conflict("需要当前执行绑定的集成准备记录")
	}
	return prep, nil
}

func (s *Service) prepareIntegration(ctx context.Context, repo Resource, binding M, prep *Resource) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	current := S(binding, "commit")
	conflicts := map[string]bool{}
	root, e := os.MkdirTemp("", "is-integration-paths-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(root)
	for i, parent := range Strings(binding["integrationParents"]) {
		if parent == current {
			continue
		}
		out, e := s.mergeTree(ctx, repo, current, parent)
		if e != nil {
			return e
		}
		parts := strings.Split(string(out), "\x00")
		if len(parts) < 2 || !CommitPattern.MatchString(parts[0]) {
			return conflict("Git 集成准备返回无效结构")
		}
		for _, path := range parts[1:] {
			if path == "" {
				continue
			}
			if _, e = source.Path(root, path); e != nil {
				return invalid("集成冲突路径不可移植：" + e.Error())
			}
			conflicts[path] = true
			if len(conflicts) > maxIntegrationConflicts {
				return conflict("集成冲突超过一百条，请分阶段集成")
			}
		}
		commit, e := s.rawGit(ctx, repo, "", nil, "commit-tree", parts[0], "-p", current, "-p", parent, "-m", "Private integration preparation")
		if e != nil {
			return e
		}
		current = strings.TrimSpace(string(commit))
		if _, e = s.rawGit(ctx, repo, "", nil, "update-ref", fmt.Sprintf("refs/intelligence/integration/%s/%d", prep.ID, i), current); e != nil {
			return e
		}
	}
	// Validate the complete raw tree, portable names, modes, sizes and entry
	// count before it can reach any execution tool.
	archive, e := s.sourceArchive(ctx, repo, current)
	if e != nil {
		return e
	}
	defer os.Remove(archive)
	file, e := os.Open(archive)
	if e != nil {
		return e
	}
	_, _, e = source.Extract(file, root)
	file.Close()
	if e != nil {
		return e
	}
	paths := []string{}
	textPaths := []string{}
	for path := range conflicts {
		paths = append(paths, path)
		name, err := source.Path(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(name)
		text := true
		if err == nil && info.Mode().IsRegular() {
			body, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			text = !bytes.ContainsRune(body[:min(len(body), 8000)], 0)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		if text {
			textPaths = append(textPaths, path)
		}
	}
	sort.Strings(paths)
	sort.Strings(textPaths)
	prep.Data["workspaceCommit"], prep.Data["conflicts"], prep.Data["textConflicts"] = current, paths, textPaths
	return ctx.Err()
}

// merge-tree exits 1 for valid conflicts. Keep that output separate from Git
// failures, and bound machine output so a malformed repository cannot exhaust
// controller memory. No worktree, hooks, filters or external merge driver runs.
func (s *Service) mergeTree(ctx context.Context, repo Resource, left, right string) ([]byte, error) {
	// Use a configless Git directory with only the trusted object store. A
	// repository's local merge-driver configuration must never execute here.
	private, e := os.MkdirTemp("", "is-merge-git-")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(private)
	for _, name := range []string{"objects", "refs"} {
		if e = os.Mkdir(filepath.Join(private, name), 0700); e != nil {
			return nil, e
		}
	}
	if e = os.WriteFile(filepath.Join(private, "HEAD"), []byte("ref: refs/heads/main\n"), 0600); e != nil {
		return nil, e
	}
	cmd := exec.CommandContext(ctx, "git", "--git-dir", private, "-c", "core.hooksPath="+os.DevNull, "-c", "core.attributesFile="+os.DevNull,
		"-c", "merge.renames=false", "merge-tree", "--write-tree", "--name-only", "-z", "--no-messages", left, right)
	objectDir, e := filepath.Abs(filepath.Join(s.RepoPath(repo), "objects"))
	if e != nil {
		return nil, e
	}
	cmd.Env = append(gitEnvironment(), "GIT_OBJECT_DIRECTORY="+objectDir, "GIT_ATTR_NOSYSTEM=1")
	out, stderr := &integrationOutput{max: 128 << 10}, &integrationOutput{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = out, stderr
	e = cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if out.exceeded || stderr.exceeded {
		return nil, conflict("集成输出超过限制，请分阶段集成")
	}
	if e != nil {
		if exit, ok := e.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			return nil, fmt.Errorf("Git integration failed: %w: %s", e, stderr.String())
		}
	}
	return out.Bytes(), nil
}

type integrationOutput struct {
	bytes.Buffer
	max      int
	exceeded bool
}

func (b *integrationOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > b.max {
		b.exceeded = true
		return 0, fmt.Errorf("integration output limit")
	}
	return b.Buffer.Write(data)
}

func (s *Service) integrationSnapshot(t *Tx, a Actor, in M, root string, entries []source.Entry) ([]string, error) {
	run, _, _, binding, e := s.integrationAuthority(t, a, in)
	if e != nil {
		return nil, e
	}
	prep, e := integrationPreparation(t, run, binding)
	if e != nil {
		return nil, e
	}
	decisions := Obj(run.Data["integrationResolutions"])
	if S(in, "integrationId") != prep.ID || S(in, "resolutionDigest") != Hash(decisions) || S(run.Data, "integrationResolutionDigest") != Hash(decisions) {
		return nil, conflict("源码快照必须使用当前集成及冲突决定")
	}
	files := map[string]source.Entry{}
	for _, entry := range entries {
		files[entry.Name] = entry
	}
	for _, name := range Strings(prep.Data["conflicts"]) {
		decision := Obj(decisions[name])
		entry, exists := files[name]
		if B(decision, "deleted") && !exists {
			continue
		}
		if !exists || len(decision) == 0 || B(decision, "deleted") || entry.Executable != B(decision, "executable") {
			return nil, conflict("冲突尚未处理或文件模式已变化：" + name)
		}
		path, e := source.Path(root, name)
		if e != nil {
			return nil, e
		}
		file, e := os.Open(path)
		if e != nil {
			return nil, e
		}
		hash := sha256.New()
		_, e = io.Copy(hash, file)
		file.Close()
		if e != nil {
			return nil, e
		}
		if fmt.Sprintf("%x", hash.Sum(nil)) != S(decision, "digest") {
			return nil, conflict("冲突处理后文件内容已变化：" + name)
		}
		body, e := os.ReadFile(path)
		if e != nil {
			return nil, e
		}
		if (Has(Strings(prep.Data["textConflicts"]), name) || !bytes.ContainsRune(body[:min(len(body), 8000)], 0)) && hasConflictMarkers(body) {
			return nil, conflict("冲突文件仍包含 Git 冲突标记：" + name)
		}
	}
	return Strings(binding["integrationParents"]), nil
}

func hasConflictMarkers(body []byte) bool {
	// Do not allocate one slice per line: a bounded archive may still contain
	// millions of empty lines in a single file.
	for len(body) > 0 {
		line, rest, _ := bytes.Cut(body, []byte("\n"))
		for _, marker := range []string{"<<<<<<<", "=======", ">>>>>>>", "|||||||"} {
			if bytes.HasPrefix(line, []byte(marker)) {
				return true
			}
		}
		body = rest
	}
	return false
}
