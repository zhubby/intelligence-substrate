package worker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestWorkerAndroidSigningRepairUsesActualSourceAndFreshFinalBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixed-program fixture; actual Windows acceptance is separate")
	}
	ctx := context.Background()
	store, err := p.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := p.NewService(store)
	h := httpapi.New(svc, false)
	if err = h.Auth.Bootstrap("repair@example.test", "isolated-signing-repair-password"); err != nil {
		t.Fatal(err)
	}
	identity, _ := h.Auth.IdentityByEmail("repair@example.test")
	owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
	command := func(name string, data p.M) p.Resource {
		t.Helper()
		v, err := svc.Command(ctx, owner, name, data)
		if err != nil {
			t.Fatal(name, err)
		}
		return v
	}
	project := command("projects.create", p.M{"title": "Actual Worker signing repair"})
	goal := command("goals.create", p.M{"projectId": project.ID, "title": "Verified Android repair", "outcome": "Repair exact source and independently verify newly signed bytes", "targets": []any{"android-huawei"}, "criteria": []any{p.M{"id": "content", "text": "Exact package has expected content", "method": "simulator"}}, "budget": 100})
	command("goals.approve", p.M{"id": goal.ID})
	repo := command("repositories.create", p.M{"projectId": project.ID, "title": "source"})
	repo = command("repositories.commit", p.M{"id": repo.ID, "branch": "seed", "message": "Broken source fixture", "files": p.M{"source.txt": "broken\n"}})
	plan := command("plans.propose", p.M{"goalId": goal.ID, "summary": "Approved deterministic implementation, unsigned packaging and dedicated signing", "tasks": []any{
		p.M{"id": "code", "title": "Implement and repair source", "kind": "code", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "commit": p.S(repo.Data, "lastCommit"), "branch": "implementation", "budgetCents": 100, "command": []any{"/bin/sh", "-c", "if test -f implemented; then printf 'expected\\n' > source.txt; else touch implemented; fi"}},
		p.M{"id": "unsigned", "title": "Unsigned packaging", "kind": "build", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "dependsOn": []any{"code"}, "target": "android-huawei", "artifactStage": "unsigned", "budgetCents": 100, "command": []any{"/bin/sh", "-c", "test -z \"$IS_WORKER_TOKEN\" && cp source.txt unsigned.apk"}, "outputPath": "unsigned.apk"},
		p.M{"id": "sign", "title": "Dedicated test transformation", "kind": "build", "role": "release", "runtime": runtime.GOOS, "dependsOn": []any{"unsigned"}, "target": "android-huawei", "signingMode": "android-apk", "inputArtifactFrom": "unsigned", "expectedSigner": strings.Repeat("a", 64), "budgetCents": 100},
	}})
	command("plans.approve", p.M{"id": plan.ID})
	pipeline := command("pipelines.create", p.M{"projectId": project.ID, "title": "Independent exact final content", "runtime": runtime.GOOS, "method": "simulator", "verificationEnvironment": "simulator", "command": []any{"/bin/sh", "-c", "test -z \"$IS_WORKER_TOKEN\" && grep -qx expected \"$IS_ARTIFACT_PATH\""}})
	policy := command("quality.bind", p.M{"goalId": goal.ID, "expectedRequirementRevision": goal.Revision, "expectedPlanId": plan.ID, "target": "android-huawei", "pipelineId": pipeline.ID, "criteria": []any{"content"}, "budgetCents": 100, "key": "approved-final-qa"})
	h.ModelCall = func(context.Context, p.Actor, p.M) (p.M, error) {
		t.Error("deterministic repair fixture unexpectedly called a model")
		return nil, fmt.Errorf("models disabled")
	}
	var signing atomic.Bool
	var sourceRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if signing.Load() && strings.Contains(r.URL.Path, "/repositories/") {
			sourceRequests.Add(1)
		}
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	runner := func(role string, caps []any, trusted bool) *Runner {
		w := command("workers.register", p.M{"projectId": project.ID, "title": role, "runtime": runtime.GOOS, "roles": []any{role}, "capabilities": caps, "trustedQuality": trusted})
		token, _, err := h.Auth.IssueToken(ctx, owner, p.M{"projectId": project.ID, "workerId": w.ID})
		if err != nil {
			t.Fatal(err)
		}
		client, err := NewClient(server.URL, token, true)
		if err != nil {
			t.Fatal(err)
		}
		return &Runner{Client: client, Config: Config{Driver: "native", AllowNative: true, StateDir: t.TempDir()}}
	}
	dev, sign, qa := runner("developer", []any{"code", "build"}, false), runner("release", []any{"build"}, false), runner("quality", []any{"test"}, true)
	// This fixed fixture is deliberately not an Android SDK signature. Actual
	// signed APK acceptance uses verify-trusted-android.go --exercise-repair.
	programRoot := t.TempDir()
	program := filepath.Join(programRoot, "sign-fixture.py")
	if err = os.WriteFile(program, []byte(fmt.Sprintf(`import hashlib,json,os,pathlib,sys
r=json.load(sys.stdin)
assert not any(k.startswith('IS_') for k in os.environ)
data=pathlib.Path(r['artifactPath']).read_bytes()
assert hashlib.sha256(data).hexdigest()==r['artifactDigest']
out=pathlib.Path(%q)/(r['operationKey']+'.apk')
out.write_bytes(data+b'signed fixture\n')
print(json.dumps({'artifactPath':str(out),'artifactDigest':hashlib.sha256(out.read_bytes()).hexdigest(),'artifactSize':out.stat().st_size,'certificateSHA256':r['expectedSigner'],'operationKey':r['operationKey'],'requestBinding':'c'*64,'payloadDigest':'b'*64,'packageName':'com.example.repair','versionCode':1,'versionName':'1.0.0','minSDK':24,'inputArtifactId':r['artifactId'],'inputArtifactDigest':r['artifactDigest']}))
`, programRoot)), 0600); err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	sign.Config.Signers = map[string][]string{"android-huawei": {python, program}}
	t.Setenv("IS_WORKER_TOKEN", "must-not-reach-any-job")
	work := func(r *Runner) {
		t.Helper()
		signing.Store(r == sign)
		worked, err := r.RunOnce(ctx)
		signing.Store(false)
		if !worked || err != nil {
			t.Fatal("actual Worker step failed", worked, err)
		}
	}
	work(dev)
	work(dev)
	if worked, err := qa.RunOnce(ctx); worked || err != nil {
		t.Fatal("unsigned original got QA", worked, err)
	}
	work(sign)
	work(qa)
	defects, _ := store.List(project.ID, "defect", goal.ID)
	if len(defects) != 1 || defects[0].Status != "repairing" {
		t.Fatal("real failing quality command did not start repair", defects)
	}
	work(dev)
	work(dev)
	if worked, err := qa.RunOnce(ctx); worked || err != nil {
		t.Fatal("unsigned repair got QA", worked, err)
	}
	work(sign)
	work(qa)
	if worked, err := qa.RunOnce(ctx); worked || err != nil {
		t.Fatal("repair QA duplicated", worked, err)
	}
	defects, _ = store.List(project.ID, "defect", goal.ID)
	dispatches, _ := store.List(project.ID, "quality_dispatch", goal.ID)
	evidence, _ := store.List(project.ID, "evidence", goal.ID)
	artifacts, _ := store.List(project.ID, "artifact", goal.ID)
	if defects[0].Status != "closed" || len(p.Items(defects[0].Data["androidSigningRepairs"])) != 1 || len(dispatches) != 2 || len(evidence) != 1 || len(artifacts) != 4 || p.S(evidence[0].Data, "qualityPolicyId") != policy.ID {
		t.Fatal("incomplete exact repair workflow", defects, len(dispatches), len(evidence), len(artifacts))
	}
	final, _ := store.Get(p.S(evidence[0].Data, "artifactId"))
	data, err := os.ReadFile(svc.ArtifactPath(p.S(final.Data, "digest")))
	if err != nil || string(data) != "expected\nsigned fixture\n" {
		t.Fatal("independent evidence does not verify actual repaired final bytes", string(data), err)
	}
	if sourceRequests.Load() != 0 {
		t.Fatal("signing program fetched repository source", sourceRequests.Load())
	}
	runs, _ := store.List(project.ID, "run", goal.ID)
	keys := map[string]bool{}
	for _, run := range runs {
		if p.S(p.Obj(run.Data["task"]), "signingMode") != "" {
			key := p.S(p.Obj(p.Obj(run.Data["task"])["signingInput"]), "operationKey")
			if key == "" || keys[key] || run.Status != "succeeded" {
				t.Fatal("repair reused or lost a confirmed signing operation", run)
			}
			keys[key] = true
		}
	}
	if len(keys) != 2 {
		t.Fatal("expected original and fresh signing operation", keys)
	}
	if _, err := svc.Command(ctx, owner, "releases.create", p.M{"goalId": goal.ID, "version": "1.0.0", "commit": p.S(final.Data, "commit"), "artifactIds": []any{final.ID}}); err == nil {
		t.Fatal("fixture simulator evidence bypassed the physical release gate")
	}
}
