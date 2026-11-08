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

	"github.com/intelligence-substrate/platform/internal/httpapi"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

// Apple success bytes are explicitly synthetic. Git/HTTP/processes/leases,
// artifact readback and the separate quality Worker execute for real.
func TestWorkerIOSSigningFixedProgramAndExactQA(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("iOS signing requires a native macOS worker")
	}
	for _, mode := range []string{"success", "private-cli", "wrong-profile", "wrong-team", "duplicate", "nonzero", "child-failure", "symlink", "missing-config", "changed-bytes"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			if evidence := os.Getenv("IS_IOS_SIGNING_EVIDENCE_DIR"); evidence != "" && (mode == "success" || mode == "private-cli") {
				name := "worker-proof"
				if mode == "private-cli" {
					name = "worker-private-proof"
				}
				root = filepath.Join(evidence, name)
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
			if err = handler.Auth.Bootstrap("ios@example.test", "ios-test-password-123"); err != nil {
				t.Fatal(err)
			}
			identity, _ := handler.Auth.IdentityByEmail("ios@example.test")
			owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
			command := func(name string, in p.M) p.Resource {
				t.Helper()
				v, e := svc.Command(ctx, owner, name, in)
				if e != nil {
					t.Fatal(name, e)
				}
				return v
			}
			project := command("projects.create", p.M{"title": "iOS signing boundary"})
			goal := command("goals.create", p.M{"projectId": project.ID, "title": "Signed device app", "outcome": "Sign exact native device payload", "targets": []any{"ios-appstore"}, "criteria": []any{p.M{"id": "payload", "text": "Exact payload independently verified", "method": "automated"}}, "budget": 100})
			command("goals.approve", p.M{"id": goal.ID})
			source, certificate, profile := "unsigned device fixture\n", strings.Repeat("a", 64), strings.Repeat("e", 64)
			buildCommand := []any{"/bin/sh", "-c", "cp source.txt unsigned.app.tar.gz"}
			qualityCommand := []any{"/bin/sh", "-c", "test -z \"$IS_WORKER_TOKEN\" && grep -qx 'synthetic signed device package' \"$IS_ARTIFACT_PATH\""}
			var privateCLI p.M
			if mode == "private-cli" {
				privateCLI = prepareIOSCLIFixture(t, filepath.Join(root, "private-cli"), project.ID, goal.ID)
				source, certificate, profile = p.S(privateCLI, "source"), p.S(privateCLI, "expectedSigner"), p.S(privateCLI, "expectedProvisioningProfile")
				buildCommand = []any{p.S(privateCLI, "python"), "-c", "import base64,pathlib;pathlib.Path('unsigned.app.tar.gz').write_bytes(base64.b64decode(pathlib.Path('source.txt').read_text()))"}
				qualityCommand = []any{p.S(privateCLI, "python"), "-c", fmt.Sprintf("import hashlib,os,zipfile;assert not os.getenv('IS_WORKER_TOKEN');z=zipfile.ZipFile(os.environ['IS_ARTIFACT_PATH']);assert z.read('Payload/Runner.app/assets/content.txt')==b'approved application resource';assert hashlib.sha256(z.read('Payload/Runner.app/embedded.mobileprovision')).hexdigest()==%q", profile)}
			}
			repo := command("repositories.create", p.M{"projectId": project.ID, "title": "source"})
			repo = command("repositories.commit", p.M{"id": repo.ID, "branch": "feature", "message": "Exact unsigned source", "files": p.M{"source.txt": source}})
			commit := p.S(repo.Data, "lastCommit")
			plan := command("plans.propose", p.M{"goalId": goal.ID, "summary": "Native build and isolated distribution signature", "tasks": []any{
				p.M{"id": "unsigned", "title": "Build native app", "kind": "build", "role": "developer", "runtime": "darwin", "repositoryId": repo.ID, "commit": commit, "target": "ios-appstore", "artifactStage": "unsigned", "command": buildCommand, "outputPath": "unsigned.app.tar.gz"},
				p.M{"id": "sign", "title": "Sign exact device app", "kind": "build", "role": "release", "runtime": "darwin", "target": "ios-appstore", "signingMode": "ios-ipa", "inputArtifactFrom": "unsigned", "dependsOn": []any{"unsigned"}, "expectedSigner": certificate, "expectedTeamId": "ABCD123456", "expectedProvisioningProfile": profile},
			}})
			command("plans.approve", p.M{"id": plan.ID})
			pipeline := command("pipelines.create", p.M{"projectId": project.ID, "title": "Independent IPA bytes", "runtime": "darwin", "method": "automated", "verificationEnvironment": "ci", "command": qualityCommand})
			goal, _ = store.Get(goal.ID)
			command("quality.bind", p.M{"goalId": goal.ID, "expectedRequirementRevision": goal.Revision, "expectedPlanId": plan.ID, "target": "ios-appstore", "pipelineId": pipeline.ID, "criteria": []any{"payload"}, "budgetCents": 100, "key": "exact-ipa"})
			var signing atomic.Bool
			var downloads, sources atomic.Int32
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
				w := command("workers.register", p.M{"projectId": project.ID, "title": role + " iOS node", "runtime": "darwin", "roles": []any{role}, "capabilities": []any{capability}, "trustedQuality": trusted})
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
			program := filepath.Join(root, "signer.py")
			output := filepath.Join(root, "synthetic.ipa")
			text := fmt.Sprintf(`import hashlib,json,os,pathlib,sys
r=json.load(sys.stdin)
assert not any(k.startswith('IS_') for k in os.environ)
assert r['protocolVersion']=='ios-1' and r['action']=='sign'
assert pathlib.Path(r['artifactPath']).read_bytes()==b'unsigned device fixture\n'
mode=%q
if mode=='nonzero':sys.exit(7)
if mode=='child-failure':
 import subprocess
 child=subprocess.Popen(['/bin/sleep','60'])
 pathlib.Path(%q).write_text(str(child.pid));sys.exit(7)
output=pathlib.Path(%q);output.write_bytes(b'synthetic signed device package\n')
result={'protocolVersion':'ios-1','state':'complete','operationKey':r['operationKey'],'requestBinding':'c'*64,'payloadDigest':'b'*64,'certificateSHA256':r['expectedSigner'],'profileSHA256':r['expectedProvisioningProfile'],'teamId':r['expectedTeamId'],'inputArtifactId':r['artifactId'],'inputArtifactDigest':r['artifactDigest'],'artifactPath':str(output),'artifactDigest':hashlib.sha256(output.read_bytes()).hexdigest(),'artifactSize':output.stat().st_size,'bundleId':'com.example.app','version':'1.0.0','build':'1'}
if mode=='wrong-profile':result['profileSHA256']='f'*64
if mode=='wrong-team':result['teamId']='OTHER12345'
if mode=='symlink':
 real=output.with_suffix('.real');output.rename(real);output.symlink_to(real)
if mode=='changed-bytes':output.write_bytes(b'changed after receipt')
if mode=='duplicate':print('{"teamId":"OTHER12345",'+json.dumps(result)[1:]);sys.exit(0)
print(json.dumps(result))
`, mode, filepath.Join(root, "child.pid"), output)
			if err = os.WriteFile(program, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			if mode != "missing-config" {
				python, e := exec.LookPath("python3")
				if e != nil {
					t.Fatal(e)
				}
				signer.Config.Signers = map[string][]string{"ios-appstore": {python, program}}
			}
			if mode == "private-cli" {
				signer.Config.Signers = map[string][]string{"ios-appstore": {p.S(privateCLI, "python"), p.S(privateCLI, "program"), "--config", p.S(privateCLI, "config")}}
			}
			t.Setenv("IS_WORKER_TOKEN", "must-not-reach-Apple-tools")
			signing.Store(true)
			if worked, e := signer.RunOnce(ctx); !worked || e != nil {
				t.Fatal(worked, e)
			}
			if mode == "child-failure" {
				data, e := os.ReadFile(filepath.Join(root, "child.pid"))
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
					t.Fatal("failed iOS signer left a live tool descendant", pid)
				}
			}
			signing.Store(false)
			tasks, _ := store.List(project.ID, "task", goal.ID)
			var task p.Resource
			for _, item := range tasks {
				if p.S(item.Data, "logicalId") == "sign" {
					task = item
				}
			}
			if mode != "success" && mode != "private-cli" {
				if task.Status != "blocked" {
					t.Fatal("unconfirmed signature was not blocked", task)
				}
				if worked, e := quality.RunOnce(ctx); worked || e != nil {
					t.Fatal("invalid signature got QA", worked, e)
				}
				return
			}
			if task.Status != "succeeded" || downloads.Load() != 1 || sources.Load() != 0 {
				t.Fatal("signing lost exact input or executed source", task, downloads.Load(), sources.Load())
			}
			if worked, e := quality.RunOnce(ctx); !worked || e != nil {
				t.Fatal("final independent quality did not run", worked, e)
			}
			artifacts, _ := store.List(project.ID, "artifact", goal.ID)
			var ids []any
			for _, a := range artifacts {
				if p.S(a.Data, "type") == "package" {
					ids = append(ids, a.ID)
				}
			}
			if len(ids) != 1 {
				t.Fatal("signer did not produce exactly one IPA", artifacts)
			}
			evidence, _ := store.List(project.ID, "evidence", goal.ID)
			if len(evidence) != 1 || evidence[0].Status != "passed" || p.S(evidence[0].Data, "artifactId") != ids[0] || p.S(evidence[0].Data, "verificationEnvironment") != "ci" {
				t.Fatal("final independent IPA evidence missing", evidence)
			}
			if _, e := svc.Command(ctx, owner, "releases.create", p.M{"goalId": goal.ID, "version": "1.0.0", "commit": commit, "artifactIds": ids}); e == nil || !strings.Contains(e.Error(), "目标环境") {
				t.Fatal("CI bypassed iOS device acceptance", e)
			}
			if err = svc.Backup(ctx, filepath.Join(root, "completed.tar.gz")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func prepareIOSCLIFixture(t *testing.T, root, project, goal string) p.M {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	module, err := filepath.Abs("../../scripts")
	if err != nil {
		t.Fatal(err)
	}
	program := fmt.Sprintf("import json,sys;sys.path.insert(0,%q);from test_ios_signer import export_cli_fixture;print(json.dumps(export_cli_fixture(%q,%q,%q)))", module, root, project, goal)
	output, err := exec.Command(python, "-c", program).CombinedOutput()
	if err != nil {
		t.Fatalf("private CLI fixture: %v %s", err, output)
	}
	var result p.M
	if err = json.Unmarshal(output, &result); err != nil {
		t.Fatal(err, string(output))
	}
	return result
}
