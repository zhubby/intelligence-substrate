package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func integrationFixture(t *testing.T, conflict bool) (*sourceFixture, string, string) {
	t.Helper()
	f := newSourceFixture(t, M{"id": "left", "branch": "left"}, M{"id": "right", "branch": "right"}, M{"id": "join", "branch": "combined", "sourceMode": "integrate", "dependsOn": []any{"left", "right"}})
	return integrationInputs(t, f, conflict)
}

func integrationInputs(t *testing.T, f *sourceFixture, conflict bool) (*sourceFixture, string, string) {
	t.Helper()
	base := S(f.repo.Data, "head")
	if conflict {
		r := must(t, f.s, f.a, "repositories.commit", M{"id": f.repo.ID, "branch": "seed", "message": "Common source", "files": M{"shared.txt": "base\n"}})
		base = S(r.Data, "lastCommit")
	}
	commits := []string{}
	for _, branch := range []string{"left", "right"} {
		files := M{branch + ".txt": branch + "\n"}
		if conflict {
			files["shared.txt"] = branch + "\n"
		}
		r := must(t, f.s, f.a, "repositories.commit", M{"id": f.repo.ID, "branch": branch, "baseCommit": base, "message": "Implement " + branch, "files": files})
		commit := S(r.Data, "lastCommit")
		commits = append(commits, commit)
		f.result(t, branch, commit)
	}
	return f, commits[0], commits[1]
}
func prepareIntegration(t *testing.T, f *sourceFixture) (Resource, Resource) {
	t.Helper()
	run := f.claim(t)
	if run.Kind != "run" {
		t.Fatal("approved integration did not claim", run, f.task(t, "join"))
	}
	run = must(t, f.s, f.worker, "sources.prepare", M{"runId": run.ID, "leaseToken": S(run.Data, "leaseToken")})
	prep, e := f.s.Store.Get(S(run.Data, "integrationId"))
	if e != nil {
		t.Fatal(e)
	}
	return run, prep
}
func integrationSnapshotInput(run Resource) M {
	b := Obj(Obj(run.Data["task"])["sourceBinding"])
	return M{"key": run.ID, "runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "branch": S(b, "targetBranch"), "baseCommit": S(b, "commit"), "expectedHead": S(b, "expectedHead"), "message": "Integrate selected work", "integrationId": S(run.Data, "integrationId"), "resolutionDigest": S(run.Data, "integrationResolutionDigest")}
}
func TestIntegrationPreparesExactInputsWithoutMovingBranches(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			f, left, right := integrationFixture(t, conflict)
			run, prep := prepareIntegration(t, f)
			workspace := S(prep.Data, "workspaceCommit")
			for name, want := range map[string]string{"left.txt": "left\n", "right.txt": "right\n"} {
				if got := string(gitBytes(t, f.s, f.repo, "show", workspace+":"+name)); got != want {
					t.Fatal("lost merged source", name, got)
				}
			}
			paths := Strings(prep.Data["conflicts"])
			if conflict && (len(paths) != 1 || paths[0] != "shared.txt") || !conflict && len(paths) != 0 {
				t.Fatal("incorrect conflict paths", prep)
			}
			if head, _ := f.s.Git(f.repo, "rev-parse", "--verify", "refs/heads/combined"); head != "" {
				t.Fatal("preparation moved output branch", head)
			}
			again := must(t, f.s, f.worker, "sources.prepare", M{"runId": run.ID, "leaseToken": S(run.Data, "leaseToken")})
			if S(again.Data, "integrationId") != prep.ID || S(again.Data, "integrationWorkspaceCommit") != workspace {
				t.Fatal("preparation was not idempotent", again)
			}
			parents := Strings(prep.Data["parents"])
			if !Has(parents, left) || !Has(parents, right) || len(parents) != 2 {
				t.Fatal("selected parents lost", parents)
			}
		})
	}
}

