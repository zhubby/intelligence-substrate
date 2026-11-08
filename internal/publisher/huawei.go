package publisher

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

type huaweiReceipt struct {
	Stage        string `json:"stage"`
	ObjectID     string `json:"objectId,omitempty"`
	PackageID    string `json:"packageId,omitempty"`
	FileName     string `json:"fileName"`
	Version      string `json:"version"`
	VersionCode  int64  `json:"versionCode"`
	VersionID    string `json:"versionId,omitempty"`
	RegisteredAt string `json:"registeredAt,omitempty"`
}

type huaweiAppInfo struct {
	PublishCountry       string `json:"publishCountry"`
	ReleaseState         *int   `json:"releaseState"`
	VersionNumber        string `json:"versionNumber"`
	VersionCode          int64  `json:"versionCode"`
	VersionID            string `json:"versionId"`
	OnShelfVersionNumber string `json:"onShelfVersionNumber"`
	OnShelfVersionCode   int64  `json:"onShelfVersionCode"`
	OnShelfVersionID     string `json:"onShelfVersionId"`
}

type huaweiPackage struct {
	PackageID   string `json:"pkgVersion"`
	FileName    string `json:"fileName"`
	VersionCode int64  `json:"versionCode"`
	VersionName string `json:"versionName"`
	PackageSize int64  `json:"packageSize"`
	SHA256      string `json:"shaCode"`
	PackageType int    `json:"packageType"`
}

type huaweiResponse struct {
	huaweiReply
	AppIDs []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"appids"`
	AppInfo    *huaweiAppInfo  `json:"appInfo"`
	URLInfo    *huaweiUpload   `json:"urlInfo"`
	PkgVersion []string        `json:"pkgVersion"`
	RetCount   *int            `json:"retCount"`
	TotalCount *int            `json:"totalCount"`
	PkgList    []huaweiPackage `json:"pkgList"`
}

var huaweiResourcePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)
var huaweiObjectPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/_.-]{0,999}$`)

func huaweiExternalID(p Profile, receipt *huaweiReceipt) string {
	return "huawei:" + p.AppID + ":" + receipt.PackageID
}
func huaweiReceiptJSON(receipt Receipt) string { b, _ := json.Marshal(receipt); return string(b) }
func (e *Engine) huaweiCheckpoint(ctx context.Context, r Request, receipt *huaweiReceipt, stage string) error {
	receipt.Stage = stage
	return e.checkpoint(ctx, r.OperationKey, Receipt{Huawei: receipt})
}

