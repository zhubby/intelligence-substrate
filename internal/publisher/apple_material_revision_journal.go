package publisher

import (
	"context"
	"fmt"
	"time"
)

func appleVersionOwner(r Request, receipt *appleReceipt) string {
	if receipt.Revision != nil {
		return receipt.Revision.RootOperationKey
	}
	return r.OperationKey
}

func (e *Engine) appleReserveMaterialRevision(ctx context.Context, p Profile, r Request, hash string, receipt *appleReceipt, parent operationJournal) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM operations WHERE operation_key=? AND request_hash=? AND state='complete' AND receipt=? AND updated_at=?`, r.SourceOperationKey, parent.requestHash, parent.receipt, parent.updatedAt).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("source journal changed before material reservation")
	}
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM apple_versions WHERE app_id=? AND version=? AND build_number=? AND operation_key=?`, p.AppID, receipt.Version, receipt.BuildNumber, receipt.Revision.RootOperationKey).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("original version reservation changed")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO apple_destinations(app_id,operation_key) VALUES(?,?)", p.AppID, r.OperationKey); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO apple_material_revisions(parent_operation_key,operation_key) VALUES(?,?)", r.SourceOperationKey, r.OperationKey); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(operation_key,request_hash,state,receipt,updated_at) VALUES(?,?,'pending',?,?)", r.OperationKey, hash, mustAppleReceiptJSON(Receipt{Apple: receipt}), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}
