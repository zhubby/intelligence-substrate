package platform

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func writeScopeFiles() map[string][]byte {
	return map[string][]byte{"backend/main.go": []byte("package main\n"), "frontend/ui.txt": []byte("protected UI\n"), "backend-extra/lookalike.txt": []byte("protected sibling\n"), "bin/tool.sh": []byte("#!/bin/sh\n"), "assets/data.bin": {0, 255, 17}, "app/[id]/page.tsx": []byte("literal route\n")}
}

func newWriteScopeFixture(t *testing.T, paths any) *sourceFixture {
	t.Helper()
	s, a, project := fixture(t)
	f := &sourceFixture{s: s, a: a, p: project}
	f.repo = must(t, s, a, "repositories.create", M{"projectId": project.ID, "title": "write scope"})
	seed, err := s.CommitSnapshot(context.Background(), a, f.repo.ID, M{"key": "seed", "branch": "seed", "baseCommit": S(f.repo.Data, "head"), "message": "Protected input"}, bytes.NewReader(sourceTar(t, writeScopeFiles(), "")))
	if err != nil {
		t.Fatal(err)
	}
	f.g = goal(t, s, a, project)
	must(t, s, a, "goals.approve", M{"id": f.g.ID})
	code := M{"id": "edit", "title": "Backend change", "kind": "code", "role": "developer", "runtime": "linux", "repositoryId": f.repo.ID, "commit": S(seed.Data, "commit"), "branch": "scoped", "budgetCents": 20}
	if paths != nil {
		code["writePaths"] = paths
	}
	f.plan = must(t, s, a, "plans.propose", M{"goalId": f.g.ID, "summary": "Approved code boundary", "tasks": []any{code, M{"id": "build", "title": "Build exact source", "kind": "build", "role": "developer", "runtime": "linux", "dependsOn": []any{"edit"}, "repositoryId": f.repo.ID, "budgetCents": 10}}})
	must(t, s, a, "plans.approve", M{"id": f.plan.ID})
	w := must(t, s, a, "workers.register", M{"projectId": project.ID, "title": "Scoped worker", "roles": []any{"developer"}, "runtime": "linux", "capabilities": []any{"code", "build"}})
	f.worker = Actor{ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: project.ID, Roles: []string{"developer"}}
	return f
}

