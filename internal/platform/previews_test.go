package platform

import (
	"archive/tar"
	"bytes"
	"context"
	"sort"
	"testing"
	"time"
)

func previewArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		body := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Typeflag: tar.TypeReg, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func previewFixture(t *testing.T) (*Service, Actor, Resource, Resource, Resource, M) {
	t.Helper()
	s, a, project := fixture(t)
	g := goal(t, s, a, project)
	must(t, s, a, "goals.approve", M{"id": g.ID})
	repo := must(t, s, a, "repositories.create", M{"projectId": project.ID, "title": "Preview application"})
	repo = must(t, s, a, "repositories.commit", M{"id": repo.ID, "branch": "preview", "message": "Add relative static application", "files": M{"index.html": "<h1>Preview</h1>"}})
	artifact, err := s.Upload(context.Background(), a, M{"goalId": g.ID, "repositoryId": repo.ID, "commit": S(repo.Data, "lastCommit"), "target": "web", "type": "package", "name": "application.tar"}, bytes.NewReader(previewArchive(t, map[string]string{"index.html": "<script type=module src='./app.js'></script>", "app.js": "document.body.dataset.ready='yes'", "data/items.json": "[]"})))
	if err != nil {
		t.Fatal(err)
	}
	in := M{"artifactId": artifact.ID, "expectedRequirementRevision": g.Revision, "key": "initial-preview", "reason": "Review the current implementation", "title": "Application preview", "ttlMinutes": 60}
	return s, a, project, g, artifact, in
}

func TestDevelopmentPreviewBindsImmutableBuildWithoutPassingQuality(t *testing.T) {
	s, a, p, g, artifact, in := previewFixture(t)
	r := must(t, s, a, "previews.create", in)
	if r.Kind != "preview" || r.Status != "active" || r.ProjectID != p.ID || r.GoalID != g.ID || revision(r) != g.Revision {
		t.Fatal("incorrect preview scope", r)
	}
	for _, key := range []string{"commit", "repositoryId", "digest", "size"} {
		if Hash(r.Data[key]) != Hash(artifact.Data[key]) {
			t.Fatalf("preview lost %s binding", key)
		}
	}
	if S(r.Data, "artifactId") != artifact.ID || N(r.Data, "fileCount") != 3 {
		t.Fatal("preview did not record exact package", r.Data)
	}
	deadline, err := time.Parse(time.RFC3339Nano, S(r.Data, "expiresAt"))
	if err != nil || time.Until(deadline) < 59*time.Minute || time.Until(deadline) > 61*time.Minute {
		t.Fatal("invalid bounded deadline", deadline, err)
	}
	replay := must(t, s, a, "previews.create", in)
	if replay.ID != r.ID {
		t.Fatal("create replay duplicated preview")
	}
	for _, kind := range []string{"evidence", "release", "deployment"} {
		items, err := s.Store.List(p.ID, kind, g.ID)
		if err != nil || len(items) != 0 {
			t.Fatal("preview passed delivery gate", kind, items, err)
		}
	}
	changed := copyM(in)
	changed["title"] = "Different preview"
	if _, err = s.Command(context.Background(), a, "previews.create", changed); err == nil {
		t.Fatal("key reused for different input")
	}
}

func TestDevelopmentPreviewStopAndReplayNeverReactivate(t *testing.T) {
	s, a, _, _, _, in := previewFixture(t)
	r := must(t, s, a, "previews.create", in)
	stop := M{"id": r.ID, "expectedRevision": r.Revision, "key": "stop-preview", "reason": "Review is complete"}
	x := must(t, s, a, "previews.stop", stop)
	if x.Status != "stopped" || x.Revision != r.Revision+1 {
		t.Fatal("stop did not change current state", x)
	}
	for _, command := range []struct {
		name  string
		input M
	}{{"previews.stop", stop}, {"previews.create", in}} {
		replay := must(t, s, a, command.name, command.input)
		if replay.ID != r.ID || replay.Status != "stopped" || replay.Revision != x.Revision {
			t.Fatal("replay revived historical preview", replay)
		}
	}
	stop["key"] = "stale-stop"
	if _, err := s.Command(context.Background(), a, "previews.stop", stop); err == nil {
		t.Fatal("stale revision accepted")
	}
}

