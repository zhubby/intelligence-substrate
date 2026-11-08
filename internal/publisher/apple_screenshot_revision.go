package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

// Reuse only the exact immutable image identity. All placements belong to this
// version and are replaced after the complete desired set finishes processing.
func appleUseRevisedScreenshots(receipt *appleReceipt, desired *appleMaterialSnapshot, source *appleReceipt) {
	for li, locale := range desired.Locales {
		groups := make([]appleGroupSnapshot, len(locale.Screenshots))
		for gi, group := range locale.Screenshots {
			groups[gi] = appleGroupSnapshot{PlacementGroup: group.PlacementGroup}
			for _, image := range group.Images {
				for _, oldGroup := range source.Material.Locales[li].Screenshots {
					if oldGroup.PlacementGroup != group.PlacementGroup {
						continue
					}
					for _, old := range oldGroup.Images {
						identity := old
						identity.ImageID, identity.PlacementID = "", ""
						if reflect.DeepEqual(identity, image) {
							image.ImageID = old.ImageID
							break
						}
					}
				}
				image.PlacementID = ""
				groups[gi].Images = append(groups[gi].Images, image)
			}
		}
		receipt.Material.Locales[li].Screenshots = groups
	}
}

func (e *Engine) appleScreenshotRevisionParent(ctx context.Context, r Request, receipt *appleReceipt) (*appleReceipt, error) {
	if receipt.Revision == nil || receipt.Revision.MaterialScope != "screenshots" || !receipt.Revision.ScreenshotsChanged || receipt.Revision.SourceOperationKey != r.SourceOperationKey {
		return nil, fmt.Errorf("screenshot continuation lacks its exact approved material scope")
	}
	parent, err := e.inspectJournal(ctx, r.SourceOperationKey)
	if err != nil || parent.State != "complete" || parent.JournalDigest != receipt.Revision.SourceJournalDigest || parent.Receipt.Apple == nil || parent.Receipt.Apple.Material == nil || parent.Receipt.Apple.Material.Digest != r.SourceMetadataDigest {
		return nil, fmt.Errorf("original screenshot journal changed; reconcile without mutation")
	}
	return parent.Receipt.Apple, nil
}

// While new images process, the current version must still display the exact
// old placements, together with the newly approved text. No write occurs here.
func (e *Engine) appleScreenshotRevisionStatus(ctx context.Context, p Profile, r Request, receipt *appleReceipt) (Result, error) {
	source, err := e.appleScreenshotRevisionParent(ctx, r, receipt)
	if err != nil {
		return Result{}, err
	}
	if err = e.appleMaterialReview(ctx, p, receipt, false); err != nil {
		return Result{}, err
	}
	if err = e.appleMaterialCatalog(ctx, p, receipt.Material); err != nil {
		return Result{}, err
	}
	hybrid := *receipt
	material := *receipt.Material
	material.Locales = append([]appleLocaleSnapshot(nil), receipt.Material.Locales...)
	for i := range material.Locales {
		material.Locales[i].Screenshots = source.Material.Locales[i].Screenshots
	}
	hybrid.Material = &material
	if ready, err := e.appleVerifyMaterials(ctx, p, &hybrid, true, true); err != nil || !ready {
		if err == nil {
			err = fmt.Errorf("original screenshot state changed during replacement processing")
		}
		return Result{}, err
	}
	ready := true
	for _, locale := range receipt.Material.Locales {
		for _, group := range locale.Screenshots {
			for _, image := range group.Images {
				processed, err := e.appleVerifyMaterialImage(ctx, p, receipt.Material.LibraryID, image, true)
				if err != nil {
					return Result{}, err
				}
				ready = ready && processed
			}
		}
	}
	result := Result{ExternalID: r.ExternalID, State: "processing", UpstreamState: "MATERIAL_PROCESSING", Reason: "原构建与展示截图保持，等待新截图处理；尚未重新提审"}
	if ready {
		result.ContinueRequired, result.UpstreamState = true, "MATERIAL_READY"
		result.Reason = "新截图已就绪，可按准确资料批准替换展示项并重新提审"
	}
	return result, nil
}

