package updater

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/intelligence-substrate/platform/internal/distribution"
)

// Only native trust/installation is substituted: fixtures are not signed PKGs.
// TLS, signed indexes, file bytes and cross-process SQLite state remain real.
type nativeFixture struct {
	mu        sync.Mutex
	version   string
	verifyErr error
	install   func() error
}

func (n *nativeFixture) Verify(context.Context, Config, string, distribution.Feed) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.verifyErr
}
func (n *nativeFixture) Installed(context.Context, Config) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.version, nil
}
func (n *nativeFixture) Install(context.Context, Config, string) error { return n.install() }

type updateFixture struct {
	t                *testing.T
	server           *httptest.Server
	config           Config
	key              ed25519.PrivateKey
	now              time.Time
	native           *nativeFixture
	mu               sync.Mutex
	feed             distribution.Feed
	body             []byte
	redirect         string
	downloadRedirect string
}

func fixture(t *testing.T) *updateFixture {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &updateFixture{t: t, key: key, now: time.Now().UTC().Truncate(time.Second), native: &nativeFixture{version: "1.0.0"}, body: []byte("exact approved package bytes")}
	digest := sha256.Sum256(f.body)
	f.feed = distribution.Feed{SchemaVersion: 1, Release: distribution.Release{
		ID: "approved-operation", App: "feedback", Target: "macos", Channel: "stable", Version: "2.0.0",
		Digest: hex.EncodeToString(digest[:]), Size: int64(len(f.body)), Filename: "feedback.pkg", Sequence: 4,
		Signer: "ABCDE12345", ProjectID: "project_one", GoalID: "goal_one", ReleaseID: "release_one", ArtifactID: "artifact_one",
		Commit: strings.Repeat("a", 40), Binding: strings.Repeat("b", 64), PublishedAt: f.now.Format(time.RFC3339),
	}, IssuedAt: f.now.Format(time.RFC3339), ExpiresAt: f.now.Add(24 * time.Hour).Format(time.RFC3339)}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method != "GET" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected request: %s", r.Method)
			w.WriteHeader(400)
			return
		}
		switch r.URL.Path {
		case "/updates/feedback/macos/stable.json":
			if f.redirect != "" {
				http.Redirect(w, r, f.redirect, 302)
				return
			}
			_ = json.NewEncoder(w).Encode(signedFixture(f.feed, f.key))
		case "/downloads/feedback/macos/approved-operation/feedback.pkg":
			if f.downloadRedirect != "" {
				http.Redirect(w, r, f.downloadRedirect, 302)
				return
			}
			_, _ = w.Write(f.body)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.server.Close)
	f.feed.URL = f.server.URL + "/downloads/feedback/macos/approved-operation/feedback.pkg"
	f.config = Config{StateDir: filepath.Join(t.TempDir(), "state"), Origin: f.server.URL, App: "feedback", Target: "macos", Channel: "stable", PublicKey: base64.StdEncoding.EncodeToString(pub), ExpectedSigner: "ABCDE12345", PackageID: "com.company.feedback"}
	return f
}
func signedFixture(feed distribution.Feed, key ed25519.PrivateKey) distribution.SignedFeed {
	b, _ := json.Marshal(feed)
	return distribution.SignedFeed{KeyID: distribution.KeyID(key.Public().(ed25519.PublicKey)), Payload: base64.StdEncoding.EncodeToString(b), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, append([]byte("intelligence-substrate-update-v1\n"), b...)))}
}
func (f *updateFixture) open() *Client {
	f.t.Helper()
	c, err := Open(f.config)
	if err != nil {
		f.t.Fatal(err)
	}
	c.http = f.server.Client()
	c.native = f.native
	c.now = func() time.Time { return f.now }
	f.t.Cleanup(func() { _ = c.Close() })
	return c
}
func (f *updateFixture) change(fn func(*distribution.Feed)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.feed)
}

