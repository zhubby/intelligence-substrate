package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

func (e *Engine) appleReserve(ctx context.Context, p Profile, r Request, hash string, receipt *appleReceipt) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if receipt.Material != nil {
		if _, err = tx.ExecContext(ctx, "INSERT INTO apple_destinations(app_id,operation_key) VALUES(?,?)", p.AppID, r.OperationKey); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO apple_versions(app_id,version,build_number,operation_key) VALUES(?,?,?,?)", p.AppID, receipt.Version, receipt.BuildNumber, r.OperationKey); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(operation_key,request_hash,state,receipt,updated_at) VALUES(?,?,'pending',?,?)", r.OperationKey, hash, mustAppleReceiptJSON(Receipt{Apple: receipt}), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (e *Engine) appleMaterialCheckpoint(ctx context.Context, r Request, receipt *appleReceipt) error {
	p, err := e.profile(r)
	if err != nil {
		return err
	}
	j, err := e.inspectJournal(ctx, r.OperationKey)
	if err != nil {
		return err
	}
	if j.State != "pending" || j.requestHash != requestHash(r, p) || j.Receipt.Apple == nil || j.Receipt.Apple.Material == nil || j.Receipt.Apple.Material.Digest != p.MetadataDigest || receipt.Material.Digest != p.MetadataDigest || receipt.Material.AppID != p.AppID {
		return fmt.Errorf("App Store material operation binding changed before external write")
	}
	if receipt.Revision != nil {
		parent, err := e.inspectJournal(ctx, receipt.Revision.SourceOperationKey)
		if err != nil || parent.State != "complete" || parent.JournalDigest != receipt.Revision.SourceJournalDigest {
			return fmt.Errorf("source material journal changed before external write")
		}
	}
	result, err := e.db.ExecContext(ctx, `UPDATE operations SET receipt=?,updated_at=? WHERE operation_key=? AND request_hash=? AND state='pending' AND receipt=? AND updated_at=?
	 AND EXISTS(SELECT 1 FROM apple_destinations WHERE app_id=? AND operation_key=operations.operation_key)
	 AND EXISTS(SELECT 1 FROM apple_versions WHERE app_id=? AND version=? AND build_number=? AND operation_key=?)
	 AND (?='' OR EXISTS(SELECT 1 FROM apple_material_revisions WHERE parent_operation_key=? AND operation_key=operations.operation_key))`, mustAppleReceiptJSON(Receipt{Apple: receipt}), time.Now().UTC().Format(time.RFC3339Nano), r.OperationKey, j.requestHash, j.receipt, j.updatedAt, p.AppID, p.AppID, receipt.Version, receipt.BuildNumber, appleVersionOwner(r, receipt), r.SourceOperationKey, r.SourceOperationKey)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("App Store material reservation changed before external write")
	}
	return nil
}

func (e *Engine) appleFinish(ctx context.Context, p Profile, r Request, hash string, receipt *appleReceipt, result Result) error {
	if receipt.Material == nil {
		return e.finish(ctx, r.OperationKey, result)
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	body, _ := json.Marshal(result)
	changed, err := tx.ExecContext(ctx, `UPDATE operations SET state='complete',result=?,updated_at=? WHERE operation_key=? AND request_hash=? AND state='pending' AND receipt=?
	 AND EXISTS(SELECT 1 FROM apple_destinations WHERE app_id=? AND operation_key=operations.operation_key)
	 AND EXISTS(SELECT 1 FROM apple_versions WHERE app_id=? AND version=? AND build_number=? AND operation_key=?)
	 AND (?='' OR EXISTS(SELECT 1 FROM apple_material_revisions WHERE parent_operation_key=? AND operation_key=operations.operation_key))`, string(body), time.Now().UTC().Format(time.RFC3339Nano), r.OperationKey, hash, mustAppleReceiptJSON(Receipt{Apple: receipt}), p.AppID, p.AppID, receipt.Version, receipt.BuildNumber, appleVersionOwner(r, receipt), r.SourceOperationKey, r.SourceOperationKey)
	if err != nil {
		return err
	}
	n, err := changed.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("App Store material completion reservation changed")
	}
	if receipt.Stage == "submitted" && result.State == "awaiting_review" {
		if _, err = tx.ExecContext(ctx, "DELETE FROM apple_destinations WHERE app_id=? AND operation_key=?", p.AppID, r.OperationKey); err != nil {
			return err
		}
	}
	return tx.Commit()
}