func TestIntegrationSnapshotRequiresExactDecisionsAndOriginalParents(t *testing.T) {
	for _, mode := range []string{"missing-decision", "wrong-bytes", "wrong-mode", "markers", "markers-with-nul", "valid", "delete"} {
		t.Run(mode, func(t *testing.T) {
			f, left, right := integrationFixture(t, true)
			run, prep := prepareIntegration(t, f)
			body := []byte("left and right\n")
			decision := M{"path": "shared.txt", "digest": fmt.Sprintf("%x", sha256.Sum256(body)), "executable": false}
			if mode == "delete" {
				decision = M{"path": "shared.txt", "deleted": true}
			}
			if mode == "wrong-mode" {
				decision["executable"] = true
			}
			if strings.HasPrefix(mode, "markers") {
				body = gitBytes(t, f.s, f.repo, "show", S(prep.Data, "workspaceCommit")+":shared.txt")
				if mode == "markers-with-nul" {
					body = append(body, 0)
				}
				decision["digest"] = fmt.Sprintf("%x", sha256.Sum256(body))
			}
			if mode != "missing-decision" {
				run = must(t, f.s, f.worker, "sources.resolve", M{"runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "resolutions": []any{decision}})
			}
			if mode == "wrong-bytes" {
				body = []byte("edited after resolution\n")
			}
			files := map[string][]byte{"left.txt": []byte("left\n"), "right.txt": []byte("right\n"), "shared.txt": body}
			if mode == "delete" {
				delete(files, "shared.txt")
			}
			op, e := f.s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, integrationSnapshotInput(run), bytes.NewReader(sourceTar(t, files, "")))
			if mode != "valid" && mode != "delete" {
				if e == nil {
					t.Fatal("invalid conflict decision committed", mode)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			commit := S(op.Data, "commit")
			parents, _ := f.s.Git(f.repo, "show", "-s", "--format=%P", commit)
			if !strings.Contains(parents, left) || !strings.Contains(parents, right) || len(strings.Fields(parents)) != 2 {
				t.Fatal("final source lost original parents", parents)
			}
			if strings.Contains(parents, S(prep.Data, "workspaceCommit")) {
				t.Fatal("intermediate conflict tree entered final history")
			}
			must(t, f.s, f.worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"repositoryId": f.repo.ID, "commit": commit, "sourceOperationId": op.ID}})
		})
	}
}

func TestIntegrationCannotUseTextCommitOrForeignResolution(t *testing.T) {
	f, _, _ := integrationFixture(t, true)
	run, prep := prepareIntegration(t, f)
	in := integrationSnapshotInput(run)
	_, e := f.s.Command(context.Background(), f.worker, "repositories.commit", M{"id": f.repo.ID, "runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "branch": S(in, "branch"), "baseCommit": S(in, "baseCommit"), "expectedHead": S(in, "expectedHead"), "message": "Bypass merge", "files": M{"shared.txt": "chosen side"}})
	if e == nil {
		t.Fatal("text endpoint bypassed integration parents and conflict checks")
	}
	_, e = f.s.Command(context.Background(), f.worker, "sources.resolve", M{"runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "resolutions": []any{M{"path": "not-a-conflict.txt", "deleted": true}}})
	if e == nil {
		t.Fatal("resolution accepted an unrelated path", prep)
	}
}

func TestIntegrationBinaryDeleteAndUnchangedTrees(t *testing.T) {
	for _, mode := range []string{"binary", "delete-modify", "already-integrated", "directory-replacement"} {
		t.Run(mode, func(t *testing.T) {
			f := newSourceFixture(t, M{"id": "left", "branch": "left"}, M{"id": "right", "branch": "right"}, M{"id": "join", "sourceMode": "integrate", "dependsOn": []any{"left", "right"}})
			commit := func(branch, base string, files map[string][]byte) string {
				t.Helper()
				op, err := f.s.CommitSnapshot(context.Background(), f.a, f.repo.ID, M{"key": branch, "branch": branch, "baseCommit": base, "message": "Source " + branch}, bytes.NewReader(sourceTar(t, files, "")))
				if err != nil {
					t.Fatal(err)
				}
				return S(op.Data, "commit")
			}
			base := commit("seed", S(f.repo.Data, "head"), map[string][]byte{"asset.bin": {0, 1}, "keep.txt": []byte("base")})
			leftFiles := map[string][]byte{"asset.bin": {0, 2}, "keep.txt": []byte("base")}
			rightFiles := map[string][]byte{"asset.bin": {0, 3}, "keep.txt": []byte("base")}
			if mode == "delete-modify" || mode == "directory-replacement" {
				delete(leftFiles, "asset.bin")
			}
			left := commit("left", base, leftFiles)
			rightBase := base
			if mode == "already-integrated" {
				rightBase = left
			}
			right := commit("right", rightBase, rightFiles)
			f.result(t, "left", left)
			f.result(t, "right", right)
			run, prep := prepareIntegration(t, f)
			if mode == "already-integrated" {
				if len(Strings(prep.Data["conflicts"])) != 0 {
					t.Fatal("integrated history reported conflicts", prep)
				}
			} else {
				if strings.Join(Strings(prep.Data["conflicts"]), ",") != "asset.bin" {
					t.Fatal("binary/deletion conflict lost", prep)
				}
				decision := M{"path": "asset.bin", "digest": fmt.Sprintf("%x", sha256.Sum256(rightFiles["asset.bin"])), "executable": false}
				if mode == "delete-modify" || mode == "directory-replacement" {
					decision = M{"path": "asset.bin", "deleted": true}
					delete(rightFiles, "asset.bin")
				}
				if mode == "directory-replacement" {
					rightFiles["asset.bin/child.txt"] = []byte("replacement child")
				}
				run = must(t, f.s, f.worker, "sources.resolve", M{"runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "resolutions": []any{decision}})
			}
			op, err := f.s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, integrationSnapshotInput(run), bytes.NewReader(sourceTar(t, rightFiles, "")))
			if err != nil {
				t.Fatal("explicit unchanged binary/ancestral tree or deletion rejected", err)
			}
			must(t, f.s, f.worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"repositoryId": f.repo.ID, "commit": S(op.Data, "commit"), "sourceOperationId": op.ID}})
		})
	}
}

func TestIntegrationPreparationSurvivesRetryWithoutInheritingDecisions(t *testing.T) {
	f, _, _ := integrationFixture(t, true)
	run, prep := prepareIntegration(t, f)
	must(t, f.s, f.worker, "sources.resolve", M{"runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "resolutions": []any{M{"path": "shared.txt", "deleted": true}}})
	must(t, f.s, f.worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 1})
	must(t, f.s, f.a, "tasks.retry", M{"id": S(run.Data, "taskId")})
	again, reused := prepareIntegration(t, f)
	if reused.ID != prep.ID || S(again.Data, "integrationWorkspaceCommit") != S(prep.Data, "workspaceCommit") || len(Obj(again.Data["integrationResolutions"])) != 0 || S(again.Data, "integrationResolutionDigest") != Hash(M{}) {
		t.Fatal("retry changed inputs/preparation or inherited decisions", again)
	}
	if _, e := f.s.Command(context.Background(), f.worker, "sources.resolve", M{"runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "resolutions": []any{M{"path": "shared.txt", "deleted": true}}}); e == nil {
		t.Fatal("old lease resolved new execution")
	}
}

func TestIntegrationSnapshotReplayCASAndDecisionFreeze(t *testing.T) {
	for _, mode := range []string{"replay", "moved-target", "missing-operation", "foreign-integration"} {
		t.Run(mode, func(t *testing.T) {
			f, _, _ := integrationFixture(t, false)
			run, _ := prepareIntegration(t, f)
			in := integrationSnapshotInput(run)
			files := map[string][]byte{"left.txt": []byte("left\n"), "right.txt": []byte("right\n")}
			if mode == "moved-target" {
				f.commit(t, "combined", S(Obj(Obj(run.Data["task"])["sourceBinding"]), "commit"))
			}
			if mode == "foreign-integration" {
				in["integrationId"] = NewID("integration")
			}
			op, e := f.s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, in, bytes.NewReader(sourceTar(t, files, "")))
			if mode == "moved-target" || mode == "foreign-integration" {
				if e == nil {
					t.Fatal("stale target or foreign preparation accepted")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if mode == "missing-operation" {
				if _, e = f.s.Command(context.Background(), f.worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"repositoryId": f.repo.ID, "commit": S(op.Data, "commit")}}); e == nil {
					t.Fatal("integration completed without confirmed operation")
				}
				return
			}
			again, e := f.s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, in, bytes.NewReader(sourceTar(t, files, "")))
			if e != nil || again.ID != op.ID {
				t.Fatal("exact integration replay failed", e)
			}
			if _, e = f.s.Command(context.Background(), f.worker, "sources.prepare", M{"runId": run.ID, "leaseToken": S(run.Data, "leaseToken")}); e == nil {
				t.Fatal("preparation reset after source submission")
			}
		})
	}
}

func TestIntegrationScopeAndResolutionValidation(t *testing.T) {
	for _, mode := range []string{"human", "expired", "other-worker", "revision", "plan", "mutated-binding", "bad-digest", "bad-mode", "mixed-delete", "duplicate-path"} {
		t.Run(mode, func(t *testing.T) {
			f, _, _ := integrationFixture(t, true)
			run, _ := prepareIntegration(t, f)
			actor := f.worker
			decision := M{"path": "shared.txt", "digest": strings.Repeat("a", 64), "executable": false}
			items := []any{decision}
			switch mode {
			case "human":
				actor = f.a
			case "expired":
				run.Data["leaseUntil"] = "2000-01-01T00:00:00Z"
				f.put(t, run)
			case "other-worker":
				actor.WorkerID = NewID("worker")
				actor.ID = "worker:" + actor.WorkerID
			case "revision":
				g, _ := f.s.Store.Get(f.g.ID)
				g.Revision++
				f.put(t, g)
			case "plan":
				g, _ := f.s.Store.Get(f.g.ID)
				g.Data["activePlanId"] = NewID("plan")
				f.put(t, g)
			case "mutated-binding":
				task := f.task(t, "join")
				delete(task.Data, "sourceMode")
				f.put(t, task)
			case "bad-digest":
				decision["digest"] = "not-a-digest"
			case "bad-mode":
				decision["executable"] = "false"
			case "mixed-delete":
				decision["deleted"] = true
			case "duplicate-path":
				items = append(items, decision)
			}
			if _, e := f.s.Command(context.Background(), actor, "sources.resolve", M{"runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "resolutions": items}); e == nil {
				t.Fatal("invalid resolution authority/decision accepted", mode)
			}
		})
	}
}

func TestIntegrationIgnoresRepositoryMergeDrivers(t *testing.T) {
	f, _, _ := integrationFixture(t, true)
	// A local custom driver combined with a source-controlled attribute must
	// never run in the trusted preparation process.
	marker := filepath.Join(t.TempDir(), "driver-was-run")
	if _, e := f.s.Git(f.repo, "config", "merge.evil.driver", "touch "+marker); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(f.s.RepoPath(f.repo), "info", "attributes"), []byte("shared.txt merge=evil\n"), 0600); e != nil {
		t.Fatal(e)
	}
	_, prep := prepareIntegration(t, f)
	if _, e := os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("trusted preparation executed repository merge driver")
	}
	if len(Strings(prep.Data["conflicts"])) != 1 {
		t.Fatal("external attribute changed conflict semantics", prep)
	}
}

func TestIntegrationRejectsInvalidPlansAndDuplicateSplits(t *testing.T) {
	for _, extra := range []M{{"sourceMode": "auto"}, {"sourceMode": true}, {"kind": "build", "sourceMode": "integrate"}, {"integrationId": "forged"}, {"integrationParents": []any{}}, {"integrationResolutions": M{}}, {"integrationResolutionDigest": "forged"}, {"repairRecipeId": "forged"}} {
		t.Run(Hash(extra)[:8], func(t *testing.T) {
			m := M{"id": "work", "title": "Work", "kind": "code", "role": "developer", "runtime": "linux"}
			for k, v := range extra {
				m[k] = v
			}
			if _, e := validateTasks([]any{m}); e == nil {
				t.Fatal("forged integration plan accepted", extra)
			}
		})
	}
	f := newSourceFixture(t, M{"id": "join", "sourceMode": "integrate"})
	task := f.task(t, "join")
	if _, e := f.s.Command(context.Background(), f.a, "tasks.split", M{"id": task.ID, "reason": "Split integration", "impact": "Duplicate inputs", "children": []any{M{"title": "One", "instructions": "Join one"}, M{"title": "Two", "instructions": "Join two"}}}); e == nil {
		t.Fatal("pending integration split into duplicate integrations")
	}
	if result := f.claim(t); result.Kind == "run" || N(f.task(t, "join").Data, "attempts") != 0 {
		t.Fatal("integration without producer inputs executed", result)
	}
}

func TestIntegrationLimitsAndUnrelatedHistories(t *testing.T) {
	for _, mode := range []string{"parents", "conflicts", "unrelated"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "parents" {
				recipes, deps := []M{}, []any{}
				for i := 0; i < 9; i++ {
					name := fmt.Sprintf("part%d", i)
					recipes = append(recipes, M{"id": name, "branch": name})
					deps = append(deps, name)
				}
				recipes = append(recipes, M{"id": "join", "sourceMode": "integrate", "dependsOn": deps})
				f := newSourceFixture(t, recipes...)
				for _, dep := range Strings(deps) {
					f.result(t, dep, f.commit(t, dep, S(f.repo.Data, "head")))
				}
				if result := f.claim(t); result.Kind == "run" || N(f.task(t, "join").Data, "attempts") != 0 {
					t.Fatal("excessive parents consumed a lease", result)
				}
				return
			}
			f, _, _ := integrationFixture(t, false)
			if mode == "unrelated" {
				tree, _ := f.s.Git(f.repo, "rev-parse", "right^{tree}")
				root, e := f.s.gitIdentity(f.repo, "commit-tree", tree, "-m", "Unrelated source")
				if e != nil {
					t.Fatal(e)
				}
				runs, _ := f.s.Store.List(f.p.ID, "run", f.g.ID)
				for _, run := range runs {
					if S(Obj(run.Data["task"]), "logicalId") == "right" {
						Obj(run.Data["result"])["commit"] = root
						f.put(t, run)
						f.put(t, resource("change", "Foreign history", f.p.ID, f.g.ID, M{"repositoryId": f.repo.ID, "commit": root, "authorId": f.worker.ID, "runId": run.ID}))
					}
				}
				if result := f.claim(t); result.Kind == "run" {
					t.Fatal("unrelated histories were integrated", result)
				}
				return
			}
			// Replace persisted producer results with 101 genuinely conflicting
			// files; the private Git preparation must stop before output is used.
			commits := map[string]string{}
			baseFiles := map[string][]byte{}
			for i := 0; i < 101; i++ {
				baseFiles[fmt.Sprintf("file%d", i)] = []byte("base\n")
			}
			seed, e := f.s.CommitSnapshot(context.Background(), f.a, f.repo.ID, M{"key": "many-seed", "branch": "seed", "baseCommit": S(f.repo.Data, "head"), "message": "Many conflicts"}, bytes.NewReader(sourceTar(t, baseFiles, "")))
			if e != nil {
				t.Fatal(e)
			}
			for _, side := range []string{"left", "right"} {
				files := map[string][]byte{}
				for name := range baseFiles {
					files[name] = []byte(side + "\n")
				}
				op, e := f.s.CommitSnapshot(context.Background(), f.a, f.repo.ID, M{"key": "many-" + side, "branch": "many-" + side, "baseCommit": S(seed.Data, "commit"), "message": "Many " + side}, bytes.NewReader(sourceTar(t, files, "")))
				if e != nil {
					t.Fatal(e)
				}
				commits[side] = S(op.Data, "commit")
			}
			runs, _ := f.s.Store.List(f.p.ID, "run", f.g.ID)
			for _, run := range runs {
				sha := commits[S(Obj(run.Data["task"]), "logicalId")]
				Obj(run.Data["result"])["commit"] = sha
				f.put(t, run)
				f.put(t, resource("change", "Many source", f.p.ID, f.g.ID, M{"repositoryId": f.repo.ID, "commit": sha, "authorId": f.worker.ID, "runId": run.ID}))
			}
			run := f.claim(t)
			if run.Kind != "run" {
				t.Fatal("valid bounded inputs could not claim", run)
			}
			if _, e := f.s.Command(context.Background(), f.worker, "sources.prepare", M{"runId": run.ID, "leaseToken": S(run.Data, "leaseToken")}); e == nil {
				t.Fatal("101 conflicts accepted")
			}
			if head, _ := f.s.Git(f.repo, "rev-parse", "--verify", "refs/heads/combined"); head != "" {
				t.Fatal("failed preparation changed branch")
			}
		})
	}
}
