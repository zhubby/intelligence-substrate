package platform

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func (s *Service) RepoPath(r Resource) string { return filepath.Join(s.Root, "repos", r.ID+".git") }
func (s *Service) Git(r Resource, args ...string) (string, error) {
	if r.Kind != "repository" || !IDPattern.MatchString(r.ID) {
		return "", missing()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--git-dir", s.RepoPath(r)}, args...)...)
	cmd.Env = gitEnvironment()
	b, e := cmd.CombinedOutput()
	if e != nil {
		return "", conflict("Git 操作失败：" + strings.TrimSpace(string(b)))
	}
	return strings.TrimSpace(string(b)), nil
}
func validBranch(branch string) bool {
	if branch == "" || strings.HasPrefix(branch, "-") || len(branch) > 160 {
		return false
	}
	return exec.Command("git", "check-ref-format", "--branch", branch).Run() == nil
}
func (s *Service) gitCommand(t *Tx, a Actor, cmd string, in M) (Resource, error) {
	if cmd == "repositories.create" {
		p := S(in, "projectId")
		if e := s.allowed(t, a, p, "developer"); e != nil {
			return Resource{}, e
		}
		if e := validateText(S(in, "title"), "仓库名称", 120); e != nil {
			return Resource{}, e
		}
		r := resource("repository", S(in, "title"), p, "", M{"description": S(in, "description"), "defaultBranch": "main", "createdBy": a.ID})
		if e := os.MkdirAll(filepath.Join(s.Root, "repos"), 0700); e != nil {
			return r, e
		}
		path := s.RepoPath(r)
		c := exec.Command("git", "init", "--bare", "--initial-branch=main", path)
		if b, e := c.CombinedOutput(); e != nil {
			return r, fmt.Errorf("git init: %w: %s", e, b)
		}
		_, e := s.Git(r, "config", "http.receivepack", "true")
		if e != nil {
			return r, e
		}
		hook := `#!/bin/sh
while read old new ref; do
  if [ "$ref" = "refs/heads/main" ]; then
    echo "main is protected; use a reviewed merge request" >&2
    exit 1
  fi
done
`
		if e = os.WriteFile(filepath.Join(path, "hooks", "pre-receive"), []byte(hook), 0700); e != nil {
			return r, e
		}
		tree, e := s.Git(r, "mktree")
		if e != nil {
			return r, e
		}
		commit, e := s.gitIdentity(r, "commit-tree", tree, "-m", "Initialize project repository")
		if e != nil {
			return r, e
		}
		if _, e = s.Git(r, "update-ref", "refs/heads/main", commit); e != nil {
			return r, e
		}
		r.Data["head"] = commit
		return r, t.Put(r)
	}
	if cmd == "repositories.commit" {
		r, e := s.scoped(t, a, S(in, "id"), "repository", "developer")
		if e != nil {
			return r, e
		}
		if a.Kind == "worker" {
			run, err := t.Get(S(in, "runId"))
			if err != nil {
				return r, err
			}
			if _, seed := Obj(run.Data["task"])["blueprintSeed"]; seed {
				return r, conflict("蓝图初始化须提交完整的批准源码归档")
			}
		}
		branch := S(in, "branch")
		if !validBranch(branch) || branch == "main" {
			return r, invalid("请使用有效的非主分支")
		}
		files := Obj(in["files"])
		if len(files) == 0 || len(files) > 100 {
			return r, invalid("提交需要 1–100 个文件")
		}
		if e = validateText(S(in, "message"), "提交说明", 1000); e != nil {
			return r, e
		}
		head, _ := s.Git(r, "rev-parse", "--verify", "refs/heads/"+branch)
		expected := S(in, "expectedHead")
		if head != "" && expected != head {
			return r, conflict("分支已变化，需要读取最新提交后再编辑")
		}
		base := S(in, "baseCommit")
		if base == "" {
			base = head
		}
		if base == "" {
			base, _ = s.Git(r, "rev-parse", "refs/heads/main")
		}
		if !CommitPattern.MatchString(base) {
			return r, invalid("基础提交无效")
		}
		dir, e := os.MkdirTemp("", "is-worktree-")
		if e != nil {
			return r, e
		}
		os.Remove(dir)
		defer os.RemoveAll(dir)
		if _, e = s.Git(r, "worktree", "add", "--detach", dir, base); e != nil {
			return r, e
		}
		defer s.Git(r, "worktree", "remove", "--force", dir)
		for name, value := range files {
			content, ok := value.(string)
			if !ok || len(content) > 1<<20 {
				return r, invalid("文件内容无效或超过 1 MiB")
			}
			dest, e := SafePath(dir, name)
			if e != nil {
				return r, e
			}
			if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
				return r, e
			}
			if e = os.WriteFile(dest, []byte(content), 0600); e != nil {
				return r, e
			}
		}
		if _, e = workGit(dir, "add", "--all"); e != nil {
			return r, e
		}
		tree, e := workGit(dir, "write-tree")
		if e != nil {
			return r, e
		}
		if e = s.snapshotWriteScope(context.Background(), t, a, r, in, tree); e != nil {
			return r, e
		}
		if _, e = workGit(dir, "-c", "user.name=Intelligence Substrate", "-c", "user.email=platform@localhost", "commit", "-m", S(in, "message")); e != nil {
			return r, e
		}
		commit, e := workGit(dir, "rev-parse", "HEAD")
		if e != nil {
			return r, e
		}
		old := head
		if old == "" {
			old = strings.Repeat("0", 40)
		}
		if _, e = s.Git(r, "update-ref", "refs/heads/"+branch, commit, old); e != nil {
			return r, conflict("分支已变化，请重新提交")
		}
		change := resource("change", S(in, "message"), r.ProjectID, "", M{"repositoryId": r.ID, "branch": branch, "commit": commit, "authorId": a.ID, "runId": S(in, "runId")})
		if e = t.Put(change); e != nil {
			return r, e
		}
		r.Data["lastCommit"] = commit
		r.Data["lastBranch"] = branch
		return r, t.Put(r)
	}
	if cmd == "reviews.create" {
		r, e := s.scoped(t, a, S(in, "repositoryId"), "repository", "developer")
		if e != nil {
			return r, e
		}
		branch := S(in, "branch")
		if !validBranch(branch) || branch == "main" {
			return r, invalid("源分支无效")
		}
		head, e := s.Git(r, "rev-parse", "--verify", "refs/heads/"+branch)
		if e != nil {
			return r, e
		}
		base, e := s.Git(r, "rev-parse", "refs/heads/main")
		if e != nil {
			return r, e
		}
		title := S(in, "title")
		if e = validateText(title, "评审标题", 200); e != nil {
			return r, e
		}
		if goalID := S(in, "goalId"); goalID != "" {
			g, e := s.scoped(t, a, goalID, "goal")
			if e != nil {
				return r, e
			}
			if g.ProjectID != r.ProjectID {
				return r, denied()
			}
		}
		review := resource("review", title, r.ProjectID, S(in, "goalId"), M{"repositoryId": r.ID, "branch": branch, "head": head, "base": base, "authorId": a.ID, "approvedBy": ""})
		review.Status = "open"
		return review, t.Put(review)
	}
	review, e := s.scoped(t, a, S(in, "id"), "review", "architect", "quality")
	if e != nil {
		return review, e
	}
	if e = human(a); e != nil {
		return review, e
	}
	repo, e := t.Get(S(review.Data, "repositoryId"))
	if e != nil {
		return review, e
	}
	head, e := s.Git(repo, "rev-parse", "refs/heads/"+S(review.Data, "branch"))
	if e != nil {
		return review, e
	}
	if head != S(review.Data, "head") {
		return review, conflict("源分支提交已变化，需创建新的评审")
	}
	if review.Status == "merged" {
		return review, conflict("评审已合并")
	}
	if cmd == "reviews.approve" {
		if a.ID == S(review.Data, "authorId") {
			return review, conflict("不能批准自己的代码评审")
		}
		changes, e := t.List(review.ProjectID, "change", "")
		if e != nil {
			return review, e
		}
		for _, change := range changes {
			if S(change.Data, "repositoryId") == repo.ID && S(change.Data, "commit") == head && S(change.Data, "authorId") == a.ID {
				return review, conflict("代码生产者不能批准同一提交")
			}
		}
		review.Status = "approved"
		review.Data["approvedBy"] = a.ID
		review.Data["approvedHead"] = head
		return review, t.Put(review)
	}
	if review.Status != "approved" || S(review.Data, "approvedHead") != head {
		return review, conflict("需要针对当前提交的独立评审")
	}
	runs, e := t.List(review.ProjectID, "run", review.GoalID)
	if e != nil {
		return review, e
	}
	passed := false
	for _, run := range runs {
		if run.Status != "succeeded" {
			continue
		}
		task, e := t.Get(S(run.Data, "taskId"))
		if e != nil {
			continue
		}
		if S(task.Data, "kind") == "test" && S(task.Data, "repositoryId") == repo.ID && S(task.Data, "commit") == head && S(run.Data, "evidenceId") != "" {
			ev, err := t.Get(S(run.Data, "evidenceId"))
			g, gerr := t.Get(run.GoalID)
			if err == nil && gerr == nil && ev.Status == "passed" && revision(ev) == g.Revision {
				passed = true
			}
		}
	}
	if !passed {
		return review, conflict("当前提交缺少独立 CI 验证")
	}
	main, e := s.Git(repo, "rev-parse", "refs/heads/main")
	if e != nil {
		return review, e
	}
	if main != S(review.Data, "base") {
		return review, conflict("主分支已变化，需要重新集成与验证")
	}
	if _, e = s.Git(repo, "merge-base", "--is-ancestor", main, head); e != nil {
		return review, conflict("请先把当前主分支集成到源分支，再验证和评审")
	}
	if _, e = s.Git(repo, "update-ref", "refs/heads/main", head, main); e != nil {
		return review, e
	}
	review.Status = "merged"
	review.Data["mergeCommit"] = head
	return review, t.Put(review)
}