func writeScopeInput(run Resource) M {
	b := Obj(Obj(run.Data["task"])["sourceBinding"])
	return M{"key": run.ID, "runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "branch": S(b, "targetBranch"), "baseCommit": S(b, "commit"), "expectedHead": S(b, "expectedHead"), "message": "Scoped implementation"}
}

func writeScopeCommitObjects(t *testing.T, f *sourceFixture) int {
	t.Helper()
	out, err := f.s.Git(f.repo, "cat-file", "--batch-all-objects", "--batch-check=%(objecttype)")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, kind := range strings.Fields(out) {
		if kind == "commit" {
			count++
		}
	}
	return count
}

// Removing the controller tree check must let these actual Git changes escape.
func TestWriteScopeSnapshotRejectsBeforeCandidateOrCommit(t *testing.T) {
	for _, mode := range []string{"edit", "add", "delete", "binary", "chmod", "prefix", "rename-out", "rename-in", "literal-route"} {
		t.Run(mode, func(t *testing.T) {
			f := newWriteScopeFixture(t, []any{"backend/", "app/[id]/page.tsx"})
			run := f.claim(t)
			files, executable := writeScopeFiles(), ""
			switch mode {
			case "edit":
				files["frontend/ui.txt"] = []byte("unapproved UI")
			case "add":
				files["frontend/new.txt"] = []byte("unapproved new file")
			case "delete":
				delete(files, "frontend/ui.txt")
			case "binary":
				files["assets/data.bin"] = []byte{0, 1, 2, 255}
			case "chmod":
				executable = "bin/tool.sh"
			case "prefix":
				files["backend-extra/lookalike.txt"] = []byte("outside directory boundary")
			case "rename-out":
				files["frontend/moved.go"] = files["backend/main.go"]
				delete(files, "backend/main.go")
			case "rename-in":
				files["backend/moved.txt"] = files["frontend/ui.txt"]
				delete(files, "frontend/ui.txt")
			case "literal-route":
				files["app/i/page.tsx"] = []byte("brackets are not glob")
			}
			commits := writeScopeCommitObjects(t, f)
			_, err := f.s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, writeScopeInput(run), bytes.NewReader(sourceTar(t, files, executable)))
			if err == nil || !strings.Contains(err.Error(), "writePaths") {
				t.Fatalf("out-of-scope %s accepted or failed for unrelated reason: %v", mode, err)
			}
			if writeScopeCommitObjects(t, f) != commits {
				t.Fatal("rejected source created a commit object")
			}
			if refs, _ := f.s.Git(f.repo, "for-each-ref", "--format=%(refname)", "refs/heads/scoped", "refs/intelligence/source/"); strings.Contains(refs, "scoped") || strings.Count(refs, "refs/intelligence/source/") != 1 {
				t.Fatal("rejected worker source moved refs", refs)
			}
			for _, kind := range []string{"source_commit", "change"} {
				rows, e := f.s.Store.List(f.p.ID, kind, "")
				if e != nil || len(rows) != 1 { // the human input seed only
					t.Fatal("rejected source produced a candidate", kind, rows, e)
				}
			}
			audits, _ := f.s.Store.Audits(f.p.ID)
			found := false
			for _, audit := range audits {
				found = found || audit.Action == "denied:repositories.snapshot"
			}
			if !found {
				t.Fatal("scope rejection lost its audit record")
			}
		})
	}
}

func TestWriteScopeLegacyTextEndpointCannotBypassScope(t *testing.T) {
	f := newWriteScopeFixture(t, []any{"backend/"})
	run := f.claim(t)
	in := writeScopeInput(run)
	in["id"], in["files"] = f.repo.ID, M{"frontend/ui.txt": "bypass"}
	commits := writeScopeCommitObjects(t, f)
	if _, err := f.s.Command(context.Background(), f.worker, "repositories.commit", in); err == nil || !strings.Contains(err.Error(), "writePaths") {
		t.Fatal("legacy text commit bypassed approved scope", err)
	}
	if writeScopeCommitObjects(t, f) != commits {
		t.Fatal("legacy rejection occurred after creating a commit")
	}
	in["files"] = M{"backend/new.go": "package backend\n"}
	repo := must(t, f.s, f.worker, "repositories.commit", in)
	if body := string(gitBytes(t, f.s, repo, "show", S(repo.Data, "lastCommit")+":frontend/ui.txt")); body != "protected UI\n" {
		t.Fatal("legacy permitted write changed protected source", body)
	}
}

func TestWriteScopePermittedChangesReachExactDownstreamSource(t *testing.T) {
	for _, paths := range []any{nil, []any{"backend/", "app/[id]/page.tsx"}} {
		t.Run(Hash(paths)[:8], func(t *testing.T) {
			f := newWriteScopeFixture(t, paths)
			run := f.claim(t)
			files := writeScopeFiles()
			files["backend/new.dat"] = []byte{0, 255, 2}
			files["app/[id]/page.tsx"] = []byte("updated literal route")
			delete(files, "backend/main.go")
			op, err := f.s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, writeScopeInput(run), bytes.NewReader(sourceTar(t, files, "backend/new.dat")))
			if err != nil {
				t.Fatal(err)
			}
			must(t, f.s, f.worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"repositoryId": f.repo.ID, "commit": S(op.Data, "commit"), "sourceOperationId": op.ID}})
			build := f.claim(t)
			if S(Obj(build.Data["task"]), "commit") != S(op.Data, "commit") {
				t.Fatal("downstream did not consume the exact scoped source", build)
			}
			if body := gitBytes(t, f.s, f.repo, "show", S(op.Data, "commit")+":backend/new.dat"); !bytes.Equal(body, []byte{0, 255, 2}) {
				t.Fatal("permitted binary bytes changed")
			}
		})
	}
}

