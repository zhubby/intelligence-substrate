package worker

import (
	"context"
	"encoding/json"
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
	"time"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

// Actual HTTP/Git/processes and independent QA, with explicitly synthetic
// Apple success receipts. No SDK, identity, notarization service or model.
func TestWorkerNativeSigningRepairFreshSourceAndIndependentFinalBytes(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Apple Worker contract requires darwin; native guard stays enabled")
	}
	for _, target := range []string{"ios-appstore", "macos"} {
		t.Run(target, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			if evidence := os.Getenv("IS_SIGNING_REPAIR_EVIDENCE_DIR"); evidence != "" {
				root = filepath.Join(evidence, "worker-"+target)
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
			}
			store, err := p.Open(filepath.Join(root, "platform.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			svc := p.NewService(store)
			handler := httpapi.New(svc, false)
			if err = handler.Auth.Bootstrap("native-repair@example.test", "isolated-native-repair-password"); err != nil {
				t.Fatal(err)
			}
			identity, _ := handler.Auth.IdentityByEmail("native-repair@example.test")
			owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
			command := func(name string, data p.M) p.Resource {
				t.Helper()
				v, e := svc.Command(ctx, owner, name, data)
				if e != nil {
					t.Fatal(name, e)
				}
				return v
			}
			project := command("projects.create", p.M{"title": "Native repair Worker proof"})
			goal := command("goals.create", p.M{"projectId": project.ID, "title": "Exact native repair", "outcome": "Repair exact failed package source and reverify fresh signed bytes", "targets": []any{target}, "criteria": []any{p.M{"id": "content", "text": "Final bytes have expected content", "method": "automated"}}, "budget": 100})
			command("goals.approve", p.M{"id": goal.ID})
			repo := command("repositories.create", p.M{"projectId": project.ID, "title": "source"})
			repo = command("repositories.commit", p.M{"id": repo.ID, "branch": "seed", "message": "Broken fixed-program fixture", "files": p.M{"source.txt": "broken\n"}})
			signRecipe := p.M{"id": "sign", "title": "Dedicated fixed signer", "kind": "build", "role": "release", "runtime": "darwin", "dependsOn": []any{"unsigned"}, "target": target, "inputArtifactFrom": "unsigned", "expectedSigner": strings.Repeat("a", 64), "expectedTeamId": "ABCD123456", "budgetCents": 100}
			if target == "macos" {
				signRecipe["signingMode"], signRecipe["expectedApplicationSigner"] = "macos-pkg", strings.Repeat("d", 64)
			} else {
				signRecipe["signingMode"], signRecipe["expectedProvisioningProfile"] = "ios-ipa", strings.Repeat("e", 64)
			}
			plan := command("plans.propose", p.M{"goalId": goal.ID, "summary": "Fixed implementation/repair, unsigned packaging and separate signature", "tasks": []any{
				p.M{"id": "code", "title": "Implement and repair source", "kind": "code", "role": "developer", "runtime": "darwin", "repositoryId": repo.ID, "commit": p.S(repo.Data, "lastCommit"), "branch": "implementation", "budgetCents": 100, "command": []any{"/bin/sh", "-c", "if test -f implemented; then printf 'expected\\n' > source.txt; else touch implemented; fi"}},
				p.M{"id": "unsigned", "title": "Unsigned packaging", "kind": "build", "role": "developer", "runtime": "darwin", "repositoryId": repo.ID, "dependsOn": []any{"code"}, "target": target, "artifactStage": "unsigned", "budgetCents": 100, "command": []any{"/bin/sh", "-c", "test -z \"$IS_WORKER_TOKEN\" && cp source.txt unsigned.app.tar.gz"}, "outputPath": "unsigned.app.tar.gz"},
				signRecipe,
			}})
			command("plans.approve", p.M{"id": plan.ID})
			pipeline := command("pipelines.create", p.M{"projectId": project.ID, "title": "Independent final-package content check", "runtime": "darwin", "method": "automated", "verificationEnvironment": "ci", "command": []any{"/bin/sh", "-c", "test -z \"$IS_WORKER_TOKEN\" && grep -qx expected \"$IS_ARTIFACT_PATH\""}})
			policy := command("quality.bind", p.M{"goalId": goal.ID, "expectedRequirementRevision": goal.Revision, "expectedPlanId": plan.ID, "target": target, "pipelineId": pipeline.ID, "criteria": []any{"content"}, "budgetCents": 100, "key": "native-repair-qa"})
			handler.ModelCall = func(context.Context, p.Actor, p.M) (p.M, error) {
				t.Error("fixed-program repair unexpectedly called a model")
				return nil, fmt.Errorf("models disabled")
			}
			var signing atomic.Bool
			var sources atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if signing.Load() && strings.Contains(r.URL.Path, "/repositories/") {
					sources.Add(1)
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			runner := func(role string, caps []any, trusted bool) *Runner {
				w := command("workers.register", p.M{"projectId": project.ID, "title": role, "runtime": "darwin", "roles": []any{role}, "capabilities": caps, "trustedQuality": trusted})
				token, _, e := handler.Auth.IssueToken(ctx, owner, p.M{"projectId": project.ID, "workerId": w.ID})
				if e != nil {
					t.Fatal(e)
				}
				client, e := NewClient(server.URL, token, true)
				if e != nil {
					t.Fatal(e)
				}
				return &Runner{Client: client, Config: Config{Driver: "native", AllowNative: true, StateDir: t.TempDir()}}
			}
			dev, sign, qa := runner("developer", []any{"code", "build"}, false), runner("release", []any{"build"}, false), runner("quality", []any{"test"}, true)
			program := filepath.Join(root, "fixed-signer.py")
			text := fmt.Sprintf(`import hashlib,json,os,pathlib,sys,uuid
r=json.load(sys.stdin)
assert not any(k.startswith('IS_') for k in os.environ)
root=pathlib.Path(%q)
out=root/(r['operationKey']+'.synthetic')
with (root/'requests.jsonl').open('a') as log:log.write(json.dumps(r)+'\n')
receipt={'protocolVersion':r['protocolVersion'],'state':'complete','operationKey':r['operationKey'],'requestBinding':'c'*64,'payloadDigest':'b'*64,'certificateSHA256':r['expectedSigner'],'teamId':r['expectedTeamId'],'inputArtifactId':r['artifactId'],'inputArtifactDigest':r['artifactDigest']}
if r['action']=='sign':
 data=pathlib.Path(r['artifactPath']).read_bytes()
 assert hashlib.sha256(data).hexdigest()==r['artifactDigest']
 assert not out.exists(),'new sign must have a fresh operation key'
 out.write_bytes(data+b'synthetic signature\n')
else:
 assert r['action']=='status' and out.is_file()
if r['protocolVersion']=='macos-1':
 receipt.update(applicationCertificateSHA256=r['expectedApplicationSigner'],notarizationId=str(uuid.uuid5(uuid.NAMESPACE_URL,r['operationKey'])))
 if r['action']=='sign':
  receipt['state']='processing';print(json.dumps(receipt));sys.exit(0)
 assert r['notarizationId']==receipt['notarizationId'] and r['requestBinding']==receipt['requestBinding'] and r['payloadDigest']==receipt['payloadDigest']
 receipt['notarized']=True
else:
 assert r['protocolVersion']=='ios-1' and r['action']=='sign'
 receipt['profileSHA256']=r['expectedProvisioningProfile']
receipt.update(artifactPath=str(out),artifactDigest=hashlib.sha256(out.read_bytes()).hexdigest(),artifactSize=out.stat().st_size,bundleId='com.example.repair',version='1.0.0',build='1')
print(json.dumps(receipt))
`, root)
			if err = os.WriteFile(program, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			python, err := exec.LookPath("python3")
			if err != nil {
				t.Fatal(err)
			}
			sign.Config.Signers = map[string][]string{target: {python, program}}
			t.Setenv("IS_WORKER_TOKEN", "must-not-reach-jobs")
			work := func(r *Runner) {
				t.Helper()
				signing.Store(r == sign)
				worked, e := r.RunOnce(ctx)
				signing.Store(false)
				if !worked || e != nil {
					t.Fatal("actual Worker step failed", worked, e)
				}
			}
			for round := 0; round < 2; round++ {
				work(dev)
				work(dev)
				if worked, e := qa.RunOnce(ctx); worked || e != nil {
					t.Fatal("unsigned package scheduled final QA", worked, e)
				}
				work(sign)
				if target == "macos" {
					if worked, e := qa.RunOnce(ctx); worked || e != nil {
						t.Fatal("notarization wait scheduled final QA", worked, e)
					}
					if err = store.Update(ctx, func(tx *p.Tx) error {
						tasks, e := tx.List(project.ID, "task", goal.ID)
						if e != nil {
							return e
						}
						for _, task := range tasks {
							if task.Status == "awaiting_signature" {
								task.Data["signingNextCheckAt"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
								return tx.Put(task)
							}
						}
						return fmt.Errorf("missing persisted notarization wait")
					}); err != nil {
						t.Fatal(err)
					}
					work(sign)
				}
				work(qa)
				defects, _ := store.List(project.ID, "defect", goal.ID)
				want := "repairing"
				if round == 1 {
					want = "closed"
				}
				if len(defects) != 1 || defects[0].Status != want {
					t.Fatal("independent failing/passing check did not drive repair lifecycle", round, defects)
				}
			}
			evidence, _ := store.List(project.ID, "evidence", goal.ID)
			dispatches, _ := store.List(project.ID, "quality_dispatch", goal.ID)
			artifacts, _ := store.List(project.ID, "artifact", goal.ID)
			if len(evidence) != 1 || len(dispatches) != 2 || len(artifacts) != 4 || p.S(evidence[0].Data, "qualityPolicyId") != policy.ID || sources.Load() != 0 {
				t.Fatal("wrong quality/signing isolation", len(evidence), len(dispatches), len(artifacts), sources.Load())
			}
			final, _ := store.Get(p.S(evidence[0].Data, "artifactId"))
			body, err := os.ReadFile(svc.ArtifactPath(p.S(final.Data, "digest")))
			if err != nil || string(body) != "expected\nsynthetic signature\n" {
				t.Fatal("final evidence is not for actual repaired bytes", string(body), err)
			}
			if _, err = svc.Command(ctx, owner, "releases.create", p.M{"goalId": goal.ID, "version": "1.0.0", "commit": p.S(final.Data, "commit"), "artifactIds": []any{final.ID}}); err == nil {
				t.Fatal("CI fixture bypassed physical native release gate")
			}
			for _, runner := range []*Runner{dev, sign, qa} {
				if worked, e := runner.RunOnce(ctx); worked || e != nil {
					t.Fatal("completed repair work repeated", worked, e)
				}
			}
			if err = svc.Backup(ctx, filepath.Join(root, "repair.tar.gz")); err != nil {
				t.Fatal(err)
			}
			if err = svc.VerifyIntegrity(ctx); err != nil {
				t.Fatal(err)
			}
			resources, _ := store.List("", "", "")
			for name, value := range map[string]any{"resources.json": resources, "result.json": p.M{"passed": true, "target": target, "resourceCount": len(resources), "finalArtifactId": final.ID, "sourceRequestsBySigner": sources.Load(), "paidModelCalled": false, "realAppleSignature": false, "realNativeApp": false}} {
				body, e := json.MarshalIndent(value, "", "  ")
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(root, name), body, 0600); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}