func (e *Engine) executeHuawei(ctx context.Context, p Profile, r Request, hash string) (Result, error) {
	journal, err := e.inspectJournal(ctx, r.OperationKey)
	if err == nil {
		if journal.requestHash != hash {
			return Result{}, fmt.Errorf("Huawei operation differs from the approved binding or administrator destination")
		}
		receipt := journal.Receipt.Huawei
		if receipt == nil {
			return Result{}, fmt.Errorf("Huawei journal is incomplete; inspect the operation")
		}
		if (r.Action == "status" || r.Action == "continue") && (receipt.PackageID == "" || r.ExternalID != huaweiExternalID(p, receipt)) {
			return Result{}, fmt.Errorf("Huawei external ID differs from the persisted approved package")
		}
		if journal.State != "complete" || journal.Result == nil {
			return Result{}, fmt.Errorf("Huawei external write is uncertain at stage %s; inspect without resubmitting", receipt.Stage)
		}
		if r.Action == "status" {
			return e.huaweiStatus(ctx, p, r, receipt)
		}
		if r.Action != "continue" || journal.Result.State != "processing" {
			return *journal.Result, nil
		}
		return e.huaweiContinue(ctx, p, r, journal)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}
	if r.Action == "status" || r.Action == "continue" {
		return Result{}, fmt.Errorf("no persisted approved Huawei upload")
	}
	artifact, cleanup, err := stageArtifact(r)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	pkg, err := e.verifyAndroidPackage(ctx, p, r, artifact)
	if err != nil {
		return Result{}, err
	}
	if err = checkRecoveredStoreVersion(ctx, e.db, p, r.OperationKey, pkg.VersionCode); err != nil {
		return Result{}, err
	}
	client, err := e.huaweiClient(ctx, p)
	if err != nil {
		return Result{}, err
	}
	app, err := client.app(ctx, p)
	if err != nil {
		return Result{}, err
	}
	if !huaweiEditable(app) {
		return Result{}, fmt.Errorf("Huawei app must have publish countries and an editable release state")
	}
	packages, err := client.packages(ctx, p)
	if err != nil {
		return Result{}, err
	}
	for _, existing := range packages {
		if existing.VersionCode == pkg.VersionCode {
			return Result{}, fmt.Errorf("Huawei already has this versionCode; do not overwrite or adopt an unrelated upload")
		}
	}
	receipt := &huaweiReceipt{Stage: "upload_url_intent", FileName: "release-" + r.ArtifactDigest[:24] + ".apk", Version: pkg.Version, VersionCode: pkg.VersionCode}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	if err = checkRecoveredStoreVersion(ctx, tx, p, r.OperationKey, pkg.VersionCode); err != nil {
		return Result{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO huawei_destinations(app_id,operation_key) VALUES(?,?)", p.AppID, r.OperationKey); err != nil {
		return Result{}, fmt.Errorf("Huawei destination has an active or uncertain operation; inspect its persisted journal")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(operation_key,request_hash,state,receipt,updated_at) VALUES(?,?,'pending',?,?)", r.OperationKey, hash, huaweiReceiptJSON(Receipt{Huawei: receipt}), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return Result{}, fmt.Errorf("Huawei operation is already reserved; inspect before retry")
	}
	if err = tx.Commit(); err != nil {
		return Result{}, err
	}
	result, err := e.huaweiPublish(ctx, client, p, r, receipt, artifact)
	if err != nil {
		return Result{}, err
	}
	if err = e.finish(ctx, r.OperationKey, result); err != nil {
		return Result{}, fmt.Errorf("Huawei upload accepted but local completion uncertain; reconcile without replay")
	}
	return result, nil
}

func huaweiEditable(app *huaweiAppInfo) bool {
	if app == nil || app.ReleaseState == nil || app.PublishCountry == "" {
		return false
	}
	switch *app.ReleaseState {
	case 0, 1, 7, 8, 11, 13:
		return true
	}
	return false
}

func (c *huaweiClient) app(ctx context.Context, p Profile) (*huaweiAppInfo, error) {
	identity, err := c.call(ctx, "GET", "/api/publish/v2/appid-list?"+url.Values{"packageName": {p.BundleID}, "packageTypes": {"1"}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if len(identity.AppIDs) != 1 || identity.AppIDs[0].Value != p.AppID {
		return nil, fmt.Errorf("Huawei appId does not uniquely match the administrator Android package")
	}
	response, err := c.call(ctx, "GET", "/api/publish/v2/app-info?"+huaweiAppQuery(p).Encode(), nil)
	if err != nil {
		return nil, err
	}
	if response.AppInfo == nil || response.AppInfo.ReleaseState == nil {
		return nil, fmt.Errorf("Huawei app information is incomplete")
	}
	return response.AppInfo, nil
}

func huaweiAppQuery(p Profile) url.Values {
	return url.Values{"appId": {p.AppID}, "releaseType": {"1"}}
}

func (c *huaweiClient) packages(ctx context.Context, p Profile) ([]huaweiPackage, error) {
	var packages []huaweiPackage
	seen := map[string]bool{}
	total := -1
	for start := 1; start <= 1000; start += 100 {
		query := url.Values{"appId": {p.AppID}, "fromRecCount": {strconv.Itoa(start)}, "maxReqCount": {"100"}}
		response, err := c.call(ctx, "GET", "/api/publish/v2/package-list?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		if response.RetCount == nil || response.TotalCount == nil || *response.TotalCount < 0 || *response.TotalCount > 1000 || *response.RetCount != len(response.PkgList) || len(response.PkgList) > 100 || (total >= 0 && total != *response.TotalCount) || (len(response.PkgList) == 0 && start-1 < *response.TotalCount) {
			return nil, fmt.Errorf("Huawei package pagination is incomplete, changed or exceeds the 1000-package bound")
		}
		total = *response.TotalCount
		for _, pkg := range response.PkgList {
			if !huaweiResourcePattern.MatchString(pkg.PackageID) || seen[pkg.PackageID] || pkg.VersionCode < 1 || pkg.PackageSize < 1 || pkg.PackageType != 1 {
				return nil, fmt.Errorf("Huawei package list contains ambiguous APK identities")
			}
			seen[pkg.PackageID] = true
			packages = append(packages, pkg)
		}
		if len(packages) == total {
			return packages, nil
		}
		if len(packages) > total || len(response.PkgList) != 100 {
			return nil, fmt.Errorf("Huawei package list is truncated or inconsistent")
		}
	}
	return nil, fmt.Errorf("Huawei package list exceeds the query bound")
}

func (e *Engine) huaweiPublish(ctx context.Context, client *huaweiClient, p Profile, r Request, receipt *huaweiReceipt, artifact string) (Result, error) {
	query := huaweiAppQuery(p)
	query.Set("fileName", receipt.FileName)
	query.Set("contentLength", strconv.FormatInt(r.ArtifactSize, 10))
	query.Set("sha256", r.ArtifactDigest)
	query.Set("chineseMainlandFlag", "0")
	if *p.ChineseMainland {
		query.Set("chineseMainlandFlag", "1")
	}
	upload, err := client.call(ctx, "GET", "/api/publish/v2/upload-url/for-obs?"+query.Encode(), nil)
	if err != nil {
		return Result{}, err
	}
	if upload.URLInfo == nil || !huaweiObjectPattern.MatchString(upload.URLInfo.ObjectID) {
		return Result{}, fmt.Errorf("Huawei upload reservation has no valid object ID")
	}
	receipt.ObjectID = upload.URLInfo.ObjectID
	if err = e.huaweiCheckpoint(ctx, r, receipt, "byte_upload_intent"); err != nil {
		return Result{}, err
	}
	if err = e.huaweiUploadBytes(ctx, client, p, r, artifact, upload.URLInfo); err != nil {
		return Result{}, err
	}
	if err = e.huaweiCheckpoint(ctx, r, receipt, "file_registration_intent"); err != nil {
		return Result{}, err
	}
	registered, err := client.call(ctx, "PUT", "/api/publish/v2/app-file-info?"+huaweiAppQuery(p).Encode(), map[string]any{"fileType": 5, "files": []map[string]string{{"fileName": receipt.FileName, "fileDestUrl": receipt.ObjectID}}})
	if err != nil {
		return Result{}, err
	}
	if len(registered.PkgVersion) != 1 || !huaweiResourcePattern.MatchString(registered.PkgVersion[0]) {
		return Result{}, fmt.Errorf("Huawei file registration did not confirm one package ID; inspect without replay")
	}
	receipt.PackageID, receipt.RegisteredAt = registered.PkgVersion[0], e.huaweiNow().UTC().Format(time.RFC3339Nano)
	if err = e.huaweiCheckpoint(ctx, r, receipt, "processing"); err != nil {
		return Result{}, err
	}
	return Result{ExternalID: huaweiExternalID(p, receipt), State: "processing", UpstreamState: "PACKAGE_REGISTERED", Reason: "APK 上传和登记已接受，至少等待两分钟并核对解析结果；尚未提审"}, nil
}
