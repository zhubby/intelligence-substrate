package publisher

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type storeRowReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func initializeStoreRecovery(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS store_reconciliations (
	 operation_key TEXT NOT NULL, journal_digest TEXT NOT NULL, input_digest TEXT NOT NULL UNIQUE, record TEXT NOT NULL,
	 PRIMARY KEY(operation_key,journal_digest));
	CREATE TABLE IF NOT EXISTS store_reconciliation_evidence (
	 input_digest TEXT NOT NULL, name TEXT NOT NULL, digest TEXT NOT NULL, bytes INTEGER NOT NULL, body BLOB NOT NULL,
	 PRIMARY KEY(input_digest,name));
	CREATE TABLE IF NOT EXISTS store_recovery_versions (
	 driver TEXT NOT NULL, environment TEXT NOT NULL, app_id TEXT NOT NULL, version_code INTEGER NOT NULL,
	 operation_key TEXT NOT NULL UNIQUE, PRIMARY KEY(driver,environment,app_id,version_code));`)
	return err
}

func (e *Engine) storeReconciliations(ctx context.Context, key string) ([]StoreReconciliation, error) {
	rows, err := e.db.QueryContext(ctx, "SELECT record FROM store_reconciliations WHERE operation_key=? ORDER BY rowid", key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var history []StoreReconciliation
	for rows.Next() {
		var body string
		var r StoreReconciliation
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(body), &r); err != nil {
			return nil, err
		}
		history = append(history, r)
	}
	return history, rows.Err()
}

func storeDestination(p Profile) (string, string, []any) {
	switch p.Driver {
	case "huawei-appgallery":
		return "huawei_destinations", "app_id=?", []any{p.AppID}
	case "xiaomi-market":
		return "xiaomi_destinations", "package_name=?", []any{p.AppID}
	case "oppo-store":
		return "oppo_destinations", "package_name=?", []any{p.BundleID}
	case "vivo-store":
		return "vivo_destinations", "environment=? AND package_name=?", []any{p.Environment, p.AppID}
	}
	panic("store destination used without domestic profile validation")
}

func checkStoreRecoveryOwners(ctx context.Context, db storeRowReader, p Profile, key string, code int64) error {
	table, where, args := storeDestination(p)
	var owner string
	if err := db.QueryRowContext(ctx, "SELECT operation_key FROM "+table+" WHERE "+where, args...).Scan(&owner); err != nil || owner != key {
		return fmt.Errorf("recovery no longer owns the exact destination; retain pending operation")
	}
	if p.Driver == "oppo-store" {
		if err := db.QueryRowContext(ctx, "SELECT operation_key FROM oppo_versions WHERE package_name=? AND version_code=?", p.BundleID, code).Scan(&owner); err != nil || owner != key {
			return fmt.Errorf("recovery does not own the OPPO version")
		}
	}
	if p.Driver == "vivo-store" {
		if err := db.QueryRowContext(ctx, "SELECT operation_key FROM vivo_versions WHERE environment=? AND package_name=? AND version_code=?", p.Environment, p.AppID, code).Scan(&owner); err != nil || owner != key {
			return fmt.Errorf("recovery does not own the vivo environment/version")
		}
	}
	return nil
}

// A recovered version remains reserved even if the upstream later hides it.
// A strictly newer approved candidate is required under a different operation.
func checkRecoveredStoreVersion(ctx context.Context, db storeRowReader, p Profile, key string, code int64) error {
	var n int
	err := db.QueryRowContext(ctx, "SELECT count(*) FROM store_recovery_versions WHERE driver=? AND environment=? AND app_id=? AND version_code>=? AND operation_key<>?", p.Driver, storeEnvironment(p), p.AppID, code, key).Scan(&n)
	if err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("a recovered store version cannot be reused or superseded by a lower version under another operation")
	}
	return nil
}

func (e *Engine) commitStoreRecovery(ctx context.Context, p Profile, j operationJournal, record StoreReconciliation, result Result, evidence *storePortalBundle) (Operation, error) {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	if err = checkStoreRecoveryOwners(ctx, tx, p, j.OperationKey, record.VersionCode); err != nil {
		return Operation{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO store_recovery_versions(driver,environment,app_id,version_code,operation_key) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING", p.Driver, storeEnvironment(p), p.AppID, record.VersionCode, j.OperationKey); err != nil {
		return Operation{}, err
	}
	var owner string
	if err = tx.QueryRowContext(ctx, "SELECT operation_key FROM store_recovery_versions WHERE driver=? AND environment=? AND app_id=? AND version_code=?", p.Driver, storeEnvironment(p), p.AppID, record.VersionCode).Scan(&owner); err != nil || owner != j.OperationKey {
		return Operation{}, fmt.Errorf("recovery version is already owned by another operation")
	}
	receipt, _ := json.Marshal(j.Receipt)
	output, _ := json.Marshal(result)
	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	var oldResult any
	if j.result.Valid {
		oldResult = j.result.String
	}
	updated, err := tx.ExecContext(ctx, `UPDATE operations SET state='complete',receipt=?,result=?,updated_at=?
	WHERE operation_key=? AND request_hash=? AND state=? AND receipt=? AND result IS ? AND updated_at=?`, string(receipt), string(output), updatedAt, j.OperationKey, j.requestHash, j.State, j.receipt, oldResult, j.updatedAt)
	if err != nil {
		return Operation{}, err
	}
	if n, err := updated.RowsAffected(); err != nil || n != 1 {
		return Operation{}, fmt.Errorf("journal changed during observation; inspect before further action")
	}
	record.ResultJournalDigest = operationJournalDigest(j.OperationKey, j.requestHash, "complete", string(receipt), sql.NullString{String: string(output), Valid: true}, updatedAt)
	body, _ := json.Marshal(record)
	if _, err = tx.ExecContext(ctx, "INSERT INTO store_reconciliations(operation_key,journal_digest,input_digest,record) VALUES(?,?,?,?)", j.OperationKey, record.JournalDigest, record.InputDigest, string(body)); err != nil {
		return Operation{}, err
	}
	if evidence != nil {
		for _, b := range evidence.bodies {
			if _, err = tx.ExecContext(ctx, "INSERT INTO store_reconciliation_evidence(input_digest,name,digest,bytes,body) VALUES(?,?,?,?,?)", record.InputDigest, b.name, b.digest, len(b.body), b.body); err != nil {
				return Operation{}, err
			}
		}
	}
	release := p.Driver != "huawei-appgallery" || result.State == "published" || result.State == "rejected" || result.State == "halted"
	if release {
		table, where, args := storeDestination(p)
		args = append(args, j.OperationKey)
		deleted, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+where+" AND operation_key=?", args...)
		if err != nil {
			return Operation{}, err
		}
		if n, err := deleted.RowsAffected(); err != nil || n != 1 {
			return Operation{}, fmt.Errorf("destination changed before recovery completion")
		}
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, err
	}
	return e.Inspect(ctx, j.OperationKey)
}