func (s *Service) gitIdentity(r Resource, args ...string) (string, error) {
	return s.Git(r, append([]string{"-c", "user.name=Intelligence Substrate", "-c", "user.email=platform@localhost"}, args...)...)
}
func workGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	c.Env = gitEnvironment()
	b, e := c.CombinedOutput()
	if e != nil {
		return "", conflict("Git 工作区操作失败：" + string(b))
	}
	return strings.TrimSpace(string(b)), nil
}
func gitEnvironment() []string {
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	for _, key := range []string{"PATH", "TMPDIR", "TMP", "TEMP", "SystemRoot", "SYSTEMROOT", "COMSPEC", "LANG", "LC_ALL"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}
func SafePath(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, "\\") {
		return "", invalid("文件路径无效")
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", invalid("路径越界")
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == ".git" {
			return "", invalid("不能直接修改 Git 元数据")
		}
	}
	dest := filepath.Join(root, clean)
	current := root
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		st, e := os.Lstat(current)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return "", e
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return "", invalid("不能穿过符号链接")
		}
	}
	return dest, nil
}

// RepositoryRefs distinguishes an absent target from a failed repository read.
// Workers use it only to check a code target; source still comes from the SHA.
func (s *Service) RepositoryRefs(ctx context.Context, a Actor, id string) (M, error) {
	repo, e := s.GetResource(ctx, a, id)
	if e != nil || repo.Kind != "repository" {
		return nil, missing()
	}
	output, e := s.Git(repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	if e != nil {
		return nil, e
	}
	heads := M{}
	for _, line := range strings.Split(output, "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 {
			heads[strings.TrimPrefix(parts[0], "refs/heads/")] = parts[1]
		}
	}
	return M{"repositoryId": repo.ID, "heads": heads}, nil
}

