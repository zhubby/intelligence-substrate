package publisher

import (
	"context"
	"crypto/md5" // Apple reports MD5; the approved local artifact is verified by SHA256 first.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

// AppleReconciliationRequest is accepted only by the local administrator CLI,
// never by the Worker publication protocol. It cannot select another Apple ID.
type AppleReconciliationRequest struct {
	Request                    Request `json:"request"`
	JournalDigest              string  `json:"journalDigest"`
	ConfirmStoppedOperationKey string  `json:"confirmStoppedOperationKey"`
	Reason                     string  `json:"reason"`
}

type AppleReconciliation struct {
	JournalDigest                string `json:"journalDigest"`
	InputDigest                  string `json:"inputDigest"`
	FromStage                    string `json:"fromStage"`
	ToStage                      string `json:"toStage"`
	ResultState                  string `json:"resultState"`
	Reason                       string `json:"reason"`
	ConfirmedStoppedOperationKey string `json:"confirmedStoppedOperationKey"`
	ObservedAt                   string `json:"observedAt"`
}

func (e *Engine) ReconcileApple(ctx context.Context, in AppleReconciliationRequest) (Operation, error) {
	if err := validateRequest(in.Request); err != nil {
		return Operation{}, err
	}
	if !hashPattern.MatchString(in.JournalDigest) || in.ConfirmStoppedOperationKey != in.Request.OperationKey || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 4096 {
		return Operation{}, fmt.Errorf("exact journal digest, stopped operation confirmation and bounded reason required")
	}
	p, err := e.profile(in.Request)
	if err != nil {
		return Operation{}, err
	}
	if p.Driver != "app-store" {
		return Operation{}, fmt.Errorf("reconciliation requires the fixed App Store profile")
	}
	j, err := e.inspectJournal(ctx, in.Request.OperationKey)
	if err != nil {
		return Operation{}, err
	}
	if j.requestHash != requestHash(in.Request, p) || j.Receipt.Apple == nil {
		return Operation{}, fmt.Errorf("recovery request differs from approved operation or administrator destination")
	}
	// Only semantic request fields are hashed: an original artifact may be restored
	// at another private path, but its approved bytes and binding cannot change.
	b, _ := json.Marshal([]string{j.requestHash, in.JournalDigest, in.ConfirmStoppedOperationKey, in.Reason})
	h := sha256.Sum256(b)
	inputDigest := hex.EncodeToString(h[:])
	receipt := j.Receipt.Apple
	if in.Request.ExternalID != "" && in.Request.ExternalID != appleExternalID(p, receipt) {
		return Operation{}, fmt.Errorf("recovery external ID differs from the persisted upload")
	}
	if n := len(receipt.Reconciliations); n > 0 {
		last := receipt.Reconciliations[n-1]
		if last.JournalDigest == in.JournalDigest {
			if last.InputDigest == inputDigest && j.State == "complete" && receipt.Stage == last.ToStage && j.Result != nil && j.Result.State == last.ResultState {
				return j.Operation, nil
			}
			return Operation{}, fmt.Errorf("recovery input or subsequent operation state changed; inspect the current journal")
		}
	}
	if j.State != "pending" || j.JournalDigest != in.JournalDigest {
		return Operation{}, fmt.Errorf("only the exact pending journal snapshot can be reconciled")
	}
	uploadPhase := containsApple([]string{"file_commit_intent", "processing"}, receipt.Stage)
	reviewPhase := containsApple([]string{"review_submit_intent", "submitted", "material_resubmit_intent"}, receipt.Stage)
	screenshotPhase := receipt.Revision != nil && containsApple([]string{"image_commit_intent", "material_images_processing"}, receipt.Stage)
	placementPhase := receipt.Revision != nil && containsApple([]string{"image_order_intent", "materials_placed", "material_resolve_intent", "material_placement_confirmed", "material_resolution_confirmed"}, receipt.Stage)
	if !uploadPhase && !reviewPhase && !screenshotPhase && !placementPhase {
		return Operation{}, fmt.Errorf("this intermediate Apple stage lacks sufficient persisted proof; retain the blocked operation")
	}
	for _, id := range []string{receipt.UploadID, receipt.FileID, receipt.VersionID} {
		if !appleResourceID.MatchString(id) {
			return Operation{}, fmt.Errorf("recovery requires exact persisted Apple identities")
		}
	}
	if (reviewPhase || screenshotPhase || placementPhase) && (!appleResourceID.MatchString(receipt.BuildID) || !appleResourceID.MatchString(receipt.SubmissionID) || !appleResourceID.MatchString(receipt.ItemID)) {
		return Operation{}, fmt.Errorf("recovery requires persisted review and build identities")
	}
	if receipt.Version != strings.TrimPrefix(in.Request.Version, "v") || !appleVersionPattern.MatchString(receipt.BuildNumber) {
		return Operation{}, fmt.Errorf("persisted package version differs from approved release")
	}
	artifact, cleanup, err := stageArtifact(in.Request)
	if err != nil {
		return Operation{}, err
	}
	defer cleanup()
	f, err := os.Open(artifact)
	if err != nil {
		return Operation{}, err
	}
	md5Hash := md5.New()
	_, err = io.Copy(md5Hash, f)
	f.Close()
	if err != nil {
		return Operation{}, err
	}
	checksum := hex.EncodeToString(md5Hash.Sum(nil))
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	if err = e.appleApp(ctx, p); err != nil {
		return Operation{}, err
	}
	if err = e.appleRecoveryFile(ctx, p, receipt, in.Request.ArtifactSize, checksum); err != nil {
		return Operation{}, err
	}
	state, build, err := e.appleProcessedBuild(ctx, p, receipt)
	if err != nil {
		return Operation{}, err
	}
	if state != "COMPLETE" {
		return Operation{}, fmt.Errorf("Apple has not positively confirmed the exact processed build; journal remains pending")
	}
	fromStage := receipt.Stage
	result := Result{ExternalID: appleExternalID(p, receipt), State: "processing", UpstreamState: "COMPLETE", ContinueRequired: true, Reason: "准确制品上传与处理已只读核实；继续提审仍须当前审批与质量门禁"}
	toStage := "processing"
	var sourceParent operationJournal
	if placementPhase {
		result, sourceParent, toStage, err = e.applePlacementRecovery(ctx, p, in.Request, receipt)
		if err != nil {
			return Operation{}, err
		}
	} else if screenshotPhase {
		result, sourceParent, err = e.appleScreenshotRecovery(ctx, p, in.Request, receipt)
		if err != nil {
			return Operation{}, err
		}
		toStage = "material_images_processing"
	} else {
		if reviewPhase {
			if err = e.appleRecoveryReview(ctx, p, receipt); err != nil {
				return Operation{}, err
			}
		}
		if receipt.Material != nil {
			ready, err := e.appleVerifyMaterials(ctx, p, receipt, reviewPhase, reviewPhase)
			if err != nil {
				return Operation{}, err
			}
			if !ready {
				return Operation{}, fmt.Errorf("App Store material processing is incomplete; retain the pending journal")
			}
		}
		version, err := e.appleVersion(ctx, p, receipt)
		if err != nil {
			return Operation{}, err
		}
		if version.Attributes.ReleaseType != "AFTER_APPROVAL" {
			return Operation{}, fmt.Errorf("Apple release policy changed; journal remains pending")
		}
		if uploadPhase {
			if version.Attributes.AppStoreState != "PREPARE_FOR_SUBMISSION" || version.related("build").ID != "" {
				return Operation{}, fmt.Errorf("prepared Apple version changed; cannot infer safe upload recovery")
			}
		} else {
			if receipt.BuildID != build || version.related("build").Type != "builds" || version.related("build").ID != build {
				return Operation{}, fmt.Errorf("review version no longer selects the approved processed build")
			}
			result, err = appleSubmittedStatus(p, receipt, version)
			if err != nil {
				return Operation{}, err
			}
			if receipt.Revision != nil && !containsApple([]string{"awaiting_review", "awaiting_release", "published"}, result.State) {
				return Operation{}, fmt.Errorf("material recovery requires positive proof of resubmission; unresolved/rejected outcome remains pending")
			}
			if result.State == "unchanged" {
				return Operation{}, fmt.Errorf("Apple version state cannot prove the outcome of the submitted review")
			}
			toStage = "submitted"
		}
	}
	observedAt := time.Now().UTC().Format(time.RFC3339Nano)
	receipt.Stage = toStage
	receipt.Reconciliations = append(receipt.Reconciliations, AppleReconciliation{JournalDigest: in.JournalDigest, InputDigest: inputDigest, FromStage: fromStage, ToStage: toStage, ResultState: result.State, Reason: in.Reason, ConfirmedStoppedOperationKey: in.ConfirmStoppedOperationKey, ObservedAt: observedAt})
	resultBody, _ := json.Marshal(result)
	var oldResult any
	if j.result.Valid {
		oldResult = j.result.String
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	query := "UPDATE operations SET state='complete',receipt=?,result=?,updated_at=? WHERE operation_key=? AND state='pending' AND request_hash=? AND receipt=? AND result IS ? AND updated_at=?"
	args := []any{mustAppleReceiptJSON(j.Receipt), string(resultBody), observedAt, j.OperationKey, j.requestHash, j.receipt, oldResult, j.updatedAt}
	if receipt.Material != nil {
		query += ` AND EXISTS(SELECT 1 FROM apple_destinations WHERE app_id=? AND operation_key=operations.operation_key)
		 AND EXISTS(SELECT 1 FROM apple_versions WHERE app_id=? AND version=? AND build_number=? AND operation_key=?)
		 AND (?='' OR EXISTS(SELECT 1 FROM apple_material_revisions WHERE parent_operation_key=? AND operation_key=operations.operation_key))`
		args = append(args, p.AppID, p.AppID, receipt.Version, receipt.BuildNumber, appleVersionOwner(in.Request, receipt), in.Request.SourceOperationKey, in.Request.SourceOperationKey)
	}
	if screenshotPhase || placementPhase {
		var parentResult any
		if sourceParent.result.Valid {
			parentResult = sourceParent.result.String
		}
		query += ` AND EXISTS(SELECT 1 FROM operations source WHERE source.operation_key=? AND source.state='complete'
		 AND source.request_hash=? AND source.receipt=? AND source.result IS ? AND source.updated_at=?)`
		args = append(args, sourceParent.OperationKey, sourceParent.requestHash, sourceParent.receipt, parentResult, sourceParent.updatedAt)
	}
	updated, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return Operation{}, err
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return Operation{}, err
	}
	if count != 1 {
		return Operation{}, fmt.Errorf("publisher journal changed during read-only reconciliation; inspect before any further action")
	}
	if receipt.Material != nil && reviewPhase {
		if _, err = tx.ExecContext(ctx, "DELETE FROM apple_destinations WHERE app_id=? AND operation_key=?", p.AppID, j.OperationKey); err != nil {
			return Operation{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, err
	}
	return e.Inspect(ctx, j.OperationKey)
}

func (e *Engine) appleRecoveryFile(ctx context.Context, p Profile, receipt *appleReceipt, size int64, checksum string) error {
	// BuildUploadFile has no reverse buildUpload relationship. Query the
	// documented parent collection to prove the association without inventing it.
	q := url.Values{"fields[buildUploadFiles]": {"assetDeliveryState,assetType,fileName,fileSize,sourceFileChecksums,uti"}, "limit": {"2"}}
	var response appleList
	if err := e.appleCall(ctx, p, "GET", "/v1/buildUploads/"+receipt.UploadID+"/buildUploadFiles?"+q.Encode(), nil, &response); err != nil {
		return err
	}
	if response.Links.Next != "" || len(response.Data) != 1 {
		return fmt.Errorf("one complete file collection is required for exact upload recovery")
	}
	file := response.Data[0]
	a := file.Attributes
	if !file.is("buildUploadFiles", receipt.FileID) || a.FileName != "approved.ipa" || a.FileSize != size || a.AssetType != "ASSET" || a.UTI != "com.apple.ipa" || !containsApple([]string{"UPLOAD_COMPLETE", "COMPLETE"}, a.AssetDeliveryState.State) || a.SourceFileChecksums.File.Algorithm != "MD5" || !strings.EqualFold(a.SourceFileChecksums.File.Hash, checksum) {
		return fmt.Errorf("Apple file identity, bytes or completed state differ from the approved artifact")
	}
	return nil
}

func (e *Engine) appleRecoveryReview(ctx context.Context, p Profile, receipt *appleReceipt) error {
	q := url.Values{"include": {"app,appStoreVersionForReview"}, "fields[reviewSubmissions]": {"platform,submittedDate,state,app,appStoreVersionForReview"}}
	var response appleResponse
	if err := e.appleCall(ctx, p, "GET", "/v1/reviewSubmissions/"+receipt.SubmissionID+"?"+q.Encode(), nil, &response); err != nil {
		return err
	}
	review := response.Data
	var state string
	_ = json.Unmarshal(review.Attributes.State, &state)
	date, dateErr := time.Parse(time.RFC3339, review.Attributes.SubmittedDate)
	if !review.is("reviewSubmissions", receipt.SubmissionID) || review.Attributes.Platform != "IOS" || review.related("app") != (appleRef{Type: "apps", ID: p.AppID}) || review.related("appStoreVersionForReview") != (appleRef{Type: "appStoreVersions", ID: receipt.VersionID}) || dateErr != nil || date.After(time.Now().Add(5*time.Minute)) || !containsApple([]string{"WAITING_FOR_REVIEW", "IN_REVIEW", "UNRESOLVED_ISSUES", "COMPLETING", "COMPLETE"}, state) {
		return fmt.Errorf("Apple has not confirmed this exact review was submitted")
	}
	q = url.Values{"fields[reviewSubmissionItems]": {"state,appStoreVersion"}, "limit": {"2"}}
	var items appleList
	if err := e.appleCall(ctx, p, "GET", "/v1/reviewSubmissions/"+receipt.SubmissionID+"/items?"+q.Encode(), nil, &items); err != nil {
		return err
	}
	if items.Links.Next != "" || len(items.Data) != 1 || !items.Data[0].is("reviewSubmissionItems", receipt.ItemID) || items.Data[0].related("appStoreVersion") != (appleRef{Type: "appStoreVersions", ID: receipt.VersionID}) {
		return fmt.Errorf("review items do not prove the persisted version and item association")
	}
	var itemState string
	_ = json.Unmarshal(items.Data[0].Attributes.State, &itemState)
	if !containsApple([]string{"ACCEPTED", "APPROVED", "REJECTED"}, itemState) {
		return fmt.Errorf("review item is still a draft, removed or unrecognized")
	}
	return nil
}