func TestDevelopmentPreviewRequirementChangePreservesStoppedHistory(t *testing.T) {
	s, a, _, g, _, in := previewFixture(t)
	r := must(t, s, a, "previews.create", in)
	must(t, s, a, "previews.stop", M{"id": r.ID, "expectedRevision": r.Revision, "key": "stop-preview", "reason": "Review complete"})
	stopped, err := s.GetResource(context.Background(), a, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	must(t, s, a, "goals.revise", M{"id": g.ID, "reason": "Extend requirements", "outcome": "Classify customer requests"})
	current, err := s.GetResource(context.Background(), a, r.ID)
	if err != nil || Hash(current) != Hash(stopped) {
		t.Fatal("requirement change rewrote stopped preview history", current, err)
	}
}

func TestDevelopmentPreviewRejectsWrongSourceScopeAndAuthority(t *testing.T) {
	for _, scenario := range []string{"revision", "report", "other-target", "corrupt", "unapproved", "viewer", "outsider", "worker", "ttl", "fractional-ttl", "missing-key", "missing-reason"} {
		t.Run(scenario, func(t *testing.T) {
			s, a, p, g, artifact, in := previewFixture(t)
			switch scenario {
			case "revision":
				in["expectedRequirementRevision"] = 2
			case "report", "other-target":
				artifact.Data["type"] = "report"
				if scenario == "other-target" {
					artifact.Data["type"], artifact.Data["target"] = "package", "windows"
				}
				if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(artifact) }); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := s.Store.Update(context.Background(), func(tx *Tx) error { artifact.Data["digest"] = Hash("wrong content"); return tx.Put(artifact) }); err != nil {
					t.Fatal(err)
				}
			case "unapproved":
				if err := s.Store.Update(context.Background(), func(tx *Tx) error { g.Data["approvedRevision"] = 0; return tx.Put(g) }); err != nil {
					t.Fatal(err)
				}
			case "viewer":
				a = Actor{ID: "viewer", Kind: "agent", ProjectID: p.ID, Roles: []string{"viewer"}}
			case "outsider":
				a = Actor{ID: "outsider", Kind: "human"}
			case "worker":
				a = Actor{ID: "worker:x", Kind: "worker", ProjectID: p.ID, Roles: []string{"developer"}}
			case "ttl":
				in["ttlMinutes"] = 241
			case "fractional-ttl":
				in["ttlMinutes"] = 5.5
			case "missing-key":
				delete(in, "key")
			case "missing-reason":
				delete(in, "reason")
			}
			if _, err := s.Command(context.Background(), a, "previews.create", in); err == nil {
				t.Fatal("invalid preview accepted", scenario)
			}
			items, err := s.Store.List(p.ID, "preview", "")
			if err != nil || len(items) != 0 {
				t.Fatal("rejected creation mutated previews", items, err)
			}
		})
	}
}

func TestDevelopmentPreviewRequirementChangeAndExpiryAreCurrentViews(t *testing.T) {
	for _, change := range []string{"baseline", "expired"} {
		t.Run(change, func(t *testing.T) {
			s, a, project, g, _, in := previewFixture(t)
			r := must(t, s, a, "previews.create", in)
			expected := "stale"
			if change == "baseline" {
				must(t, s, a, "goals.revise", M{"id": g.ID, "reason": "Add new acceptance condition", "outcome": "Customers submit and classify requests"})
			} else {
				expected = "expired"
				r.Data["expiresAt"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
				if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(r) }); err != nil {
					t.Fatal(err)
				}
			}
			read, err := s.GetResource(context.Background(), a, r.ID)
			if err != nil || read.Status != expected {
				t.Fatal("resource read hid current state", read.Status, err)
			}
			replay := must(t, s, a, "previews.create", in)
			if replay.Status != expected {
				t.Fatal("old creation revived preview", replay.Status)
			}
			items, err := s.Workspace(context.Background(), a, project.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if item.ID == r.ID && item.Status != expected {
					t.Fatal("workspace state differs")
				}
			}
		})
	}
}

func TestDevelopmentPreviewQuotaCountsOnlyActiveUnexpiredPreviews(t *testing.T) {
	s, a, _, _, _, in := previewFixture(t)
	var first Resource
	for i := 0; i < 10; i++ {
		in["key"] = NewID("preview")
		r := must(t, s, a, "previews.create", in)
		if i == 0 {
			first = r
		}
	}
	in["key"] = "over-quota"
	if _, err := s.Command(context.Background(), a, "previews.create", in); err == nil {
		t.Fatal("unbounded active previews")
	}
	must(t, s, a, "previews.stop", M{"id": first.ID, "expectedRevision": first.Revision, "key": "release-slot", "reason": "Release the unused slot"})
	must(t, s, a, "previews.create", in)
}

func TestDevelopmentPreviewRejectsIncompleteWorkerOutput(t *testing.T) {
	s, a, _, _, artifact, in := previewFixture(t)
	artifact.Data["runId"] = "run-not-completed"
	if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(artifact) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Command(context.Background(), a, "previews.create", in); err == nil {
		t.Fatal("unconfirmed worker output became previewable")
	}
}

func TestDevelopmentPreviewAuditFailureDoesNotCreateUnauditedResource(t *testing.T) {
	s, a, project, _, _, in := previewFixture(t)
	if _, err := s.Store.DB.Exec(`CREATE TRIGGER preview_audit_failure BEFORE INSERT ON audit WHEN json_extract(NEW.body,'$.action')='previews.create' BEGIN SELECT RAISE(FAIL,'injected preview audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Command(context.Background(), a, "previews.create", in); err == nil {
		t.Fatal("audit fault did not trigger")
	}
	items, err := s.Store.List(project.ID, "preview", "")
	if err != nil || len(items) != 0 {
		t.Fatal("unaudited preview survived", items, err)
	}
	if _, err = s.Store.DB.Exec(`DROP TRIGGER preview_audit_failure`); err != nil {
		t.Fatal(err)
	}
	must(t, s, a, "previews.create", in)
}