func (s *Service) RepositoryView(ctx context.Context, a Actor, id, branch, path string) (M, error) {
	r, e := s.GetResource(ctx, a, id)
	if e != nil || r.Kind != "repository" {
		return nil, missing()
	}
	if branch == "" {
		branch = "main"
	}
	if !validBranch(branch) {
		return nil, invalid("分支无效")
	}
	branches, e := s.Git(r, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if e != nil {
		return nil, e
	}
	log, e := s.Git(r, "log", "-15", "--format=%H%x09%s", "refs/heads/"+branch)
	if e != nil {
		return nil, e
	}
	files, e := s.Git(r, "ls-tree", "-r", "--name-only", "refs/heads/"+branch)
	if e != nil {
		return nil, e
	}
	out := M{"repository": r, "branches": strings.Fields(branches), "files": strings.Split(files, "\n"), "log": log}
	if path != "" {
		if _, e = SafePath("/unused-root", path); e != nil {
			return nil, e
		}
		blob, e := s.Git(r, "show", "refs/heads/"+branch+":"+path)
		if e != nil {
			return nil, e
		}
		if len(blob) > 1<<20 {
			return nil, invalid("文件过大，无法在线预览")
		}
		out["content"] = blob
	}
	return out, nil
}
func (s *Service) GitHTTP(a Actor, w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/git/"), "/")
	if len(parts) < 2 || !strings.HasSuffix(parts[0], ".git") {
		http.NotFound(w, r)
		return
	}
	endpoint := strings.Join(parts[1:], "/")
	if !Has([]string{"info/refs", "git-upload-pack", "git-receive-pack"}, endpoint) {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimSuffix(parts[0], ".git")
	if !IDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	repo, e := s.GetResource(r.Context(), a, id)
	if e != nil || repo.Kind != "repository" {
		http.Error(w, "repository inaccessible", 403)
		return
	}
	push := strings.Contains(r.URL.Path, "git-receive-pack") || r.URL.Query().Get("service") == "git-receive-pack"
	if push && a.Kind == "worker" {
		http.Error(w, "workers submit changes through the lease-bound API", 403)
		return
	}
	if push && s.CanAccess(r.Context(), a, repo.ProjectID, "developer") != nil {
		http.Error(w, "push forbidden", 403)
		return
	}
	git, e := exec.LookPath("git")
	if e != nil {
		http.Error(w, "git unavailable", 503)
		return
	}
	execPath, e := exec.Command(git, "--exec-path").Output()
	if e != nil {
		http.Error(w, "git unavailable", 503)
		return
	}
	handler := cgi.Handler{Path: filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend"), Root: "/git", Env: []string{"GIT_PROJECT_ROOT=" + filepath.Join(s.Root, "repos"), "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=" + a.ID, "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=receive.hideRefs", "GIT_CONFIG_VALUE_0=refs/intelligence/"}}
	if push && r.Method == "POST" {
		e = s.Store.Update(r.Context(), func(t *Tx) error {
			if e := s.allowed(t, a, repo.ProjectID, "developer"); e != nil {
				return e
			}
			before, e := s.Git(repo, "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads")
			if e != nil {
				return e
			}
			original := map[string]string{}
			for _, line := range strings.Split(before, "\n") {
				pair := strings.Fields(line)
				if len(pair) == 2 {
					original[pair[0]] = pair[1]
				}
			}
			handler.ServeHTTP(w, r)
			refs, e := s.Git(repo, "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads")
			if e != nil {
				return e
			}
			for _, line := range strings.Split(refs, "\n") {
				pair := strings.Fields(line)
				if len(pair) != 2 || pair[0] == "main" || original[pair[0]] == pair[1] {
					continue
				}
				change := resource("change", "Git push", repo.ProjectID, "", M{"repositoryId": repo.ID, "branch": pair[0], "commit": pair[1], "authorId": a.ID})
				if e = t.Put(change); e != nil {
					return e
				}
			}
			return t.Audit(a, "git.push", repo, M{})
		})
		if e != nil {
			slog.Error("Git transport or provenance recording failed", "repository", repo.ID, "error", e)
		}
	} else {
		handler.ServeHTTP(w, r)
	}
}