func TestUpdaterRemembersSequenceAndRejectsSignedRollbackAfterRestart(t *testing.T) {
	f := fixture(t)
	c := f.open()
	ctx := context.Background()
	s, err := c.Check(ctx)
	if err != nil || s.State != "available" || s.MinimumSequence != 4 {
		t.Fatalf("first check: %+v %v", s, err)
	}
	_ = c.Close()
	c = f.open()
	f.change(func(feed *distribution.Feed) { feed.Sequence = 3 })
	if _, err = c.Check(ctx); err == nil {
		t.Fatal("older signed index replaced persistent floor")
	}
	f.change(func(feed *distribution.Feed) {
		feed.Sequence = 4
		feed.ExpiresAt = f.now.Add(48 * time.Hour).Format(time.RFC3339)
	})
	if _, err = c.Check(ctx); err == nil {
		t.Fatal("same sequence accepted a different signed payload")
	}
	f.change(func(feed *distribution.Feed) { feed.Sequence = 5 })
	if s, err = c.Check(ctx); err != nil || s.MinimumSequence != 5 {
		t.Fatalf("legitimate renewal failed: %+v %v", s, err)
	}
	f.change(func(feed *distribution.Feed) { feed.Sequence = 6; feed.Version = "1.9.0" })
	if _, err = c.Check(ctx); err == nil {
		t.Fatal("higher sequence downgraded package version")
	}
	f.change(func(feed *distribution.Feed) { feed.Version = "2.0.0"; feed.Digest = strings.Repeat("c", 64) })
	if _, err = c.Check(ctx); err == nil {
		t.Fatal("same version silently changed approved bytes")
	}
	s, err = c.Status(ctx)
	if err != nil || s.MinimumSequence != 5 || s.Feed.Version != "2.0.0" {
		t.Fatalf("rejected data poisoned state: %+v %v", s, err)
	}
}

func TestUpdaterRejectsWrongProvenanceAndUnsafeFeedBeforeStaging(t *testing.T) {
	tests := map[string]func(*updateFixture){
		"wrong-key":        func(f *updateFixture) { _, f.key, _ = ed25519.GenerateKey(rand.Reader) },
		"cross-app":        func(f *updateFixture) { f.feed.App = "other" },
		"cross-target":     func(f *updateFixture) { f.feed.Target = "windows" },
		"wrong-signer":     func(f *updateFixture) { f.feed.Signer = "OTHER12345" },
		"foreign-download": func(f *updateFixture) { f.feed.URL = "https://foreign.example/installer.pkg" },
		"url-query":        func(f *updateFixture) { f.feed.URL += "?token=untrusted" },
		"expired":          func(f *updateFixture) { f.feed.ExpiresAt = f.now.Add(-time.Second).Format(time.RFC3339) },
		"reversed-lifetime": func(f *updateFixture) {
			f.feed.IssuedAt = f.now.Add(3 * time.Minute).Format(time.RFC3339)
			f.feed.ExpiresAt = f.now.Add(time.Minute).Format(time.RFC3339)
		},
		"invalid-version": func(f *updateFixture) { f.feed.Version = "2.invalid" },
		"no-commit":       func(f *updateFixture) { f.feed.Commit = "" },
		"oversized":       func(f *updateFixture) { f.feed.Size = 513 << 20 },
		"path":            func(f *updateFixture) { f.feed.Filename = "../../installer.pkg" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			f := fixture(t)
			mutate(f)
			c := f.open()
			if _, err := c.Check(context.Background()); err == nil {
				t.Fatal("unsafe index accepted")
			}
			s, err := c.Status(context.Background())
			if err != nil || s.MinimumSequence != 0 || s.PackagePath != "" {
				t.Fatalf("untrusted feed changed durable state: %+v %v", s, err)
			}
		})
	}
}