func TestWriteScopeInvalidPlansRejected(t *testing.T) {
	for _, paths := range []any{nil, true, "backend/", []any{}, []any{nil}, []any{7}, []any{""}, []any{"/etc/"}, []any{"../backend/"}, []any{"a/../b"}, []any{"backend//"}, []any{".git/config"}, []any{".GIT/"}, []any{"x\\y"}, []any{"x*"}, []any{"NUL.txt"}, []any{"x\ny"}, []any{"a", "a"}, []any{"src/", "SRC/"}, []any{"caf\u00e9/", "cafe\u0301/"}, []any{"dir./"}} {
		t.Run(Hash(paths)[:8], func(t *testing.T) {
			_, err := validateTasks([]any{M{"id": "code", "title": "Code", "kind": "code", "role": "developer", "runtime": "linux", "writePaths": paths}})
			if err == nil {
				t.Fatal("invalid writePaths accepted", paths)
			}
		})
	}
	for _, kind := range []string{"analysis", "design", "build", "security", "test", "manual"} {
		if _, err := validateTasks([]any{M{"id": "work", "title": "Work", "kind": kind, "role": "developer", "runtime": "linux", "writePaths": []any{"backend/"}}}); err == nil {
			t.Fatal("non-code task accepted write permission", kind)
		}
	}
}

func TestWriteScopeMutationAfterClaimCannotGrantPermission(t *testing.T) {
	for _, mutation := range []string{"remove", "enlarge", "run-only"} {
		t.Run(mutation, func(t *testing.T) {
			f := newWriteScopeFixture(t, []any{"backend/"})
			run := f.claim(t)
			task := f.task(t, "edit")
			if mutation == "remove" {
				delete(task.Data, "writePaths")
			} else if mutation == "enlarge" {
				task.Data["writePaths"] = []any{"frontend/"}
			} else {
				Obj(run.Data["task"])["writePaths"] = []any{"frontend/"}
				f.put(t, run)
			}
			f.put(t, task)
			files := writeScopeFiles()
			files["frontend/ui.txt"] = []byte("must remain denied")
			if _, err := f.s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, writeScopeInput(run), bytes.NewReader(sourceTar(t, files, ""))); err == nil {
				t.Fatal("mutated task granted source authority", mutation)
			}
		})
	}
}

func TestWriteScopeSplitAndReorderCannotOverrideApproval(t *testing.T) {
	for _, command := range []string{"tasks.split", "plans.reorder"} {
		t.Run(command, func(t *testing.T) {
			f := newWriteScopeFixture(t, []any{"backend/"})
			task := f.task(t, "edit")
			in := M{"id": task.ID, "goalId": f.g.ID, "reason": "Rearrange approved work", "impact": "Same source boundary", "children": []any{M{"title": "Part one", "instructions": "One", "writePaths": []any{"frontend/"}}, M{"title": "Part two", "instructions": "Two"}}, "tasks": []any{M{"id": "edit", "dependsOn": []any{}, "writePaths": []any{"frontend/"}}}}
			if _, err := f.s.Command(context.Background(), f.a, command, in); err == nil {
				t.Fatal("adaptive request silently accepted a scope override", command)
			}
			if command == "tasks.split" {
				delete(Obj(Items(in["children"])[0]), "writePaths")
				must(t, f.s, f.a, command, in)
				parent := f.task(t, "edit")
				for _, id := range Strings(parent.Data["children"]) {
					child, e := f.s.Store.Get(id)
					if e != nil || Hash(child.Data["writePaths"]) != Hash([]any{"backend/"}) {
						t.Fatal("split lost original boundary", child, e)
					}
				}
			}
		})
	}
}

