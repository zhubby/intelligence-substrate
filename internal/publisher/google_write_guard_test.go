package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func nextGoogleRequest(t *testing.T, r Request) Request {
	t.Helper()
	r.OperationKey, r.Version, r.ArtifactID = strings.Repeat("c", 64), "2.0.0", "artifact_two"
	r.ArtifactPath = filepath.Join(t.TempDir(), "different-synthetic-artifact")
	body := []byte("different synthetic upload bytes; not an AAB")
	if err := os.WriteFile(r.ArtifactPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(body)
	r.ArtifactDigest, r.ArtifactSize = hex.EncodeToString(h[:]), int64(len(body))
	return r
}

func TestGoogleWriteGuardUnknownResultBlocksNewKeysAndTracksBeforeNetwork(t *testing.T) {
	for _, scenario := range []string{"same bytes", "different version and bytes", "different track", "reopen"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, r, e, s, _ := googleLostCommit(t)
			changed := r
			changed.OperationKey = strings.Repeat("c", 64)
			if scenario != "same bytes" {
				changed = nextGoogleRequest(t, r)
			}
			if scenario == "different track" {
				e.Config.Profiles[0].Track = "internal"
			}
			if scenario == "reopen" {
				if err := e.Close(); err != nil {
					t.Fatal(err)
				}
				var err error
				e, err = Open(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer e.Close()
			}
			s.mu.Lock()
			calls := len(s.calls)
			s.mu.Unlock()
			if _, err := e.Execute(context.Background(), changed); err == nil {
				t.Fatal("unknown application write accepted another key")
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.calls) != calls {
				t.Fatal("new key reached Google before the unknown operation was reconciled", s.calls)
			}
			if _, err := e.Inspect(context.Background(), changed.OperationKey); err == nil {
				t.Fatal("rejected contention reserved the new operation key")
			}
		})
	}
}

