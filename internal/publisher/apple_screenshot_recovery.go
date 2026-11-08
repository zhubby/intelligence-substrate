package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
)

// Recovery observes a complete accepted image set. It never fills missing IDs
// or repairs drafts: ordinary protected continuation owns all later writes.
func (e *Engine) appleScreenshotRecovery(ctx context.Context, p Profile, r Request, receipt *appleReceipt) (Result, operationJournal, error) {
	parent, err := e.appleScreenshotRecoveryInputs(ctx, p, r, receipt, false)
	if err != nil {
		return Result{}, parent, err
	}
	// Accepted images may still be processing, while the original display stays.
	observed, err := e.appleScreenshotRevisionStatus(ctx, p, r, receipt)
	if err != nil {
		return Result{}, parent, err
	}
	observed.ContinueRequired = false
	observed.Reason = "准确截图提交与原展示项已只读核实，恢复处理等待；替换展示与重新提审仍须当前审批及独立质量门禁"
	return observed, parent, nil
}

// Both recovery paths bind the exact approved bytes and source. The placed
// path additionally requires every unique display ID to have been persisted.
func (e *Engine) appleScreenshotRecoveryInputs(ctx context.Context, p Profile, r Request, receipt *appleReceipt, placed bool) (operationJournal, error) {
	var parent operationJournal
	revision := receipt.Revision
	if receipt.Material == nil || revision == nil || r.MaterialScope != "screenshots" || revision.MaterialScope != "screenshots" || !revision.ScreenshotsChanged || revision.RevisionID != r.MaterialRevisionID || revision.Binding != r.Binding || revision.BaseHash != appleMaterialBaseHash(r, p) || revision.SourceOperationKey != r.SourceOperationKey {
		return parent, fmt.Errorf("screenshot recovery requires the exact approved revision and source binding")
	}
	parent, err := e.inspectJournal(ctx, r.SourceOperationKey)
	if err != nil || parent.State != "complete" || parent.Result == nil || parent.JournalDigest != revision.SourceJournalDigest || parent.Receipt.Apple == nil || parent.Receipt.Apple.Stage != "submitted" || parent.Receipt.Apple.Material == nil || parent.Receipt.Apple.Material.Digest != r.SourceMetadataDigest {
		return parent, fmt.Errorf("screenshot recovery source journal changed or is incomplete")
	}
	approved, err := stageAppleMaterial(p, r)
	if err != nil {
		return parent, err
	}
	defer os.RemoveAll(approved.Dir)
	// The approved manifest has no server resource IDs. Remove only those IDs
	// from a deep copy, keeping the original receipt intact for observation/CAS.
	body, err := json.Marshal(receipt.Material)
	if err != nil {
		return parent, err
	}
	var material appleMaterialSnapshot
	if err = json.Unmarshal(body, &material); err != nil {
		return parent, err
	}
	material.LibraryID, material.AppInfoID = "", ""
	placements := map[string]bool{}
	for li := range material.Locales {
		locale := &material.Locales[li]
		locale.VersionLocalizationID, locale.InfoLocalizationID = "", ""
		for gi := range locale.Screenshots {
			for ii := range locale.Screenshots[gi].Images {
				image := &locale.Screenshots[gi].Images[ii]
				if !appleResourceID.MatchString(image.ImageID) || (!placed && image.PlacementID != "") || (placed && (!appleResourceID.MatchString(image.PlacementID) || placements[image.PlacementID])) {
					return parent, fmt.Errorf("all approved image and expected unique placement IDs must be persisted exactly")
				}
				placements[image.PlacementID] = true
				image.ImageID, image.PlacementID = "", ""
			}
		}
	}
	if !reflect.DeepEqual(&material, approved.Snapshot) {
		return parent, fmt.Errorf("persisted screenshot material differs from the exact approved manifest and image bytes")
	}
	return parent, nil
}
