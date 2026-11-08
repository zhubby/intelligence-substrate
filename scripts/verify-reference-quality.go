//go:build ignore

// Real HTTP/Git/Flutter/Chrome acceptance. No model or public release is invoked.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/worker"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func shell(dir string, argv ...string) string {
	c := exec.Command("rtk", append([]string{"proxy"}, argv...)...)
	c.Dir = dir
	output, err := c.CombinedOutput()
	if err != nil {
		panic(fmt.Sprintf("%s: %v: %s", argv[0], err, output))
	}
	return strings.TrimSpace(string(output))
}
func main() {
	workDir := flag.String("work-dir", "", "New evidence directory, never overwritten")
	flag.Parse()
	cwd, err := os.Getwd()
	must(err)
	root := *workDir
	if root == "" {
		root, err = os.MkdirTemp(filepath.Join(cwd, "work"), "reference-quality-live-")
		must(err)
	} else {
		root, err = filepath.Abs(root)
		must(err)
		must(os.Mkdir(root, 0700))
	}
	fmt.Println("Evidence directory:", root)
	store, err := p.Open(filepath.Join(root, "control.db"))
	must(err)
	defer store.Close()
	svc := p.NewService(store)
	h := httpapi.New(svc, false)
	must(h.Auth.Bootstrap("reference-quality@example.test", "isolated-reference-quality-password"))
	identity, err := h.Auth.IdentityByEmail("reference-quality@example.test")
	must(err)
	owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	cmd := func(name string, in p.M) p.Resource { r, e := svc.Command(ctx, owner, name, in); must(e); return r }
	server := httptest.NewServer(h)
	defer server.Close()
	report := p.M{"kind": "same-package-real-flutter-build-and-automatic-browser-quality", "syntheticApprovals": true, "modelCalls": 0, "storePublished": false, "physicalDevice": false, "startedAt": time.Now().UTC(), "applications": []any{}}
	results := []any{}
	save := func() {
		b, e := json.MarshalIndent(report, "", "  ")
		must(e)
		must(os.WriteFile(filepath.Join(root, "result.json"), append(b, '\n'), 0600))
	}
	for index, mode := range []string{"feedback", "booking"} {
		generated := filepath.Join(root, mode+"-generated")
		fmt.Println("Generating fresh current-blueprint application:", mode)
		fmt.Println(shell(cwd, "python3", filepath.Join(cwd, "work/run-native.py"), "rtk", "proxy", "python3", "scripts/new-app.py", generated, "--name", "quality_"+mode, "--mode", mode, "--organization", "com.intelligencesubstrate.quality", "--create-platforms"))
		project := cmd("projects.create", p.M{"title": "Actual application QA - " + mode})
		goal := cmd("goals.create", p.M{"projectId": project.ID, "title": "Reference Web quality", "outcome": "Verify actual compiled application business against exact source backend", "targets": []any{"web"}, "criteria": []any{p.M{"id": "business", "text": "Actual business, persistence, ownership and offline recovery in Chrome", "method": "automated"}}, "budget": 100})
		cmd("goals.approve", p.M{"id": goal.ID})
		repository := cmd("repositories.create", p.M{"projectId": project.ID, "title": "Fresh " + mode})
		source := filepath.Join(root, mode+"-source")
		shell(cwd, "git", "clone", svc.RepoPath(repository), source)
		shell(source, "git", "checkout", "-b", "reference-quality")
		shell(source, "python3", "-c", `import shutil,sys;shutil.copytree(sys.argv[1],sys.argv[2],dirs_exist_ok=True,ignore=shutil.ignore_patterns('.git','.dart_tool','build','.gradle','Pods','ephemeral','.symlinks','local.properties','.flutter-plugins-dependencies','Generated.xcconfig','flutter_export_environment.sh'))`, generated, source)
		shell(source, "git", "add", ".")
		shell(source, "git", "-c", "user.name=Reference acceptance", "-c", "user.email=reference@example.test", "commit", "-m", "Import fresh reference application")
		commit := shell(source, "git", "rev-parse", "HEAD")
		shell(source, "git", "push", "origin", "reference-quality")
		api := fmt.Sprintf("http://127.0.0.1:%d", 18136+index)
		plan := cmd("plans.propose", p.M{"goalId": goal.ID, "summary": "Compile actual source using isolated native diagnostic runner", "tasks": []any{p.M{"id": "web-build", "title": "Build actual Flutter Web " + mode, "role": "developer", "kind": "build", "runtime": runtime.GOOS, "repositoryId": repository.ID, "commit": commit, "branch": "reference-quality", "command": []any{"rtk", "proxy", "python3", filepath.Join(cwd, "work/run-native.py"), "rtk", "proxy", "python3", "scripts/build-reference-client.py", "--application", ".", "--target", "web", "--api-url", api, "--local-testing", "--output", "artifacts/web.tar.gz"}, "outputPath": "artifacts/web.tar.gz", "target": "web", "timeoutSeconds": 900}}})
		cmd("plans.approve", p.M{"id": plan.ID})
		pipeline := cmd("pipelines.create", p.M{"projectId": project.ID, "title": "Actual Chrome business acceptance", "command": []any{"rtk", "proxy", "node", filepath.Join(cwd, "scripts/verify-reference-web.mjs"), "--application", ".", "--api-url", api, "--output", "quality/browser.tar.gz"}, "reportPath": "quality/browser.tar.gz", "method": "automated", "runtime": runtime.GOOS, "verificationEnvironment": "browser"})
		policy := cmd("quality.bind", p.M{"goalId": goal.ID, "expectedRequirementRevision": goal.Revision, "expectedPlanId": plan.ID, "pipelineId": pipeline.ID, "target": "web", "criteria": []any{"business"}, "budgetCents": 100, "key": "actual-browser"})
		runner := func(role, capability string, quality bool) *worker.Runner {
			node := cmd("workers.register", p.M{"projectId": project.ID, "title": mode + "-" + role, "runtime": runtime.GOOS, "roles": []any{role}, "capabilities": []any{capability}, "trustedQuality": quality})
			token, _, e := h.Auth.IssueToken(ctx, owner, p.M{"projectId": project.ID, "workerId": node.ID})
			must(e)
			client, e := worker.NewClient(server.URL, token, true)
			must(e)
			return &worker.Runner{Client: client, Config: worker.Config{Driver: "native", AllowNative: true, StateDir: filepath.Join(root, mode+"-"+role)}}
		}
		dev := runner("developer", "build", false)
		fmt.Println("Running actual Worker build:", mode, commit)
		worked, err := dev.RunOnce(ctx)
		must(err)
		if !worked {
			panic("build was not claimed")
		}
		artifacts, err := store.List(project.ID, "artifact", goal.ID)
		must(err)
		dump := func() []p.Resource {
			runs, e := store.List(project.ID, "run", goal.ID)
			must(e)
			b, _ := json.MarshalIndent(runs, "", "  ")
			must(os.WriteFile(filepath.Join(root, mode+"-runs.json"), b, 0600))
			return runs
		}
		if len(artifacts) != 1 {
			dump()
			panic("no actual build artifact; inspect " + root)
		}
		artifact := artifacts[0]
		must(svc.VerifyArtifact(artifact))
		if p.S(artifact.Data, "commit") != commit {
			panic("artifact commit mismatch")
		}
		qa := runner("quality", "test", true)
		fmt.Println("Polling protected automatic browser QA:", mode, artifact.ID)
		worked, err = qa.RunOnce(ctx)
		must(err)
		if !worked {
			panic("automatic quality was not claimed")
		}
		runs := dump()
		evidence, err := store.List(project.ID, "evidence", goal.ID)
		must(err)
		if len(evidence) != 1 || evidence[0].Status != "passed" || p.S(evidence[0].Data, "artifactId") != artifact.ID || p.S(evidence[0].Data, "qualityPolicyId") != policy.ID {
			panic("actual business QA did not pass; inspect " + root)
		}
		proof, err := store.Get(p.S(evidence[0].Data, "proofArtifactId"))
		must(err)
		must(svc.VerifyArtifact(proof))
		if p.S(proof.Data, "testedArtifactId") != artifact.ID || p.S(proof.Data, "runId") != p.S(evidence[0].Data, "runId") || p.S(proof.Data, "digest") != p.S(evidence[0].Data, "proofDigest") {
			panic("report is not tied to exact tested package/run")
		}
		bytes, err := os.ReadFile(svc.ArtifactPath(p.S(proof.Data, "digest")))
		must(err)
		proofFile := filepath.Join(root, mode+"-browser-report.tar.gz")
		must(os.WriteFile(proofFile, bytes, 0600))
		browserDir := filepath.Join(root, mode+"-browser")
		shell(cwd, "python3", "-c", `import json,pathlib,sys,tarfile; r=pathlib.Path(sys.argv[2]);r.mkdir();t=tarfile.open(sys.argv[1]);names=t.getnames();assert all('/' not in n and '..' not in n and t.getmember(n).isfile() for n in names);t.extractall(r,filter='data');v=json.loads((r/'result.json').read_text());assert v['passed'] and v['archive']['sha256']==sys.argv[3] and all(x['stopped'] for x in v['cleanup'])`, proofFile, browserDir, p.S(artifact.Data, "digest"))
		shell(cwd, "python3", "-c", `import hashlib,json,pathlib,subprocess,sys
v=json.loads(pathlib.Path(sys.argv[1]).read_text())
def git(*args): return subprocess.check_output(['rtk','proxy','git','-C',sys.argv[2],*args])
files=set(git('ls-tree','-r','--name-only','-z',sys.argv[3]).decode().split('\0'))-{''}
assert files==set(v['sourceFiles']), {'missing':sorted(files-set(v['sourceFiles'])),'extra':sorted(set(v['sourceFiles'])-files)}
for name,digest in v['sourceFiles'].items(): assert hashlib.sha256(git('show',sys.argv[3]+':'+name)).hexdigest()==digest,name
print('Every report source file matches the complete exact Git tree')`, filepath.Join(browserDir, "result.json"), source, commit)
		candidate := cmd("releases.create", p.M{"goalId": goal.ID, "version": "1.0.0", "commit": commit, "artifactIds": []any{artifact.ID}})
		if candidate.Status != "awaiting_approval" {
			panic("unexpected candidate status: " + candidate.Status)
		}
		for range 2 {
			if idle, e := qa.RunOnce(ctx); e != nil || idle {
				panic("repeat poll duplicated QA")
			}
		}
		dispatches, err := store.List(project.ID, "quality_dispatch", goal.ID)
		must(err)
		if len(dispatches) != 1 {
			panic("duplicate automatic dispatch")
		}
		results = append(results, p.M{"mode": mode, "projectId": project.ID, "goalId": goal.ID, "repositoryId": repository.ID, "commit": commit, "artifact": artifact, "reportArtifact": proof, "evidence": evidence[0], "runs": runs, "policy": policy, "dispatches": dispatches, "candidate": candidate, "browserReportPath": browserDir})
		report["applications"] = results
		save()
		fmt.Println("Actual same-package browser QA and candidate verified:", mode, artifact.ID, proof.ID)
	}
	must(svc.VerifyIntegrity(ctx))
	report["passed"] = true
	report["finishedAt"] = time.Now().UTC()
	report["serverStoppedOnExit"] = true
	save()
	fmt.Println("Passed:", filepath.Join(root, "result.json"))
}