func TestGoogleWriteGuardRecoveryReleasesOnlyApplicationAndRetainsVersionAndBytes(t *testing.T) {
	for _, scenario := range []string{"same version", "same bytes"} {
		t.Run(scenario, func(t *testing.T) {
			_, r, e, s, in := googleLostCommit(t)
			if _, err := e.ReconcileGoogle(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			changed := nextGoogleRequest(t, r)
			if scenario == "same version" {
				changed.Version = r.Version
			}
			if scenario == "same bytes" {
				changed.ArtifactPath, changed.ArtifactDigest, changed.ArtifactSize = r.ArtifactPath, r.ArtifactDigest, r.ArtifactSize
			}
			s.mu.Lock()
			calls := len(s.calls)
			s.mu.Unlock()
			if _, err := e.Execute(context.Background(), changed); err == nil {
				t.Fatal("permanent version or byte reservation was discarded")
			}
			if result, err := e.Execute(context.Background(), r); err != nil || result.State != "awaiting_review" {
				t.Fatal("original publication no longer replays", result, err)
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.calls) != calls {
				t.Fatal("version/byte conflict or cached result called provider")
			}
			var count int
			if err := e.db.QueryRow("SELECT count(*) FROM google_destinations").Scan(&count); err != nil || count != 0 {
				t.Fatal("successful recovery left application locked", count, err)
			}
		})
	}
}

type googleWriteTestProvider struct {
	mu          sync.Mutex
	requests    []string
	edit        int
	code        int
	hook        func(*http.Request)
	tracks      map[string]json.RawMessage
	openEdits   map[string]bool
	certificate []byte
}

func googleWriteFixture(t *testing.T) (Config, Request, *Engine, *googleWriteTestProvider) {
	t.Helper()
	cfg, r := publisherFixture(t)
	r.Target = "android-google-play"
	cfg.Profiles = []Profile{{ProjectID: r.ProjectID, GoalID: r.GoalID, Target: r.Target, Driver: "google-play", AppID: "com.example.feedback", Track: "production", CredentialsFile: filepath.Join(cfg.StateDir, "unused-credentials")}}
	s := &googleWriteTestProvider{tracks: map[string]json.RawMessage{}, openEdits: map[string]bool{}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.TLS == nil || q.Header.Get("Authorization") != "Bearer local-write-guard-token" {
			t.Error("unauthenticated or non-TLS request")
			w.WriteHeader(401)
			return
		}
		s.mu.Lock()
		s.requests = append(s.requests, q.Method+" "+q.URL.RequestURI())
		hook := s.hook
		s.mu.Unlock()
		if hook != nil {
			hook(q)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case q.Method == "POST" && strings.HasSuffix(q.URL.Path, "/edits"):
			s.edit++
			parts := strings.Split(q.URL.Path, "/")
			s.openEdits[parts[4]+":"+fmt.Sprintf("edit-%d", s.edit)] = true
			_ = json.NewEncoder(w).Encode(map[string]string{"id": fmt.Sprintf("edit-%d", s.edit)})
		case q.Method == "POST" && strings.HasSuffix(q.URL.Path, "/bundles"):
			body, err := io.ReadAll(q.Body)
			if err != nil || int64(len(body)) != q.ContentLength {
				t.Error("upload bytes were truncated")
			}
			digest := sha256.Sum256(body)
			s.code++
			_ = json.NewEncoder(w).Encode(map[string]any{"versionCode": 40 + s.code, "sha256": hex.EncodeToString(digest[:])})
		case q.Method == "PUT" && strings.Contains(q.URL.Path, "/tracks/"):
			body, err := io.ReadAll(q.Body)
			if err != nil || !json.Valid(body) {
				t.Error("invalid track update")
				w.WriteHeader(400)
				return
			}
			// Store by application and track, independent of the edit snapshot.
			parts := strings.Split(q.URL.Path, "/")
			s.tracks[parts[4]+":"+parts[len(parts)-1]] = append(json.RawMessage(nil), body...)
			_, _ = w.Write(body)
		case q.Method == "GET" && strings.Contains(q.URL.Path, "/tracks/"):
			parts := strings.Split(q.URL.Path, "/")
			body, ok := s.tracks[parts[4]+":"+parts[len(parts)-1]]
			if !ok {
				http.NotFound(w, q)
				return
			}
			_, _ = w.Write(body)
		case q.Method == "DELETE" && strings.Contains(q.URL.Path, "/edits/"):
			parts := strings.Split(q.URL.Path, "/")
			key := parts[4] + ":" + parts[len(parts)-1]
			if !s.openEdits[key] {
				http.NotFound(w, q)
				return
			}
			delete(s.openEdits, key)
			w.WriteHeader(204)
		case q.Method == "POST" && strings.HasSuffix(q.URL.Path, ":commit"):
			parts := strings.Split(q.URL.Path, "/")
			delete(s.openEdits, parts[4]+":"+strings.TrimSuffix(parts[len(parts)-1], ":commit"))
			_ = json.NewEncoder(w).Encode(map[string]string{"id": strings.TrimSuffix(parts[len(parts)-1], ":commit")})
		default:
			t.Errorf("unexpected write-guard provider call %s %s", q.Method, q.URL)
			http.NotFound(w, q)
		}
	}))
	s.certificate = append([]byte(nil), server.Certificate().Raw...)
	t.Cleanup(server.Close)
	e, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	e.apiBase, e.HTTP = server.URL, server.Client()
	e.tokenSource = func(context.Context, Profile) (oauth2.TokenSource, error) {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "local-write-guard-token"}), nil
	}
	return cfg, r, e, s
}

