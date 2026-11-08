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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/process"
)

func TestMacOSAppleToolsReceiveWorkerCancellation(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native macOS worker cancellation requires darwin")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	module, err := filepath.Abs("../../scripts")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	marker, tool := filepath.Join(root, "children.json"), filepath.Join(root, "fixed-notarytool")
	body := fmt.Sprintf("#!%s\nimport json,os,pathlib,subprocess,time\nchild=subprocess.Popen(['/bin/sleep','60'])\npathlib.Path(%q).write_text(json.dumps([os.getpid(),child.pid,os.getpgrp()]))\ntime.sleep(60)\n", python, marker)
	if err = os.WriteFile(tool, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	program := fmt.Sprintf("import sys;sys.path.insert(0,%q);from macos_signer import AppleTools;AppleTools({'tools':{'notarytool':%q}}).run('notarytool',%q,'info')", module, tool, root)
	cmd := exec.CommandContext(ctx, python, "-c", program)
	process.Protect(cmd)
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var children []int
	t.Cleanup(func() {
		cancel()
		_ = cmd.Cancel()
		// The pre-fix reproducer creates its own known child group. Never signal
		// the shared test or shell group when cleaning up that escaped process.
		if len(children) == 3 && children[2] == children[0] {
			_ = exec.Command(python, "-c", "import os,signal,sys\ntry:os.killpg(int(sys.argv[1]),signal.SIGKILL)\nexcept ProcessLookupError:pass", strconv.Itoa(children[0])).Run()
		}
	})
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if data, e := os.ReadFile(marker); e == nil && json.Unmarshal(data, &children) == nil && len(children) == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(children) != 3 {
		t.Fatal("fixed tool did not start its real child process")
	}
	cancel()
	if err = cmd.Wait(); err == nil {
		t.Fatal("cancelled signer unexpectedly succeeded")
	}
	for _, pid := range children[:2] {
		alive := true
		for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
			state, e := exec.Command("/bin/ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
			alive = e == nil && strings.TrimSpace(string(state)) != "" && !strings.Contains(string(state), "Z")
			if !alive {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if alive {
			t.Errorf("Apple tool or its child %d escaped Worker cancellation", pid)
		}
	}
}

// The fixed program deliberately returns synthetic bytes. This proves the
// protocol and independent quality boundary, not an Apple signature or install.
func TestWorkerMacOSSigningWaitAndFixedProgram(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native macOS worker contract requires darwin")
	}
	for _, mode := range []string{"wait", "private-cli", "success", "missing-config", "wrong-certificate", "wrong-team", "wrong-notary", "duplicate-output", "nonzero", "child-failure", "symlink-output"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			if evidence := os.Getenv("IS_MACOS_SIGNING_EVIDENCE_DIR"); evidence != "" && (mode == "wait" || mode == "private-cli") {
				directory := "worker-proof"
				if mode == "private-cli" {
					directory = "worker-private-proof"
				}
				root = filepath.Join(evidence, directory)
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
			if err = handler.Auth.Bootstrap("macos@example.test", "test-macos-password-123"); err != nil {
				t.Fatal(err)
			}
			identity, _ := handler.Auth.IdentityByEmail("macos@example.test")
			owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
			command := func(name string, in p.M) p.Resource {
				t.Helper()
				v, e := svc.Command(ctx, owner, name, in)
				if e != nil {
					t.Fatal(name, e)
				}
				return v
			}
			project := command("projects.create", p.M{"title": "macOS signing boundary"})
			goal := command("goals.create", p.M{"projectId": project.ID, "title": "Signed desktop", "outcome": "Notarize exact native application", "targets": []any{"macos"}, "criteria": []any{p.M{"id": "payload", "text": "Exact payload independently verified", "method": "automated"}}, "budget": 100})
			command("goals.approve", p.M{"id": goal.ID})
			inputSource, installer, application := "unsigned native fixture\n", strings.Repeat("a", 64), strings.Repeat("d", 64)
			buildCommand := []any{"/bin/sh", "-c", "cp source.txt unsigned.app.tar.gz"}
			qualityCommand := "test -z \"$IS_WORKER_TOKEN\" && grep -qx 'signed native fixture' \"$IS_ARTIFACT_PATH\""
			var privateCLI p.M
			if mode == "private-cli" {
				privateCLI = prepareMacOSCLIFixture(t, filepath.Join(root, "private-cli"), project.ID, goal.ID)
				inputSource, installer, application = p.S(privateCLI, "source"), p.S(privateCLI, "expectedSigner"), p.S(privateCLI, "expectedApplicationSigner")
				buildCommand = []any{p.S(privateCLI, "python"), "-c", "import base64,pathlib;pathlib.Path('unsigned.app.tar.gz').write_bytes(base64.b64decode(pathlib.Path('source.txt').read_text()))"}
				qualityCommand = "test -z \"$IS_WORKER_TOKEN\" && grep -qx 'fixture signed flat package ticket' \"$IS_ARTIFACT_PATH\""
			}
			repo := command("repositories.create", p.M{"projectId": project.ID, "title": "source"})
			repo = command("repositories.commit", p.M{"id": repo.ID, "branch": "feature", "message": "Unsigned native fixture", "files": p.M{"source.txt": inputSource}})
			commit := p.S(repo.Data, "lastCommit")
			plan := command("plans.propose", p.M{"goalId": goal.ID, "summary": "Isolated native signing", "tasks": []any{
				p.M{"id": "unsigned", "title": "Build unsigned app", "kind": "build", "role": "developer", "runtime": "darwin", "repositoryId": repo.ID, "commit": commit, "target": "macos", "artifactStage": "unsigned", "command": buildCommand, "outputPath": "unsigned.app.tar.gz", "budgetCents": 100},
				p.M{"id": "sign", "title": "Notarize exact app", "kind": "build", "role": "release", "runtime": "darwin", "target": "macos", "signingMode": "macos-pkg", "inputArtifactFrom": "unsigned", "dependsOn": []any{"unsigned"}, "expectedSigner": installer, "expectedApplicationSigner": application, "expectedTeamId": "ABCD123456", "budgetCents": 100},
			}})
			command("plans.approve", p.M{"id": plan.ID})
			pipeline := command("pipelines.create", p.M{"projectId": project.ID, "title": "Independent signed bytes", "runtime": "darwin", "method": "automated", "verificationEnvironment": "ci", "command": []any{"/bin/sh", "-c", qualityCommand}})
			goal, _ = store.Get(goal.ID)
			command("quality.bind", p.M{"goalId": goal.ID, "expectedRequirementRevision": goal.Revision, "expectedPlanId": plan.ID, "target": "macos", "pipelineId": pipeline.ID, "criteria": []any{"payload"}, "budgetCents": 100, "key": "notarized-only"})
			var signing atomic.Bool
			var sources, downloads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if signing.Load() && strings.Contains(r.URL.Path, "/repositories/") {
					sources.Add(1)
				}
				if signing.Load() && r.Method == "GET" && strings.Contains(r.URL.Path, "/artifacts/") && strings.HasSuffix(r.URL.Path, "/content") {
					downloads.Add(1)
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			runnerFor := func(role, capability string, trusted bool) *Runner {
				w := command("workers.register", p.M{"projectId": project.ID, "title": role + " macOS worker", "runtime": "darwin", "roles": []any{role}, "capabilities": []any{capability}, "trustedQuality": trusted})
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
			programRoot := t.TempDir()
			program, record, output := filepath.Join(programRoot, "signer.py"), filepath.Join(programRoot, "calls.jsonl"), filepath.Join(programRoot, "signed.pkg")
			text := fmt.Sprintf(`import hashlib,json,os,pathlib,sys
r=json.load(sys.stdin)
assert not any(k.startswith('IS_') for k in os.environ)
assert r['protocolVersion']=='macos-1'
record=pathlib.Path(%q)
previous=record.read_text().splitlines() if record.exists() else []
with record.open('a') as f:f.write(json.dumps(r)+'\n')
if r['action']=='sign':
 assert not previous
 assert pathlib.Path(r['artifactPath']).read_bytes()==b'unsigned native fixture\n'
else:
 assert r['action']=='status' and len(previous)==1 and r['artifactPath']==''
 assert r['notarizationId']=='11111111-2222-4333-8444-555555555555'
 assert r['requestBinding']=='c'*64 and r['payloadDigest']=='b'*64
mode=%q
if mode=='nonzero':print('private Apple diagnostic',file=sys.stderr);sys.exit(7)
if mode=='child-failure':
 import subprocess
 child=subprocess.Popen(['/bin/sleep','60'])
 record.with_suffix('.child').write_text(str(child.pid))
 sys.exit(7)
result={'protocolVersion':'macos-1','state':'complete','operationKey':r['operationKey'],'requestBinding':'c'*64,'payloadDigest':'b'*64,'certificateSHA256':r['expectedSigner'],'applicationCertificateSHA256':r['expectedApplicationSigner'],'teamId':r['expectedTeamId'],'notarizationId':'11111111-2222-4333-8444-555555555555','inputArtifactId':r['artifactId'],'inputArtifactDigest':r['artifactDigest']}
if mode=='wait' and r['action']=='sign':result['state']='processing'
else:
 output=pathlib.Path(%q);output.write_bytes(b'signed native fixture\n')
 if mode=='symlink-output':
  real=output.with_suffix('.real');output.rename(real);output.symlink_to(real)
 result.update(artifactPath=str(output),artifactDigest=hashlib.sha256(output.read_bytes()).hexdigest(),artifactSize=output.stat().st_size,notarized=True,bundleId='com.example.app',version='1.0.0',build='1')
if mode=='wrong-certificate':result['applicationCertificateSHA256']='e'*64
if mode=='wrong-team':result['teamId']='OTHER12345'
if mode=='wrong-notary':result['notarizationId']='wrong'
if mode=='duplicate-output':print('{"teamId":"OTHER12345",'+json.dumps(result)[1:]);sys.exit(0)
print(json.dumps(result))
`, record, mode, output)
			if mode == "private-cli" {
				text = fmt.Sprintf(`import json,pathlib,subprocess,sys
request=json.load(sys.stdin)
with pathlib.Path(%q).open('a') as stream:stream.write(json.dumps(request)+'\n')
result=subprocess.run([%q,%q,'--config',%q],input=json.dumps(request),text=True)
sys.exit(result.returncode)
`, record, p.S(privateCLI, "python"), p.S(privateCLI, "program"), p.S(privateCLI, "config"))
			}
			if err = os.WriteFile(program, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			if mode != "missing-config" {
				python, e := exec.LookPath("python3")
				if e != nil {
					t.Fatal(e)
				}
				signer.Config.Signers = map[string][]string{"macos": {python, program}}
			}
			t.Setenv("IS_WORKER_TOKEN", "must-not-reach-Apple-tools")
			signing.Store(true)
			if worked, e := signer.RunOnce(ctx); !worked || e != nil {
				t.Fatal(worked, e)
			}
			if mode == "child-failure" {
				data, e := os.ReadFile(strings.TrimSuffix(record, ".jsonl") + ".child")
				if e != nil {
					t.Fatal(e)
				}
				pid, e := strconv.Atoi(string(data))
				if e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() {
					if child, e := os.FindProcess(pid); e == nil {
						_ = child.Kill()
					}
				})
				state, e := exec.Command("/bin/ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
				if e == nil && strings.TrimSpace(string(state)) != "" && !strings.Contains(string(state), "Z") {
					t.Fatal("failed macOS signer left a live tool descendant", pid)
				}
			}
			if mode == "wait" || mode == "private-cli" {
				runs, _ := store.List(project.ID, "run", goal.ID)
				var waiting p.Resource
				for _, r := range runs {
					if r.Status == "waiting_signature" {
						waiting = r
					}
				}
				if waiting.ID == "" {
					t.Fatal("known operation did not enter durable waiting state", runs)
				}
				if worked, e := quality.RunOnce(ctx); worked || e != nil {
					t.Fatal("pending operation got QA", worked, e)
				}
				if worked, e := signer.RunOnce(ctx); worked || e != nil {
					t.Fatal("notarization queried early", worked, e)
				}
				if err = store.Update(ctx, func(tx *p.Tx) error {
					task, e := tx.Get(p.S(waiting.Data, "taskId"))
					if e != nil {
						return e
					}
					task.Data["signingNextCheckAt"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
					return tx.Put(task)
				}); err != nil {
					t.Fatal(err)
				}
				if mode == "private-cli" {
					body, e := os.ReadFile(p.S(privateCLI, "fixture"))
					if e != nil {
						t.Fatal(e)
					}
					var data p.M
					if e = json.Unmarshal(body, &data); e != nil {
						t.Fatal(e)
					}
					data["info_status"] = "Accepted"
					body, _ = json.Marshal(data)
					if e = os.WriteFile(p.S(privateCLI, "fixture"), body, 0600); e != nil {
						t.Fatal(e)
					}
				}
				if worked, e := signer.RunOnce(ctx); !worked || e != nil {
					t.Fatal(worked, e)
				}
			}
			signing.Store(false)
			if sources.Load() != 0 {
				t.Fatal("signer checked out repository source")
			}
			runs, _ := store.List(project.ID, "run", goal.ID)
			var latest p.Resource
			for _, r := range runs {
				if p.S(p.Obj(r.Data["task"]), "signingMode") != "" && r.Status != "waiting_signature" {
					latest = r
				}
			}
			if mode != "wait" && mode != "private-cli" && mode != "success" {
				if latest.Status != "blocked" || strings.Contains(p.S(latest.Data, "log"), "private Apple") {
					t.Fatal("unconfirmed signing or private diagnostic escaped", latest)
				}
				artifacts, _ := store.List(project.ID, "artifact", goal.ID)
				if len(artifacts) != 1 {
					t.Fatal("invalid signature registered a final package", artifacts)
				}
				return
			}
			if latest.Status != "succeeded" || downloads.Load() != 1 {
				t.Fatal("resumed signing downloaded/repeated input or did not succeed", latest, downloads.Load())
			}
			calls, e := os.ReadFile(record)
			if e != nil {
				t.Fatal(e)
			}
			if count := len(strings.Split(strings.TrimSpace(string(calls)), "\n")); ((mode == "wait" || mode == "private-cli") && count != 2) || (mode == "success" && count != 1) {
				t.Fatal("unexpected sign/status invocation count", count)
			}
			if worked, e := quality.RunOnce(ctx); !worked || e != nil {
				t.Fatal(worked, e)
			}
			evidence, _ := store.List(project.ID, "evidence", goal.ID)
			if len(evidence) != 1 || p.S(evidence[0].Data, "artifactId") != p.S(p.Obj(latest.Data["result"]), "artifactId") {
				t.Fatal("independent final package evidence missing", evidence)
			}
			if _, e := svc.Command(ctx, owner, "releases.create", p.M{"goalId": goal.ID, "version": "1.0.0", "commit": commit, "artifactIds": []any{p.S(evidence[0].Data, "artifactId")}}); e == nil || !strings.Contains(e.Error(), "目标环境") {
				t.Fatal("CI bytes bypassed desktop-native acceptance", e)
			}
			var request p.M
			if e := json.Unmarshal([]byte(strings.Split(string(calls), "\n")[0]), &request); e != nil || p.S(request, "signingInputDigest") == "" {
				t.Fatal("missing authoritative request binding", e)
			}
			if os.Getenv("IS_MACOS_SIGNING_EVIDENCE_DIR") != "" && (mode == "wait" || mode == "private-cli") {
				if err = svc.VerifyIntegrity(ctx); err != nil {
					t.Fatal(err)
				}
				if err = svc.Backup(ctx, filepath.Join(root, "backup.tar.gz")); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(root, "requests.jsonl"), calls, 0600); err != nil {
					t.Fatal(err)
				}
				facts := p.M{"passed": true, "syntheticAppleOnly": true, "realNotarization": false, "projectId": project.ID, "goalId": goal.ID, "taskId": p.S(latest.Data, "taskId"), "runId": latest.ID, "artifactId": p.S(p.Obj(latest.Data["result"]), "artifactId"), "evidenceId": evidence[0].ID, "inputDownloads": downloads.Load(), "sourceRequests": sources.Load(), "programCalls": 2}
				body, _ := json.MarshalIndent(facts, "", "  ")
				if err = os.WriteFile(filepath.Join(root, "result.json"), append(body, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// Only the Apple boundary is replaced. The actual administrator CLI, private
// archive parser, journal, fences and child process runner execute unchanged.
func prepareMacOSCLIFixture(t *testing.T, root, project, goal string) p.M {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	scripts, err := filepath.Abs("../../scripts")
	if err != nil {
		t.Fatal(err)
	}
	program := `import base64,hashlib,json,pathlib,plistlib,sys
sys.path.insert(0,sys.argv[1])
import test_macos_signer as fixture
root=pathlib.Path(sys.argv[2]).resolve();root.mkdir(mode=0o700)
for name,body in [('application.der',b'fixture application certificate'),('installer.der',b'fixture installer certificate'),('signing.keychain-db',b'fixture private keychain'),('entitlements.plist',plistlib.dumps({'com.apple.security.network.client':True}))]:
 path=root/name;path.write_bytes(body);path.chmod(0o600)
profile={'projectId':sys.argv[3],'goalId':sys.argv[4],'target':'macos','bundleId':'com.example.native','expectedTeamId':'ABCD123456','notaryProfile':'fixed-notary','tools':{k:'/usr/bin/true' for k in fixture.signer.TOOLS}}
for key,name in [('applicationCertificateFile','application.der'),('installerCertificateFile','installer.der'),('keychainFile','signing.keychain-db'),('entitlementsFile','entitlements.plist')]:profile[key]=str(root/name)
profile['expectedSigner']=hashlib.sha256((root/'installer.der').read_bytes()).hexdigest()
profile['expectedApplicationSigner']=hashlib.sha256((root/'application.der').read_bytes()).hexdigest()
tool_fixture=fixture.configure_cli_tools(root,profile)
config=root/'config.json';config.write_text(json.dumps({'stateDir':str(root/'state'),'profiles':[profile]}));config.chmod(0o600)
archive=fixture.archive(root/'unsigned.tar.gz')
print(json.dumps({'python':sys.executable,'program':str(pathlib.Path(sys.argv[1])/'sign-macos-pkg.py'),'config':str(config),'fixture':str(tool_fixture),'source':base64.b64encode(archive.read_bytes()).decode(),'expectedSigner':profile['expectedSigner'],'expectedApplicationSigner':profile['expectedApplicationSigner']}))
`
	cmd := exec.Command(python, "-c", program, scripts, root, project, goal)
	body, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixed CLI fixture: %v: %s", err, body)
	}
	var result p.M
	if err = json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