func TestUpdaterDoesNotFollowFeedOrPackageRedirects(t *testing.T) {
	f := fixture(t)
	foreignCalls := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignCalls++; w.WriteHeader(200) }))
	defer other.Close()
	c := f.open()
	ctx := context.Background()
	f.redirect = other.URL
	if _, err := c.Check(ctx); err == nil {
		t.Fatal("feed redirect accepted")
	}
	f.mu.Lock()
	f.redirect = ""
	f.mu.Unlock()
	if _, err := c.Check(ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.downloadRedirect = other.URL
	f.mu.Unlock()
	if _, err := c.Stage(ctx); err == nil {
		t.Fatal("package redirect accepted")
	}
	if foreignCalls != 0 {
		t.Fatal("contacted unapproved redirect destination")
	}
}

func TestUpdaterStagesExactBytesAndRejectsTamperWithoutInstaller(t *testing.T) {
	f := fixture(t)
	c := f.open()
	ctx := context.Background()
	if _, err := c.Check(ctx); err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(f.body)
	f.mu.Lock()
	f.body = []byte("corrupt")
	f.mu.Unlock()
	if _, err := c.Stage(ctx); err == nil {
		t.Fatal("corrupt package accepted")
	}
	f.mu.Lock()
	f.body = original
	f.mu.Unlock()
	f.native.verifyErr = errors.New("untrusted native certificate")
	if result, err := c.Stage(ctx); err == nil || result.PackagePath != "" {
		t.Fatal("native verification failure ignored or returned uncommitted package path")
	}
	f.native.verifyErr = nil
	s, err := c.Stage(ctx)
	if err != nil || s.State != "staged" {
		t.Fatalf("stage failed: %+v %v", s, err)
	}
	body, err := os.ReadFile(s.PackagePath)
	if err != nil || !bytes.Equal(body, original) {
		t.Fatal("staged bytes differ from signed artifact")
	}
	if err = os.WriteFile(s.PackagePath, []byte("changed after staging"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Install(ctx, s.Feed.Digest); err == nil {
		t.Fatal("tampered staged package reached installer")
	}
}

func TestUpdaterBindsTrustProfileToExistingState(t *testing.T) {
	f := fixture(t)
	c := f.open()
	if _, err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	changed := f.config
	changed.PackageID = "com.company.other"
	if other, err := Open(changed); err == nil {
		other.Close()
		t.Fatal("trust/destination drift silently reused state")
	}
	bad := f.config
	bad.StateDir = filepath.Join(t.TempDir(), "must-not-exist")
	bad.Origin = "http://download.example"
	if other, err := Open(bad); err == nil {
		other.Close()
		t.Fatal("plaintext distribution accepted")
	}
	if _, err := os.Stat(bad.StateDir); !os.IsNotExist(err) {
		t.Fatal("invalid profile created state")
	}
}

func TestWindowsProfileCannotIdentifyInstalledAppByNameAlone(t *testing.T) {
	f := fixture(t)
	config := f.config
	config.Target, config.ExpectedSigner = "windows", strings.Repeat("A", 40)
	if client, err := Open(config); err == nil {
		client.Close()
		t.Fatal("Windows profile accepted no fixed publisher identity")
	}
}

func TestUpdaterRejectsSharedWritableStateDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows state privacy is enforced by native ACL configuration")
	}
	f := fixture(t)
	if err := os.Mkdir(f.config.StateDir, 0755); err != nil {
		t.Fatal(err)
	}
	if client, err := Open(f.config); err == nil {
		client.Close()
		t.Fatal("state directory was accessible to other users")
	}
}

func TestAdministratorPrivateCAUsesActualTLSVerification(t *testing.T) {
	f := fixture(t)
	f.config.CAFile = filepath.Join(t.TempDir(), "enterprise-ca.pem")
	if err := os.WriteFile(f.config.CAFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Open(f.config)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.native = f.native
	if s, err := c.Check(context.Background()); err != nil || s.MinimumSequence != 4 {
		t.Fatalf("administrator CA did not validate actual HTTPS: %+v %v", s, err)
	}
}
