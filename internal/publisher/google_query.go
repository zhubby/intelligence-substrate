package publisher

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Query windows share the authoritative SQLite journal with publication holds.
// They have no time-based takeover: creating an edit may have succeeded even
// when its response, or the process receiving it, was lost.
type GoogleQuery struct {
	QueryID        string                     `json:"queryId"`
	QueryDigest    string                     `json:"queryDigest"`
	OperationKey   string                     `json:"operationKey"`
	AppID          string                     `json:"appId"`
	Track          string                     `json:"track"`
	Kind           string                     `json:"kind"`
	State          string                     `json:"state"`
	EditID         string                     `json:"editId,omitempty"`
	CleanupSource  string                     `json:"cleanupSource,omitempty"`
	StartedAt      string                     `json:"startedAt"`
	UpdatedAt      string                     `json:"updatedAt"`
	WindowHeld     bool                       `json:"windowHeld"`
	Reconciliation *GoogleQueryReconciliation `json:"reconciliation,omitempty"`
}

type googleQueryJournal struct {
	GoogleQuery
	requestHash    string
	reconciliation string
}

const googleQueryColumns = `query_id,operation_key,request_hash,app_id,track,kind,state,edit_id,cleanup_source,started_at,updated_at,reconciliation`

func initializeGoogleQueryJournal(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS google_queries (
	 query_id TEXT PRIMARY KEY, operation_key TEXT NOT NULL, request_hash TEXT NOT NULL,
	 app_id TEXT NOT NULL, track TEXT NOT NULL, kind TEXT NOT NULL, state TEXT NOT NULL,
	 edit_id TEXT NOT NULL DEFAULT '', cleanup_source TEXT NOT NULL DEFAULT '',
	 started_at TEXT NOT NULL, updated_at TEXT NOT NULL, reconciliation TEXT NOT NULL DEFAULT '');
	 CREATE INDEX IF NOT EXISTS google_queries_by_operation ON google_queries(operation_key);
	 CREATE TABLE IF NOT EXISTS google_query_locks(app_id TEXT PRIMARY KEY, query_id TEXT NOT NULL UNIQUE);`)
	return err
}

func scanGoogleQuery(row interface{ Scan(...any) error }) (googleQueryJournal, error) {
	var j googleQueryJournal
	err := row.Scan(&j.QueryID, &j.OperationKey, &j.requestHash, &j.AppID, &j.Track, &j.Kind, &j.State, &j.EditID, &j.CleanupSource, &j.StartedAt, &j.UpdatedAt, &j.reconciliation, &j.WindowHeld)
	if err != nil {
		return j, err
	}
	if j.reconciliation != "" {
		var record GoogleQueryReconciliation
		if err = json.Unmarshal([]byte(j.reconciliation), &record); err != nil {
			return j, err
		}
		j.Reconciliation = &record
	}
	body, _ := json.Marshal([]string{j.QueryID, j.OperationKey, j.requestHash, j.AppID, j.Track, j.Kind, j.State, j.EditID, j.CleanupSource, j.StartedAt, j.UpdatedAt, j.reconciliation})
	digest := sha256.Sum256(body)
	j.QueryDigest = hex.EncodeToString(digest[:])
	return j, nil
}

func (e *Engine) inspectGoogleQuery(ctx context.Context, id string) (googleQueryJournal, error) {
	return scanGoogleQuery(e.db.QueryRowContext(ctx, `SELECT `+googleQueryColumns+`,
	 EXISTS(SELECT 1 FROM google_query_locks l WHERE l.app_id=q.app_id AND l.query_id=q.query_id)
	 FROM google_queries q WHERE query_id=?`, id))
}

func (e *Engine) InspectGoogleQuery(ctx context.Context, id string) (GoogleQuery, error) {
	if !hashPattern.MatchString(id) {
		return GoogleQuery{}, fmt.Errorf("exact Google query ID required")
	}
	j, err := e.inspectGoogleQuery(ctx, id)
	return j.GoogleQuery, err
}

func (e *Engine) latestGoogleQuery(ctx context.Context, key string) (*GoogleQuery, error) {
	var id string
	err := e.db.QueryRowContext(ctx, "SELECT query_id FROM google_queries WHERE operation_key=? ORDER BY rowid DESC LIMIT 1", key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	q, err := e.InspectGoogleQuery(ctx, id)
	return &q, err
}

func noGoogleQueryWindow(ctx context.Context, tx *sql.Tx, app string) error {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM google_query_locks WHERE app_id=?", app).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("Google application has an active or uncertain query; inspect its query window before Google editing")
	}
	return nil
}

func (e *Engine) reserveGoogleQuery(ctx context.Context, p Profile, r Request, kind string) (googleQueryJournal, error) {
	var j googleQueryJournal
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return j, err
	}
	defer tx.Rollback()
	var hash, state string
	if err = tx.QueryRowContext(ctx, "SELECT request_hash,state FROM operations WHERE operation_key=?", r.OperationKey).Scan(&hash, &state); err != nil {
		return j, err
	}
	if hash != requestHash(r, p) {
		return j, fmt.Errorf("Google query differs from the approved operation or destination")
	}
	var owner string
	err = tx.QueryRowContext(ctx, "SELECT operation_key FROM google_destinations WHERE app_id=?", p.AppID).Scan(&owner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return j, err
	}
	switch kind {
	case "status":
		if state != "complete" || owner != "" {
			return j, fmt.Errorf("status cannot enter an executing or uncertain publication's edit namespace")
		}
		if err = noUnmappedGoogleIntents(ctx, tx); err != nil {
			return j, err
		}
	case "recovery":
		if state != "pending" || owner != r.OperationKey {
			return j, fmt.Errorf("recovery query requires its own stopped publication hold")
		}
	default:
		return j, fmt.Errorf("unsupported Google query kind")
	}
	if err = noGoogleQueryWindow(ctx, tx, p.AppID); err != nil {
		return j, err
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return j, err
	}
	j.GoogleQuery = GoogleQuery{QueryID: hex.EncodeToString(random[:]), OperationKey: r.OperationKey, AppID: p.AppID, Track: p.Track, Kind: kind, State: "reserved", StartedAt: time.Now().UTC().Format(time.RFC3339Nano), WindowHeld: true}
	j.UpdatedAt, j.requestHash = j.StartedAt, hash
	if _, err = tx.ExecContext(ctx, `INSERT INTO google_queries(`+googleQueryColumns+`) VALUES(?,?,?,?,?,?,'reserved','','',?,?,'')`, j.QueryID, j.OperationKey, hash, j.AppID, j.Track, j.Kind, j.StartedAt, j.UpdatedAt); err != nil {
		return j, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO google_query_locks(app_id,query_id) VALUES(?,?)", j.AppID, j.QueryID); err != nil {
		return j, err
	}
	if err = tx.Commit(); err != nil {
		return j, err
	}
	return e.inspectGoogleQuery(ctx, j.QueryID)
}

// A mutation compares every stored field and the separately held query window.
// Closing the journal and releasing only its own window are one transaction.
func (e *Engine) updateGoogleQuery(ctx context.Context, old, next googleQueryJournal, release bool) (googleQueryJournal, error) {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return old, err
	}
	defer tx.Rollback()
	updated, err := tx.ExecContext(ctx, `UPDATE google_queries SET state=?,edit_id=?,cleanup_source=?,updated_at=?,reconciliation=?
	 WHERE query_id=? AND operation_key=? AND request_hash=? AND app_id=? AND track=? AND kind=?
	 AND state=? AND edit_id=? AND cleanup_source=? AND started_at=? AND updated_at=? AND reconciliation=?
	 AND EXISTS(SELECT 1 FROM google_query_locks WHERE app_id=google_queries.app_id AND query_id=google_queries.query_id)`,
		next.State, next.EditID, next.CleanupSource, next.UpdatedAt, next.reconciliation,
		old.QueryID, old.OperationKey, old.requestHash, old.AppID, old.Track, old.Kind,
		old.State, old.EditID, old.CleanupSource, old.StartedAt, old.UpdatedAt, old.reconciliation)
	if err != nil {
		return old, err
	}
	count, err := updated.RowsAffected()
	if err != nil || count != 1 {
		return old, fmt.Errorf("Google query journal or ownership changed; inspect its current snapshot")
	}
	if release {
		deleted, err := tx.ExecContext(ctx, "DELETE FROM google_query_locks WHERE app_id=? AND query_id=?", old.AppID, old.QueryID)
		if err != nil {
			return old, err
		}
		count, err := deleted.RowsAffected()
		if err != nil || count != 1 {
			return old, fmt.Errorf("Google query window owner changed before cleanup completion")
		}
	}
	if err = tx.Commit(); err != nil {
		return old, err
	}
	return e.inspectGoogleQuery(ctx, old.QueryID)
}

func (e *Engine) finishGoogleQuery(ctx context.Context, old googleQueryJournal, editID, source string, record *GoogleQueryReconciliation) (googleQueryJournal, error) {
	if source == "not-started" {
		if old.State != "reserved" || old.EditID != "" || editID != "" {
			return old, fmt.Errorf("Google query may already have created an edit; retain its window")
		}
	} else if (source != "delete-confirmed" && source != "already-absent") || editID == "" || (old.EditID != "" && old.EditID != editID) || (old.State != "created" && old.State != "creating") {
		return old, fmt.Errorf("Google query cleanup lacks its exact known edit")
	}
	next := old
	next.State, next.EditID, next.CleanupSource, next.UpdatedAt = "cleaned", editID, source, time.Now().UTC().Format(time.RFC3339Nano)
	if record != nil {
		record.ObservedAt = next.UpdatedAt
		body, _ := json.Marshal(record)
		next.reconciliation = string(body)
	}
	return e.updateGoogleQuery(ctx, old, next, true)
}

type googleQuerySession struct {
	engine  *Engine
	profile Profile
	journal googleQueryJournal
	client  *http.Client
	editID  string
}

func (e *Engine) openGoogleQuery(ctx context.Context, p Profile, r Request, kind, originalEdit string) (session *googleQuerySession, resultErr error) {
	j, err := e.reserveGoogleQuery(ctx, p, r, kind)
	if err != nil {
		return nil, err
	}
	session = &googleQuerySession{engine: e, profile: p, journal: j}
	defer func() {
		if resultErr != nil {
			if err := session.close(ctx); err != nil {
				resultErr = fmt.Errorf("%w; query cleanup unconfirmed, retain its durable window", resultErr)
			}
		}
	}()
	if session.client, err = e.googleClient(ctx, p); err != nil {
		return session, err
	}
	next := session.journal
	next.State, next.UpdatedAt = "creating", time.Now().UTC().Format(time.RFC3339Nano)
	if next, err = e.updateGoogleQuery(ctx, session.journal, next, false); err != nil {
		return session, err
	}
	session.journal = next
	var edit struct {
		ID string `json:"id"`
	}
	if err = e.googleCall(ctx, session.client, http.MethodPost, "/androidpublisher/v3/applications/"+url.PathEscape(p.AppID)+"/edits", struct{}{}, &edit); err != nil {
		return session, err
	}
	if edit.ID == "" || edit.ID == originalEdit {
		return session, fmt.Errorf("Google did not identify a distinct query edit; retain its durable window")
	}
	session.editID = edit.ID
	next = session.journal
	next.State, next.EditID, next.UpdatedAt = "created", edit.ID, time.Now().UTC().Format(time.RFC3339Nano)
	if next, err = e.updateGoogleQuery(ctx, session.journal, next, false); err != nil {
		return session, err
	}
	session.journal = next
	return session, nil
}

func deleteGoogleQueryEdit(ctx context.Context, e *Engine, client *http.Client, p Profile, editID string) (string, error) {
	q, err := http.NewRequestWithContext(ctx, http.MethodDelete, e.apiBase+editPath(p, editID), nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(q)
	if err != nil {
		return "", fmt.Errorf("Google query edit deletion interrupted; retain its durable window")
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return "delete-confirmed", nil
	}
	if response.StatusCode == http.StatusNotFound {
		return "already-absent", nil
	}
	return "", fmt.Errorf("Google query edit deletion returned HTTP %d; retain its durable window", response.StatusCode)
}

func (session *googleQuerySession) close(parent context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
	defer cancel()
	source := "not-started"
	if session.journal.State != "reserved" {
		if session.editID == "" || session.client == nil {
			return fmt.Errorf("Google query edit identity is unknown; retain its durable window")
		}
		var err error
		if source, err = deleteGoogleQueryEdit(ctx, session.engine, session.client, session.profile, session.editID); err != nil {
			return err
		}
	}
	updated, err := session.engine.finishGoogleQuery(ctx, session.journal, session.editID, source, nil)
	if err == nil {
		session.journal = updated
	}
	return err
}
