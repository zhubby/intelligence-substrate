package publisher

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Reservations belong to the one authoritative publisher journal for an app.
// A different track does not create an independent Google edit namespace.
type GoogleReservation struct {
	AppID           string `json:"appId"`
	Track           string `json:"track"`
	Version         string `json:"version"`
	ArtifactDigest  string `json:"artifactDigest"`
	VersionCode     string `json:"versionCode,omitempty"`
	Source          string `json:"source"`
	EnrolledAt      string `json:"enrolledAt"`
	ApplicationHeld bool   `json:"applicationHeld"`
}

func initializeGooglePublicationJournal(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS google_publications (
	 operation_key TEXT PRIMARY KEY, request_hash TEXT NOT NULL, app_id TEXT NOT NULL,
	 track TEXT NOT NULL, version TEXT NOT NULL, artifact_digest TEXT NOT NULL,
	 version_code TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT 'publish',
	 enrolled_at TEXT NOT NULL DEFAULT '', UNIQUE(app_id,version), UNIQUE(app_id,artifact_digest));
	 CREATE UNIQUE INDEX IF NOT EXISTS google_publication_codes ON google_publications(app_id,version_code) WHERE version_code<>'';
	 CREATE TABLE IF NOT EXISTS google_destinations (app_id TEXT PRIMARY KEY, operation_key TEXT NOT NULL UNIQUE);`)
	return err
}

func canonicalGoogleCode(code string) bool {
	n, err := strconv.ParseInt(code, 10, 32)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == code
}

func (e *Engine) googleReservation(ctx context.Context, key string) (*GoogleReservation, error) {
	var r GoogleReservation
	err := e.db.QueryRowContext(ctx, `SELECT app_id,track,version,artifact_digest,version_code,source,enrolled_at,
	 EXISTS(SELECT 1 FROM google_destinations d WHERE d.app_id=p.app_id AND d.operation_key=p.operation_key)
	 FROM google_publications p WHERE operation_key=?`, key).Scan(&r.AppID, &r.Track, &r.Version, &r.ArtifactDigest, &r.VersionCode, &r.Source, &r.EnrolledAt, &r.ApplicationHeld)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}

func reserveGooglePublication(ctx context.Context, tx *sql.Tx, p Profile, r Request, hash string, receipt Receipt, hold bool, source string) error {
	if receipt.VersionCode != "" && !canonicalGoogleCode(receipt.VersionCode) {
		return fmt.Errorf("persisted Google version code cannot be safely reserved")
	}
	if hold {
		if err := noGoogleQueryWindow(ctx, tx, p.AppID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO google_destinations(app_id,operation_key) VALUES(?,?) ON CONFLICT(app_id) DO NOTHING", p.AppID, r.OperationKey); err != nil {
			return err
		}
		var owner string
		if err := tx.QueryRowContext(ctx, "SELECT operation_key FROM google_destinations WHERE app_id=?", p.AppID).Scan(&owner); err != nil || owner != r.OperationKey {
			return fmt.Errorf("Google application has an executing or uncertain publication; reconcile it before another write")
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO google_publications(operation_key,request_hash,app_id,track,version,artifact_digest,version_code,source,enrolled_at)
	 VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(operation_key) DO NOTHING`, r.OperationKey, hash, p.AppID, p.Track, r.Version, r.ArtifactDigest, receipt.VersionCode, source, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("Google version, artifact or provider version code is already reserved; inspect the original journal")
	}
	var previousHash, app, track, version, digest, code string
	if err = tx.QueryRowContext(ctx, "SELECT request_hash,app_id,track,version,artifact_digest,version_code FROM google_publications WHERE operation_key=?", r.OperationKey).Scan(&previousHash, &app, &track, &version, &digest, &code); err != nil {
		return err
	}
	if previousHash != hash || app != p.AppID || track != p.Track || version != r.Version || digest != r.ArtifactDigest || code != receipt.VersionCode {
		return fmt.Errorf("Google reservation differs from the original approved operation")
	}
	return nil
}

