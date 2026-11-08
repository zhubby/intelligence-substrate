package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// This fixture exercises the fixed-program boundary, not an Android signature.
// Real Android SDK and emulator acceptance is recorded separately.
func TestWorkerAndroidSigningFixedProgramAndIndependentQuality(t *testing.T) {
	testWorkerAndroidSigningTarget(t, "android-huawei")
}

func TestWorkerAABSigningFixedProgramAndIndependentQuality(t *testing.T) {
	testWorkerAndroidSigningTarget(t, "android-google-play")
}

func testWorkerAndroidSigningTarget(t *testing.T, target string) {
	signingMode := "android-apk"
	if target == "android-google-play" {
		signingMode = "android-aab"
	}
	for _, mode := range []string{"success", "missing-config", "wrong-digest", "wrong-certificate", "extra-output", "duplicate-output", "nonzero", "symlink-output"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store, err := p.Open(filepath.Join(t.TempDir(), "control.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			svc := p.NewService(store)
			handler := httpapi.New(svc, false)
			if err = handler.Auth.Bootstrap("signer@example.test", "test-signing-password-123"); err != nil {
				t.Fatal(err)
			}
			identity, _ := handler.Auth.IdentityByEmail("signer@example.test")
			owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
			command := func(name string, in p.M) p.Resource {
				t.Helper()
				v, e := svc.Command(ctx, owner, name, in)
				if e != nil {
					t.Fatal(name, e)
				}
				return v
			}
			project := command("projects.create", p.M{"title": "Trusted APK signing"})
			goal := command("goals.create", p.M{"projectId": project.ID, "title": "Signed client", "outcome": "Sign exact release input", "targets": []any{target}, "criteria": []any{p.M{"id": "payload", "text": "Exact payload is verified", "method": "simulator"}}, "budget": 100})
			command("goals.approve", p.M{"id": goal.ID})
			repo := command("repositories.create", p.M{"projectId": project.ID, "title": "source"})
			repo = command("repositories.commit", p.M{"id": repo.ID, "branch": "feature", "message": "Input fixture", "files": p.M{"source.txt": "unsigned fixture\n"}})
			commit := p.S(repo.Data, "lastCommit")
			plan := command("plans.propose", p.M{"goalId": goal.ID, "summary": "Build then sign exact APK", "tasks": []any{
				p.M{"id": "unsigned", "title": "Unsigned build", "kind": "build", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "commit": commit, "target": target, "artifactStage": "unsigned", "command": []any{"/bin/sh", "-c", "cp source.txt unsigned.apk"}, "outputPath": "unsigned.apk", "budgetCents": 100},
				p.M{"id": "sign", "title": "Sign on dedicated node", "kind": "build", "role": "release", "runtime": runtime.GOOS, "target": target, "signingMode": signingMode, "inputArtifactFrom": "unsigned", "dependsOn": []any{"unsigned"}, "expectedSigner": strings.Repeat("a", 64), "budgetCents": 100},
			}})
			command("plans.approve", p.M{"id": plan.ID})
			pipeline := command("pipelines.create", p.M{"projectId": project.ID, "title": "Independent final bytes", "runtime": runtime.GOOS, "method": "simulator", "verificationEnvironment": "simulator", "command": []any{"/bin/sh", "-c", "test -z \"$IS_WORKER_TOKEN\" && grep -qx 'signed fixture' \"$IS_ARTIFACT_PATH\""}})
			goal, _ = store.Get(goal.ID)
			command("quality.bind", p.M{"goalId": goal.ID, "expectedRequirementRevision": goal.Revision, "expectedPlanId": plan.ID, "pipelineId": pipeline.ID, "target": target, "criteria": []any{"payload"}, "budgetCents": 100, "key": "final-only"})
			var signingPhase atomic.Bool
			var sourceRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if signingPhase.Load() && strings.Contains(r.URL.Path, "/repositories/") {
					sourceRequests.Add(1)
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			runnerFor := func(role, capability string, trusted bool) *Runner {
				w := command("workers.register", p.M{"projectId": project.ID, "title": role + " worker", "runtime": runtime.GOOS, "roles": []any{role}, "capabilities": []any{capability}, "trustedQuality": trusted})
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
			builder, signer, quality := runnerFor("developer", "build", false), runnerFor("release", "build", false), runnerFor("quality", "test", true)
			if worked, e := builder.RunOnce(ctx); !worked || e != nil {
				t.Fatal(worked, e)
			}
			if worked, e := quality.RunOnce(ctx); worked || e != nil {
				t.Fatal("unsigned input got independent QA", worked, e)
			}
			programRoot := t.TempDir()
			program := filepath.Join(programRoot, "fixed-signer.py")
			record := filepath.Join(programRoot, "request.json")
			output := filepath.Join(programRoot, "signed.apk")
			text := fmt.Sprintf(`import hashlib,json,os,pathlib,sys
request=json.load(sys.stdin)
assert not any(k.startswith('IS_') for k in os.environ)
assert request['protocolVersion']=='1' and request['action']=='sign'
assert pathlib.Path(request['artifactPath']).read_bytes()==b'unsigned fixture\n'
assert hashlib.sha256(pathlib.Path(request['artifactPath']).read_bytes()).hexdigest()==request['artifactDigest']
pathlib.Path(%q).write_text(json.dumps(request))
output=pathlib.Path(%q);output.write_bytes(b'signed fixture\n')
mode=%q
if mode=='nonzero':print('private SDK diagnostic must not enter public logs',file=sys.stderr);sys.exit(7)
if mode=='symlink-output':
 real=output.with_suffix('.real');output.rename(real);output.symlink_to(real)
result={'artifactPath':str(output),'artifactDigest':hashlib.sha256(output.read_bytes()).hexdigest(),'artifactSize':output.stat().st_size,'certificateSHA256':request['expectedSigner'],'operationKey':request['operationKey'],'requestBinding':'c'*64,'payloadDigest':'b'*64,'packageName':'com.example.release','versionCode':1,'versionName':'1.0.0','minSDK':24,'inputArtifactId':request['artifactId'],'inputArtifactDigest':request['artifactDigest']}
if mode=='wrong-digest':result['artifactDigest']='d'*64
if mode=='wrong-certificate':result['certificateSHA256']='d'*64
if mode=='extra-output':result['privateKey']='must-not-persist'
if mode=='duplicate-output':
 print('{"certificateSHA256":"'+'d'*64+'",'+json.dumps(result)[1:]);sys.exit(0)
print(json.dumps(result))
`, record, output, mode)
			if err = os.WriteFile(program, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			if mode != "missing-config" {
				python, e := exec.LookPath("python3")
				if e != nil {
					t.Fatal(e)
				}
				config, _ := json.Marshal(p.M{"signers": map[string][]string{target: {python, program}}})
				if err = json.Unmarshal(config, &signer.Config); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("IS_WORKER_TOKEN", "must-not-enter-fixed-program")
			signingPhase.Store(true)
			if worked, e := signer.RunOnce(ctx); !worked || e != nil {
				t.Fatal(worked, e)
			}
			signingPhase.Store(false)
			if sourceRequests.Load() != 0 {
				t.Fatal("signing node downloaded repository source", sourceRequests.Load())
			}
			runs, _ := store.List(project.ID, "run", goal.ID)
			var signedRun p.Resource
			for _, run := range runs {
				if p.S(p.Obj(run.Data["task"]), "signingMode") != "" {
					signedRun = run
				}
			}
			if mode != "success" {
				if signedRun.Status != "blocked" {
					t.Fatal("unconfirmed signing not blocked", signedRun)
				}
				if strings.Contains(p.S(signedRun.Data, "log"), "private SDK diagnostic") || strings.Contains(p.S(signedRun.Data, "log"), "must-not-persist") {
					t.Fatal("private diagnostic leaked")
				}
				if mode == "missing-config" {
					if _, e := os.Stat(record); !os.IsNotExist(e) {
						t.Fatal("missing signer ran a program")
					}
				}
				artifacts, _ := store.List(project.ID, "artifact", goal.ID)
				if len(artifacts) != 1 {
					t.Fatal("invalid signer output registered", artifacts)
				}
				return
			}
			if signedRun.Status != "succeeded" {
				t.Fatal("fixed signer failed", signedRun)
			}
			if worked, e := quality.RunOnce(ctx); !worked || e != nil {
				t.Fatal(worked, e)
			}
			evidence, _ := store.List(project.ID, "evidence", goal.ID)
			if len(evidence) != 1 || evidence[0].Status != "passed" || p.S(evidence[0].Data, "artifactId") != p.S(p.Obj(signedRun.Data["result"]), "artifactId") {
				t.Fatal("fresh independent evidence missing", evidence)
			}
			if _, err = svc.Command(ctx, owner, "releases.create", p.M{"goalId": goal.ID, "version": "1.0.0", "commit": commit, "artifactIds": []any{p.S(evidence[0].Data, "artifactId")}}); err == nil || !strings.Contains(err.Error(), "目标环境") {
				t.Fatal("physical gate bypassed", err)
			}
		})
	}
}

func TestWorkerAndroidSigningNeverFallsBackToRepositoryCommand(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; http.Error(w, "must not fetch source", 500) }))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token", true)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "repository-command-ran")
	run := p.Resource{ID: "run_test", ProjectID: "project_test", GoalID: "goal_test", Data: p.M{"task": p.M{"kind": "build", "role": "release", "runtime": runtime.GOOS, "target": "android-huawei", "signingMode": "android-apk", "command": []any{"/bin/sh", "-c", "touch '" + marker + "'"}}}}
	runner := Runner{Client: client, Config: Config{Driver: "native", AllowNative: true}}
	if _, exit, e := runner.execute(context.Background(), run, io.Discard); e == nil || exit == 0 {
		t.Fatal("missing fixed signer accepted")
	}
	if requests != 0 {
		t.Fatal("missing signer touched repository", requests)
	}
	if _, e := os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("repository command executed")
	}
}

func TestWorkerAndroidSigningConfigurationRejectsOtherExecution(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "must not fetch source", 500)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token", true)
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{Client: client, Config: Config{Driver: "native", AllowNative: true, Signers: map[string][]string{"android-huawei": {"/private/fixed-signer"}}}}
	run := p.Resource{ID: "run_test", ProjectID: "project_test", GoalID: "goal_test", Data: p.M{"task": p.M{"kind": "build", "role": "release", "runtime": runtime.GOOS, "command": []any{"/bin/sh", "-c", "exit 0"}}}}
	result, exit, err := runner.execute(context.Background(), run, io.Discard)
	if err == nil || exit == 0 || !p.B(result, "blocked") {
		t.Fatal("signing configuration allowed ordinary execution", result, exit, err)
	}
	if requests.Load() != 0 {
		t.Fatal("signing configuration downloaded a repository workspace", requests.Load())
	}
}
