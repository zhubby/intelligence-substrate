package distribution

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func webPackage(t *testing.T, files map[string]string) (string, string, int64) {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tarWriter := tar.NewWriter(gz)
	for name, body := range files {
		if e := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(body)), Typeflag: tar.TypeReg}); e != nil {
			t.Fatal(e)
		}
		if _, e := tarWriter.Write([]byte(body)); e != nil {
			t.Fatal(e)
		}
	}
	if e := tarWriter.Close(); e != nil {
		t.Fatal(e)
	}
	if e := gz.Close(); e != nil {
		t.Fatal(e)
	}
	name := filepath.Join(t.TempDir(), "site.tar.gz")
	if e := os.WriteFile(name, b.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(b.Bytes())
	return name, hex.EncodeToString(digest[:]), int64(b.Len())
}

func TestDistributionActivationRejectsDowngradeAndRenewalKeepsArtifact(t *testing.T) {
	root := t.TempDir()
	store, e := Open(root)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	other, e := Open(root)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	name, digest, size := webPackage(t, map[string]string{"index.html": "published"})
	r := Release{ID: "first", RequestHash: "first", App: "portal", Target: "web", Channel: "stable", Version: "2.0.0", Digest: digest, Size: size, Filename: "portal.tar.gz"}
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for _, writer := range []*Store{store, other} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			_, err := s.Publish(context.Background(), r, name, "https://download.example", key)
			errors <- err
		}(writer)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	r.ID, r.RequestHash, r.Version = "old-version", "old-version", "1.9.9"
	if _, e = store.Publish(context.Background(), r, name, "https://download.example", key); e == nil {
		t.Fatal("late old publication rolled back a newer live version")
	}
	if e = store.Renew(context.Background(), "portal", "web", "stable", "https://download.example", key); e != nil {
		t.Fatal(e)
	}
	current, e := store.Current(context.Background(), "portal", "web", "stable")
	if e != nil || current.ID != "first" || current.Version != "2.0.0" || current.Sequence != 2 {
		t.Fatalf("renewal changed package or duplicate activation occurred: %+v %v", current, e)
	}
	server := httptest.NewServer(store.Handler())
	defer server.Close()
	response, e := http.Get(server.URL + "/updates/portal/web/stable.json")
	if e != nil {
		t.Fatal(e)
	}
	var envelope SignedFeed
	e = json.NewDecoder(response.Body).Decode(&envelope)
	response.Body.Close()
	if e != nil {
		t.Fatal(e)
	}
	if feed, err := VerifyFeed(envelope, pub, "portal", "web", "stable", 2, time.Now()); err != nil || feed.Digest != digest {
		t.Fatalf("renewed signed index invalid: %+v %v", feed, err)
	}
}

func TestDesktopDistributionRequiresSigningAttestationAndSafeFilename(t *testing.T) {
	store, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	name, digest, size := webPackage(t, map[string]string{"index.html": "fixture"})
	r := Release{ID: "unsafe-desktop", RequestHash: "binding", App: "desktop", Target: "windows", Channel: "stable", Version: "1.0.0", Digest: digest, Size: size, Filename: "setup.exe"}
	if _, e = store.Publish(context.Background(), r, name, "https://download.example", key); e == nil {
		t.Fatal("desktop package published without OS signing attestation")
	}
	r.Target = "web"
	r.Filename = `setup"; alternate="payload.tar.gz`
	if _, e = store.Publish(context.Background(), r, name, "https://download.example", key); e == nil {
		t.Fatal("unsafe Content-Disposition filename accepted")
	}
}

func TestWebApplicationsCannotShareOnePublicServiceRoot(t *testing.T) {
	store, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	name, digest, size := webPackage(t, map[string]string{"index.html": "app-one"})
	r := Release{ID: "app-one-release", RequestHash: "approved-one", App: "app-one", Target: "web", Channel: "stable", Version: "1.0.0", Digest: digest, Size: size, Filename: "site.tar.gz"}
	if _, e = store.Publish(context.Background(), r, name, "https://app.example", key); e != nil {
		t.Fatal(e)
	}
	r.ID, r.RequestHash, r.App = "app-two-release", "approved-two", "app-two"
	if _, e = store.Publish(context.Background(), r, name, "https://app.example", key); e == nil {
		t.Fatal("different applications share script and browser storage origin")
	}
}

func TestWebArchiveCannotCreateWindowsAlternateDataStream(t *testing.T) {
	store, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	name, digest, size := webPackage(t, map[string]string{"index.html": "safe", "index.html:script": "hidden stream"})
	r := Release{ID: "stream", RequestHash: "approved", App: "portal", Target: "web", Channel: "stable", Version: "1.0.0", Digest: digest, Size: size, Filename: "site.tar.gz"}
	if _, e = store.Publish(context.Background(), r, name, "https://app.example", key); e == nil {
		t.Fatal("Windows alternate stream accepted as a regular Web file")
	}
}

