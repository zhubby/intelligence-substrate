package publisher

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"time"
)

type appleMaterialRevisionReceipt struct {
	RevisionID          string `json:"revisionId"`
	Binding             string `json:"binding"`
	BaseHash            string `json:"baseHash"`
	SourceOperationKey  string `json:"sourceOperationKey"`
	SourceJournalDigest string `json:"sourceJournalDigest"`
	RootOperationKey    string `json:"rootOperationKey"`
	MaterialScope       string `json:"materialScope,omitempty"`
	ScreenshotsChanged  bool   `json:"screenshotsChanged,omitempty"`
}

// This hash retains the exact package, candidate and administrator destination.
// Only the distinct material approval and predecessor identities are excluded.
func appleMaterialBaseHash(r Request, p Profile) string {
	r.OperationKey, r.Binding, r.MetadataDigest = "", "", ""
	r.MaterialRevisionID, r.SourceOperationKey, r.SourceBinding, r.SourceMetadataDigest = "", "", "", ""
	p.MetadataDigest, r.MaterialScope = "", ""
	return requestHash(r, p)
}

func (e *Engine) executeAppleMaterialRevision(ctx context.Context, p Profile, r Request, hash string) (Result, error) {
	if err := appleMaterialBinding(p, r); err != nil {
		return Result{}, err
	}
	var child int
	if err := e.db.QueryRowContext(ctx, "SELECT count(*) FROM apple_material_revisions WHERE parent_operation_key=?", r.OperationKey).Scan(&child); err != nil || child != 0 {
		return Result{}, fmt.Errorf("material operation is superseded; inspect its successor")
	}
	j, err := e.inspectJournal(ctx, r.OperationKey)
	if err == nil {
		receipt := j.Receipt.Apple
		if j.requestHash != hash || receipt == nil || receipt.Revision == nil || receipt.Revision.RevisionID != r.MaterialRevisionID || receipt.Revision.MaterialScope != r.MaterialScope || r.ExternalID != appleExternalID(p, receipt) {
			return Result{}, fmt.Errorf("material operation differs from exact approved revision")
		}
		if j.State != "complete" || j.Result == nil {
			return Result{}, fmt.Errorf("material write is uncertain at %s; reconcile exact journal without retry", receipt.Stage)
		}
		if receipt.Stage == "material_images_processing" {
			if r.Action == "status" {
				return e.appleScreenshotRevisionStatus(ctx, p, r, receipt)
			}
			if r.Action == "continue" {
				return e.appleContinueScreenshotRevision(ctx, p, r, hash, j)
			}
		}
		if containsApple([]string{"material_placement_confirmed", "material_resolution_confirmed"}, receipt.Stage) {
			if r.Action == "status" {
				return e.applePlacementStatus(ctx, p, r, receipt)
			}
			if r.Action == "continue" {
				return e.appleContinuePlacedScreenshots(ctx, p, r, hash, j)
			}
		}
		if r.Action == "status" {
			return e.appleStatus(ctx, p, r, receipt)
		}
		return *j.Result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}
	if r.Action != "materials" {
		return Result{}, fmt.Errorf("material status requires a persisted revision operation")
	}
	parent, err := e.inspectJournal(ctx, r.SourceOperationKey)
	if err != nil {
		return Result{}, fmt.Errorf("exact predecessor journal required: %w", err)
	}
	source := parent.Receipt.Apple
	if parent.State != "complete" || parent.Result == nil || source == nil || source.Stage != "submitted" || source.Material == nil || source.Material.Digest != r.SourceMetadataDigest || r.ExternalID != appleExternalID(p, source) {
		return Result{}, fmt.Errorf("material predecessor is incomplete or differs from approved source")
	}
	root := r.SourceOperationKey
	if source.Revision == nil {
		previous := r
		previous.OperationKey, previous.Binding, previous.MetadataDigest = r.SourceOperationKey, r.SourceBinding, r.SourceMetadataDigest
		previous.MaterialRevisionID, previous.SourceOperationKey, previous.SourceBinding, previous.SourceMetadataDigest, previous.MaterialScope = "", "", "", "", ""
		oldProfile := p
		oldProfile.MetadataDigest = r.SourceMetadataDigest
		if requestHash(previous, oldProfile) != parent.requestHash {
			return Result{}, fmt.Errorf("source package, release or administrator profile changed")
		}
	} else {
		if source.Revision.Binding != r.SourceBinding || source.Revision.BaseHash != appleMaterialBaseHash(r, p) {
			return Result{}, fmt.Errorf("source material approval or immutable publication identity changed")
		}
		root = source.Revision.RootOperationKey
	}
	material, err := stageAppleMaterial(p, r)
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(material.Dir)
	// Copy only the approved text into a clone of the exact published snapshot.
	b, _ := json.Marshal(source)
	var receipt appleReceipt
	_ = json.Unmarshal(b, &receipt)
	if len(material.Snapshot.Locales) != len(source.Material.Locales) {
		return Result{}, fmt.Errorf("text correction cannot add or remove locales")
	}
	changed, screenshotsChanged := false, false
	for i, locale := range material.Snapshot.Locales {
		old := source.Material.Locales[i]
		copyBody, _ := json.Marshal(old)
		var compare appleLocaleSnapshot
		_ = json.Unmarshal(copyBody, &compare)
		compare.VersionLocalizationID, compare.InfoLocalizationID = "", ""
		compare.VersionText, compare.AppInfo = locale.VersionText, locale.AppInfo
		for g := range compare.Screenshots {
			for n := range compare.Screenshots[g].Images {
				compare.Screenshots[g].Images[n].ImageID = ""
				compare.Screenshots[g].Images[n].PlacementID = ""
			}
		}
		if compare.Locale != locale.Locale || (r.MaterialScope != "screenshots" && !reflect.DeepEqual(compare, locale)) {
			return Result{}, fmt.Errorf("text correction must preserve exact locales and screenshot bytes/order/specifications")
		}
		screenshotsChanged = screenshotsChanged || !reflect.DeepEqual(compare.Screenshots, locale.Screenshots)
		changed = changed || old.VersionText != locale.VersionText || old.AppInfo != locale.AppInfo || screenshotsChanged
		receipt.Material.Locales[i].VersionText, receipt.Material.Locales[i].AppInfo = locale.VersionText, locale.AppInfo
	}
	if !changed {
		return Result{}, fmt.Errorf("material revision must change approved text, links or screenshots")
	}
	if screenshotsChanged {
		appleUseRevisedScreenshots(&receipt, material.Snapshot, source)
		if err = e.appleMaterialCatalog(ctx, p, receipt.Material); err != nil {
			return Result{}, err
		}
	}
	oldProfile := p
	oldProfile.MetadataDigest = r.SourceMetadataDigest
	if err = e.appleApp(ctx, p); err != nil {
		return Result{}, err
	}
	if err = e.appleMaterialReview(ctx, p, source, false); err != nil {
		return Result{}, err
	}
	if ready, err := e.appleVerifyMaterials(ctx, oldProfile, source, true, true); err != nil || !ready {
		if err == nil {
			err = fmt.Errorf("source media is not ready")
		}
		return Result{}, err
	}
	receipt.Material.Digest = r.MetadataDigest
	receipt.Revision = &appleMaterialRevisionReceipt{RevisionID: r.MaterialRevisionID, Binding: r.Binding, BaseHash: appleMaterialBaseHash(r, p), SourceOperationKey: r.SourceOperationKey, SourceJournalDigest: parent.JournalDigest, RootOperationKey: root, MaterialScope: r.MaterialScope, ScreenshotsChanged: screenshotsChanged}
	receipt.Reconciliations = nil
	receipt.Stage = "material_text_intent"
	if err = e.appleReserveMaterialRevision(ctx, p, r, hash, &receipt, parent); err != nil {
		return Result{}, err
	}
	for _, locale := range receipt.Material.Locales {
		for _, patch := range []struct {
			kind, id   string
			attributes any
		}{{"appStoreVersionLocalizations", locale.VersionLocalizationID, locale.VersionText}, {"appInfoLocalizations", locale.InfoLocalizationID, locale.AppInfo}} {
			if err = e.appleCheckpoint(ctx, r, &receipt, "material_text_intent"); err != nil {
				return Result{}, err
			}
			var response appleResponse
			if err = e.appleCall(ctx, p, "PATCH", "/v1/"+patch.kind+"/"+patch.id, applePayload(patch.kind, patch.id, appleTextPatch(patch.attributes), nil), &response); err != nil {
				return Result{}, err
			}
			if !response.Data.is(patch.kind, patch.id) {
				return Result{}, fmt.Errorf("material update returned another localization")
			}
		}
	}
	if screenshotsChanged {
		return e.appleStartScreenshotRevision(ctx, p, r, hash, &receipt, material)
	}
	return e.appleResubmitMaterialRevision(ctx, p, r, hash, receipt)
}