// Earlier journals did not store a reversible profile identity. Recognizable
// Google receipts and unidentifiable pending intents must not be guessed into a
// destination or treated as proof that an upload never happened.
func noUnmappedGoogleIntents(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT o.state,o.receipt FROM operations o LEFT JOIN google_publications p ON p.operation_key=o.operation_key WHERE p.operation_key IS NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var state, text string
		if err = rows.Scan(&state, &text); err != nil {
			return err
		}
		var receipt Receipt
		if json.Unmarshal([]byte(text), &receipt) != nil {
			return fmt.Errorf("unmapped publisher journal is unreadable; inspect before Google editing")
		}
		google := receipt.EditID != "" || receipt.VersionCode != ""
		unknown := state == "pending" && receipt.Apple == nil && receipt.Huawei == nil && receipt.Xiaomi == nil && receipt.Oppo == nil && receipt.Vivo == nil
		if google || unknown {
			return fmt.Errorf("unmapped Google or unknown pending journal requires its original approved request and reconciliation before Google editing")
		}
	}
	return rows.Err()
}

func (e *Engine) executeGoogle(ctx context.Context, p Profile, r Request, hash string) (Result, error) {
	if r.Action == "continue" {
		return Result{}, fmt.Errorf("continuation is unsupported for this driver")
	}
	query := r.Action == "status"
	cleanup := func() {}
	var artifact string
	var err error
	if !query {
		artifact, cleanup, err = stageArtifact(r)
		if err != nil {
			return Result{}, err
		}
	}
	defer cleanup()
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	var previousHash, state, receiptText string
	var previousResult sql.NullString
	err = tx.QueryRowContext(ctx, "SELECT request_hash,state,receipt,result FROM operations WHERE operation_key=?", r.OperationKey).Scan(&previousHash, &state, &receiptText, &previousResult)
	if err == nil {
		if previousHash != hash {
			return Result{}, fmt.Errorf("operation key conflicts with approved binding or publishing destination")
		}
		if state != "complete" || !previousResult.Valid {
			return Result{}, fmt.Errorf("external result is uncertain; inspect persisted operation and reconcile without resubmitting")
		}
		var result Result
		var receipt Receipt
		if json.Unmarshal([]byte(previousResult.String), &result) != nil || json.Unmarshal([]byte(receiptText), &receipt) != nil || receipt.EditID == "" || !canonicalGoogleCode(receipt.VersionCode) || result.ExternalID != p.AppID+":"+receipt.VersionCode {
			return Result{}, fmt.Errorf("completed Google journal lacks exact application and upload identities")
		}
		if err = reserveGooglePublication(ctx, tx, p, r, hash, receipt, false, "legacy-complete-replay"); err != nil {
			return Result{}, err
		}
		if query {
			if r.ExternalID == "" || r.ExternalID != result.ExternalID {
				return Result{}, fmt.Errorf("query external ID differs from persisted upload")
			}
			var held int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM google_destinations WHERE app_id=?", p.AppID).Scan(&held); err != nil {
				return Result{}, err
			}
			if held != 0 {
				return Result{}, fmt.Errorf("Google application has an active or uncertain publication; status cannot enter its edit namespace")
			}
			if err = noUnmappedGoogleIntents(ctx, tx); err != nil {
				return Result{}, err
			}
		}
		if err = tx.Commit(); err != nil {
			return Result{}, err
		}
		if query {
			return e.googleStatus(ctx, p, r)
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}
	if query {
		return Result{}, fmt.Errorf("query requires the persisted completed approved operation")
	}
	if err = noUnmappedGoogleIntents(ctx, tx); err != nil {
		return Result{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(operation_key,request_hash,state,receipt,updated_at) VALUES(?,?,'pending','{}',?)", r.OperationKey, hash, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return Result{}, err
	}
	if err = reserveGooglePublication(ctx, tx, p, r, hash, Receipt{}, true, "publish"); err != nil {
		return Result{}, err
	}
	if err = tx.Commit(); err != nil {
		return Result{}, err
	}
	result, err := e.googlePublish(ctx, p, r, artifact)
	if err != nil {
		return Result{}, err
	}
	if err = e.finishGoogle(ctx, p, r, hash, result); err != nil {
		return Result{}, fmt.Errorf("Google operation completed but its journal or application ownership could not be confirmed; reconcile before further action")
	}
	return result, nil
}

func (e *Engine) checkpointGoogle(ctx context.Context, p Profile, r Request, receipt Receipt) error {
	if receipt.VersionCode != "" && !canonicalGoogleCode(receipt.VersionCode) {
		return fmt.Errorf("Google did not return a canonical positive version code")
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	updated, err := tx.ExecContext(ctx, `UPDATE google_publications SET version_code=? WHERE operation_key=? AND app_id=? AND request_hash=?
	 AND (version_code='' OR version_code=?) AND EXISTS(SELECT 1 FROM google_destinations WHERE app_id=? AND operation_key=google_publications.operation_key)
	 AND EXISTS(SELECT 1 FROM operations WHERE operation_key=google_publications.operation_key AND state='pending')
	 AND NOT EXISTS(SELECT 1 FROM google_query_locks WHERE app_id=google_publications.app_id)`, receipt.VersionCode, r.OperationKey, p.AppID, requestHash(r, p), receipt.VersionCode, p.AppID)
	if err != nil {
		return fmt.Errorf("Google upload version code could not be reserved; retain the unknown operation")
	}
	count, err := updated.RowsAffected()
	if err != nil || count != 1 {
		return fmt.Errorf("Google application ownership changed before checkpoint")
	}
	body, _ := json.Marshal(receipt)
	updated, err = tx.ExecContext(ctx, "UPDATE operations SET receipt=?,updated_at=? WHERE operation_key=? AND state='pending' AND request_hash=?", string(body), time.Now().UTC().Format(time.RFC3339Nano), r.OperationKey, requestHash(r, p))
	if err != nil {
		return err
	}
	count, err = updated.RowsAffected()
	if err != nil || count != 1 {
		return fmt.Errorf("Google operation changed before checkpoint")
	}
	return tx.Commit()
}

func releaseGoogleDestination(ctx context.Context, tx *sql.Tx, app, key string) error {
	deleted, err := tx.ExecContext(ctx, "DELETE FROM google_destinations WHERE app_id=? AND operation_key=?", app, key)
	if err != nil {
		return err
	}
	count, err := deleted.RowsAffected()
	if err != nil || count != 1 {
		return fmt.Errorf("Google application ownership changed before completion")
	}
	return nil
}

func (e *Engine) finishGoogle(ctx context.Context, p Profile, r Request, hash string, result Result) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	body, _ := json.Marshal(result)
	updated, err := tx.ExecContext(ctx, `UPDATE operations SET state='complete',result=?,updated_at=? WHERE operation_key=? AND state='pending' AND request_hash=?
	 AND EXISTS(SELECT 1 FROM google_destinations WHERE app_id=? AND operation_key=operations.operation_key)
	 AND EXISTS(SELECT 1 FROM google_publications WHERE operation_key=operations.operation_key AND app_id=? AND request_hash=operations.request_hash AND version_code<>'')
	 AND NOT EXISTS(SELECT 1 FROM google_query_locks WHERE app_id=?)`, string(body), time.Now().UTC().Format(time.RFC3339Nano), r.OperationKey, hash, p.AppID, p.AppID, p.AppID)
	if err != nil {
		return err
	}
	count, err := updated.RowsAffected()
	if err != nil || count != 1 {
		return fmt.Errorf("Google operation or application ownership changed before completion")
	}
	if err = releaseGoogleDestination(ctx, tx, p.AppID, r.OperationKey); err != nil {
		return err
	}
	return tx.Commit()
}

func (e *Engine) reserveStoppedGoogle(ctx context.Context, p Profile, r Request, j operationJournal) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldResult any
	if j.result.Valid {
		oldResult = j.result.String
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM operations WHERE operation_key=? AND state='pending' AND request_hash=? AND receipt=? AND result IS ? AND updated_at=?", r.OperationKey, j.requestHash, j.receipt, oldResult, j.updatedAt).Scan(&count); err != nil || count != 1 {
		return fmt.Errorf("Google journal changed before stopped-operation enrollment")
	}
	if err = reserveGooglePublication(ctx, tx, p, r, j.requestHash, j.Receipt, true, "legacy-stopped-recovery"); err != nil {
		return err
	}
	return tx.Commit()
}
