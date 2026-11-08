package platform

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/intelligence-substrate/platform/internal/source"
)

func sourceTar(t *testing.T, files map[string][]byte, executable string) []byte {
	t.Helper()
	root := t.TempDir()
	entries := []source.Entry{}
	for name, body := range files {
		path := filepath.Join(root, name)
		if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(path, body, 0600); e != nil {
			t.Fatal(e)
		}
		entries = append(entries, source.Entry{Name: name, Size: int64(len(body)), Executable: name == executable})
	}
	var out bytes.Buffer
	if e := source.Write(&out, root, entries); e != nil {
		t.Fatal(e)
	}
	return out.Bytes()
}
func gitBytes(t *testing.T, s *Service, repo Resource, args ...string) []byte {
	t.Helper()
	c := exec.Command("git", append([]string{"--git-dir", s.RepoPath(repo)}, args...)...)
	c.Env = gitEnvironment()
	b, e := c.Output()
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func snapshotInput(repo Resource) M {
	return M{"key": "source-one", "branch": "feature", "baseCommit": S(repo.Data, "head"), "expectedHead": "", "message": "Complete generated source"}
}

func TestSnapshotPreservesRawTreeDeletionModesAndReplay(t *testing.T) {
	s, a, p := fixture(t)
	repo := must(t, s, a, "repositories.create", M{"projectId": p.ID, "title": "source"})
	repo = must(t, s, a, "repositories.commit", M{"id": repo.ID, "branch": "feature", "message": "Old file", "files": M{"removed.txt": "old"}})
	base := S(repo.Data, "lastCommit")
	in := snapshotInput(repo)
	in["baseCommit"], in["expectedHead"] = base, base
	binary := bytes.Repeat([]byte{0, 255, 128, 13, 10}, 250000)
	body := sourceTar(t, map[string][]byte{"assets/font.bin": binary, "bin/start.sh": []byte("#!/bin/sh\r\necho ready\r\n"), ".gitattributes": []byte("* text eol=lf\nassets/* export-ignore\n")}, "bin/start.sh")
	op, e := s.CommitSnapshot(context.Background(), a, repo.ID, in, bytes.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	commit := S(op.Data, "commit")
	if op.Status != "committed" || !CommitPattern.MatchString(commit) {
		t.Fatalf("missing committed operation: %+v", op)
	}
	if !bytes.Equal(gitBytes(t, s, repo, "show", commit+":assets/font.bin"), binary) {
		t.Fatal("Git changed binary source bytes")
	}
	tree, _ := s.Git(repo, "ls-tree", "-r", commit)
	if strings.Contains(tree, "removed.txt") || !strings.Contains(tree, "100755 blob") {
		t.Fatalf("deletion/mode lost: %s", tree)
	}
	if parent, _ := s.Git(repo, "rev-parse", commit+"^"); parent != base {
		t.Fatal("snapshot lost approved parent")
	}
	again, e := s.CommitSnapshot(context.Background(), a, repo.ID, in, bytes.NewReader(body))
	if e != nil || again.ID != op.ID || S(again.Data, "commit") != commit {
		t.Fatalf("replay duplicated commit: %+v %v", again, e)
	}
	if _, e = s.CommitSnapshot(context.Background(), a, repo.ID, in, bytes.NewReader(sourceTar(t, map[string][]byte{"other": []byte("different")}, ""))); e == nil {
		t.Fatal("same key accepted different source")
	}
	stale := copyM(in)
	stale["key"] = "source-two"
	if _, e = s.CommitSnapshot(context.Background(), a, repo.ID, stale, bytes.NewReader(body)); e == nil {
		t.Fatal("stale branch overwrote source")
	}
	archive, e := s.SourceArchive(context.Background(), a, repo.ID, commit)
	if e != nil {
		t.Fatal(e)
	}
	defer os.Remove(archive)
	f, _ := os.Open(archive)
	defer f.Close()
	extracted := t.TempDir()
	entries, _, e := source.Extract(f, extracted)
	if e != nil || len(entries) != 3 {
		t.Fatalf("full export omitted attribute-ignored source: %v %+v", e, entries)
	}
	b, e := os.ReadFile(filepath.Join(extracted, "assets/font.bin"))
	if e != nil || !bytes.Equal(b, binary) {
		t.Fatal("raw export changed asset", e)
	}
}

func TestSnapshotPreparedRecoveryKeepsProducerAndRejectsMovedRef(t *testing.T) {
	s, a, p := fixture(t)
	repo := must(t, s, a, "repositories.create", M{"projectId": p.ID, "title": "source"})
	in := snapshotInput(repo)
	body := sourceTar(t, map[string][]byte{"main.txt": []byte("source")}, "")
	_, e := s.Store.DB.Exec(`CREATE TRIGGER fail_source_finalize BEFORE UPDATE ON resources WHEN NEW.kind='source_commit' AND NEW.status='committed' BEGIN SELECT RAISE(ABORT,'injected finalization failure'); END`)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CommitSnapshot(context.Background(), a, repo.ID, in, bytes.NewReader(body)); e == nil {
		t.Fatal("injected finalization did not fail")
	}
	ops, e := s.Store.List(p.ID, "source_commit", "")
	if e != nil || len(ops) != 1 || ops[0].Status != "prepared" {
		t.Fatalf("prepared operation lost: %+v %v", ops, e)
	}
	commit := S(ops[0].Data, "commit")
	head, _ := s.Git(repo, "rev-parse", "refs/heads/feature")
	if head != commit {
		t.Fatal("fault did not exercise moved Git ref")
	}
	changes, _ := s.Store.List(p.ID, "change", "")
	if len(changes) != 1 || S(changes[0].Data, "authorId") != a.ID || S(changes[0].Data, "commit") != commit {
		t.Fatal("producer provenance was lost")
	}
	reviewer := Actor{ID: "other", Kind: "human", Admin: true}
	review := must(t, s, reviewer, "reviews.create", M{"repositoryId": repo.ID, "branch": "feature", "title": "Independent review"})
	if _, e = s.Command(context.Background(), a, "reviews.approve", M{"id": review.ID}); e == nil {
		t.Fatal("interrupted producer approved own source")
	}
	if _, e = s.Store.DB.Exec(`DROP TRIGGER fail_source_finalize`); e != nil {
		t.Fatal(e)
	}
	// A fresh service instance reconciles persisted provenance rather than rebuilding.
	recovered, e := NewService(s.Store).CommitSnapshot(context.Background(), a, repo.ID, in, bytes.NewReader(body))
	if e != nil || recovered.Status != "committed" || S(recovered.Data, "commit") != commit {
		t.Fatalf("recovery failed: %+v %v", recovered, e)
	}
	ops, _ = s.Store.List(p.ID, "source_commit", "")
	if len(ops) != 1 {
		t.Fatal("recovery duplicated operation")
	}
}

func TestSnapshotRecoveryNeverOverwritesUnexpectedBranch(t *testing.T) {
	s, a, p := fixture(t)
	repo := must(t, s, a, "repositories.create", M{"projectId": p.ID, "title": "source"})
	in := snapshotInput(repo)
	body := sourceTar(t, map[string][]byte{"file": []byte("candidate")}, "")
	_, e := s.Store.DB.Exec(`CREATE TRIGGER fail_source_finalize BEFORE UPDATE ON resources WHEN NEW.kind='source_commit' AND NEW.status='committed' BEGIN SELECT RAISE(ABORT,'injected'); END`)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CommitSnapshot(context.Background(), a, repo.ID, in, bytes.NewReader(body)); e == nil {
		t.Fatal("fault did not trigger")
	}
	if _, e = s.Store.DB.Exec(`DROP TRIGGER fail_source_finalize`); e != nil {
		t.Fatal(e)
	}
	ops, _ := s.Store.List(p.ID, "source_commit", "")
	candidate := S(ops[0].Data, "commit")
	if _, e = s.Git(repo, "update-ref", "refs/heads/feature", S(repo.Data, "head"), candidate); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CommitSnapshot(context.Background(), a, repo.ID, in, bytes.NewReader(body)); e == nil {
		t.Fatal("recovery rewound unexpected branch")
	}
	head, _ := s.Git(repo, "rev-parse", "refs/heads/feature")
	if head != S(repo.Data, "head") {
		t.Fatal("unexpected branch was overwritten")
	}
}

type unreadSource struct{ reads int }

func (b *unreadSource) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }
func TestSnapshotAuthorizesBeforeReadingAndRejectsInvalidTrees(t *testing.T) {
	s, a, p := fixture(t)
	repo := must(t, s, a, "repositories.create", M{"projectId": p.ID, "title": "source"})
	in := snapshotInput(repo)
	reader := &unreadSource{}
	if _, e := s.CommitSnapshot(context.Background(), Actor{ID: "outsider", Kind: "human"}, repo.ID, in, reader); e == nil || reader.reads != 0 {
		t.Fatal("unauthorized body was read")
	}
	for _, branch := range []string{"main", "-bad"} {
		bad := copyM(in)
		bad["branch"] = branch
		if _, e := s.CommitSnapshot(context.Background(), a, repo.ID, bad, bytes.NewReader(sourceTar(t, map[string][]byte{"x": []byte("x")}, ""))); e == nil {
			t.Fatal("protected or invalid branch accepted")
		}
	}
	for _, name := range []string{"../escape", ".GIT/config"} {
		var out bytes.Buffer
		w := tar.NewWriter(&out)
		_ = w.WriteHeader(&tar.Header{Name: name, Size: 1, Mode: 0644})
		_, _ = w.Write([]byte("x"))
		_ = w.Close()
		if _, e := s.CommitSnapshot(context.Background(), a, repo.ID, in, &out); e == nil {
			t.Fatal("unsafe tree accepted", name)
		}
	}
	ops, _ := s.Store.List(p.ID, "source_commit", "")
	if len(ops) != 0 {
		t.Fatal("rejected source prepared a commit")
	}
	audits, _ := s.Store.Audits(p.ID)
	found := false
	for _, v := range audits {
		if v.Action == "denied:repositories.snapshot" {
			found = true
		}
	}
	if !found {
		t.Fatal("rejected source action not audited")
	}
}

func TestSnapshotConcurrentSubmissionsCompareAndSwap(t *testing.T) {
	s, a, p := fixture(t)
	repo := must(t, s, a, "repositories.create", M{"projectId": p.ID, "title": "source"})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, key := range []string{"one", "two"} {
		body := sourceTar(t, map[string][]byte{"value": []byte(key)}, "")
		in := snapshotInput(repo)
		in["key"] = key
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.CommitSnapshot(context.Background(), a, repo.ID, in, bytes.NewReader(body))
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("concurrent source accepted %d writers", success)
	}
}

func TestSnapshotWorkerRequiresCurrentCodeLeaseAndBinding(t *testing.T) {
	for _, kind := range []string{"code", "analysis"} {
		t.Run(kind, func(t *testing.T) {
			s, a, p := fixture(t)
			repo := must(t, s, a, "repositories.create", M{"projectId": p.ID, "title": "source"})
			g := goal(t, s, a, p)
			must(t, s, a, "goals.approve", M{"id": g.ID})
			pl := must(t, s, a, "plans.propose", M{"goalId": g.ID, "summary": "Source work", "tasks": []any{M{"id": "work", "title": "Source work", "kind": kind, "role": "developer", "runtime": "linux", "repositoryId": repo.ID, "branch": "approved", "commit": S(repo.Data, "head")}}})
			must(t, s, a, "plans.approve", M{"id": pl.ID})
			w := must(t, s, a, "workers.register", M{"projectId": p.ID, "title": "Source runner", "roles": []any{"developer"}, "capabilities": []any{kind}, "runtime": "linux"})
			wa := Actor{ID: "worker:" + w.ID, Kind: "worker", ProjectID: p.ID, WorkerID: w.ID, Roles: []string{"developer"}}
			run := must(t, s, wa, "tasks.claim", M{})
			in := snapshotInput(repo)
			in["key"], in["branch"], in["runId"], in["leaseToken"] = run.ID, "approved", run.ID, S(run.Data, "leaseToken")
			body := sourceTar(t, map[string][]byte{"source": []byte("source")}, "")
			if kind == "analysis" {
				if _, e := s.CommitSnapshot(context.Background(), wa, repo.ID, in, bytes.NewReader(body)); e == nil {
					t.Fatal("analysis task committed source")
				}
				legacy := copyM(in)
				legacy["id"], legacy["files"] = repo.ID, M{"other": "unauthorized"}
				if _, e := s.Command(context.Background(), wa, "repositories.commit", legacy); e == nil {
					t.Fatal("legacy text endpoint bypassed code-task boundary")
				}
				return
			}
			for _, mutation := range []M{{"branch": "unapproved"}, {"baseCommit": strings.Repeat("a", 40)}, {"leaseToken": "stale"}, {"key": "another"}} {
				bad := copyM(in)
				for k, v := range mutation {
					bad[k] = v
				}
				if _, e := s.CommitSnapshot(context.Background(), wa, repo.ID, bad, bytes.NewReader(body)); e == nil {
					t.Fatal("worker bypassed task binding", mutation)
				}
			}
			// Revoke the requirement after body processing begins: the final authority check must stop the commit.
			changed := false
			sourceReader := &afterRead{Reader: bytes.NewReader(body), after: func() {
				if !changed {
					changed = true
					must(t, s, a, "goals.revise", M{"id": g.ID, "reason": "scope changed", "outcome": "new scope"})
				}
			}}
			if _, e := s.CommitSnapshot(context.Background(), wa, repo.ID, in, sourceReader); e == nil {
				t.Fatal("lease invalidated during upload committed source")
			}
			if head, _ := s.Git(repo, "rev-parse", "--verify", "refs/heads/approved"); head != "" {
				t.Fatal("revoked upload moved branch")
			}
		})
	}
}

type afterRead struct {
	io.Reader
	after func()
}

func TestWorkerCannotCompletePreparedSource(t *testing.T) {
	s, a, project := fixture(t)
	repo := must(t, s, a, "repositories.create", M{"projectId": project.ID, "title": "source"})
	g := goal(t, s, a, project)
	must(t, s, a, "goals.approve", M{"id": g.ID})
	pl := must(t, s, a, "plans.propose", M{"goalId": g.ID, "summary": "Code completion", "tasks": []any{M{"id": "code", "title": "Code", "kind": "code", "role": "developer", "runtime": "linux", "repositoryId": repo.ID}}})
	must(t, s, a, "plans.approve", M{"id": pl.ID})
	w := must(t, s, a, "workers.register", M{"projectId": project.ID, "title": "Source worker", "roles": []any{"developer"}, "capabilities": []any{"code"}, "runtime": "linux"})
	wa := Actor{ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: project.ID, Roles: []string{"developer"}}
	run := must(t, s, wa, "tasks.claim", M{})
	in := snapshotInput(repo)
	in["key"], in["runId"], in["leaseToken"], in["branch"] = run.ID, run.ID, S(run.Data, "leaseToken"), "agent/"+g.ID
	body := sourceTar(t, map[string][]byte{"file": []byte("source")}, "")
	_, e := s.Store.DB.Exec(`CREATE TRIGGER fail_source_finalize BEFORE UPDATE ON resources WHEN NEW.kind='source_commit' AND NEW.status='committed' BEGIN SELECT RAISE(ABORT,'injected'); END`)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CommitSnapshot(context.Background(), wa, repo.ID, in, bytes.NewReader(body)); e == nil {
		t.Fatal("fault did not trigger")
	}
	ops, e := s.Store.List(project.ID, "source_commit", g.ID)
	if e != nil || len(ops) != 1 {
		t.Fatal("missing prepared source", e)
	}
	completion := M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"repositoryId": repo.ID, "commit": S(ops[0].Data, "commit"), "sourceOperationId": ops[0].ID}}
	if _, e = s.Command(context.Background(), wa, "tasks.complete", completion); e == nil {
		t.Fatal("prepared source was accepted as completed development")
	}
	if _, e = s.Store.DB.Exec(`DROP TRIGGER fail_source_finalize`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CommitSnapshot(context.Background(), wa, repo.ID, in, bytes.NewReader(body)); e != nil {
		t.Fatal(e)
	}
	finished := must(t, s, wa, "tasks.complete", completion)
	if finished.Status != "succeeded" {
		t.Fatal("confirmed source could not complete development")
	}
}

func (r *afterRead) Read(b []byte) (int, error) { n, e := r.Reader.Read(b); r.after(); return n, e }
