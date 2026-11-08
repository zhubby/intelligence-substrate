package publisher

import (
	"context"
	"crypto/md5" // App Store's file checksum protocol; approval integrity uses SHA256.
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

type appleRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}
type appleAttributes struct {
	appleVersionText
	appleInfoText
	Locale         string `json:"locale"`
	Category       string `json:"category"`
	SpecID         string `json:"specId"`
	MediaType      string `json:"mediaType"`
	PlacementType  string `json:"placementType"`
	PlacementGroup string `json:"placementGroup"`
	ImageAsset     struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"imageAsset"`
	BundleID            string          `json:"bundleId"`
	ShortVersion        string          `json:"cfBundleShortVersionString"`
	BuildNumber         string          `json:"cfBundleVersion"`
	Platform            string          `json:"platform"`
	State               json.RawMessage `json:"state"`
	FileSize            int64           `json:"fileSize"`
	FileName            string          `json:"fileName"`
	AssetType           string          `json:"assetType"`
	UTI                 string          `json:"uti"`
	SourceFileChecksums struct {
		File struct {
			Algorithm string `json:"algorithm"`
			Hash      string `json:"hash"`
		} `json:"file"`
	} `json:"sourceFileChecksums"`
	SubmittedDate      string `json:"submittedDate"`
	AssetDeliveryState struct {
		State string `json:"state"`
	} `json:"assetDeliveryState"`
	UploadOperations []appleUploadOperation `json:"uploadOperations"`
	Version          string                 `json:"version"`
	ProcessingState  string                 `json:"processingState"`
	Expired          bool                   `json:"expired"`
	VersionString    string                 `json:"versionString"`
	AppStoreState    string                 `json:"appStoreState"`
	ReleaseType      string                 `json:"releaseType"`
}
type appleResource struct {
	Type          string          `json:"type"`
	ID            string          `json:"id"`
	Attributes    appleAttributes `json:"attributes"`
	Relationships map[string]struct {
		Data json.RawMessage `json:"data"`
	} `json:"relationships"`
}
type appleResponse struct {
	Data     appleResource   `json:"data"`
	Included []appleResource `json:"included"`
}
type appleList struct {
	Data  []appleResource `json:"data"`
	Links struct {
		Next string `json:"next"`
	} `json:"links"`
}

var appleResourceID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,127}$`)

func (r appleResource) is(kind, id string) bool {
	return r.Type == kind && appleResourceID.MatchString(r.ID) && (id == "" || r.ID == id)
}
func (r appleResource) related(kind string) appleRef {
	var ref appleRef
	if json.Unmarshal(r.Relationships[kind].Data, &ref) != nil {
		return appleRef{}
	}
	return ref
}
func appleRel(kind, id string) map[string]any {
	return map[string]any{"data": appleRef{Type: kind, ID: id}}
}
func applePayload(kind, id string, attributes, relationships any) map[string]any {
	data := map[string]any{"type": kind}
	if id != "" {
		data["id"] = id
	}
	if attributes != nil {
		data["attributes"] = attributes
	}
	if relationships != nil {
		data["relationships"] = relationships
	}
	return map[string]any{"data": data}
}
func appleExternalID(p Profile, receipt *appleReceipt) string {
	return "apple:" + p.AppID + ":" + receipt.UploadID
}
func appleUploadState(resource appleResource) string {
	var state struct {
		State string `json:"state"`
	}
	if json.Unmarshal(resource.Attributes.State, &state) != nil {
		return ""
	}
	return state.State
}

