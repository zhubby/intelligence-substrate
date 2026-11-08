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

	"github.com/intelligence-substrate/platform/internal/httpapi"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

// RunOnce uses real HTTP leases, downloads, subprocesses and completion. The
// package, device attestations and upstream responses are synthetic fixtures.
func TestWorkerStoreMaterialRemediationHTTPRunOnceAndIndependentAcceptance(t *testing.T) {
	testWorkerStoreMaterialRemediation(t, false)
}

func TestWorkerStoreScreenshotRevisionHTTPRunOnceAndIndependentAcceptance(t *testing.T) {
	testWorkerStoreMaterialRemediation(t, true)
}

func testWorkerStoreMaterialRemediation(t *testing.T, screenshots bool) {
	ctx := context.Background()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	evidenceRoot := os.Getenv("IS_STORE_REVISION_EVIDENCE_DIR")
	if screenshots {
		evidenceRoot = os.Getenv("IS_SCREENSHOT_REVISION_EVIDENCE_DIR")
	}
	if evidenceRoot != "" {
		root = filepath.Join(evidenceRoot, "worker-proof")
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
	if err = h.Auth.Bootstrap("materials@example.test", "isolated-materials-password-2026"); err != nil {
		t.Fatal(err)
	}
	identity, _ := h.Auth.IdentityByEmail("materials@example.test")
	owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
	author := p.Actor{ID: "independent-material-author", Kind: "human", Admin: true}
	officer := p.Actor{ID: "independent-release-officer", Kind: "human", Admin: true}
	quality := p.Actor{ID: "independent-quality-officer", Kind: "human", Admin: true}
	command := func(a p.Actor, name string, in p.M) p.Resource {
		t.Helper()
		r, err := s.Command(ctx, a, name, in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return r
	}
	h.ModelCall = func(context.Context, p.Actor, p.M) (p.M, error) {
		t.Error("material correction must not invoke a model")
		return nil, fmt.Errorf("models disabled")
	}
	project := command(owner, "projects.create", p.M{"title": "Real HTTP material Worker acceptance"})
	goal := command(owner, "goals.create", p.M{"projectId": project.ID, "title": "Original iOS build", "outcome": "Correct privacy link without replacing the build", "targets": []any{"ios-appstore"}, "criteria": []any{p.M{"id": "content", "text": "Content is accessible", "method": "manual"}}, "budget": 50})
	command(owner, "goals.approve", p.M{"id": goal.ID})
	plan := command(author, "plans.propose", p.M{"goalId": goal.ID, "summary": "Verify original package", "tasks": []any{p.M{"id": "analysis", "title": "Inspect package", "kind": "analysis", "role": "analysis", "runtime": runtime.GOOS}}})
	command(owner, "plans.approve", p.M{"id": plan.ID})
	repo := command(author, "repositories.create", p.M{"projectId": project.ID, "title": "material-fixture"})
	commit := command(author, "repositories.commit", p.M{"id": repo.ID, "branch": "fixture", "message": "Explicit synthetic package source", "files": p.M{"README.md": "No real IPA, signing or store account"}})
	upload := func(a p.Actor, name, kind, body string) p.Resource {
		t.Helper()
		artifact, err := s.Upload(ctx, a, p.M{"goalId": goal.ID, "repositoryId": repo.ID, "commit": p.S(commit.Data, "lastCommit"), "target": "ios-appstore", "type": kind, "name": name, "mediaType": "application/json"}, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return artifact
	}
	pkg := upload(author, "synthetic.ipa", "package", "Explicit synthetic IPA fixture")
	proof := upload(quality, "synthetic-device.txt", "report", "Synthetic device and publication attestation; no real device or Apple delivery")
	command(quality, "evidence.record", p.M{"artifactId": pkg.ID, "proofArtifactId": proof.ID, "method": "manual", "verificationEnvironment": "simulator", "criteria": []any{"content"}, "statement": "Synthetic simulator fixture only"})
	command(quality, "evidence.record", p.M{"artifactId": pkg.ID, "proofArtifactId": proof.ID, "method": "device", "purpose": "environment", "verificationEnvironment": "ios-device", "statement": "Synthetic device fixture; not physical acceptance"})
	old := upload(author, "original.json", "report", `{"schemaVersion":1,"description":"Original privacy link"}`)
	next := upload(author, "correction.json", "report", `{"schemaVersion":1,"description":"Corrected privacy link"}`)
	release := command(author, "releases.create", p.M{"goalId": goal.ID, "version": "1.0.0", "commit": p.S(commit.Data, "lastCommit"), "artifactIds": []any{pkg.ID}, "storeMetadataArtifactIds": []any{old.ID}})
	command(officer, "releases.approve", p.M{"id": release.ID})
	node := command(owner, "workers.register", p.M{"projectId": project.ID, "title": "Original trusted material publisher", "runtime": runtime.GOOS, "roles": []any{"release"}, "capabilities": []any{"publish"}})
	token, _, err := h.Auth.IssueToken(ctx, owner, p.M{"projectId": project.ID, "workerId": node.ID})
	if err != nil {
		t.Fatal(err)
	}
	var downloads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/artifacts/"+pkg.ID+"/content" {
			downloads.Add(1)
		}
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, token, true)
	if err != nil {
		t.Fatal(err)
	}
	provider, requests, corrected := filepath.Join(root, "provider.py"), filepath.Join(root, "requests.jsonl"), filepath.Join(root, "corrected")
	if err = os.WriteFile(provider, []byte(`import hashlib,json,sys
from pathlib import Path
mode,log,marker=sys.argv[1:]
r=json.load(sys.stdin)
assert r['action']==mode
with open(log,'a') as f: f.write(json.dumps(r,sort_keys=True)+'\n')
if mode=='publish':
    b=Path(r['artifactPath']).read_bytes()
    assert hashlib.sha256(b).hexdigest()==r['artifactDigest']
    state='awaiting_review'
elif mode=='materials':
    assert not r['artifactPath'] and r['sourceBinding']!=r['binding']
    assert r['metadataDigest']!=r['sourceMetadataDigest'] and r['materialRevisionId']
    media=r.get('materialScope')=='screenshots'
    Path(marker).write_text(r['binding']+('\nprocessing' if media else ''))
    state='processing' if media else 'awaiting_review'
elif mode=='continue':
    assert not r['artifactPath'] and r['materialScope']=='screenshots'
    assert Path(marker).read_text()==r['binding']+'\nprocessing'
    Path(marker).write_text(r['binding'])
    state='awaiting_review'
else:
    assert mode=='status' and not r['artifactPath']
    if Path(marker).exists():
        marker_value=Path(marker).read_text()
        assert marker_value in [r['binding'],r['binding']+'\nprocessing']
        state='processing' if marker_value.endswith('\nprocessing') else 'published'
    else: state='rejected'
result={'externalId':'apple:123:synthetic-original','state':state,'upstreamState':'LOCAL_SYNTHETIC_'+state.upper()}
if state=='rejected': result['reason']='Correct privacy policy link'
if state=='processing' and mode=='status': result['continueRequired']=True
print(json.dumps(result))
`), 0600); err != nil {
		t.Fatal(err)
	}
	argv := func(mode string) []string { return []string{python, provider, mode, requests, corrected} }
	runner := &Runner{Client: client, Config: Config{Driver: "native", AllowNative: true, StateDir: filepath.Join(root, "worker-state"), Publishers: map[string][]string{"ios-appstore": argv("publish")}, StatusProviders: map[string][]string{"ios-appstore": argv("status")}, MaterialProviders: map[string][]string{"ios-appstore": argv("materials")}, ContinuationProviders: map[string][]string{"ios-appstore": argv("continue")}}}
	work := func() {
		t.Helper()
		if worked, err := runner.RunOnce(ctx); !worked || err != nil {
			t.Fatal("real HTTP Worker failed", worked, err)
		}
	}
	command(officer, "releases.dispatch", p.M{"id": release.ID, "target": "ios-appstore", "workerId": node.ID, "key": "original"})
	work()
	if downloads.Load() != 1 {
		t.Fatal("original package did not download exactly once", downloads.Load())
	}
	command(officer, "releases.refresh", p.M{"id": release.ID, "target": "ios-appstore", "key": "rejection"})
	work()
	issues, _ := store.List(project.ID, "release_issue", goal.ID)
	if len(issues) != 1 {
		t.Fatal("observed rejection did not create an issue", issues)
	}
	issue := issues[0]
	strategy := "store-text"
	if screenshots {
		strategy = "store-media"
	}
	command(officer, "release_issues.triage", p.M{"id": issue.ID, "expectedRevision": issue.Revision, "expectedBinding": p.S(issue.Data, "binding"), "key": "material-scope", "strategy": strategy, "instructions": "Correct only the independently approved store materials on the original build"})
	revision := command(author, "store_revisions.propose", p.M{"issueId": issue.ID, "metadataArtifactId": next.ID, "key": "proposal"})
	command(officer, "store_revisions.approve", p.M{"id": revision.ID, "expectedBinding": p.S(revision.Data, "binding")})
	dispatch := p.M{"id": revision.ID, "workerId": node.ID, "key": "single-material-operation"}
	task := command(officer, "store_revisions.dispatch", dispatch)
	work()
	current, _ := store.Get(release.ID)
	if screenshots {
		target := p.Obj(p.Items(current.Data["targets"])[0])
		if p.S(target, "status") != "processing" || p.S(target, "materialScope") != "screenshots" {
			t.Fatal("screenshot upload did not enter durable processing", target)
		}
		if worked, err := runner.RunOnce(ctx); worked || err != nil {
			t.Fatal("image wait retained an active task", worked, err)
		}
		command(officer, "releases.refresh", p.M{"id": release.ID, "target": "ios-appstore", "key": "images-ready"})
		work()
		// Query schedules one exact continuation, which the next HTTP lease executes.
		work()
		current, _ = store.Get(release.ID)
	}
	target := p.Obj(p.Items(current.Data["targets"])[0])
	if p.S(target, "status") != "awaiting_review" || p.S(target, "artifactId") != pkg.ID || p.S(current.Data, "binding") != p.S(release.Data, "binding") || downloads.Load() != 1 {
		t.Fatal("text revision changed package, original approval or download count", current, downloads.Load())
	}
	if worked, err := runner.RunOnce(ctx); worked || err != nil {
		t.Fatal("waiting for review retained or duplicated an executable task", worked, err)
	}
	command(officer, "releases.refresh", p.M{"id": release.ID, "target": "ios-appstore", "key": "published"})
	work()
	issue, _ = store.Get(issue.ID)
	if issue.Status == "resolved" {
		t.Fatal("publisher closed issue without independent acceptance")
	}
	evidence := command(quality, "evidence.record", p.M{"artifactId": pkg.ID, "proofArtifactId": proof.ID, "method": "device", "purpose": "publication", "verificationEnvironment": "ios-device", "externalId": "apple:123:synthetic-original", "releaseId": release.ID, "publicationBinding": p.S(revision.Data, "binding"), "statement": "Synthetic independent exact-material publication attestation"})
	command(quality, "releases.verify", p.M{"id": release.ID, "target": "ios-appstore", "evidenceId": evidence.ID})
	current, _ = store.Get(release.ID)
	issue, _ = store.Get(issue.ID)
	if current.Status != "delivered" || issue.Status != "resolved" || downloads.Load() != 1 {
		t.Fatal("independent material acceptance failed", current.Status, issue.Status, downloads.Load())
	}
	if replay := command(officer, "store_revisions.dispatch", dispatch); replay.ID != task.ID {
		t.Fatal("completed dispatch replay created another task")
	}
	if err = s.VerifyIntegrity(ctx); err != nil {
		t.Fatal(err)
	}
	if evidenceRoot != "" {
		if err = s.Backup(ctx, filepath.Join(root, "backup.tar.gz")); err != nil {
			t.Fatal(err)
		}
		facts := p.M{"passed": true, "paidModelCalled": false, "syntheticChannelOnly": true, "syntheticPackageAndDevice": true, "productionDelivered": false, "projectId": project.ID, "goalId": goal.ID, "releaseId": release.ID, "issueId": issue.ID, "revisionId": revision.ID, "taskId": task.ID, "artifactId": pkg.ID, "evidenceId": evidence.ID, "packageDownloads": downloads.Load(), "screenshots": screenshots}
		b, _ := json.MarshalIndent(facts, "", "  ")
		if err = os.WriteFile(filepath.Join(root, "result.json"), append(b, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