func (e *Engine) appleResubmitMaterialRevision(ctx context.Context, p Profile, r Request, hash string, receipt appleReceipt) (Result, error) {
	var err error
	if ready, err := e.appleVerifyMaterials(ctx, p, &receipt, true, true); err != nil || !ready {
		if err == nil {
			err = fmt.Errorf("revised media is not ready")
		}
		return Result{}, err
	}
	resolved := receipt.Stage == "material_resolution_confirmed"
	if err = e.appleMaterialReview(ctx, p, &receipt, resolved); err != nil {
		return Result{}, err
	}
	var response appleResponse
	if !resolved {
		if err = e.appleCheckpoint(ctx, r, &receipt, "material_resolve_intent"); err != nil {
			return Result{}, err
		}
		if err = e.appleCall(ctx, p, "PATCH", "/v1/reviewSubmissionItems/"+receipt.ItemID, applePayload("reviewSubmissionItems", receipt.ItemID, map[string]bool{"resolved": true}, nil), &response); err != nil {
			return Result{}, err
		}
		if !response.Data.is("reviewSubmissionItems", receipt.ItemID) {
			return Result{}, fmt.Errorf("resolved response differs from original item")
		}
	}
	// Recheck the full version/build/submission/item and material after resolution.
	if err = e.appleMaterialReview(ctx, p, &receipt, true); err != nil {
		return Result{}, err
	}
	if ready, err := e.appleVerifyMaterials(ctx, p, &receipt, true, true); err != nil || !ready {
		if err == nil {
			err = fmt.Errorf("revised material is not ready")
		}
		return Result{}, err
	}
	if err = e.appleCheckpoint(ctx, r, &receipt, "material_resubmit_intent"); err != nil {
		return Result{}, err
	}
	if err = e.appleCall(ctx, p, "PATCH", "/v1/reviewSubmissions/"+receipt.SubmissionID, applePayload("reviewSubmissions", receipt.SubmissionID, map[string]bool{"submitted": true}, nil), &response); err != nil {
		return Result{}, err
	}
	if !response.Data.is("reviewSubmissions", receipt.SubmissionID) {
		return Result{}, fmt.Errorf("resubmission response differs from original submission")
	}
	if err = e.appleRecoveryReview(ctx, p, &receipt); err != nil {
		return Result{}, err
	}
	version, err := e.appleVersion(ctx, p, &receipt)
	if err != nil {
		return Result{}, err
	}
	if version.Attributes.AppStoreState != "WAITING_FOR_REVIEW" && version.Attributes.AppStoreState != "IN_REVIEW" {
		return Result{}, fmt.Errorf("exact revised version has not positively entered review")
	}
	if version.related("build") != (appleRef{Type: "builds", ID: receipt.BuildID}) {
		return Result{}, fmt.Errorf("resubmitted build changed")
	}
	if ready, err := e.appleVerifyMaterials(ctx, p, &receipt, true, true); err != nil || !ready {
		if err == nil {
			err = fmt.Errorf("revised material is not ready")
		}
		return Result{}, err
	}
	if err = e.appleCheckpoint(ctx, r, &receipt, "submitted"); err != nil {
		return Result{}, err
	}
	result := Result{ExternalID: r.ExternalID, State: "awaiting_review", UpstreamState: version.Attributes.AppStoreState, Reason: "批准的商店资料已在原构建和原审核项重新提审，等待实际审核"}
	if err = e.appleFinish(ctx, p, r, hash, &receipt, result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (e *Engine) appleMaterialReview(ctx context.Context, p Profile, receipt *appleReceipt, resolved bool) error {
	expected := "REJECTED"
	if resolved {
		expected = "READY_FOR_REVIEW"
	}
	_, err := e.appleMaterialReviewState(ctx, p, receipt, expected)
	return err
}

// Recovery may observe either side of an uncertain resolve, but only after
// proving the exact original rejected version, submission, item and build.
func (e *Engine) appleMaterialReviewState(ctx context.Context, p Profile, receipt *appleReceipt, allowed ...string) (bool, error) {
	for _, id := range []string{receipt.UploadID, receipt.FileID, receipt.VersionID, receipt.BuildID, receipt.SubmissionID, receipt.ItemID} {
		if !appleResourceID.MatchString(id) {
			return false, fmt.Errorf("complete original Apple identity required")
		}
	}
	state, build, err := e.appleProcessedBuild(ctx, p, receipt)
	if err != nil {
		return false, err
	}
	if state != "COMPLETE" || build != receipt.BuildID {
		return false, fmt.Errorf("exact original processed build is not ready")
	}
	version, err := e.appleVersion(ctx, p, receipt)
	if err != nil {
		return false, err
	}
	if version.Attributes.ReleaseType != "AFTER_APPROVAL" || version.Attributes.AppStoreState != "METADATA_REJECTED" || version.related("build") != (appleRef{Type: "builds", ID: receipt.BuildID}) {
		return false, fmt.Errorf("only the exact metadata-rejected version and original linked build can be amended")
	}
	var response appleResponse
	q := url.Values{"include": {"app,appStoreVersionForReview"}, "fields[reviewSubmissions]": {"platform,submittedDate,state,app,appStoreVersionForReview"}}
	if err = e.appleCall(ctx, p, "GET", "/v1/reviewSubmissions/"+receipt.SubmissionID+"?"+q.Encode(), nil, &response); err != nil {
		return false, err
	}
	review := response.Data
	date, err := time.Parse(time.RFC3339, review.Attributes.SubmittedDate)
	if err != nil || date.After(time.Now().Add(5*time.Minute)) || !review.is("reviewSubmissions", receipt.SubmissionID) || review.Attributes.Platform != "IOS" || appleStringState(review) != "UNRESOLVED_ISSUES" || review.related("app") != (appleRef{Type: "apps", ID: p.AppID}) || review.related("appStoreVersionForReview") != (appleRef{Type: "appStoreVersions", ID: receipt.VersionID}) {
		return false, fmt.Errorf("exact original submission must have unresolved issues")
	}
	var items appleList
	q = url.Values{"fields[reviewSubmissionItems]": {"state,appStoreVersion"}, "limit": {"2"}}
	if err = e.appleCall(ctx, p, "GET", "/v1/reviewSubmissions/"+receipt.SubmissionID+"/items?"+q.Encode(), nil, &items); err != nil {
		return false, err
	}
	if items.Links.Next != "" || len(items.Data) != 1 || !items.Data[0].is("reviewSubmissionItems", receipt.ItemID) || items.Data[0].related("appStoreVersion") != (appleRef{Type: "appStoreVersions", ID: receipt.VersionID}) || !containsApple(allowed, appleStringState(items.Data[0])) {
		return false, fmt.Errorf("exact original rejected/resolved review item required")
	}
	return appleStringState(items.Data[0]) == "READY_FOR_REVIEW", nil
}
