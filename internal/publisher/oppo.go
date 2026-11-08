package publisher

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type oppoReceipt struct {
	Stage       string `json:"stage"`
	Version     string `json:"version"`
	VersionCode int64  `json:"versionCode"`
	APKMD5      string `json:"apkMD5"`
}

type oppoAppInfo struct {
	AppID         string `json:"app_id"`
	Package       string `json:"pkg_name"`
	Type          int    `json:"type"`
	Business      string `json:"is_business"`
	Frozen        string `json:"is_freeze"`
	Transfer      *int   `json:"transfer_state"`
	InfoCheck     *int   `json:"update_info_check"`
	VersionID     string `json:"version_id"`
	VersionCode   string `json:"version_code"`
	Version       string `json:"version_name"`
	VersionType   string `json:"version_type"`
	MD5           string `json:"apk_md5"`
	Size          string `json:"apk_size"`
	Audit         string `json:"audit_status"`
	State         string `json:"state"`
	ReleaseType   string `json:"release_type"`
	ReleaseStatus string `json:"release_status"`
	Online        []struct {
		AppID       string `json:"app_id"`
		VersionID   string `json:"version_id"`
		State       string `json:"online_state"`
		OnlineTime  string `json:"online_time"`
		OfflineTime string `json:"offline_time"`
	} `json:"online_info_offline_apply"`
}

func oppoExternalID(p Profile, r *oppoReceipt) string {
	return "oppo:" + p.AppID + ":" + strconv.FormatInt(r.VersionCode, 10)
}

func (c *oppoClient) app(ctx context.Context, p Profile, code string) (oppoAppInfo, error) {
	var app oppoAppInfo
	params := url.Values{"pkg_name": {p.BundleID}}
	if code != "" {
		params.Set("version_code", code)
	}
	err := c.call(ctx, "GET", "/resource/v1/app/info", params, &app)
	return app, err
}

func oppoOrdinary(p Profile, a oppoAppInfo) bool {
	return a.AppID == p.AppID && a.Package == p.BundleID && a.Type == 7 && a.Business == "0"
}
func oppoUnencumbered(a oppoAppInfo) bool {
	return a.Frozen == "0" && a.Transfer != nil && *a.Transfer == -1 && a.InfoCheck != nil && *a.InfoCheck == 0 && a.ReleaseType == "1" && (a.ReleaseStatus == "0" || a.ReleaseStatus == "4")
}
func (c *oppoClient) editable(ctx context.Context, p Profile, version int64, operationKey string) error {
	app, err := c.app(ctx, p, "")
	if err != nil {
		return err
	}
	code, parseErr := strconv.ParseInt(app.VersionCode, 10, 64)
	if !oppoOrdinary(p, app) || !oppoUnencumbered(app) || app.State != "1" || app.Audit != "111" || !oppoIDPattern.MatchString(app.VersionID) || parseErr != nil || code < 1 || code >= version {
		return fmt.Errorf("OPPO requires the exact existing ordinary app, a fully published editable prior version and a strictly newer APK; complete creation/first publication or resolve channel state in the portal")
	}
	// The asynchronous task may have been accepted while app/info still exposes
	// the old on-shelf version. A new operation cannot race that observation lag.
	var previous int64
	if err = c.engine.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version_code),0) FROM oppo_versions WHERE package_name=? AND operation_key<>?", p.BundleID, operationKey).Scan(&previous); err != nil {
		return err
	}
	if code < previous {
		return fmt.Errorf("OPPO has a newer locally accepted or uncertain version; reconcile it before another update")
	}
	return nil
}

