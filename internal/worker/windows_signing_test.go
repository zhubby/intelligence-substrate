package worker

import (
	"context"
	"encoding/base64"
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
	"time"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

// This cross-host test exercises the transport below the native OS guard.
// Native Windows compilation, certificate trust and installation are separate acceptance.
func TestWorkerWindowsSignerExactTransportPrivateCLIAndIndependentQA(t *testing.T) {
	for _, mode := range []string{"success", "content", "certificate", "timestamp", "missing-config", "duplicate", "changed-bytes", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			if evidence := os.Getenv("IS_WINDOWS_SIGNING_EVIDENCE_DIR"); evidence != "" && mode == "success" {
				root = filepath.Join(evidence, "worker-proof")
				if e := os.Mkdir(root, 0700); e != nil {
					t.Fatal(e)
				}
			}
			store, err := p.Open(filepath.Join(root, "platform.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			svc := p.NewService(store)
			handler := httpapi.New(svc, false)
			if err = handler.Auth.Bootstrap("windows@example.test", "windows-test-password-123"); err != nil {
				t.Fatal(err)
			}
			identity, _ := handler.Auth.IdentityByEmail("windows@example.test")
			owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
			command := func(name string, in p.M) p.Resource {
				t.Helper()
				r, e := svc.Command(ctx, owner, name, in)
				if e != nil {
					t.Fatal(name, e)
				}
				return r
			}
			project := command("projects.create", p.M{"title": "Windows exact signing transport"})
			goal := command("goals.create", p.M{"projectId": project.ID, "title": "Governed MSIX", "outcome": "Exact private signing then independent CI", "targets": []any{"windows"}, "criteria": []any{p.M{"id": "payload", "text": "Exact approved payload", "method": "automated"}}, "budget": 100})
			command("goals.approve", p.M{"id": goal.ID})
			python, e := exec.LookPath("python3")
			if e != nil {
				t.Fatal(e)
			}
			module, e := filepath.Abs("../../scripts")
			if e != nil {
				t.Fatal(e)
			}
			providerMode := mode
			if !p.Has([]string{"content", "certificate", "timestamp"}, mode) {
				providerMode = "ok"
			}
			program := fmt.Sprintf("import json,sys;sys.path.insert(0,%q);from test_windows_signer import export_cli_fixture;print(json.dumps(export_cli_fixture(%q,%q,%q,%q)))", module, filepath.Join(root, "private-cli"), project.ID, goal.ID, providerMode)
			body, e := exec.Command(python, "-c", program).CombinedOutput()
			if e != nil {
				t.Fatalf("fixture: %v %s", e, body)
			}
			var config p.M
			if e = json.Unmarshal(body, &config); e != nil {
				t.Fatal(e)
			}
			source, e := base64.StdEncoding.DecodeString(p.S(config, "source"))
			if e != nil {
				t.Fatal(e)
			}
			repo := command("repositories.create", p.M{"projectId": project.ID, "title": "Windows source"})
			repo = command("repositories.commit", p.M{"id": repo.ID, "branch": "feature", "message": "Explicit structural MSIX fixture", "files": p.M{"source.txt": p.S(config, "source")}})
			commit := p.S(repo.Data, "lastCommit")
			plan := command("plans.propose", p.M{"goalId": goal.ID, "summary": "Native build then fixed signature", "tasks": []any{
				p.M{"id": "unsigned", "title": "Unsigned native build", "kind": "build", "role": "developer", "runtime": "windows", "repositoryId": repo.ID, "commit": commit, "target": "windows", "artifactStage": "unsigned", "command": []any{"true"}, "outputPath": "unsigned.msix"},
				p.M{"id": "sign", "title": "Sign exact Windows package", "kind": "build", "role": "release", "runtime": "windows", "target": "windows", "signingMode": "windows-msix", "inputArtifactFrom": "unsigned", "dependsOn": []any{"unsigned"}, "expectedSigner": p.S(config, "expectedSigner"), "expectedWindowsPublisher": "CN=Company", "expectedWindowsPackage": "com.company.feedback", "expectedWindowsVersion": "1.0.0.0"}}})
			command("plans.approve", p.M{"id": plan.ID})
			pipeline := command("pipelines.create", p.M{"projectId": project.ID, "title": "Independent cross-host MSIX integrity", "runtime": runtime.GOOS, "method": "automated", "verificationEnvironment": "ci", "command": []any{python, "-c", "import os,zipfile;assert not os.getenv('IS_WORKER_TOKEN');z=zipfile.ZipFile(os.environ['IS_ARTIFACT_PATH']);assert z.read('app/data/flutter_assets/content.txt')==b'approved Windows resource';assert z.read('AppxSignature.p7x').startswith(b'PKCX')"}})
			goal, _ = store.Get(goal.ID)
			command("quality.bind", p.M{"goalId": goal.ID, "expectedRequirementRevision": goal.Revision, "expectedPlanId": plan.ID, "target": "windows", "pipelineId": pipeline.ID, "criteria": []any{"payload"}, "budgetCents": 100, "key": "exact-msix"})
			var downloads, sources atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/repositories/") {
					sources.Add(1)
				}
				if r.Method == "GET" && strings.Contains(r.URL.Path, "/artifacts/") && strings.HasSuffix(r.URL.Path, "/content") {
					downloads.Add(1)
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			runner := func(role, capability, system string, trusted bool) (*Runner, p.Actor) {
				w := command("workers.register", p.M{"projectId": project.ID, "title": role + " node", "runtime": system, "roles": []any{role}, "capabilities": []any{capability}, "trustedQuality": trusted})
				token, _, e := handler.Auth.IssueToken(ctx, owner, p.M{"projectId": project.ID, "workerId": w.ID})
				if e != nil {
					t.Fatal(e)
				}
				client, e := NewClient(server.URL, token, true)
				if e != nil {
					t.Fatal(e)
				}
				return &Runner{Client: client, Config: Config{Driver: "native", AllowNative: true, StateDir: t.TempDir()}}, p.Actor{ID: "worker:" + w.ID, Kind: "worker", ProjectID: project.ID, WorkerID: w.ID, Roles: []string{role}}
			}
			builder, builderActor := runner("developer", "build", "windows", false)
			signer, _ := runner("release", "build", "windows", false)
			quality, _ := runner("quality", "test", runtime.GOOS, true)
			built, e := builder.Client.Command(ctx, "tasks.claim", p.M{})
			if e != nil {
				t.Fatal(e)
			}
			artifact, e := svc.Upload(ctx, builderActor, p.M{"goalId": goal.ID, "runId": built.ID, "leaseToken": p.S(built.Data, "leaseToken"), "repositoryId": repo.ID, "commit": commit, "target": "windows", "type": "unsigned-package", "name": "unsigned.msix"}, strings.NewReader(string(source)))
			if e != nil {
				t.Fatal(e)
			}
			if _, e = builder.Client.Command(ctx, "tasks.complete", p.M{"id": built.ID, "leaseToken": p.S(built.Data, "leaseToken"), "exitCode": 0, "result": p.M{"artifactId": artifact.ID, "repositoryId": repo.ID, "commit": commit}}); e != nil {
				t.Fatal(e)
			}
			run, e := signer.Client.Command(ctx, "tasks.claim", p.M{})
			if e != nil {
				t.Fatal(e)
			}
			in := p.Obj(p.Obj(run.Data["task"])["signingInput"])
			if runtime.GOOS != "windows" {
				if _, exit, e := signer.execute(ctx, run, io.Discard); e == nil || exit == 0 {
					t.Fatal("non-Windows host executed a Windows signing task")
				}
			}
			if mode != "missing-config" {
				signer.Config.Signers = map[string][]string{"windows": {p.S(config, "python"), p.S(config, "program")}}
			}
			if mode == "duplicate" || mode == "changed-bytes" || mode == "cancel" {
				text := "import json,sys,time\nr=json.load(sys.stdin)\n"
				if mode == "cancel" {
					text += "time.sleep(60)\n"
				} else {
					text += "print('{\"state\":\"complete\",\"State\":\"other\"}')\n"
				}
				if mode == "changed-bytes" {
					text = "import json,sys,pathlib,runpy\n" + fmt.Sprintf("sys.path.insert(0,%q)\n", module) + "from windows_signer import Signer,load_config,decode_request\nfrom test_windows_signer import WindowsProvider\n" + fmt.Sprintf("config=load_config(%q)\n", p.S(config, "config")) + "r=Signer(config,tools=WindowsProvider(config['profiles'][0])).handle(decode_request(sys.stdin.buffer.read(65537)))\npathlib.Path(r['artifactPath']).write_bytes(b'changed')\nprint(json.dumps(r))\n"
				}
				path := filepath.Join(root, "boundary.py")
				if e = os.WriteFile(path, []byte(text), 0600); e != nil {
					t.Fatal(e)
				}
				signer.Config.Signers = map[string][]string{"windows": {python, path}}
			}
			t.Setenv("IS_WORKER_TOKEN", "must-not-reach-Windows-provider")
			job := ctx
			cancel := func() {}
			if mode == "cancel" {
				job, cancel = context.WithTimeout(ctx, 300*time.Millisecond)
			}
			defer cancel()
			result, exit, execErr := signer.signWindowsArtifact(job, run, in)
			if mode != "success" {
				if execErr == nil || exit == 0 || !p.B(result, "blocked") {
					t.Fatal("invalid signature accepted", result, exit, execErr)
				}
			} else if execErr != nil || exit != 0 {
				t.Fatal(result, exit, execErr)
			}
			if _, e = signer.Client.Command(ctx, "tasks.complete", p.M{"id": run.ID, "leaseToken": p.S(run.Data, "leaseToken"), "exitCode": exit, "result": result}); e != nil {
				t.Fatal(e)
			}
			if mode != "success" {
				if worked, e := quality.RunOnce(ctx); worked || e != nil {
					t.Fatal("invalid signature received QA", worked, e)
				}
				return
			}
			if downloads.Load() != 1 || sources.Load() != 0 {
				t.Fatal("signature fetched source or repeated unsigned input", downloads.Load(), sources.Load())
			}
			if worked, e := quality.RunOnce(ctx); !worked || e != nil {
				t.Fatal("final independent CI missing", worked, e)
			}
			evidence, _ := store.List(project.ID, "evidence", goal.ID)
			if len(evidence) != 1 || evidence[0].Status != "passed" || p.S(evidence[0].Data, "artifactId") != p.S(result, "artifactId") {
				t.Fatal("exact final artifact evidence missing", evidence)
			}
			if _, e = svc.Command(ctx, owner, "releases.create", p.M{"goalId": goal.ID, "version": "1.0.0", "commit": commit, "artifactIds": []any{p.S(result, "artifactId")}}); e == nil || !strings.Contains(e.Error(), "目标环境") {
				t.Fatal("CI bypassed native Windows acceptance", e)
			}
			if e = svc.Backup(ctx, filepath.Join(root, "completed.tar.gz")); e != nil {
				t.Fatal(e)
			}
		})
	}
}
