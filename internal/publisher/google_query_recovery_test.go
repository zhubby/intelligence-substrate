package publisher

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type googleQueryFaultTransport struct {
	base http.RoundTripper
	lost string
}

func (tr googleQueryFaultTransport) RoundTrip(q *http.Request) (*http.Response, error) {
	response, err := tr.base.RoundTrip(q)
	if err == nil && tr.lost == "same" && q.Method == "POST" && strings.HasSuffix(q.URL.Path, "/edits") {
		response.Body.Close()
		response.Body = io.NopCloser(strings.NewReader(`{"id":"edit-1"}`))
		return response, nil
	}
	if err == nil && ((tr.lost == "create" && q.Method == "POST" && strings.HasSuffix(q.URL.Path, "/edits")) || (tr.lost == "delete" && q.Method == "DELETE") || (tr.lost == "read" && q.Method == "GET")) {
		response.Body.Close()
		response.StatusCode = 503
		response.Body = io.NopCloser(strings.NewReader(`{"error":"private-query-provider-detail"}`))
	}
	return response, err
}

func TestGoogleQueryGuardNeverDeletesOriginalPublishEdit(t *testing.T) {
	_, r, e, upstream := completedGoogleQueryFixture(t)
	e.HTTP.Transport = googleQueryFaultTransport{base: e.HTTP.Transport, lost: "same"}
	if _, err := e.Execute(context.Background(), r); err == nil {
		t.Fatal("query reused the original publishing edit")
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || op.GoogleQuery == nil || op.GoogleQuery.State != "creating" || op.GoogleQuery.EditID != "" || !op.GoogleQuery.WindowHeld {
		t.Fatal("original edit was recorded as a disposable read edit", op, err)
	}
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if len(upstream.requests) != 5 {
		t.Fatal("unidentified query read or deleted the original publish edit", upstream.requests)
	}
}

func TestGoogleQueryGuardReadFailureClosesOnlyItsQueryAndKeepsPublication(t *testing.T) {
	_, r, e, upstream := completedGoogleQueryFixture(t)
	before, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	e.HTTP.Transport = googleQueryFaultTransport{base: e.HTTP.Transport, lost: "read"}
	if _, err = e.Execute(context.Background(), r); err == nil || strings.Contains(err.Error(), "private-query-provider-detail") {
		t.Fatal("failed channel read returned an observation or private response", err)
	}
	after, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || after.JournalDigest != before.JournalDigest || after.GoogleQuery == nil || after.GoogleQuery.State != "cleaned" || after.GoogleQuery.WindowHeld || after.GoogleQuery.CleanupSource != "delete-confirmed" {
		t.Fatal("read failure changed publication or leaked the confirmed-cleaned edit", after, err)
	}
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if len(upstream.requests) != 7 || !strings.HasPrefix(upstream.requests[6], "DELETE ") {
		t.Fatal("read failure did not delete only the query", upstream.requests)
	}
}

func TestGoogleQueryGuardAuthenticationFailureIsDurableButNeverStarted(t *testing.T) {
	_, r, e, upstream := completedGoogleQueryFixture(t)
	e.tokenSource = func(ctx context.Context, _ Profile) (oauth2.TokenSource, error) {
		op, err := e.Inspect(ctx, r.OperationKey)
		if err != nil || op.GoogleQuery == nil || op.GoogleQuery.State != "reserved" || !op.GoogleQuery.WindowHeld {
			t.Error("authentication started without durable query ownership", op, err)
		}
		return nil, errors.New("credentials unavailable")
	}
	if _, err := e.Execute(context.Background(), r); err == nil {
		t.Fatal("failed query authentication returned an observation")
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || op.GoogleQuery == nil || op.GoogleQuery.State != "cleaned" || op.GoogleQuery.WindowHeld || op.GoogleQuery.CleanupSource != "not-started" {
		t.Fatal("pre-edit authentication failure left a live edit window", op, err)
	}
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if len(upstream.requests) != 4 {
		t.Fatal("failed authentication created an edit", upstream.requests)
	}
}

func TestGoogleQueryGuardCancelledReadStillConfirmsCleanup(t *testing.T) {
	_, r, e, upstream := completedGoogleQueryFixture(t)
	ready := make(chan struct{})
	upstream.mu.Lock()
	upstream.hook = func(q *http.Request) {
		if q.Method == "GET" {
			close(ready)
			<-q.Context().Done()
		}
	}
	upstream.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := e.Execute(ctx, r); finished <- err }()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("query never began reading")
	}
	cancel()
	if err := <-finished; err == nil {
		t.Fatal("cancelled query returned a channel observation")
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || op.GoogleQuery == nil || op.GoogleQuery.State != "cleaned" || op.GoogleQuery.WindowHeld || op.GoogleQuery.CleanupSource != "delete-confirmed" {
		t.Fatal("cancelled read skipped its bounded independent cleanup", op, err)
	}
}

func completedGoogleQueryFixture(t *testing.T) (Config, Request, *Engine, *googleWriteTestProvider) {
	t.Helper()
	cfg, r, e, upstream := googleWriteFixture(t)
	result, err := e.Execute(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.Action, r.ExternalID, r.ArtifactPath = "status", result.ExternalID, ""
	return cfg, r, e, upstream
}

func stoppedGoogleQueryInput(t *testing.T, e *Engine, r Request) GoogleQueryRecoveryRequest {
	t.Helper()
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || op.GoogleQuery == nil || !op.GoogleQuery.WindowHeld {
		t.Fatal("query has no durable owned window", op, err)
	}
	return GoogleQueryRecoveryRequest{Request: r, QueryID: op.GoogleQuery.QueryID, QueryDigest: op.GoogleQuery.QueryDigest, ConfirmStoppedQueryID: op.GoogleQuery.QueryID, Reason: "Administrator stopped the exact query; exclusive edit window"}
}

func TestGoogleQueryGuardLostDeleteSurvivesRestartAndExactAdminCleanup(t *testing.T) {
	cfg, r, e, upstream := completedGoogleQueryFixture(t)
	before, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	baseURL, base := e.apiBase, e.HTTP.Transport
	cleanClient := *e.HTTP
	e.HTTP.Transport = googleQueryFaultTransport{base: base, lost: "delete"}
	if _, err = e.Execute(context.Background(), r); err == nil || strings.Contains(err.Error(), "private-query-provider-detail") {
		t.Fatal("unconfirmed query cleanup returned a channel observation", err)
	}
	in := stoppedGoogleQueryInput(t, e, r)
	q, err := e.InspectGoogleQuery(context.Background(), in.QueryID)
	if err != nil || q.State != "created" || q.EditID != "edit-2" || !q.WindowHeld {
		t.Fatal("lost deletion did not retain its exact query identity", q, err)
	}
	tokens := e.tokenSource
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	e, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.apiBase, e.HTTP, e.tokenSource = baseURL, &cleanClient, tokens
	upstream.mu.Lock()
	calls := len(upstream.requests)
	upstream.mu.Unlock()
	if _, err = e.Execute(context.Background(), r); err == nil {
		t.Fatal("restarted query silently replaced the uncertain edit")
	}
	changed := nextGoogleRequest(t, r)
	changed.Action, changed.ExternalID = "publish", ""
	if _, err = e.Execute(context.Background(), changed); err == nil {
		t.Fatal("restarted publication bypassed query ownership")
	}
	upstream.mu.Lock()
	if len(upstream.requests) != calls {
		t.Error("uncertain query contention reached Google", upstream.requests)
	}
	upstream.mu.Unlock()
	q, err = e.ReconcileGoogleQuery(context.Background(), in)
	if err != nil || q.State != "cleaned" || q.WindowHeld || q.CleanupSource != "already-absent" || q.Reconciliation == nil || q.Reconciliation.Reason != in.Reason {
		t.Fatal("known deleted edit could not be safely confirmed", q, err)
	}
	after, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || after.JournalDigest != before.JournalDigest || after.Result == nil || after.Result.State != "awaiting_review" {
		t.Fatal("query cleanup changed original publication or its approval snapshot", after, err)
	}
	if result, err := e.Execute(context.Background(), changed); err != nil || result.ExternalID != "com.example.feedback:42" {
		t.Fatal("confirmed query cleanup did not release the app for a new publication", result, err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	// No fixture origin or credentials on reopen: exact replay must be local.
	again, err := reopened.ReconcileGoogleQuery(context.Background(), in)
	if err != nil || again.QueryDigest != q.QueryDigest {
		t.Fatal("query cleanup was not idempotent after restart", again, err)
	}
	changedInput := in
	changedInput.Reason = "a different assertion"
	if _, err = reopened.ReconcileGoogleQuery(context.Background(), changedInput); err == nil {
		t.Fatal("changed administrator assertion borrowed an old cleanup")
	}
}

func TestGoogleQueryGuardUnknownCreateCannotBeForcedOpen(t *testing.T) {
	cfg, r, e, upstream := completedGoogleQueryFixture(t)
	baseURL, client, tokens := e.apiBase, e.HTTP, e.tokenSource
	client.Transport = googleQueryFaultTransport{base: client.Transport, lost: "create"}
	if _, err := e.Execute(context.Background(), r); err == nil {
		t.Fatal("lost creation response returned success")
	}
	in := stoppedGoogleQueryInput(t, e, r)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	e, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.apiBase, e.HTTP, e.tokenSource = baseURL, client, tokens
	upstream.mu.Lock()
	before := len(upstream.requests)
	upstream.mu.Unlock()
	if _, err = e.ReconcileGoogleQuery(context.Background(), in); err == nil {
		t.Fatal("administrator reason forced open an unidentified external query")
	}
	q, err := e.InspectGoogleQuery(context.Background(), in.QueryID)
	if err != nil || q.State != "creating" || q.EditID != "" || !q.WindowHeld {
		t.Fatal("unknown query identity was guessed", q, err)
	}
	if _, err = e.Execute(context.Background(), r); err == nil {
		t.Fatal("unknown query was silently retried")
	}
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if len(upstream.requests) != before {
		t.Fatal("unknown query recovery reached another edit", upstream.requests)
	}
}

func TestGoogleQueryGuardDatabaseFailuresDoNotExposeAnEditWindow(t *testing.T) {
	for _, scenario := range []string{"reserve", "creating", "checkpoint", "finish", "release"} {
		t.Run(scenario, func(t *testing.T) {
			_, r, e, upstream := completedGoogleQueryFixture(t)
			queries := map[string]string{
				"reserve":    `CREATE TRIGGER block_query BEFORE INSERT ON google_query_locks BEGIN SELECT RAISE(ABORT,'forced query reserve failure'); END`,
				"creating":   `CREATE TRIGGER block_query BEFORE UPDATE ON google_queries WHEN NEW.state='creating' BEGIN SELECT RAISE(ABORT,'forced query intent failure'); END`,
				"checkpoint": `CREATE TRIGGER block_query BEFORE UPDATE ON google_queries WHEN NEW.state='created' BEGIN SELECT RAISE(ABORT,'forced query checkpoint failure'); END`,
				"finish":     `CREATE TRIGGER block_query BEFORE UPDATE ON google_queries WHEN NEW.state='cleaned' BEGIN SELECT RAISE(ABORT,'forced query finish failure'); END`,
				"release":    `CREATE TRIGGER block_query BEFORE DELETE ON google_query_locks BEGIN SELECT RAISE(ABORT,'forced query release failure'); END`,
			}
			if _, err := e.db.Exec(queries[scenario]); err != nil {
				t.Fatal(err)
			}
			upstream.mu.Lock()
			before := len(upstream.requests)
			upstream.mu.Unlock()
			if _, err := e.Execute(context.Background(), r); err == nil {
				t.Fatal("query database failure returned an observation")
			}
			upstream.mu.Lock()
			calls := len(upstream.requests) - before
			upstream.mu.Unlock()
			op, err := e.Inspect(context.Background(), r.OperationKey)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "reserve":
				if calls != 0 || op.GoogleQuery != nil {
					t.Fatal("query reservation failure left a partial intent or external edit", calls, op)
				}
			case "creating":
				if calls != 0 || op.GoogleQuery == nil || op.GoogleQuery.State != "cleaned" || op.GoogleQuery.CleanupSource != "not-started" || op.GoogleQuery.WindowHeld {
					t.Fatal("pre-network failure could not safely close its own reservation", calls, op)
				}
			case "checkpoint":
				if calls != 2 || op.GoogleQuery == nil || op.GoogleQuery.State != "cleaned" || op.GoogleQuery.EditID != "edit-2" || op.GoogleQuery.WindowHeld {
					t.Fatal("failed checkpoint did not persist confirmed known-edit cleanup", calls, op)
				}
			default:
				if calls != 3 || op.GoogleQuery == nil || op.GoogleQuery.State != "created" || !op.GoogleQuery.WindowHeld {
					t.Fatal("finish/release failure exposed a partly closed window", calls, op)
				}
				in := stoppedGoogleQueryInput(t, e, r)
				if _, err = e.db.Exec("DROP TRIGGER block_query"); err != nil {
					t.Fatal(err)
				}
				if _, err = e.ReconcileGoogleQuery(context.Background(), in); err != nil {
					t.Fatal("known stopped query could not recover after DB repair", err)
				}
			}
		})
	}
}

func TestGoogleQueryRecoveryRejectsChangedAuthorityBeforeNetwork(t *testing.T) {
	for _, scenario := range []string{"snapshot", "confirmation", "reason", "long reason", "binding", "operation", "version", "application", "track", "driver", "external", "continue", "foreign query", "owner"} {
		t.Run(scenario, func(t *testing.T) {
			_, r, e, upstream := completedGoogleQueryFixture(t)
			e.HTTP.Transport = googleQueryFaultTransport{base: e.HTTP.Transport, lost: "delete"}
			if _, err := e.Execute(context.Background(), r); err == nil {
				t.Fatal("test did not retain its uncertain query")
			}
			in := stoppedGoogleQueryInput(t, e, r)
			switch scenario {
			case "snapshot":
				in.QueryDigest = strings.Repeat("0", 64)
			case "confirmation":
				in.ConfirmStoppedQueryID = r.OperationKey
			case "reason":
				in.Reason = " "
			case "long reason":
				in.Reason = strings.Repeat("r", 4097)
			case "binding":
				in.Request.Binding = strings.Repeat("f", 64)
			case "operation":
				in.Request.OperationKey = strings.Repeat("f", 64)
			case "version":
				in.Request.Version = "2.0.0"
			case "application":
				e.Config.Profiles[0].AppID = "com.example.other"
			case "track":
				e.Config.Profiles[0].Track = "internal"
			case "driver":
				e.Config.Profiles[0].Driver = "hosted"
			case "external":
				in.Request.ExternalID = "com.example.feedback:99"
			case "continue":
				in.Request.Action = "continue"
			case "foreign query":
				in.QueryID, in.ConfirmStoppedQueryID = strings.Repeat("f", 64), strings.Repeat("f", 64)
			case "owner":
				if _, err := e.db.Exec("UPDATE google_query_locks SET query_id=?", strings.Repeat("f", 64)); err != nil {
					t.Fatal(err)
				}
			}
			upstream.mu.Lock()
			before := len(upstream.requests)
			upstream.mu.Unlock()
			if _, err := e.ReconcileGoogleQuery(context.Background(), in); err == nil {
				t.Fatal("changed query cleanup authority accepted")
			}
			upstream.mu.Lock()
			defer upstream.mu.Unlock()
			if len(upstream.requests) != before {
				t.Fatal("invalid cleanup authority reached Google", upstream.requests)
			}
		})
	}
}

func TestGoogleQueryRecoveryNeverTargetsPersistedOriginalPublishEdit(t *testing.T) {
	_, r, e, upstream := completedGoogleQueryFixture(t)
	e.HTTP.Transport = googleQueryFaultTransport{base: e.HTTP.Transport, lost: "delete"}
	if _, err := e.Execute(context.Background(), r); err == nil {
		t.Fatal("test did not retain a query")
	}
	in := stoppedGoogleQueryInput(t, e, r)
	if _, err := e.db.Exec("UPDATE google_queries SET edit_id='edit-1' WHERE query_id=?", in.QueryID); err != nil {
		t.Fatal(err)
	}
	q, err := e.InspectGoogleQuery(context.Background(), in.QueryID)
	if err != nil {
		t.Fatal(err)
	}
	in.QueryDigest = q.QueryDigest
	upstream.mu.Lock()
	before := len(upstream.requests)
	upstream.mu.Unlock()
	if _, err = e.ReconcileGoogleQuery(context.Background(), in); err == nil {
		t.Fatal("cleanup trusted an original publishing edit as a query identity")
	}
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if len(upstream.requests) != before {
		t.Fatal("cleanup attempted to delete the original publishing edit", upstream.requests)
	}
}

func TestGoogleQueryRecoverySnapshotAndOwnerChangesCannotPartlyRelease(t *testing.T) {
	for _, ownerChanged := range []bool{false, true} {
		t.Run(map[bool]string{false: "snapshot", true: "owner"}[ownerChanged], func(t *testing.T) {
			_, r, e, upstream := completedGoogleQueryFixture(t)
			base := e.HTTP.Transport
			e.HTTP.Transport = googleQueryFaultTransport{base: base, lost: "delete"}
			if _, err := e.Execute(context.Background(), r); err == nil {
				t.Fatal("test did not retain an uncertain query")
			}
			in := stoppedGoogleQueryInput(t, e, r)
			e.HTTP.Transport = base
			upstream.mu.Lock()
			upstream.hook = func(q *http.Request) {
				if q.Method == "DELETE" {
					statement, value := "UPDATE google_queries SET updated_at=? WHERE query_id=?", "concurrent"
					if ownerChanged {
						statement, value = "UPDATE google_query_locks SET query_id=? WHERE query_id=?", strings.Repeat("f", 64)
					}
					if _, err := e.db.Exec(statement, value, in.QueryID); err != nil {
						t.Error(err)
					}
				}
			}
			upstream.mu.Unlock()
			if _, err := e.ReconcileGoogleQuery(context.Background(), in); err == nil {
				t.Fatal("changed cleanup snapshot or owner was overwritten")
			}
			q, err := e.InspectGoogleQuery(context.Background(), in.QueryID)
			if err != nil || q.State != "created" || q.Reconciliation != nil {
				t.Fatal("failed cleanup persisted a partial reconciliation", q, err)
			}
			var count int
			if err = e.db.QueryRow("SELECT count(*) FROM google_query_locks").Scan(&count); err != nil || count != 1 {
				t.Fatal("failed cleanup removed its own or a foreign window", count, err)
			}
		})
	}
}

func TestGoogleQueryRecoveryInputStrictAndOrdinaryWorkerCannotUseIt(t *testing.T) {
	in := GoogleQueryRecoveryRequest{Request: Request{ProtocolVersion: "1", OperationKey: strings.Repeat("a", 64)}, QueryID: strings.Repeat("b", 64), QueryDigest: strings.Repeat("c", 64), ConfirmStoppedQueryID: strings.Repeat("b", 64), Reason: "已停止准确查询"}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeGoogleQueryRecoveryInput(strings.NewReader(string(body)))
	if err != nil || decoded.QueryID != in.QueryID || decoded.Reason != in.Reason {
		t.Fatal(decoded, err)
	}
	for _, text := range []string{"", "null", "[]", string(body) + "{}", string(body) + strings.Repeat(" ", 65536), `{"unknown":true}`, `{"request":{"unknown":true}}`, `{"queryId":"a","queryId":"b"}`, `{"reason":"a","\u0072eason":"b"}`, "{\"reason\":\"\xff\"}"} {
		if _, err = DecodeGoogleQueryRecoveryInput(strings.NewReader(text)); err == nil {
			t.Fatal("ambiguous or oversized query cleanup input accepted")
		}
	}
	_, r, e, _ := googleWriteFixture(t)
	r.Action = "reconcile-google-query"
	if _, err = e.Execute(context.Background(), r); err == nil {
		t.Fatal("ordinary Worker acquired query cleanup authority")
	}
}
