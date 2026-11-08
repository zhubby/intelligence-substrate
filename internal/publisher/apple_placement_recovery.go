package publisher

import (
	"context"
	"fmt"
	"time"
)

// Complete ordered placements can be observed without repeating ordering or
// deletion. A possibly accepted resolve is distinguished from a rejected item.
func (e *Engine) applePlacementRecovery(ctx context.Context, p Profile, r Request, receipt *appleReceipt) (Result, operationJournal, string, error) {
	parent, err := e.appleScreenshotRecoveryInputs(ctx, p, r, receipt, true)
	if err != nil {
		return Result{}, parent, "", err
	}
	allowed := []string{"REJECTED"}
	if receipt.Stage == "material_resolve_intent" {
		allowed = append(allowed, "READY_FOR_REVIEW")
	} else if receipt.Stage == "material_resolution_confirmed" {
		allowed = []string{"READY_FOR_REVIEW"}
	}
	resolved, err := e.appleMaterialReviewState(ctx, p, receipt, allowed...)
	if err != nil {
		return Result{}, parent, "", err
	}
	ready, err := e.appleVerifyMaterials(ctx, p, receipt, true, true)
	if err != nil || !ready {
		if err == nil {
			err = fmt.Errorf("every exact replacement image and ordered placement must be ready")
		}
		return Result{}, parent, "", err
	}
	stage := "material_placement_confirmed"
	if resolved {
		stage = "material_resolution_confirmed"
	}
	result := Result{ExternalID: r.ExternalID, State: "processing", UpstreamState: "MATERIAL_DISPLAY_CONFIRMED",
		Reason: "准确新展示与原审核项已只读核实；剩余审核处理和重新提审须当前审批及独立质量门禁"}
	if resolved {
		result.UpstreamState = "MATERIAL_RESOLUTION_CONFIRMED"
	}
	return result, parent, stage, nil
}

// Recovery is a durable wait. Only a subsequent observation can request an
// ordinary protected continuation, and it must still see this exact review state.
func (e *Engine) applePlacementStatus(ctx context.Context, p Profile, r Request, receipt *appleReceipt) (Result, error) {
	if !containsApple([]string{"material_placement_confirmed", "material_resolution_confirmed"}, receipt.Stage) {
		return Result{}, fmt.Errorf("complete screenshot display has not been independently confirmed")
	}
	if _, err := e.appleScreenshotRevisionParent(ctx, r, receipt); err != nil {
		return Result{}, err
	}
	resolved := receipt.Stage == "material_resolution_confirmed"
	if err := e.appleMaterialReview(ctx, p, receipt, resolved); err != nil {
		return Result{}, err
	}
	ready, err := e.appleVerifyMaterials(ctx, p, receipt, true, true)
	if err != nil || !ready {
		if err == nil {
			err = fmt.Errorf("confirmed screenshot display changed or is not ready")
		}
		return Result{}, err
	}
	return Result{ExternalID: r.ExternalID, State: "processing", UpstreamState: "MATERIAL_RESUBMISSION_READY", ContinueRequired: true,
		Reason: "准确展示与原审核项再次核实，可在当前审批及质量门禁下完成剩余审核操作"}, nil
}

func (e *Engine) appleContinuePlacedScreenshots(ctx context.Context, p Profile, r Request, hash string, journal operationJournal) (Result, error) {
	receipt := journal.Receipt.Apple
	if journal.State != "complete" || journal.Result == nil || journal.Result.State != "processing" || receipt == nil {
		return Result{}, fmt.Errorf("screenshot display is not a confirmed protected wait")
	}
	observed, err := e.applePlacementStatus(ctx, p, r, receipt)
	if err != nil || !observed.ContinueRequired {
		return observed, err
	}
	parent, err := e.inspectJournal(ctx, r.SourceOperationKey)
	if err != nil || receipt.Revision == nil || parent.State != "complete" || parent.JournalDigest != receipt.Revision.SourceJournalDigest {
		return Result{}, fmt.Errorf("source changed during screenshot continuation observation")
	}
	var sourceResult, oldResult any
	if parent.result.Valid {
		sourceResult = parent.result.String
	}
	if journal.result.Valid {
		oldResult = journal.result.String
	}
	changed, err := e.db.ExecContext(ctx, `UPDATE operations SET state='pending',updated_at=?
	 WHERE operation_key=? AND request_hash=? AND state='complete' AND receipt=? AND result IS ? AND updated_at=?
	 AND EXISTS(SELECT 1 FROM apple_destinations WHERE app_id=? AND operation_key=operations.operation_key)
	 AND EXISTS(SELECT 1 FROM apple_versions WHERE app_id=? AND version=? AND build_number=? AND operation_key=?)
	 AND EXISTS(SELECT 1 FROM apple_material_revisions WHERE parent_operation_key=? AND operation_key=operations.operation_key)
	 AND EXISTS(SELECT 1 FROM operations source WHERE source.operation_key=? AND source.state='complete'
	 AND source.request_hash=? AND source.receipt=? AND source.result IS ? AND source.updated_at=?)`,
		time.Now().UTC().Format(time.RFC3339Nano), r.OperationKey, hash, journal.receipt, oldResult, journal.updatedAt,
		p.AppID, p.AppID, receipt.Version, receipt.BuildNumber, receipt.Revision.RootOperationKey, r.SourceOperationKey,
		parent.OperationKey, parent.requestHash, parent.receipt, sourceResult, parent.updatedAt)
	if err != nil {
		return Result{}, err
	}
	n, err := changed.RowsAffected()
	if err != nil || n != 1 {
		return Result{}, fmt.Errorf("screenshot continuation already claimed or source/authority changed")
	}
	return e.appleResubmitMaterialRevision(ctx, p, r, hash, *receipt)
}
