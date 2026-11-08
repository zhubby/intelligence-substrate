package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

// Git changes, commands, exact package downloads and QA reports are real.
// Channel receipts and human browser attestations are explicitly synthetic.
func TestWorkerRepairsChannelRejectionThroughFreshBuildQualityAndApproval(t *testing.T) {
	ctx := context.Background()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required for real fixed-command acceptance", err)
	}
	root := t.TempDir()
	if evidence := os.Getenv("IS_RELEASE_ISSUE_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, "worker-proof")
		if err = os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := p.Open(filepath.Join(root, "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &p.Service{Store: store, Root: root}
	h := httpapi.New(s, false)
	if err = h.Auth.Bootstrap("remediation@example.test", "isolated-remediation-password-2026"); err != nil {
		t.Fatal(err)
	}
	identity, _ := h.Auth.IdentityByEmail("remediation@example.test")
	owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
	officer := p.Actor{ID: "remediation-release-officer", Kind: "human", Admin: true}
	qaHuman := p.Actor{ID: "remediation-quality-officer", Kind: "human", Admin: true}
	command := func(actor p.Actor, name string, in p.M) p.Resource {
		t.Helper()
		r, err := s.Command(ctx, actor, name, in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return r
	}
	h.ModelCall = func(context.Context, p.Actor, p.M) (p.M, error) {
		t.Error("deterministic remediation unexpectedly called a model")
		return nil, fmt.Errorf("models disabled in this acceptance")
	}
	project := command(owner, "projects.create", p.M{"title": "Actual channel remediation execution"})
	g := command(owner, "goals.create", p.M{"projectId": project.ID, "title": "Contact privacy flow", "outcome": "Accept valid messages with consent", "targets": []any{"web"}, "criteria": []any{p.M{"id": "contact", "text": "Validate accepted contact payloads", "method": "automated"}}, "budget": 100})
	command(owner, "goals.approve", p.M{"id": g.ID})
	repo := command(owner, "repositories.create", p.M{"projectId": project.ID, "title": "contact-flow"})
	seed := command(owner, "repositories.commit", p.M{"id": repo.ID, "branch": "seed", "message": "Initial contact validation", "files": p.M{"app.py": "def accepts(data):\n    return bool(data.get('message'))\n"}})
	server := httptest.NewServer(h)
	defer server.Close()
	runner := func(role string, capabilities []any, quality bool) (*Runner, string) {
		t.Helper()
		node := command(owner, "workers.register", p.M{"projectId": project.ID, "title": role + " remediation node", "runtime": runtime.GOOS, "roles": []any{role}, "capabilities": capabilities, "trustedQuality": quality})
		token, _, err := h.Auth.IssueToken(ctx, owner, p.M{"projectId": project.ID, "workerId": node.ID})
		if err != nil {
			t.Fatal(err)
		}
		client, err := NewClient(server.URL, token, true)
		if err != nil {
			t.Fatal(err)
		}
		return &Runner{Client: client, Config: Config{Driver: "native", AllowNative: true, StateDir: filepath.Join(root, role+"-state")}}, node.ID
	}
	dev, _ := runner("developer", []any{"code", "build"}, false)
	qa, _ := runner("quality", []any{"test"}, true)
	publisher, publisherID := runner("release", []any{"publish"}, false)
	trusted := filepath.Join(root, "trusted-local-channel.py")
	if err = os.WriteFile(trusted, []byte(`import json, sys
request = json.load(sys.stdin)
if request['action'] == 'status':
    result = {'externalId': request['externalId'], 'state': 'rejected', 'reason': 'Missing explicit privacy consent', 'upstreamState': 'LOCAL_REJECTION_FIXTURE'}
else:
    result = {'externalId': 'synthetic-contact:' + request['version'], 'state': 'awaiting_review' if request['version'] == '1.0.0' else 'published'}
print(json.dumps(result))
`), 0600); err != nil {
		t.Fatal(err)
	}
	publisher.Config.Publishers = map[string][]string{"web": {python, trusted}}
	publisher.Config.StatusProviders = map[string][]string{"web": {python, trusted}}
	work := func(r *Runner) {
		t.Helper()
		if worked, err := r.RunOnce(ctx); err != nil || !worked {
			t.Fatal("real Worker execution did not finish", worked, err)
		}
	}
	buildTask := func(commit string, dependency bool) p.M {
		task := p.M{"id": "build", "title": "Build exact contact validator", "kind": "build", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "target": "web", "outputPath": "package.py", "command": []any{python, "-c", "from pathlib import Path; Path('package.py').write_bytes(Path('app.py').read_bytes())"}}
		if dependency {
			task["dependsOn"] = []any{"repair"}
		} else {
			task["commit"] = commit
		}
		return task
	}
	phase := func(repair bool, oldRelease p.Resource) (p.Resource, p.Resource, p.Resource) {
		t.Helper()
		tasks := []any{buildTask(p.S(seed.Data, "lastCommit"), false)}
		if repair {
			tasks = []any{p.M{"id": "repair", "title": "Require explicit privacy consent", "kind": "code", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "commit": p.S(oldRelease.Data, "commit"), "branch": "repair-consent", "command": []any{python, "-c", "from pathlib import Path; Path('app.py').write_text(\"def accepts(data):\\n    return bool(data.get('message')) and data.get('consent') is True\\n\")"}}, buildTask("", true)}
		}
		plan := command(owner, "plans.propose", p.M{"goalId": g.ID, "summary": "Build and independently verify contact validation", "tasks": tasks})
		command(owner, "plans.approve", p.M{"id": plan.ID})
		checks := "assert accept({'message':'hello','consent':True}); assert not accept({'message':''})\n"
		if repair {
			checks += "assert not accept({'message':'hello'}); assert not accept({'message':'hello','consent':False})\n"
		}
		qaCode := "import os,json,hashlib,runpy\nfrom pathlib import Path\np=Path(os.environ['IS_ARTIFACT_PATH'])\naccept=runpy.run_path(str(p))['accepts']\n" + checks + "Path('qa-report.json').write_text(json.dumps({'passed':True,'artifactSHA256':hashlib.sha256(p.read_bytes()).hexdigest(),'consentChecks':" + map[bool]string{false: "False", true: "True"}[repair] + "}))\n"
		pipe := command(owner, "pipelines.create", p.M{"projectId": project.ID, "title": "Independent contact checks", "runtime": runtime.GOOS, "method": "automated", "command": []any{python, "-c", qaCode}, "reportPath": "qa-report.json"})
		command(owner, "quality.bind", p.M{"goalId": g.ID, "expectedRequirementRevision": g.Revision, "expectedPlanId": plan.ID, "pipelineId": pipe.ID, "target": "web", "criteria": []any{"contact"}, "budgetCents": 100, "key": p.NewID("policy")})
		if repair {
			work(dev)
		}
		work(dev)
		work(qa)
		runs, _ := store.List(project.ID, "run", g.ID)
		var artifact, report p.Resource
		for _, run := range runs {
			if run.Status != "succeeded" {
				t.Fatal("real command failed", run)
			}
			task := p.Obj(run.Data["task"])
			if p.S(task, "planId") == plan.ID && p.S(task, "kind") == "test" {
				artifact, _ = store.Get(p.S(task, "artifactId"))
				report, _ = store.Get(p.S(p.Obj(run.Data["result"]), "reportArtifactId"))
			}
		}
		if artifact.ID == "" || report.ID == "" {
			t.Fatal("actual package or independent report missing")
		}
		// This fixture deliberately cannot prove a browser or production service.
		// A separately labelled human attestation is needed for the protocol gate.
		proof, err := s.Upload(ctx, qaHuman, p.M{"goalId": g.ID, "repositoryId": repo.ID, "commit": p.S(artifact.Data, "commit"), "target": "web", "type": "report", "name": "synthetic-environment.txt"}, strings.NewReader("Synthetic protocol-only browser attestation; no real browser application or public store claim"))
		if err != nil {
			t.Fatal(err)
		}
		command(qaHuman, "evidence.record", p.M{"artifactId": artifact.ID, "proofArtifactId": proof.ID, "method": "manual", "purpose": "environment", "verificationEnvironment": "browser", "statement": "Synthetic environment attestation for end-to-end control contract only"})
		version := map[bool]string{false: "1.0.0", true: "1.0.1"}[repair]
		r := command(owner, "releases.create", p.M{"goalId": g.ID, "version": version, "commit": p.S(artifact.Data, "commit"), "artifactIds": []any{artifact.ID}})
		command(officer, "releases.approve", p.M{"id": r.ID})
		command(officer, "releases.dispatch", p.M{"id": r.ID, "target": "web", "workerId": publisherID, "key": "publish-" + version})
		work(publisher)
		return r, artifact, report
	}
	original, oldPackage, _ := phase(false, p.Resource{})
	command(officer, "releases.refresh", p.M{"id": original.ID, "target": "web", "key": "observe-rejection"})
	work(publisher)
	issues, _ := store.List(project.ID, "release_issue", g.ID)
	if len(issues) != 1 {
		t.Fatal("actual channel query did not create one issue", issues)
	}
	issue := issues[0]
	command(officer, "release_issues.triage", p.M{"id": issue.ID, "expectedRevision": issue.Revision, "expectedBinding": p.S(issue.Data, "binding"), "key": "confirmed-repair", "strategy": "new-build", "instructions": "Require affirmative privacy consent; independently test absent and false consent on the rebuilt bytes"})
	replacement, newPackage, report := phase(true, original)
	current, _ := store.Get(issue.ID)
	if current.Status == "resolved" || p.S(newPackage.Data, "digest") == p.S(oldPackage.Data, "digest") {
		t.Fatal("publisher receipt closed issue or reused rejected bytes")
	}
	proof, err := s.Upload(ctx, qaHuman, p.M{"goalId": g.ID, "repositoryId": repo.ID, "commit": p.S(newPackage.Data, "commit"), "target": "web", "type": "report", "name": "synthetic-publication.txt"}, strings.NewReader("Synthetic local channel only; public availability not tested"))
	if err != nil {
		t.Fatal(err)
	}
	evidence := command(qaHuman, "evidence.record", p.M{"artifactId": newPackage.ID, "proofArtifactId": proof.ID, "method": "manual", "purpose": "publication", "verificationEnvironment": "browser", "externalId": "synthetic-contact:1.0.1", "statement": "Synthetic independent publication attestation; not a real production release"})
	command(qaHuman, "releases.verify", p.M{"id": replacement.ID, "target": "web", "evidenceId": evidence.ID})
	current, _ = store.Get(issue.ID)
	if current.Status != "resolved" || p.S(p.Obj(current.Data["resolution"]), "releaseId") != replacement.ID {
		t.Fatal("independent replacement acceptance did not close exact issue", current)
	}
	if err = s.VerifyIntegrity(ctx); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("IS_RELEASE_ISSUE_EVIDENCE_DIR") != "" {
		if err = s.Backup(ctx, filepath.Join(root, "backup.tar.gz")); err != nil {
			t.Fatal(err)
		}
		facts := p.M{"passed": true, "paidModelCalled": false, "syntheticChannelOnly": true, "syntheticBrowserAttestation": true, "productionDelivered": false, "projectId": project.ID, "goalId": g.ID, "issueId": issue.ID, "originalReleaseId": original.ID, "replacementReleaseId": replacement.ID, "oldPackageId": oldPackage.ID, "newPackageId": newPackage.ID, "reportId": report.ID, "evidenceId": evidence.ID, "repositoryId": repo.ID, "oldCommit": p.S(oldPackage.Data, "commit"), "newCommit": p.S(newPackage.Data, "commit")}
		body, _ := json.MarshalIndent(facts, "", "  ")
		if err = os.WriteFile(filepath.Join(root, "result.json"), append(body, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