func TestGoogleWriteGuardSuccessAllowsNewVersionAndDifferentApplication(t *testing.T) {
	_, r, e, s := googleWriteFixture(t)
	if result, err := e.Execute(context.Background(), r); err != nil || result.State != "awaiting_review" {
		t.Fatal(result, err)
	}
	second := nextGoogleRequest(t, r)
	if _, err := e.Execute(context.Background(), second); err != nil {
		t.Fatal("successful app was never released for a new version", err)
	}
	other := e.Config.Profiles[0]
	other.GoalID, other.AppID = "goal_two", "com.example.booking"
	e.Config.Profiles = append(e.Config.Profiles, other)
	third := r
	third.OperationKey, third.GoalID = strings.Repeat("d", 64), other.GoalID
	if _, err := e.Execute(context.Background(), third); err != nil {
		t.Fatal("another app incorrectly shared version/byte reservations", err)
	}
	if _, err := e.Execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) != 12 {
		t.Fatal("successful replay produced another write", s.requests)
	}
}

func TestGoogleWriteGuardDuplicateProviderCodeStopsBeforeTrackAndCommit(t *testing.T) {
	_, r, e, s := googleWriteFixture(t)
	if _, err := e.Execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	// A new upload returns the already used code despite different approved
	// bytes. Removing the provider-code uniqueness check would let it commit.
	s.mu.Lock()
	s.code = 0
	before := len(s.requests)
	s.mu.Unlock()
	changed := nextGoogleRequest(t, r)
	if _, err := e.Execute(context.Background(), changed); err == nil {
		t.Fatal("duplicate provider code reached successful publication")
	}
	op, err := e.Inspect(context.Background(), changed.OperationKey)
	if err != nil || op.State != "pending" || op.Receipt.EditID != "edit-2" || op.Receipt.VersionCode != "" || op.GoogleReservation == nil || !op.GoogleReservation.ApplicationHeld || op.GoogleReservation.VersionCode != "" {
		t.Fatal("code collision discarded the unknown upload or replaced the old reservation", op, err)
	}
	if _, err = e.Execute(context.Background(), changed); err == nil {
		t.Fatal("code collision replay uploaded again")
	}
	newKey := changed
	newKey.OperationKey = strings.Repeat("d", 64)
	if _, err = e.Execute(context.Background(), newKey); err == nil {
		t.Fatal("code collision let another key bypass app ownership")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) != before+2 || !strings.HasSuffix(s.requests[len(s.requests)-1], "/bundles?uploadType=media") {
		t.Fatal("code collision modified the track, committed or retried", s.requests)
	}
}