func (e *Engine) executeApple(ctx context.Context, p Profile, r Request, hash string) (Result, error) {
	if err := appleMaterialBinding(p, r); err != nil {
		return Result{}, err
	}
	var superseded int
	if err := e.db.QueryRowContext(ctx, "SELECT count(*) FROM apple_material_revisions WHERE parent_operation_key=?", r.OperationKey).Scan(&superseded); err != nil || superseded != 0 {
		return Result{}, fmt.Errorf("App Store operation has a reserved material successor; use its exact approval and journal")
	}
	var stored string
	err := e.db.QueryRowContext(ctx, "SELECT request_hash FROM operations WHERE operation_key=?", r.OperationKey).Scan(&stored)
	if err == nil {
		if stored != hash {
			return Result{}, fmt.Errorf("App Store operation differs from approved binding or administrator profile")
		}
		op, err := e.Inspect(ctx, r.OperationKey)
		if err != nil {
			return Result{}, err
		}
		receipt := op.Receipt.Apple
		if receipt == nil {
			return Result{}, fmt.Errorf("App Store journal is incomplete; reconcile the operation")
		}
		if r.Action == "status" || r.Action == "continue" {
			if receipt.UploadID == "" || r.ExternalID != appleExternalID(p, receipt) {
				return Result{}, fmt.Errorf("App Store external ID differs from the persisted upload")
			}
		}
		if op.State != "complete" || op.Result == nil {
			return Result{}, fmt.Errorf("App Store external write is uncertain at stage %s; inspect and reconcile without resubmitting", receipt.Stage)
		}
		if r.Action == "status" {
			return e.appleStatus(ctx, p, r, receipt)
		}
		if r.Action != "continue" || op.Result.State != "processing" {
			return *op.Result, nil
		}
		observed, err := e.appleStatus(ctx, p, r, receipt)
		if err != nil {
			return Result{}, err
		}
		if observed.State == "rejected" {
			return Result{}, fmt.Errorf("Apple rejected the upload; do not submit this build")
		}
		if !observed.ContinueRequired {
			return observed, nil
		}
		// All safe reads happen before acquiring a new write reservation.
		if err = e.appleApp(ctx, p); err != nil {
			return Result{}, err
		}
		version, err := e.appleVersion(ctx, p, receipt)
		if err != nil {
			return Result{}, err
		}
		if version.Attributes.ReleaseType != "AFTER_APPROVAL" || version.Attributes.AppStoreState != "PREPARE_FOR_SUBMISSION" || (version.related("build").ID != "" && version.related("build").ID != receipt.BuildID) {
			return Result{}, fmt.Errorf("prepared App Store version changed; operator must reconcile before review")
		}
		if receipt.Material != nil {
			if ready, err := e.appleVerifyMaterials(ctx, p, receipt, false); err != nil || !ready {
				if err == nil {
					err = fmt.Errorf("App Store image processing is incomplete")
				}
				return Result{}, err
			}
		}
		res, err := e.db.ExecContext(ctx, "UPDATE operations SET state='pending',updated_at=? WHERE operation_key=? AND state='complete' AND receipt=?", time.Now().UTC().Format(time.RFC3339Nano), r.OperationKey, mustAppleReceiptJSON(op.Receipt))
		if err != nil {
			return Result{}, err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return Result{}, fmt.Errorf("App Store continuation already reserved; inspect before retry")
		}
		result, err := e.appleSubmit(ctx, p, r, receipt)
		if err != nil {
			return Result{}, err
		}
		if err = e.appleFinish(ctx, p, r, hash, receipt, result); err != nil {
			return Result{}, fmt.Errorf("review accepted but local completion uncertain; reconcile before further mutation")
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}
	if r.Action == "status" || r.Action == "continue" {
		return Result{}, fmt.Errorf("no persisted approved App Store upload")
	}
	var material *appleMaterial
	if p.MetadataFile != "" {
		material, err = stageAppleMaterial(p, r)
		if err != nil {
			return Result{}, err
		}
		defer os.RemoveAll(material.Dir)
	}
	artifact, cleanup, err := stageArtifact(r)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	pkg, err := e.verifyApplePackage(ctx, p, r, artifact)
	if err != nil {
		return Result{}, err
	}
	if pkg.Version != strings.TrimPrefix(r.Version, "v") || !appleVersionPattern.MatchString(pkg.Version) || !appleVersionPattern.MatchString(pkg.BuildNumber) {
		return Result{}, fmt.Errorf("IPA version does not match approved release")
	}
	if err = e.appleApp(ctx, p); err != nil {
		return Result{}, err
	}
	receipt := &appleReceipt{Version: pkg.Version, BuildNumber: pkg.BuildNumber, Stage: "upload_create_intent"}
	version, err := e.applePreparedVersion(ctx, p, receipt.Version)
	if err != nil {
		return Result{}, err
	}
	if version.Attributes.AppStoreState != "PREPARE_FOR_SUBMISSION" || version.Attributes.ReleaseType != "AFTER_APPROVAL" || version.related("build").ID != "" {
		return Result{}, fmt.Errorf("App Store requires an unlinked prepared version with AFTER_APPROVAL release policy")
	}
	receipt.VersionID = version.ID
	if material != nil {
		receipt.Material = material.Snapshot
		receipt.Stage = "metadata_begin_intent"
		if err = e.appleDiscoverMaterials(ctx, p, receipt); err != nil {
			return Result{}, err
		}
	}
	if err = e.appleReserve(ctx, p, r, hash, receipt); err != nil {
		return Result{}, fmt.Errorf("App Store upload already reserved; inspect the journal")
	}
	if material != nil {
		if err = e.appleWriteMaterials(ctx, p, r, receipt, material); err != nil {
			return Result{}, err
		}
		if err = e.appleCheckpoint(ctx, r, receipt, "upload_create_intent"); err != nil {
			return Result{}, err
		}
	}
	result, err := e.applePublish(ctx, p, r, receipt, artifact)
	if err != nil {
		return Result{}, err
	}
	if err = e.appleFinish(ctx, p, r, hash, receipt, result); err != nil {
		return Result{}, fmt.Errorf("upload accepted but local completion uncertain; reconcile before further mutation")
	}
	return result, nil
}
func mustAppleReceiptJSON(receipt Receipt) string { b, _ := json.Marshal(receipt); return string(b) }
func (e *Engine) appleCheckpoint(ctx context.Context, r Request, receipt *appleReceipt, stage string) error {
	receipt.Stage = stage
	if receipt.Material != nil {
		return e.appleMaterialCheckpoint(ctx, r, receipt)
	}
	return e.checkpoint(ctx, r.OperationKey, Receipt{Apple: receipt})
}

func (e *Engine) appleApp(ctx context.Context, p Profile) error {
	var response appleResponse
	if err := e.appleCall(ctx, p, "GET", "/v1/apps/"+p.AppID+"?fields%5Bapps%5D=bundleId", nil, &response); err != nil {
		return err
	}
	if !response.Data.is("apps", p.AppID) || response.Data.Attributes.BundleID != p.BundleID {
		return fmt.Errorf("App Store app and bundle differ from administrator profile")
	}
	return nil
}
func validAppleVersion(v appleResource, p Profile, version string) bool {
	app := v.related("app")
	return v.is("appStoreVersions", "") && v.Attributes.VersionString == version && v.Attributes.Platform == "IOS" && app.Type == "apps" && app.ID == p.AppID
}
func (e *Engine) applePreparedVersion(ctx context.Context, p Profile, version string) (appleResource, error) {
	var response appleList
	query := url.Values{"filter[versionString]": {version}, "filter[platform]": {"IOS"}, "include": {"app,build"}, "limit": {"2"}}
	if err := e.appleCall(ctx, p, "GET", "/v1/apps/"+p.AppID+"/appStoreVersions?"+query.Encode(), nil, &response); err != nil {
		return appleResource{}, err
	}
	if response.Links.Next != "" || len(response.Data) != 1 || !validAppleVersion(response.Data[0], p, version) {
		return appleResource{}, fmt.Errorf("exactly one prepared App Store version with matching app/platform/version is required")
	}
	return response.Data[0], nil
}
func (e *Engine) appleVersion(ctx context.Context, p Profile, receipt *appleReceipt) (appleResource, error) {
	var response appleResponse
	if !appleResourceID.MatchString(receipt.VersionID) {
		return appleResource{}, fmt.Errorf("missing persisted App Store version")
	}
	if err := e.appleCall(ctx, p, "GET", "/v1/appStoreVersions/"+receipt.VersionID+"?include=app,build", nil, &response); err != nil {
		return appleResource{}, err
	}
	if response.Data.ID != receipt.VersionID || !validAppleVersion(response.Data, p, receipt.Version) {
		return appleResource{}, fmt.Errorf("App Store version no longer matches this approved operation")
	}
	return response.Data, nil
}

func (e *Engine) applePublish(ctx context.Context, p Profile, r Request, receipt *appleReceipt, artifact string) (Result, error) {
	if receipt.Material != nil {
		if err := e.appleCheckpoint(ctx, r, receipt, "upload_create_intent"); err != nil {
			return Result{}, err
		}
	}
	var upload appleResponse
	attrs := map[string]string{"cfBundleShortVersionString": receipt.Version, "cfBundleVersion": receipt.BuildNumber, "platform": "IOS"}
	if err := e.appleCall(ctx, p, "POST", "/v1/buildUploads", applePayload("buildUploads", "", attrs, map[string]any{"app": appleRel("apps", p.AppID)}), &upload); err != nil {
		return Result{}, err
	}
	if !upload.Data.is("buildUploads", "") {
		return Result{}, fmt.Errorf("invalid Apple build upload receipt")
	}
	receipt.UploadID = upload.Data.ID
	if err := e.appleCheckpoint(ctx, r, receipt, "file_create_intent"); err != nil {
		return Result{}, err
	}
	var file appleResponse
	fileAttrs := map[string]any{"assetType": "ASSET", "fileName": "approved.ipa", "fileSize": r.ArtifactSize, "uti": "com.apple.ipa"}
	if err := e.appleCall(ctx, p, "POST", "/v1/buildUploadFiles", applePayload("buildUploadFiles", "", fileAttrs, map[string]any{"buildUpload": appleRel("buildUploads", receipt.UploadID)}), &file); err != nil {
		return Result{}, err
	}
	if !file.Data.is("buildUploadFiles", "") || file.Data.Attributes.FileSize != r.ArtifactSize || file.Data.Attributes.FileName != "approved.ipa" {
		return Result{}, fmt.Errorf("Apple file reservation differs from approved package")
	}
	receipt.FileID = file.Data.ID
	if err := e.appleCheckpoint(ctx, r, receipt, "bytes_upload_intent"); err != nil {
		return Result{}, err
	}
	var guard func() error
	if receipt.Material != nil {
		guard = func() error { return e.appleCheckpoint(ctx, r, receipt, "bytes_upload_intent") }
	}
	if err := e.appleUploadBytes(ctx, p, artifact, file.Data.Attributes.UploadOperations, r.ArtifactSize, guard); err != nil {
		return Result{}, err
	}
	f, err := os.Open(artifact)
	if err != nil {
		return Result{}, err
	}
	hash := md5.New()
	_, err = io.Copy(hash, f)
	f.Close()
	if err != nil {
		return Result{}, err
	}
	if err = e.appleCheckpoint(ctx, r, receipt, "file_commit_intent"); err != nil {
		return Result{}, err
	}
	commit := map[string]any{"uploaded": true, "sourceFileChecksums": map[string]any{"file": map[string]string{"algorithm": "MD5", "hash": hex.EncodeToString(hash.Sum(nil))}}}
	var confirmed appleResponse
	if err = e.appleCall(ctx, p, "PATCH", "/v1/buildUploadFiles/"+receipt.FileID, applePayload("buildUploadFiles", receipt.FileID, commit, nil), &confirmed); err != nil {
		return Result{}, err
	}
	if !confirmed.Data.is("buildUploadFiles", receipt.FileID) || !containsApple([]string{"UPLOAD_COMPLETE", "COMPLETE"}, confirmed.Data.Attributes.AssetDeliveryState.State) {
		return Result{}, fmt.Errorf("Apple file commit has not confirmed the complete upload")
	}
	if err = e.appleCheckpoint(ctx, r, receipt, "processing"); err != nil {
		return Result{}, err
	}
	return Result{ExternalID: appleExternalID(p, receipt), State: "processing", UpstreamState: "UPLOAD_COMPLETE", Reason: "IPA 上传已接受，等待 Apple 处理；尚未提交审核"}, nil
}
func containsApple(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}

func (e *Engine) appleProcessedBuild(ctx context.Context, p Profile, receipt *appleReceipt) (string, string, error) {
	var upload appleResponse
	if err := e.appleCall(ctx, p, "GET", "/v1/buildUploads/"+receipt.UploadID+"?include=build", nil, &upload); err != nil {
		return "", "", err
	}
	attrs := upload.Data.Attributes
	if !upload.Data.is("buildUploads", receipt.UploadID) || attrs.ShortVersion != receipt.Version || attrs.BuildNumber != receipt.BuildNumber || attrs.Platform != "IOS" {
		return "", "", fmt.Errorf("Apple upload identity changed")
	}
	state := appleUploadState(upload.Data)
	if state != "COMPLETE" {
		if !containsApple([]string{"AWAITING_UPLOAD", "PROCESSING", "FAILED"}, state) {
			return "", "", fmt.Errorf("unknown Apple build processing state")
		}
		return state, "", nil
	}
	build := upload.Data.related("build")
	if build.Type != "builds" || !appleResourceID.MatchString(build.ID) || (receipt.BuildID != "" && receipt.BuildID != build.ID) {
		return "", "", fmt.Errorf("Apple processed build differs from upload journal")
	}
	var response appleResponse
	if err := e.appleCall(ctx, p, "GET", "/v1/builds/"+build.ID+"?include=app,preReleaseVersion,buildUpload", nil, &response); err != nil {
		return "", "", err
	}
	b := response.Data
	app := b.related("app")
	source := b.related("buildUpload")
	pre := b.related("preReleaseVersion")
	if !b.is("builds", build.ID) || b.Attributes.Version != receipt.BuildNumber || app.Type != "apps" || app.ID != p.AppID || source.Type != "buildUploads" || source.ID != receipt.UploadID || b.Attributes.Expired {
		return "", "", fmt.Errorf("processed build is not the approved app/upload/version")
	}
	validPre := false
	for _, included := range response.Included {
		if included.is("preReleaseVersions", pre.ID) && pre.Type == "preReleaseVersions" && included.Attributes.Platform == "IOS" && included.Attributes.Version == receipt.Version {
			validPre = true
		}
	}
	if !validPre {
		return "", "", fmt.Errorf("processed build has a different marketing version or platform")
	}
	if b.Attributes.ProcessingState == "PROCESSING" {
		return "PROCESSING", "", nil
	}
	if b.Attributes.ProcessingState != "VALID" {
		return "FAILED", "", nil
	}
	return state, build.ID, nil
}

func (e *Engine) appleStatus(ctx context.Context, p Profile, r Request, receipt *appleReceipt) (Result, error) {
	result := Result{ExternalID: appleExternalID(p, receipt), State: "processing"}
	if receipt.Stage == "processing" {
		imagesReady := true
		if receipt.Material != nil {
			var err error
			imagesReady, err = e.appleVerifyMaterials(ctx, p, receipt, false)
			if err != nil {
				return Result{}, err
			}
		}
		state, _, err := e.appleProcessedBuild(ctx, p, receipt)
		if err != nil {
			return Result{}, err
		}
		result.UpstreamState = state
		if state == "FAILED" {
			result.State = "rejected"
			result.Reason = "Apple 构建处理失败；需要修复并提交新的已审批构建"
			return result, nil
		}
		if state == "COMPLETE" && imagesReady {
			result.ContinueRequired = true
			result.Reason = "Apple 构建处理完成，可按原审批继续提审"
		}
		if !imagesReady {
			result.UpstreamState = "MATERIAL_PROCESSING"
			result.Reason = "IPA 与商店截图分别处理；截图就绪前不会提审"
		}
		return result, nil
	}
	if receipt.Stage != "submitted" {
		return Result{}, fmt.Errorf("App Store stage is not safely observable; reconcile the journal")
	}
	version, err := e.appleVersion(ctx, p, receipt)
	if err != nil {
		return Result{}, err
	}
	return appleSubmittedStatus(p, receipt, version)
}

func appleSubmittedStatus(p Profile, receipt *appleReceipt, version appleResource) (Result, error) {
	result := Result{ExternalID: appleExternalID(p, receipt)}
	if version.related("build").Type != "builds" || version.related("build").ID != receipt.BuildID {
		return Result{}, fmt.Errorf("App Store selected build differs from approved upload")
	}
	state := version.Attributes.AppStoreState
	result.UpstreamState = state
	switch state {
	case "READY_FOR_SALE":
		result.State = "published"
		result.Reason = "App Store 报告可售；仍需在目标地区和真实设备独立验证"
	case "REJECTED", "METADATA_REJECTED", "INVALID_BINARY", "DEVELOPER_REJECTED":
		result.State = "rejected"
	case "REMOVED_FROM_SALE", "DEVELOPER_REMOVED_FROM_SALE", "REPLACED_WITH_NEW_VERSION":
		result.State = "halted"
	case "PENDING_APPLE_RELEASE", "PROCESSING_FOR_APP_STORE", "PENDING_DEVELOPER_RELEASE", "PENDING_CONTRACT", "ACCEPTED":
		result.State = "awaiting_release"
		result.Reason = "审核已处理，等待商店发布或发布条件；尚未证明公开可用"
	case "WAITING_FOR_REVIEW", "IN_REVIEW", "WAITING_FOR_EXPORT_COMPLIANCE":
		result.State = "awaiting_review"
		result.Reason = "已提审，审核、合规或发布条件尚未完成"
	default:
		result.State = "unchanged"
		result.Reason = "App Store 返回的状态不足以改变上次已知发布结论，请核对"
	}
	return result, nil
}

func (e *Engine) appleSubmit(ctx context.Context, p Profile, r Request, receipt *appleReceipt) (Result, error) {
	if receipt.Material != nil {
		if err := e.applePlaceMaterials(ctx, p, r, receipt); err != nil {
			return Result{}, err
		}
	}
	// Re-read the exact build immediately before linking. No artifact/credential is selected by a task.
	state, build, err := e.appleProcessedBuild(ctx, p, receipt)
	if err != nil || state != "COMPLETE" {
		if err == nil {
			err = fmt.Errorf("Apple build no longer ready")
		}
		return Result{}, err
	}
	receipt.BuildID = build
	if err = e.appleCheckpoint(ctx, r, receipt, "version_link_intent"); err != nil {
		return Result{}, err
	}
	var version appleResponse
	if err = e.appleCall(ctx, p, "PATCH", "/v1/appStoreVersions/"+receipt.VersionID, applePayload("appStoreVersions", receipt.VersionID, nil, map[string]any{"build": appleRel("builds", build)}), &version); err != nil {
		return Result{}, err
	}
	if !validAppleVersion(version.Data, p, receipt.Version) || version.Data.ID != receipt.VersionID || version.Data.related("build").ID != build {
		return Result{}, fmt.Errorf("Apple version linking receipt differs from approved build")
	}
	if err = e.appleCheckpoint(ctx, r, receipt, "review_create_intent"); err != nil {
		return Result{}, err
	}
	var review appleResponse
	if err = e.appleCall(ctx, p, "POST", "/v1/reviewSubmissions", applePayload("reviewSubmissions", "", nil, map[string]any{"app": appleRel("apps", p.AppID)}), &review); err != nil {
		return Result{}, err
	}
	if !review.Data.is("reviewSubmissions", "") || review.Data.related("app").ID != p.AppID {
		return Result{}, fmt.Errorf("invalid Apple review submission receipt")
	}
	receipt.SubmissionID = review.Data.ID
	if err = e.appleCheckpoint(ctx, r, receipt, "review_item_intent"); err != nil {
		return Result{}, err
	}
	var item appleResponse
	if err = e.appleCall(ctx, p, "POST", "/v1/reviewSubmissionItems", applePayload("reviewSubmissionItems", "", nil, map[string]any{"reviewSubmission": appleRel("reviewSubmissions", receipt.SubmissionID), "appStoreVersion": appleRel("appStoreVersions", receipt.VersionID)}), &item); err != nil {
		return Result{}, err
	}
	if !item.Data.is("reviewSubmissionItems", "") {
		return Result{}, fmt.Errorf("invalid Apple review item receipt")
	}
	receipt.ItemID = item.Data.ID
	if receipt.Material != nil {
		if ready, err := e.appleVerifyMaterials(ctx, p, receipt, true); err != nil || !ready {
			if err == nil {
				err = fmt.Errorf("App Store materials changed before review submission")
			}
			return Result{}, err
		}
	}
	if receipt.Material != nil {
		current, err := e.appleVersion(ctx, p, receipt)
		if err != nil {
			return Result{}, err
		}
		if current.related("build") != (appleRef{Type: "builds", ID: receipt.BuildID}) || current.Attributes.ReleaseType != "AFTER_APPROVAL" || !containsApple([]string{"PREPARE_FOR_SUBMISSION", "READY_FOR_REVIEW"}, current.Attributes.AppStoreState) {
			return Result{}, fmt.Errorf("App Store version/build/release policy changed before review submission")
		}
	}
	if err = e.appleCheckpoint(ctx, r, receipt, "review_submit_intent"); err != nil {
		return Result{}, err
	}
	var submitted appleResponse
	if err = e.appleCall(ctx, p, "PATCH", "/v1/reviewSubmissions/"+receipt.SubmissionID, applePayload("reviewSubmissions", receipt.SubmissionID, map[string]bool{"submitted": true}, nil), &submitted); err != nil {
		return Result{}, err
	}
	var reviewState string
	_ = json.Unmarshal(submitted.Data.Attributes.State, &reviewState)
	if !submitted.Data.is("reviewSubmissions", receipt.SubmissionID) || !containsApple([]string{"WAITING_FOR_REVIEW", "IN_REVIEW"}, reviewState) {
		return Result{}, fmt.Errorf("Apple has not confirmed review submission")
	}
	if err = e.appleCheckpoint(ctx, r, receipt, "submitted"); err != nil {
		return Result{}, err
	}
	return Result{ExternalID: appleExternalID(p, receipt), State: "awaiting_review", UpstreamState: reviewState, Reason: "准确构建已提交 App Store 审核，尚未证明上架或通过真机验收"}, nil
}