func (e *Engine) appleStartScreenshotRevision(ctx context.Context, p Profile, r Request, hash string, receipt *appleReceipt, material *appleMaterial) (Result, error) {
	for li := range receipt.Material.Locales {
		for gi := range receipt.Material.Locales[li].Screenshots {
			for ii := range receipt.Material.Locales[li].Screenshots[gi].Images {
				image := &receipt.Material.Locales[li].Screenshots[gi].Images[ii]
				if image.ImageID == "" {
					if err := e.appleUploadMaterialImage(ctx, p, r, receipt, material, image); err != nil {
						return Result{}, err
					}
				}
			}
		}
	}
	if err := e.appleCheckpoint(ctx, r, receipt, "material_images_processing"); err != nil {
		return Result{}, err
	}
	result, err := e.appleScreenshotRevisionStatus(ctx, p, r, receipt)
	if err != nil {
		return Result{}, err
	}
	// A successful upload is a durable wait. A separate read-only observation
	// authorizes the continuation, so the Worker releases its active lease now.
	result.ContinueRequired = false
	if err = e.appleFinish(ctx, p, r, hash, receipt, result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (e *Engine) appleContinueScreenshotRevision(ctx context.Context, p Profile, r Request, hash string, journal operationJournal) (Result, error) {
	receipt := journal.Receipt.Apple
	if journal.State != "complete" || journal.Result == nil || journal.Result.State != "processing" || receipt.Stage != "material_images_processing" {
		return Result{}, fmt.Errorf("screenshot operation is not a confirmed processing wait")
	}
	observed, err := e.appleScreenshotRevisionStatus(ctx, p, r, receipt)
	if err != nil || !observed.ContinueRequired {
		return observed, err
	}
	changed, err := e.db.ExecContext(ctx, `UPDATE operations SET state='pending',updated_at=?
	 WHERE operation_key=? AND request_hash=? AND state='complete' AND receipt=? AND updated_at=?
	 AND EXISTS(SELECT 1 FROM apple_destinations WHERE app_id=? AND operation_key=operations.operation_key)
	 AND EXISTS(SELECT 1 FROM apple_versions WHERE app_id=? AND version=? AND build_number=? AND operation_key=?)
	 AND EXISTS(SELECT 1 FROM apple_material_revisions WHERE parent_operation_key=? AND operation_key=operations.operation_key)`,
		time.Now().UTC().Format(time.RFC3339Nano), r.OperationKey, hash, journal.receipt, journal.updatedAt,
		p.AppID, p.AppID, receipt.Version, receipt.BuildNumber, receipt.Revision.RootOperationKey, r.SourceOperationKey)
	if err != nil {
		return Result{}, err
	}
	n, err := changed.RowsAffected()
	if err != nil || n != 1 {
		return Result{}, fmt.Errorf("screenshot continuation already reserved or authority changed")
	}
	if err = e.appleDeletePreviousPlacements(ctx, p, r, receipt); err != nil {
		return Result{}, err
	}
	if err = e.applePlaceMaterials(ctx, p, r, receipt, true); err != nil {
		return Result{}, err
	}
	return e.appleResubmitMaterialRevision(ctx, p, r, hash, *receipt)
}

func (e *Engine) appleDeletePreviousPlacements(ctx context.Context, p Profile, r Request, receipt *appleReceipt) error {
	source, err := e.appleScreenshotRevisionParent(ctx, r, receipt)
	if err != nil {
		return err
	}
	var remaining appleMaterialSnapshot
	body, err := json.Marshal(source.Material)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(body, &remaining); err != nil {
		return err
	}
	for li := range remaining.Locales {
		locale := &remaining.Locales[li]
		ids := []string{}
		for _, group := range locale.Screenshots {
			for _, image := range group.Images {
				ids = append(ids, image.PlacementID)
			}
		}
		for _, id := range ids {
			if err = e.appleMaterialMedia(ctx, p, locale, true, true); err != nil {
				return err
			}
			if err = e.appleCheckpoint(ctx, r, receipt, "material_placement_delete_intent"); err != nil {
				return err
			}
			if err = e.appleCall(ctx, p, "DELETE", "/v1/appAssetLibraryPlacements/"+id, nil, nil); err != nil {
				return err
			}
			for gi := range locale.Screenshots {
				images := locale.Screenshots[gi].Images
				for ii, image := range images {
					if image.PlacementID == id {
						locale.Screenshots[gi].Images = append(images[:ii], images[ii+1:]...)
						break
					}
				}
			}
			if err = e.appleMaterialMedia(ctx, p, locale, true, true); err != nil {
				return err
			}
		}
	}
	return nil
}