func (e *Engine) executeOppo(ctx context.Context, p Profile, r Request, hash string) (Result, error) {
	if err := validateOppoProfile(p); err != nil {
		return Result{}, err
	}
	if r.Action == "continue" {
		return Result{}, fmt.Errorf("OPPO has no automatic mutation continuation")
	}
	journal, err := e.inspectJournal(ctx, r.OperationKey)
	if err == nil {
		if journal.requestHash != hash {
			return Result{}, fmt.Errorf("OPPO operation differs from the approved binding, material or destination")
		}
		receipt := journal.Receipt.Oppo
		if journal.State != "complete" || journal.Result == nil || receipt == nil || receipt.Stage != "submitted" {
			return Result{}, fmt.Errorf("OPPO operation is pending or uncertain; inspect without resubmitting")
		}
		if r.Action == "status" {
			if r.ExternalID != oppoExternalID(p, receipt) {
				return Result{}, fmt.Errorf("OPPO external ID differs from the persisted approved version")
			}
			return e.oppoStatus(ctx, p, r, receipt)
		}
		return *journal.Result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}
	if r.Action == "status" {
		return Result{}, fmt.Errorf("no persisted approved OPPO submission")
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
	material, err := stageOppoMaterial(p)
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(material.Dir)
	digest, err := oppoFileMD5(artifact)
	if err != nil {
		return Result{}, err
	}
	c, err := e.oppoClient(ctx, p)
	if err != nil {
		return Result{}, err
	}
	if err = c.editable(ctx, p, pkg.VersionCode, r.OperationKey); err != nil {
		return Result{}, err
	}
	receipt := &oppoReceipt{Stage: "permission_check", Version: pkg.Version, VersionCode: pkg.VersionCode, APKMD5: digest}
	encoded, _ := json.Marshal(Receipt{Oppo: receipt})
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO oppo_destinations(package_name,operation_key) VALUES(?,?)", p.BundleID, r.OperationKey); err != nil {
		return Result{}, fmt.Errorf("OPPO package has an active or uncertain operation; inspect its journal")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO oppo_versions(package_name,version_code,operation_key) VALUES(?,?,?)", p.BundleID, pkg.VersionCode, r.OperationKey); err != nil {
		return Result{}, fmt.Errorf("OPPO version already belongs to another accepted or uncertain operation")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(operation_key,request_hash,state,receipt,updated_at) VALUES(?,?,'pending',?,?)", r.OperationKey, hash, string(encoded), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return Result{}, fmt.Errorf("OPPO operation already reserved; inspect before retry")
	}
	if err = tx.Commit(); err != nil {
		return Result{}, err
	}
	if err = c.editable(ctx, p, pkg.VersionCode, r.OperationKey); err != nil {
		return Result{}, err
	}
	files := append([]oppoFile{{Name: "apk", Path: artifact, Filename: "release.apk", Type: "apk", Bytes: r.ArtifactSize}}, material.Files...)
	urls := map[string]string{}
	for _, file := range files {
		receipt.Stage = file.Name + "_upload_config_intent"
		if err = e.checkpoint(ctx, r.OperationKey, Receipt{Oppo: receipt}); err != nil {
			return Result{}, err
		}
		md, err := oppoFileMD5(file.Path)
		if err != nil {
			return Result{}, err
		}
		uploaded, err := c.upload(ctx, p, file, md, func() error {
			receipt.Stage = file.Name + "_bytes_intent"
			return e.checkpoint(ctx, r.OperationKey, Receipt{Oppo: receipt})
		})
		if err != nil {
			return Result{}, err
		}
		urls[file.Name] = uploaded.URL
	}
	m := material.Metadata
	apk, _ := json.Marshal([]struct {
		URL string `json:"url"`
		MD5 string `json:"md5"`
		CPU int    `json:"cpu_code"`
	}{{urls["apk"], receipt.APKMD5, 0}})
	params := url.Values{"pkg_name": {p.BundleID}, "version_code": {strconv.FormatInt(pkg.VersionCode, 10)}, "apk_url": {string(apk)}, "app_name": {m.AppName}, "second_category_id": {strconv.Itoa(m.SecondCategoryID)}, "third_category_id": {strconv.Itoa(m.ThirdCategoryID)}, "summary": {m.Summary}, "detail_desc": {m.DetailDesc}, "update_desc": {m.UpdateDesc}, "privacy_source_url": {m.PrivacyURL}, "test_desc": {m.TestDesc}, "business_username": {m.BusinessUsername}, "business_email": {m.BusinessEmail}, "business_mobile": {m.BusinessMobile}, "age_level": {strconv.Itoa(*m.AgeLevel)}, "adaptive_equipment": {strconv.Itoa(m.AdaptiveEquipment)}, "online_type": {"1"}, "icon_url": {urls["icon"]}, "copyright_url": {urls["copyright"]}}
	var screens []string
	for i := range m.Screenshots {
		screens = append(screens, urls[fmt.Sprintf("screen%d", i+1)])
	}
	params.Set("pic_url", strings.Join(screens, ","))
	if m.AdaptiveType != 0 {
		params.Set("adaptive_type", strconv.Itoa(m.AdaptiveType))
	}
	if m.ICP != "" {
		params.Set("icp_url", m.ICP)
	}
	var special []string
	for i := range m.Special {
		special = append(special, urls[fmt.Sprintf("special%d", i+1)])
	}
	if len(special) > 0 {
		params.Set("special_url", strings.Join(special, ","))
	}
	receipt.Stage = "submit_intent"
	if err = e.checkpoint(ctx, r.OperationKey, Receipt{Oppo: receipt}); err != nil {
		return Result{}, err
	}
	var accepted struct {
		Success *bool `json:"success"`
	}
	if err = c.call(ctx, "POST", "/resource/v1/app/upd", params, &accepted); err != nil {
		return Result{}, err
	}
	if accepted.Success == nil || !*accepted.Success {
		return Result{}, fmt.Errorf("OPPO did not confirm asynchronous task acceptance; inspect without resubmitting")
	}
	result := Result{ExternalID: oppoExternalID(p, receipt), State: "processing", UpstreamState: "TASK_ACCEPTED", Reason: "OPPO 已接受版本更新异步任务；处理、审核、实际上架与独立验收分别跟踪"}
	if err = e.finishOppo(ctx, p, r, hash, receipt, result); err != nil {
		return Result{}, fmt.Errorf("OPPO task accepted but local completion is uncertain; inspect without replay")
	}
	return result, nil
}

func (e *Engine) finishOppo(ctx context.Context, p Profile, r Request, hash string, receipt *oppoReceipt, result Result) error {
	receipt.Stage = "submitted"
	encoded, _ := json.Marshal(Receipt{Oppo: receipt})
	output, _ := json.Marshal(result)
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	updated, err := tx.ExecContext(ctx, "UPDATE operations SET state='complete',receipt=?,result=?,updated_at=? WHERE operation_key=? AND request_hash=? AND state='pending'", string(encoded), string(output), time.Now().UTC().Format(time.RFC3339Nano), r.OperationKey, hash)
	if err != nil {
		return err
	}
	if n, err := updated.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("OPPO intent changed before completion")
	}
	deleted, err := tx.ExecContext(ctx, "DELETE FROM oppo_destinations WHERE package_name=? AND operation_key=?", p.BundleID, r.OperationKey)
	if err != nil {
		return err
	}
	if n, err := deleted.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("OPPO package reservation changed before completion")
	}
	return tx.Commit()
}