func TestImmutableWebPublicationAndSignedFeed(t *testing.T) {
	store, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	name, digest, size := webPackage(t, map[string]string{"index.html": "<h1>Customer feedback</h1>", "assets/app.js": "console.log('feedback')"})
	release := Release{ID: "operation-one", RequestHash: "approved-request-one", App: "feedback", Target: "web", Channel: "stable", Version: "1.0.0", Digest: digest, Size: size, Filename: "web.tar.gz", ProjectID: "project_one", GoalID: "goal_one", ReleaseID: "release_one", ArtifactID: "artifact_one", Commit: "1111111111111111111111111111111111111111", Binding: "approved-binding"}
	first, e := store.Publish(context.Background(), release, name, "https://downloads.example", private)
	if e != nil {
		t.Fatal(e)
	}
	if first.Sequence != 1 {
		t.Fatalf("first activation sequence: %d", first.Sequence)
	}
	second, e := store.Publish(context.Background(), release, name, "https://downloads.example", private)
	if e != nil || second.Sequence != 1 {
		t.Fatalf("replay activated twice: %+v %v", second, e)
	}
	conflicting := release
	conflicting.Binding = "different-binding"
	if _, e = store.Publish(context.Background(), conflicting, name, "https://downloads.example", private); e == nil {
		t.Fatal("operation key reused for another approved binding")
	}
	server := httptest.NewServer(store.Handler())
	defer server.Close()
	response, e := http.Get(server.URL + "/apps/feedback/web/")
	if e != nil {
		t.Fatal(e)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || string(body) != "<h1>Customer feedback</h1>" {
		t.Fatalf("actual Web deployment missing: %d %s", response.StatusCode, body)
	}
	response, e = http.Get(server.URL + "/updates/feedback/web/stable.json")
	if e != nil {
		t.Fatal(e)
	}
	var envelope SignedFeed
	if e = json.NewDecoder(response.Body).Decode(&envelope); e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	feed, e := VerifyFeed(envelope, public, "feedback", "web", "stable", 1, time.Now())
	if e != nil || feed.Digest != digest || feed.Size != size || feed.ReleaseID != "release_one" {
		t.Fatalf("signed provenance lost: %+v %v", feed, e)
	}
	if _, e = VerifyFeed(envelope, public, "feedback", "macos", "stable", 1, time.Now()); e == nil {
		t.Fatal("cross-platform feed accepted")
	}
	if _, e = VerifyFeed(envelope, public, "feedback", "web", "stable", 2, time.Now()); e == nil {
		t.Fatal("old sequence accepted")
	}
	if _, e = VerifyFeed(envelope, public, "feedback", "web", "stable", 1, time.Now().Add(8*24*time.Hour)); e == nil {
		t.Fatal("expired update index accepted")
	}
	envelope.Signature = "modified"
	if _, e = VerifyFeed(envelope, public, "feedback", "web", "stable", 0, time.Now()); e == nil {
		t.Fatal("tampered update accepted")
	}
	for _, path := range []string{"/distribution.db", "/apps/feedback/web/assets/", "/apps/feedback/web/../../distribution.db"} {
		response, e = http.Get(server.URL + path)
		if e != nil {
			t.Fatal(e)
		}
		response.Body.Close()
		if response.StatusCode == 200 {
			t.Fatalf("private file or directory listing exposed: %s", path)
		}
	}
}

func TestDistributionRejectsUntrustedArchiveAndDoesNotReplaceCurrent(t *testing.T) {
	store, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	name, digest, size := webPackage(t, map[string]string{"index.html": "old"})
	release := Release{ID: "one", RequestHash: "one", App: "portal", Target: "web", Channel: "stable", Version: "1.0.0", Digest: digest, Size: size, Filename: "site.tar.gz"}
	if _, e = store.Publish(context.Background(), release, name, "https://download.example", key); e != nil {
		t.Fatal(e)
	}
	badName, badDigest, badSize := webPackage(t, map[string]string{"../../escape": "unsafe", "index.html": "bad"})
	release.ID, release.RequestHash, release.Version = "two", "two", "1.0.1"
	release.Digest, release.Size = badDigest, badSize
	if _, e = store.Publish(context.Background(), release, badName, "https://download.example", key); e == nil {
		t.Fatal("archive escaped publication directory")
	}
	current, e := store.Current(context.Background(), "portal", "web", "stable")
	if e != nil || current.Version != "1.0.0" || current.Sequence != 1 {
		t.Fatalf("failed upload replaced live version: %+v %v", current, e)
	}
	release.Digest = digest
	if _, e = store.Publish(context.Background(), release, badName, "https://download.example", key); e == nil {
		t.Fatal("changed artifact digest accepted")
	}
}
