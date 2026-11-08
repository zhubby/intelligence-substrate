package publisher

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/intelligence-substrate/platform/internal/distribution"
	"golang.org/x/oauth2"
)

func publisherFixture(t *testing.T) (Config, Request) {
	t.Helper()
	root := t.TempDir()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	keyPath := filepath.Join(root, "update-key")
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(key.Seed())), 0600); err != nil {
		t.Fatal(err)
	}
	var content bytes.Buffer
	gz := gzip.NewWriter(&content)
	tw := tar.NewWriter(gz)
	body := "<h1>Delivered feedback</h1>"
	if err := tw.WriteHeader(&tar.Header{Name: "index.html", Mode: 0644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(root, "approved.tar.gz")
	if err := os.WriteFile(artifact, content.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content.Bytes())
	profile := Profile{ProjectID: "project_one", GoalID: "goal_one", Target: "web", Driver: "hosted", AppID: "feedback", Channel: "stable", DistributionRoot: filepath.Join(root, "distribution"), PublicURL: "https://apps.example", SigningKeyFile: keyPath}
	config := Config{StateDir: filepath.Join(root, "state"), Profiles: []Profile{profile}}
	request := Request{ProtocolVersion: "1", Action: "publish", OperationKey: strings.Repeat("a", 64), ProjectID: profile.ProjectID, GoalID: profile.GoalID, ReleaseID: "release_one", Target: "web", ArtifactID: "artifact_one", ArtifactPath: artifact, ArtifactDigest: hex.EncodeToString(digest[:]), ArtifactSize: int64(content.Len()), Version: "1.0.0", Commit: strings.Repeat("1", 40), Binding: strings.Repeat("b", 64)}
	return config, request
}

func TestUnsignedNativeInstallerCannotBecomeHostedRelease(t *testing.T) {
	config, r := publisherFixture(t)
	profile := &config.Profiles[0]
	profile.Target = "windows"
	profile.ExpectedSigner = strings.Repeat("A", 40)
	profile.InstallerFilename = "feedback.exe"
	if runtime.GOOS == "darwin" {
		profile.Target = "macos"
		profile.ExpectedSigner = "ABCD123456"
		profile.InstallerFilename = "feedback.pkg"
	}
	r.Target = profile.Target
	engine, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err = engine.Execute(context.Background(), r); err == nil {
		t.Fatal("unsigned fixture became a trusted desktop release")
	}
	if _, err = os.Stat(filepath.Join(profile.DistributionRoot, "distribution.db")); !os.IsNotExist(err) {
		t.Fatal("native signature failure reached distribution activation")
	}
}

func TestHostedProfileKeepsSigningKeysOutsidePublicServiceVolume(t *testing.T) {
	config, _ := publisherFixture(t)
	config.Profiles[0].SigningKeyFile = filepath.Join(config.Profiles[0].DistributionRoot, "private-signing-key")
	if engine, err := Open(config); err == nil {
		engine.Close()
		t.Fatal("public distribution volume contains the private signing key")
	}
	config, _ = publisherFixture(t)
	config.Profiles[0].PublicURL = "http://insecure.example"
	if engine, err := Open(config); err == nil {
		engine.Close()
		t.Fatal("insecure public feed URL accepted")
	}
	config, _ = publisherFixture(t)
	other := config.Profiles[0]
	other.GoalID = "goal_other"
	other.AppID = "other-app"
	other.PublicURL = "https://other-app.example"
	other.DistributionRoot = filepath.Join(t.TempDir(), "other-volume")
	config.Profiles = append(config.Profiles, other)
	config.Profiles[0].SigningKeyFile = filepath.Join(other.DistributionRoot, "private-key")
	if engine, err := Open(config); err == nil {
		engine.Close()
		t.Fatal("signing key leaked into another profile's public volume")
	}
	config, _ = publisherFixture(t)
	other = config.Profiles[0]
	other.GoalID = "goal_other"
	other.AppID = "other-app"
	other.DistributionRoot = filepath.Join(t.TempDir(), "different-root")
	config.Profiles = append(config.Profiles, other)
	if engine, err := Open(config); err == nil {
		engine.Close()
		t.Fatal("separate generated applications share one browser origin")
	}
}

func TestHostedPublisherChecksApprovedBindingAndReplaysWithoutActivation(t *testing.T) {
	ctx := context.Background()
	config, request := publisherFixture(t)
	engine, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	result, err := engine.Execute(ctx, request)
	if err != nil || result.State != "published" || !strings.HasPrefix(result.ExternalID, "hosted:feedback:web:") {
		t.Fatalf("real hosted publish: %+v %v", result, err)
	}
	replay, err := engine.Execute(ctx, request)
	if err != nil || replay.ExternalID != result.ExternalID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	dist, err := distribution.OpenReadOnly(config.Profiles[0].DistributionRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer dist.Close()
	current, err := dist.Current(ctx, "feedback", "web", "stable")
	if err != nil || current.Sequence != 1 || current.Binding != request.Binding {
		t.Fatalf("publication not bound or activated twice: %+v %v", current, err)
	}
	query := request
	query.Action = "status"
	query.ArtifactPath = ""
	query.ExternalID = result.ExternalID
	if status, e := engine.Execute(ctx, query); e != nil || status.State != "published" {
		t.Fatalf("read-only actual hosted state: %+v %v", status, e)
	}
	foreign := request
	foreign.ProjectID = "another-project"
	if _, err = engine.Execute(ctx, foreign); err == nil {
		t.Fatal("project selected another publishing destination")
	}
	changed := request
	changed.Binding = strings.Repeat("c", 64)
	if _, err = engine.Execute(ctx, changed); err == nil {
		t.Fatal("operation reused for another approval")
	}
	changed = request
	changed.ArtifactDigest = strings.Repeat("0", 64)
	changed.OperationKey = strings.Repeat("d", 64)
	if _, err = engine.Execute(ctx, changed); err == nil {
		t.Fatal("publisher accepted altered artifact bytes")
	}
}

func TestGooglePlayCommitIsAwaitingReviewAndUncertainCommitNeverReplays(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "unknown-result"}[uncertain], func(t *testing.T) {
			ctx := context.Background()
			config, request := publisherFixture(t)
			config.Profiles[0] = Profile{ProjectID: request.ProjectID, GoalID: request.GoalID, Target: "android-google-play", Driver: "google-play", AppID: "com.example.feedback", Track: "production", CredentialsFile: filepath.Join(t.TempDir(), "credentials.json")}
			request.Target = "android-google-play"
			var commits, uploads, edits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer unit-test-token" {
					t.Errorf("missing scoped OAuth access token")
					http.Error(w, "unauthorized", 401)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == "POST" && r.URL.Path == "/androidpublisher/v3/applications/com.example.feedback/edits":
					if edits.Add(1) == 1 {
						_, _ = io.WriteString(w, `{"id":"edit-123","expiryTimeSeconds":"9999999999"}`)
					} else {
						_, _ = io.WriteString(w, `{"id":"query-456","expiryTimeSeconds":"9999999999"}`)
					}
				case r.Method == "POST" && r.URL.Path == "/upload/androidpublisher/v3/applications/com.example.feedback/edits/edit-123/bundles":
					uploads.Add(1)
					body, _ := io.ReadAll(r.Body)
					digest := sha256.Sum256(body)
					if hex.EncodeToString(digest[:]) != request.ArtifactDigest || r.URL.Query().Get("uploadType") != "media" {
						t.Errorf("unapproved bytes or invalid upload protocol")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"versionCode": 42, "sha256": request.ArtifactDigest, "sha1": "not-used"})
				case r.Method == "PUT" && r.URL.Path == "/androidpublisher/v3/applications/com.example.feedback/edits/edit-123/tracks/production":
					var track struct {
						Track    string `json:"track"`
						Releases []struct {
							VersionCodes []string `json:"versionCodes"`
							Status       string   `json:"status"`
						} `json:"releases"`
					}
					if err := json.NewDecoder(r.Body).Decode(&track); err != nil || track.Track != "production" || len(track.Releases) != 1 || len(track.Releases[0].VersionCodes) != 1 || track.Releases[0].VersionCodes[0] != "42" || track.Releases[0].Status != "completed" {
						t.Errorf("incorrect release track: %+v %v", track, err)
					}
					_, _ = io.WriteString(w, `{"track":"production","releases":[{"versionCodes":["42"],"status":"completed"}]}`)
				case r.Method == "POST" && r.URL.Path == "/androidpublisher/v3/applications/com.example.feedback/edits/edit-123:commit":
					commits.Add(1)
					if uncertain {
						http.Error(w, `{"error":{"message":"example-secret-response"}}`, 502)
						return
					}
					_, _ = io.WriteString(w, `{"id":"edit-123","expiryTimeSeconds":"9999999999"}`)
				case r.Method == "GET" && r.URL.Path == "/androidpublisher/v3/applications/com.example.feedback/edits/query-456/tracks/production":
					_, _ = io.WriteString(w, `{"track":"production","releases":[{"versionCodes":["42"],"status":"completed"}]}`)
				case r.Method == "DELETE" && r.URL.Path == "/androidpublisher/v3/applications/com.example.feedback/edits/query-456":
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected API call %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			engine, err := Open(config)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			engine.apiBase = server.URL
			engine.tokenSource = func(context.Context, Profile) (oauth2.TokenSource, error) {
				return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "unit-test-token"}), nil
			}
			result, err := engine.Execute(ctx, request)
			if uncertain {
				if err == nil || strings.Contains(err.Error(), "example-secret-response") {
					t.Fatalf("unknown commit was accepted or leaked response: %v", err)
				}
				if _, err = engine.Execute(ctx, request); err == nil {
					t.Fatal("unknown external publication retried")
				}
				if commits.Load() != 1 || uploads.Load() != 1 {
					t.Fatalf("external side effect replayed: commits=%d uploads=%d", commits.Load(), uploads.Load())
				}
				return
			}
			if err != nil || result.State != "awaiting_review" || result.ExternalID != "com.example.feedback:42" {
				t.Fatalf("commit mistaken for public availability: %+v %v", result, err)
			}
			query := request
			query.Action = "status"
			query.ExternalID = result.ExternalID
			query.ArtifactPath = ""
			status, err := engine.Execute(ctx, query)
			if err != nil || status.State != "unchanged" || status.UpstreamState != "completed" {
				t.Fatalf("track completed was mistaken for store review approval: %+v %v", status, err)
			}
			if _, err = engine.Execute(ctx, request); err != nil || commits.Load() != 1 || uploads.Load() != 1 {
				t.Fatalf("completed journal not idempotent: %v", err)
			}
		})
	}
}