func TestWriteScopeRetryKeepsFrozenBoundaryAndHistoricalDigest(t *testing.T) {
	if sourceSelectionDigest(M{}) != "6691f58b198bb232649fdab8111114abe3da268d2615efd096e258c709f803b7" {
		t.Fatal("legacy source digest changed")
	}
	f := newWriteScopeFixture(t, []any{"backend/"})
	run := f.claim(t)
	original := Obj(Obj(run.Data["task"])["sourceBinding"])
	if Hash(original["writePaths"]) != Hash([]any{"backend/"}) {
		t.Fatal("source binding omitted approved boundary", original)
	}
	must(t, f.s, f.worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 1, "result": M{"blocked": true, "reason": "Need to retry within approved scope"}})
	must(t, f.s, f.a, "tasks.retry", M{"id": S(run.Data, "taskId")})
	next := f.claim(t)
	if Hash(Obj(next.Data["task"])["sourceBinding"]) != Hash(original) {
		t.Fatal("retry changed fixed boundary", next)
	}
	files := writeScopeFiles()
	files["frontend/ui.txt"] = []byte("still forbidden")
	if _, err := f.s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, writeScopeInput(next), bytes.NewReader(sourceTar(t, files, ""))); err == nil {
		t.Fatal("retry reset approved boundary")
	}
}

func TestWriteScopeIntegrationIncludesChangesBroughtByMerge(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing-merge-path", true: "approved-all-changes"}[allowed], func(t *testing.T) {
			paths := []any{"left.txt"}
			if allowed {
				paths = append(paths, "right.txt")
			}
			f := newSourceFixture(t, M{"id": "left", "branch": "left"}, M{"id": "right", "branch": "right"}, M{"id": "join", "branch": "combined", "sourceMode": "integrate", "dependsOn": []any{"left", "right"}, "writePaths": paths})
			integrationInputs(t, f, false)
			run, _ := prepareIntegration(t, f)
			files := map[string][]byte{"left.txt": []byte("changed in integration\n"), "right.txt": []byte("changed in integration\n")}
			op, err := f.s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, integrationSnapshotInput(run), bytes.NewReader(sourceTar(t, files, "")))
			if !allowed {
				if err == nil || !strings.Contains(err.Error(), "writePaths") {
					t.Fatal("merge input bypassed scope", err)
				}
				return
			}
			if err != nil || op.Status != "committed" {
				t.Fatal("approved integration could not commit", op, err)
			}
		})
	}
}