func TestGoogleWriteGuardTwoConnectionsSerializeOnlySameApplication(t *testing.T) {
	for _, anotherApp := range []bool{false, true} {
		t.Run(fmt.Sprint("another-app=", anotherApp), func(t *testing.T) {
			cfg, r, e, s := googleWriteFixture(t)
			if anotherApp {
				p := cfg.Profiles[0]
				p.GoalID, p.AppID = "goal_two", "com.example.booking"
				cfg.Profiles = append(cfg.Profiles, p)
			}
			other, err := Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			other.apiBase, other.HTTP, other.tokenSource = e.apiBase, e.HTTP, e.tokenSource
			ready, resume := make(chan struct{}, 1), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(resume) })
			s.hook = func(q *http.Request) {
				if strings.Contains(q.URL.Path, "/com.example.feedback/") && strings.HasSuffix(q.URL.Path, "/bundles") {
					ready <- struct{}{}
					<-resume
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			first := make(chan error, 1)
			go func() { _, err := e.Execute(ctx, r); first <- err }()
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal("original upload never started")
			}
			changed := nextGoogleRequest(t, r)
			if anotherApp {
				changed.GoalID = "goal_two"
			}
			_, err = other.Execute(ctx, changed)
			if (err == nil) != anotherApp {
				t.Fatal("application ownership did not distinguish destinations", err)
			}
			once.Do(func() { close(resume) })
			if err = <-first; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGoogleWriteGuardSQLFailuresCannotExposeOrUnlockUnknownWrites(t *testing.T) {
	for _, scenario := range []string{"reserve", "checkpoint", "finish", "release"} {
		t.Run(scenario, func(t *testing.T) {
			_, r, e, s := googleWriteFixture(t)
			queries := map[string]string{
				"reserve":    `CREATE TRIGGER block_guard BEFORE INSERT ON google_destinations BEGIN SELECT RAISE(ABORT,'forced reserve failure'); END`,
				"checkpoint": `CREATE TRIGGER block_guard BEFORE UPDATE ON google_publications WHEN NEW.version_code<>'' BEGIN SELECT RAISE(ABORT,'forced checkpoint failure'); END`,
				"finish":     `CREATE TRIGGER block_guard AFTER UPDATE ON operations WHEN NEW.state='complete' BEGIN SELECT RAISE(ABORT,'forced finish failure'); END`,
				"release":    `CREATE TRIGGER block_guard BEFORE DELETE ON google_destinations BEGIN SELECT RAISE(ABORT,'forced release failure'); END`,
			}
			if _, err := e.db.Exec(queries[scenario]); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Execute(context.Background(), r); err == nil {
				t.Fatal("faulty database reported successful publication")
			}
			s.mu.Lock()
			calls := len(s.requests)
			s.mu.Unlock()
			if scenario == "reserve" {
				if calls != 0 {
					t.Fatal("external call occurred without durable ownership")
				}
				if _, err := e.Inspect(context.Background(), r.OperationKey); err == nil {
					t.Fatal("failed reservation left a partial pending operation")
				}
				return
			}
			op, err := e.Inspect(context.Background(), r.OperationKey)
			if err != nil || op.State != "pending" {
				t.Fatal("post-write failure did not retain pending operation", op, err)
			}
			changed := nextGoogleRequest(t, r)
			if _, err = e.Execute(context.Background(), changed); err == nil {
				t.Fatal("new key bypassed failed publication")
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.requests) != calls {
				t.Fatal("failed publication lost its durable app ownership")
			}
		})
	}
}

func TestGoogleWriteGuardRecoveryReleaseFailureIsAtomic(t *testing.T) {
	_, r, e, s, in := googleLostCommit(t)
	if _, err := e.db.Exec(`CREATE TRIGGER fail_google_release BEFORE DELETE ON google_destinations BEGIN SELECT RAISE(ABORT,'forced recovery release failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReconcileGoogle(context.Background(), in); err == nil {
		t.Fatal("recovery succeeded despite failure to release its own app")
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || op.State != "pending" || op.JournalDigest != in.JournalDigest || len(op.Receipt.GoogleReconciliations) != 0 {
		t.Fatal("failed release left a half-completed recovery", op, err)
	}
	s.mu.Lock()
	calls := len(s.calls)
	s.mu.Unlock()
	if _, err = e.Execute(context.Background(), nextGoogleRequest(t, r)); err == nil {
		t.Fatal("failed recovery let another key publish")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) != calls {
		t.Fatal("failed recovery released the application")
	}
}

func TestGoogleWriteGuardRecoveryCannotReleaseChangedOwner(t *testing.T) {
	_, r, e, s, in := googleLostCommit(t)
	otherKey := strings.Repeat("d", 64)
	s.hook = func(q *http.Request) {
		if q.Method == "GET" && strings.HasSuffix(q.URL.Path, "/tracks/production") {
			if _, err := e.db.Exec("UPDATE google_destinations SET operation_key=? WHERE app_id=?", otherKey, "com.example.feedback"); err != nil {
				t.Error(err)
			}
		}
	}
	if _, err := e.ReconcileGoogle(context.Background(), in); err == nil {
		t.Fatal("recovery trusted an owner that changed while reading channel proof")
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || op.State != "pending" || op.JournalDigest != in.JournalDigest || len(op.Receipt.GoogleReconciliations) != 0 {
		t.Fatal("lost ownership changed the recovery journal", op, err)
	}
	var owner string
	if err = e.db.QueryRow("SELECT operation_key FROM google_destinations WHERE app_id=?", "com.example.feedback").Scan(&owner); err != nil || owner != otherKey {
		t.Fatal("recovery released another owner's app", owner, err)
	}
}

func TestGoogleWriteGuardOldJournalsRequireExactKnownIdentity(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy-pending=", pending), func(t *testing.T) {
			cfg, r, e, s, in := googleLostCommit(t)
			if !pending {
				if _, err := e.ReconcileGoogle(context.Background(), in); err != nil {
					t.Fatal(err)
				}
			}
			// Remove only new metadata in this disposable fixture, leaving the
			// exact legacy operations table and approved request hash intact.
			if _, err := e.db.Exec("DELETE FROM google_destinations; DELETE FROM google_publications"); err != nil {
				t.Fatal(err)
			}
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			oldBase, oldHTTP, oldToken := e.apiBase, e.HTTP, e.tokenSource
			var err error
			e, err = Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			e.apiBase, e.HTTP, e.tokenSource = oldBase, oldHTTP, oldToken
			s.mu.Lock()
			calls := len(s.calls)
			s.mu.Unlock()
			if _, err = e.Execute(context.Background(), nextGoogleRequest(t, r)); err == nil {
				t.Fatal("unmapped legacy Google journal allowed a new write")
			}
			s.mu.Lock()
			if len(s.calls) != calls {
				t.Error("unmapped legacy operation contacted Google")
			}
			s.mu.Unlock()
			if pending {
				if _, err = e.ReconcileGoogle(context.Background(), in); err != nil {
					t.Fatal("exact stopped legacy request could not recover", err)
				}
			} else if _, err = e.Execute(context.Background(), r); err != nil {
				t.Fatal("completed legacy request could not enroll via exact replay", err)
			}
			var app, version, digest string
			if err = e.db.QueryRow("SELECT app_id,version,artifact_digest FROM google_publications WHERE operation_key=?", r.OperationKey).Scan(&app, &version, &digest); err != nil || app != "com.example.feedback" || version != "1.0.0" || digest != r.ArtifactDigest {
				t.Fatal("legacy request enrolled guessed identity", app, version, digest, err)
			}
		})
	}
}

func TestGoogleWriteGuardUnknownEmptyIntentAndStatusCannotClearOwnership(t *testing.T) {
	_, r, e, s, _ := googleLostCommit(t)
	query := r
	query.Action, query.ExternalID, query.ArtifactPath = "status", "com.example.feedback:42", ""
	s.mu.Lock()
	calls := len(s.calls)
	s.mu.Unlock()
	if _, err := e.Execute(context.Background(), query); err == nil {
		t.Fatal("status entered an unknown publisher's active edit")
	}
	s.mu.Lock()
	if len(s.calls) != calls {
		t.Error("status invalidated a potentially active publication edit")
	}
	s.mu.Unlock()
	if _, err := e.db.Exec("DELETE FROM google_destinations; DELETE FROM google_publications; UPDATE operations SET receipt='{}'"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background(), nextGoogleRequest(t, r)); err == nil {
		t.Fatal("unidentifiable pre-edit legacy intent was assumed harmless")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) != calls {
		t.Fatal("unidentified intent allowed another external write")
	}
}

func TestGoogleWriteGuardOldVersionStatusCannotEnterNewUnknownWrite(t *testing.T) {
	_, r, e, s := googleWriteFixture(t)
	if _, err := e.Execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`CREATE TRIGGER fail_next_finish BEFORE UPDATE ON operations WHEN NEW.state='complete' BEGIN SELECT RAISE(ABORT,'forced next finish failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background(), nextGoogleRequest(t, r)); err == nil {
		t.Fatal("fault injection did not retain new publication")
	}
	s.mu.Lock()
	before := len(s.requests)
	s.mu.Unlock()
	query := r
	query.Action, query.ExternalID, query.ArtifactPath = "status", "com.example.feedback:41", ""
	if _, err := e.Execute(context.Background(), query); err == nil {
		t.Fatal("completed old version status entered a newer unknown edit namespace")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) != before {
		t.Fatal("old version status contacted Google while a new write was uncertain", s.requests)
	}
}

func TestGoogleWriteGuardConflictingLegacyHistoryCannotBeGuessedOrOverwritten(t *testing.T) {
	_, r, e, s, in := googleLostCommit(t)
	if _, err := e.ReconcileGoogle(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	// Two pre-upgrade records claim different approved requests for the same
	// version/bytes/code. Only their exact requests can identify them; they
	// must never silently overwrite the first enrolled record.
	second := r
	second.OperationKey = strings.Repeat("d", 64)
	if _, err := e.db.Exec("DELETE FROM google_publications; INSERT INTO operations(operation_key,request_hash,state,receipt,result,updated_at) SELECT ?,?,state,receipt,result,updated_at FROM operations WHERE operation_key=?", second.OperationKey, requestHash(second, e.Config.Profiles[0]), r.OperationKey); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	before := len(s.calls)
	s.mu.Unlock()
	if _, err := e.Execute(context.Background(), r); err != nil {
		t.Fatal("first exact historical request could not enroll", err)
	}
	if _, err := e.Execute(context.Background(), second); err == nil {
		t.Fatal("conflicting historical request replaced the reservation")
	}
	if _, err := e.Execute(context.Background(), nextGoogleRequest(t, r)); err == nil {
		t.Fatal("unresolved conflicting history allowed another write")
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || op.GoogleReservation == nil || op.GoogleReservation.VersionCode != "42" || op.GoogleReservation.ArtifactDigest != r.ArtifactDigest {
		t.Fatal("conflicting historical request changed original reservation", op, err)
	}
	var count int
	if err = e.db.QueryRow("SELECT count(*) FROM google_publications").Scan(&count); err != nil || count != 1 {
		t.Fatal("conflicting historical request left partial enrollment", count, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) != before {
		t.Fatal("unresolved historical conflict contacted Google", s.calls)
	}
}

// An opt-in export retains raw SQLite snapshots for an independent reader. All
// assertions also run in the ordinary suite; no production API is overridden.
func TestGoogleWriteGuardDurableEvidence(t *testing.T) {
	root := os.Getenv("IS_GOOGLE_WRITE_GUARD_PROOF_DIR")
	if root == "" {
		root = t.TempDir()
	} else if !filepath.IsAbs(root) {
		t.Fatal("proof directory must be absolute")
	} else if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("proof directory must not already exist", err)
	}
	write := func(name string, value any) {
		t.Helper()
		body, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, name), append(body, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	retainArtifact := func(r Request, name string) Request {
		t.Helper()
		body, err := os.ReadFile(r.ArtifactPath)
		if err != nil {
			t.Fatal(err)
		}
		r.ArtifactPath = filepath.Join(root, name)
		if err = os.WriteFile(r.ArtifactPath, body, 0600); err != nil {
			t.Fatal(err)
		}
		return r
	}
	cfg, r, e, lost, in := googleLostCommit(t)
	defer func() { e.Close() }()
	r = retainArtifact(r, "first-synthetic-artifact.bin")
	in.Request = r
	write("first-request.json", r)
	write("recovery.json", in)
	snapshot := func(name string) {
		t.Helper()
		base, client, tokens := e.apiBase, e.HTTP, e.tokenSource
		cfg = e.Config
		if err := e.Close(); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(filepath.Join(cfg.StateDir, "publisher.db"))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, name+"-publisher.db"), body, 0600); err != nil {
			t.Fatal(err)
		}
		e, err = Open(cfg)
		if err != nil {
			t.Fatal(err)
		}
		e.apiBase, e.HTTP, e.tokenSource = base, client, tokens
	}
	snapshot("unknown")
	unknown, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || unknown.GoogleReservation == nil || !unknown.GoogleReservation.ApplicationHeld {
		t.Fatal("unknown publication did not survive restart with app ownership", unknown, err)
	}
	write("unknown.json", unknown)
	next := retainArtifact(nextGoogleRequest(t, r), "next-synthetic-artifact.bin")
	write("next-request.json", next)
	lost.mu.Lock()
	before := len(lost.calls)
	lost.mu.Unlock()
	if _, err = e.Execute(context.Background(), next); err == nil {
		t.Fatal("restarted unknown app accepted another key")
	}
	e.Config.Profiles[0].Track = "internal"
	if _, err = e.Execute(context.Background(), next); err == nil {
		t.Fatal("unknown app accepted a different track")
	}
	e.Config.Profiles[0].Track = "production"
	lost.mu.Lock()
	if len(lost.calls) != before {
		t.Error("unknown write contention contacted provider")
	}
	lost.mu.Unlock()
	recovered, err := e.ReconcileGoogle(context.Background(), in)
	if err != nil || recovered.Result == nil || recovered.Result.State != "awaiting_review" || recovered.GoogleReservation == nil || recovered.GoogleReservation.ApplicationHeld {
		t.Fatal("recovery failed to release only its app", recovered, err)
	}
	write("recovered.json", recovered)
	snapshot("recovered")
	for _, sameBytes := range []bool{false, true} {
		conflict := next
		if sameBytes {
			conflict.ArtifactPath, conflict.ArtifactDigest, conflict.ArtifactSize = r.ArtifactPath, r.ArtifactDigest, r.ArtifactSize
		} else {
			conflict.Version = r.Version
		}
		if _, err = e.Execute(context.Background(), conflict); err == nil {
			t.Fatal("recovery lost its permanent version or byte reservation")
		}
	}
	_, _, success, upstream := googleWriteFixture(t)
	upstream.mu.Lock()
	upstream.code, upstream.edit = 2, 3
	upstream.mu.Unlock()
	e.apiBase, e.HTTP, e.tokenSource = success.apiBase, success.HTTP, success.tokenSource
	if result, err := e.Execute(context.Background(), next); err != nil || result.ExternalID != "com.example.feedback:43" {
		t.Fatal("recovered app could not publish distinct new version", result, err)
	}
	otherProfile := e.Config.Profiles[0]
	otherProfile.GoalID, otherProfile.AppID = "goal_two", "com.example.booking"
	e.Config.Profiles = append(e.Config.Profiles, otherProfile)
	other := r
	other.GoalID, other.OperationKey = otherProfile.GoalID, strings.Repeat("d", 64)
	write("other-request.json", other)
	if result, err := e.Execute(context.Background(), other); err != nil || result.ExternalID != "com.example.booking:44" {
		t.Fatal("distinct app incorrectly shared version or byte reservation", result, err)
	}
	if result, err := e.Execute(context.Background(), r); err != nil || result.ExternalID != "com.example.feedback:42" {
		t.Fatal("original key failed to replay original result", result, err)
	}
	snapshot("final")
	write("config.json", cfg)
	lost.mu.Lock()
	write("unknown-and-recovery-api-calls.json", lost.calls)
	if lost.writes != 3 || lost.edits != 2 || lost.deletes != 1 {
		t.Error("recovery repeated original release writes", lost.calls)
	}
	lost.mu.Unlock()
	upstream.mu.Lock()
	write("new-publication-api-calls.json", upstream.requests)
	if len(upstream.requests) != 8 {
		t.Error("new versions or original replay produced unexpected writes", upstream.requests)
	}
	upstream.mu.Unlock()
	write("result.json", map[string]any{"passed": !t.Failed(), "liveGoogle": false, "validAAB": false, "realDevice": false, "overallP3Complete": false, "snapshots": []string{"unknown", "recovered", "final"}})
}
