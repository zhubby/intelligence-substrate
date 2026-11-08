package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestStatusQueryNeverInvokesLegacyMutatingPublisher(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "legacy-executed")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "package") }))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token", true)
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{Client: client, Config: Config{Publishers: map[string][]string{"web": {"/bin/sh", "-c", "touch '" + marker + "'; echo '{\"externalId\":\"deploy-one\",\"state\":\"published\"}'"}}}}
	run := p.Resource{ID: "run_query", ProjectID: "project_test", GoalID: "goal_test", Data: p.M{"task": p.M{"kind": "publish_status", "target": "web", "artifactId": "artifact_test", "externalId": "deploy-one"}}}
	_, exit, err := runner.publish(context.Background(), run, io.Discard)
	if err == nil || exit == 0 {
		t.Fatal("status query used the mutating publisher as an implicit fallback")
	}
	if _, e := os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("legacy publisher executed during read-only status query")
	}
}

func TestRollbackRequiresSeparateProviderAndPassesExactActivation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "rollback must not download a package", 500)
	}))
	defer server.Close()
	client, e := NewClient(server.URL, "test-token", true)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "legacy")
	record := filepath.Join(dir, "request.json")
	runner := Runner{Client: client, Config: Config{Publishers: map[string][]string{"web": {"/bin/sh", "-c", "touch '" + marker + "'"}}}}
	run := p.Resource{ID: "run_recovery", ProjectID: "project_test", GoalID: "goal_test", Data: p.M{"task": p.M{"kind": "runtime_rollback", "target": "web", "expectedActive": "runtime:feedback:new", "rollbackTo": "runtime:feedback:old", "expectedExternalId": "runtime:feedback:approved", "artifactDigest": strings.Repeat("a", 64), "artifactSize": 12}}}
	if _, exit, err := runner.publish(context.Background(), run, io.Discard); err == nil || exit == 0 {
		t.Fatal("rollback fell back to a normal publisher")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("legacy publisher executed a rollback")
	}
	runner.Config.RollbackProviders = map[string][]string{"web": {"/bin/sh", "-c", "cat > '" + record + "'; printf '%s' '{\"externalId\":\"runtime:feedback:approved\",\"state\":\"published\"}'"}}
	if _, exit, err := runner.publish(context.Background(), run, io.Discard); err != nil || exit != 0 {
		t.Fatal(exit, err)
	}
	var request p.M
	body, _ := os.ReadFile(record)
	if e = json.Unmarshal(body, &request); e != nil {
		t.Fatal(e)
	}
	if p.S(request, "action") != "rollback" || p.S(request, "expectedActive") != "runtime:feedback:new" || p.S(request, "rollbackTo") != "runtime:feedback:old" || p.S(request, "artifactPath") != "" || calls != 0 {
		t.Fatal("rollback protocol lost exact approval or fetched a new package")
	}
}

func TestContinuationRequiresSeparateProviderAndNoPackageDownload(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "must not download", 500) }))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token", true)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "legacy")
	record := filepath.Join(dir, "request.json")
	runner := Runner{Client: client, Config: Config{Publishers: map[string][]string{"ios-appstore": {"/bin/sh", "-c", "touch '" + marker + "'"}}}}
	run := p.Resource{ID: "run_continue", ProjectID: "project_test", GoalID: "goal_test", Data: p.M{"task": p.M{"kind": "publish_continue", "target": "ios-appstore", "externalId": "apple:123:upload", "operationKey": strings.Repeat("a", 64)}}}
	p.Obj(run.Data["task"])["metadataDigest"] = strings.Repeat("b", 64)
	if _, exit, e := runner.publish(context.Background(), run, io.Discard); e == nil || exit == 0 {
		t.Fatal("continuation fell back to ordinary publisher")
	}
	if _, e := os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("legacy publisher executed continuation")
	}
	configJSON, _ := json.Marshal(map[string]any{"continuationProviders": map[string][]string{"ios-appstore": {"/bin/sh", "-c", "cat > '" + record + "'; printf '%s' '{\"externalId\":\"apple:123:upload\",\"state\":\"awaiting_review\"}'"}}})
	if err = json.Unmarshal(configJSON, &runner.Config); err != nil {
		t.Fatal(err)
	}
	if _, exit, e := runner.publish(context.Background(), run, io.Discard); e != nil || exit != 0 {
		t.Fatal(exit, e)
	}
	body, _ := os.ReadFile(record)
	var request p.M
	if err = json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || p.S(request, "metadataDigest") != strings.Repeat("b", 64) || p.S(request, "action") != "continue" || p.S(request, "artifactPath") != "" || p.S(request, "externalId") != "apple:123:upload" {
		t.Fatalf("lost exact continuation binding: %+v calls=%d", request, calls)
	}
}

func TestMaterialRevisionExplicitProviderExactBindingAndNoPackageDownload(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "must not download original IPA", 500)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture-token", true)
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "material-request.json")
	runner := Runner{Client: client, Config: Config{Publishers: map[string][]string{"ios-appstore": {"/usr/bin/false"}}, StatusProviders: map[string][]string{"ios-appstore": {"/usr/bin/false"}}}}
	task := p.M{"kind": "publish_materials", "target": "ios-appstore", "externalId": "apple:123:upload1", "binding": strings.Repeat("a", 64), "publicationBinding": strings.Repeat("b", 64), "materialRevisionId": "store_revision_one", "metadataDigest": strings.Repeat("c", 64), "sourceOperationKey": strings.Repeat("d", 64), "sourcePublicationBinding": strings.Repeat("e", 64), "sourceMetadataDigest": strings.Repeat("f", 64)}
	run := p.Resource{ID: "run_material", ProjectID: "project_fixture", GoalID: "goal_fixture", Data: p.M{"task": task}}
	if _, exit, err := runner.publish(context.Background(), run, io.Discard); err == nil || exit == 0 {
		t.Fatal("material revision fell back to normal/status provider")
	}
	runner.Config.MaterialProviders = map[string][]string{"ios-appstore": {"/bin/sh", "-c", "cat > '" + record + "'; printf '%s' '{\"externalId\":\"apple:123:upload1\",\"state\":\"awaiting_review\"}'"}}
	if _, exit, err := runner.publish(context.Background(), run, io.Discard); err != nil || exit != 0 {
		t.Fatal(exit, err)
	}
	body, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var request p.M
	if json.Unmarshal(body, &request) != nil {
		t.Fatal("invalid request")
	}
	if calls != 0 || p.S(request, "artifactPath") != "" || p.S(request, "action") != "materials" || p.S(request, "binding") != p.S(task, "publicationBinding") || p.S(request, "sourceBinding") != p.S(task, "sourcePublicationBinding") || p.S(request, "metadataDigest") != p.S(task, "metadataDigest") || p.S(request, "materialRevisionId") != p.S(task, "materialRevisionId") {
		t.Fatal("material protocol lost new approval/source or downloaded package", request, calls)
	}
}