func TestWriteScopePreparedReplayAndOwnerRecoveryRetainBoundary(t *testing.T) {
	for _, mode := range []string{"replay", "confirm", "mutated"} {
		t.Run(mode, func(t *testing.T) {
			f := newWriteScopeFixture(t, []any{"backend/"})
			run := f.claim(t)
			in, files := writeScopeInput(run), writeScopeFiles()
			files["backend/main.go"] = []byte("package backend\n")
			body := sourceTar(t, files, "")
			if _, err := f.s.Store.DB.Exec(`CREATE TRIGGER fail_scope_finalize BEFORE UPDATE ON resources WHEN NEW.kind='source_commit' AND NEW.status='committed' BEGIN SELECT RAISE(ABORT,'injected finalize fault'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, in, bytes.NewReader(body)); err == nil {
				t.Fatal("finalize fault did not fire")
			}
			if _, err := f.s.Store.DB.Exec(`DROP TRIGGER fail_scope_finalize`); err != nil {
				t.Fatal(err)
			}
			ops, _ := f.s.Store.List(f.p.ID, "source_commit", f.g.ID)
			if len(ops) != 1 || ops[0].Status != "prepared" {
				t.Fatal("missing interrupted source", ops)
			}
			if mode == "replay" {
				op, err := NewService(f.s.Store).CommitSnapshot(context.Background(), f.worker, f.repo.ID, in, bytes.NewReader(body))
				if err != nil || op.ID != ops[0].ID || op.Status != "committed" {
					t.Fatal("scoped replay lost original candidate", op, err)
				}
				return
			}
			expireSourceRun(t, f, run)
			if mode == "mutated" {
				task := f.task(t, "edit")
				delete(task.Data, "writePaths")
				f.put(t, task)
				if _, err := f.s.Command(context.Background(), f.a, "sources.reconcile", recoveryInput(ops[0])); err == nil {
					t.Fatal("owner recovery silently adopted a changed approval boundary")
				}
				return
			}
			decision := must(t, f.s, f.a, "sources.reconcile", recoveryInput(ops[0]))
			if decision.Status != "confirmed" || Hash(f.task(t, "edit").Data["writePaths"]) != Hash([]any{"backend/"}) {
				t.Fatal("owner recovery lost scope", decision)
			}
		})
	}
}

func TestWriteScopeWaitAndProgressCannotDropScopeOnResume(t *testing.T) {
	for _, mode := range []string{"wait", "progress"} {
		for _, mutated := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/valid", true: "/removed-scope"}[mutated], func(t *testing.T) {
				f := newWriteScopeFixture(t, []any{"backend/"})
				run := f.claim(t)
				mf := &messageFixture{s: f.s, a: f.a, p: f.p, g: f.g, plan: f.plan, worker: f.worker, run: run}
				binding := Obj(Obj(run.Data["task"])["sourceBinding"])
				meta := M{"repositoryId": f.repo.ID, "commit": binding["commit"], "leaseToken": run.Data["leaseToken"]}
				files := writeScopeFiles()
				files["backend/main.go"] = []byte("saved scoped draft\n")
				body := sourceTar(t, files, "")
				if mode == "wait" {
					checkpoint, err := f.s.UploadWorkspaceCheckpoint(context.Background(), f.worker, run.ID, meta, bytes.NewReader(body))
					if err != nil {
						t.Fatal(err)
					}
					request, input := waitRequest(t, mf)
					input["source"], input["checkpointId"] = M{"repositoryId": f.repo.ID, "commit": binding["commit"]}, checkpoint.ID
					must(t, f.s, f.worker, "tasks.wait", input)
					waitReply(t, mf, request, "scoped-reply")
				} else {
					step := prepareAgentStep(t, mf, 1, M{"action": "write", "path": "backend/main.go", "content": "saved scoped draft\n"}, M{"repositoryId": f.repo.ID, "commit": binding["commit"]})
					checkpoint, err := f.s.UploadAgentCheckpoint(context.Background(), f.worker, step.ID, S(run.Data, "leaseToken"), bytes.NewReader(body))
					if err != nil {
						t.Fatal(err)
					}
					must(t, f.s, f.worker, "agent_steps.complete", M{"id": step.ID, "runId": run.ID, "leaseToken": run.Data["leaseToken"], "observation": "Protocol fixture saved approved draft", "checkpointId": checkpoint.ID})
					seedAgentUsage(t, mf, "running", 1, 1, "settled", 5)
				}
				if mutated {
					task := f.task(t, "edit")
					delete(task.Data, "writePaths")
					f.put(t, task)
				}
				next := f.claim(t)
				if mutated {
					if next.Kind == "run" {
						t.Fatal("resume accepted changed permission recipe", mode)
					}
					return
				}
				if next.Kind != "run" || Hash(Obj(next.Data["task"])["sourceBinding"]) != Hash(binding) || Hash(Obj(next.Data["task"])["writePaths"]) != Hash([]any{"backend/"}) {
					t.Fatal("resume lost original boundary", mode, next)
				}
				files["frontend/ui.txt"] = []byte("still outside after resume")
				if _, err := f.s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, writeScopeInput(next), bytes.NewReader(sourceTar(t, files, ""))); err == nil {
					t.Fatal("resume bypassed final source scope", mode)
				}
			})
		}
	}
}
