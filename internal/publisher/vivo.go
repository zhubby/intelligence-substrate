package publisher

import (
	"context"
	"crypto/md5" // vivo requires MD5 on the wire; approval binds SHA256.
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type vivoReceipt struct {
	Stage       string            `json:"stage"`
	Environment string            `json:"environment"`
	PackageName string            `json:"packageName"`
	VersionCode int64             `json:"versionCode"`
	VersionName string            `json:"versionName"`
	APKMD5      string            `json:"apkMD5"`
	APKSize     int64             `json:"apkSize"`
	Serials     map[string]string `json:"serials"`
}

func vivoExternalID(v *vivoReceipt) string {
	// The synchronous endpoint returns no official task ID. Include the fixed
	// environment so sandbox observations can never impersonate production.
	return "vivo:" + v.Environment + ":" + v.PackageName + ":" + strconv.FormatInt(v.VersionCode, 10)
}

func (e *Engine) executeVivo(ctx context.Context, p Profile, r Request, hash string) (Result, error) {
	if err := validateVivoProfile(p); err != nil {
		return Result{}, err
	}
	if r.Action == "continue" {
		return Result{}, fmt.Errorf("vivo does not support automatic update continuation")
	}
	journal, err := e.inspectJournal(ctx, r.OperationKey)
	if err == nil {
		if journal.requestHash != hash {
			return Result{}, fmt.Errorf("vivo operation differs from the approved binding, environment, destination or material")
		}
		v := journal.Receipt.Vivo
		if v == nil || journal.State != "complete" || journal.Result == nil || v.Stage != "submitted" || v.Environment != p.Environment || v.PackageName != p.AppID || v.VersionName != strings.TrimPrefix(r.Version, "v") || v.APKSize != r.ArtifactSize || v.VersionCode < 1 || journal.Result.ExternalID != vivoExternalID(v) {
			return Result{}, fmt.Errorf("vivo operation is pending or uncertain; inspect without resubmitting")
		}
		if r.Action == "status" {
			if r.ExternalID != vivoExternalID(v) {
				return Result{}, fmt.Errorf("vivo external ID differs from the persisted approved environment/package/version")
			}
			client, err := e.vivoClient(p)
			if err != nil {
				return Result{}, err
			}
			return client.status(ctx, v)
		}
		return *journal.Result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}
	if r.Action == "status" {
		return Result{}, fmt.Errorf("no persisted approved vivo submission")
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
	material, err := stageVivoMaterial(p)
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(material.Dir)
	client, err := e.vivoClient(p)
	if err != nil {
		return Result{}, err
	}
	var reserved int
	err = e.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM vivo_destinations WHERE environment=? AND package_name=?) OR EXISTS(SELECT 1 FROM vivo_versions WHERE environment=? AND package_name=? AND version_code=?)`, p.Environment, p.AppID, p.Environment, p.AppID, pkg.VersionCode).Scan(&reserved)
	if err != nil {
		return Result{}, err
	}
	if reserved != 0 {
		return Result{}, fmt.Errorf("vivo package/version is active or reserved; inspect without resubmitting")
	}
	if err = client.editable(ctx, p, pkg); err != nil {
		return Result{}, err
	}
	in, err := os.Open(artifact)
	if err != nil {
		return Result{}, err
	}
	h := md5.New()
	_, err = io.Copy(h, in)
	_ = in.Close()
	if err != nil {
		return Result{}, err
	}
	v := &vivoReceipt{Stage: "permission_check", Environment: p.Environment, PackageName: p.AppID, VersionCode: pkg.VersionCode, VersionName: pkg.Version, APKMD5: hex.EncodeToString(h.Sum(nil)), APKSize: r.ArtifactSize, Serials: map[string]string{}}
	encoded, _ := json.Marshal(Receipt{Vivo: v})
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO vivo_destinations(environment,package_name,operation_key) VALUES(?,?,?)", p.Environment, p.AppID, r.OperationKey); err != nil {
		return Result{}, fmt.Errorf("vivo package is active or reserved; inspect its persisted operation")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO vivo_versions(environment,package_name,version_code,operation_key) VALUES(?,?,?,?)", p.Environment, p.AppID, pkg.VersionCode, r.OperationKey); err != nil {
		return Result{}, fmt.Errorf("vivo versionCode is already reserved; inspect before retry")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(operation_key,request_hash,state,receipt,updated_at) VALUES(?,?,'pending',?,?)", r.OperationKey, hash, string(encoded), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return Result{}, fmt.Errorf("vivo operation is already reserved; inspect before retry")
	}
	if err = tx.Commit(); err != nil {
		return Result{}, err
	}
	// Recheck account state while holding our durable package reservation.
	if err = client.editable(ctx, p, pkg); err != nil {
		return Result{}, err
	}
	files := append([]vivoFile{{Name: "apk", Method: "app.upload.apk.app", Path: artifact, Filename: "release.apk"}}, material.Files...)
	for _, file := range files {
		v.Stage = file.Name + "_upload_intent"
		if err = e.checkpointVivo(ctx, r, hash, v); err != nil {
			return Result{}, err
		}
		values := url.Values{"packageName": {p.AppID}}
		if file.Name == "apk" {
			values.Set("fileMd5", v.APKMD5)
		}
		var reply vivoUploadReply
		if err = client.call(ctx, file.Method, values, &file, &reply); err != nil {
			return Result{}, err
		}
		if reply.PackageName != p.AppID || !vivoSerialPattern.MatchString(reply.SerialNumber) || (file.Name == "apk" && (reply.VersionCode != v.VersionCode || reply.VersionName != v.VersionName || reply.FileMD5 != v.APKMD5)) {
			return Result{}, fmt.Errorf("vivo upload receipt does not match the approved package, version or bytes; inspect without replay")
		}
		for _, previous := range v.Serials {
			if previous == reply.SerialNumber {
				return Result{}, fmt.Errorf("vivo upload receipt reused another attachment's serial; inspect without replay")
			}
		}
		v.Serials[file.Name] = reply.SerialNumber
	}
	v.Stage = "submit_intent"
	if err = e.checkpointVivo(ctx, r, hash, v); err != nil {
		return Result{}, err
	}
	m := material.Metadata
	values := url.Values{"packageName": {p.AppID}, "versionCode": {strconv.FormatInt(v.VersionCode, 10)}, "apk": {v.Serials["apk"]}, "fileMd5": {v.APKMD5}, "onlineType": {"1"}, "updateDesc": {m.UpdateDesc}, "compatibleDevice": {strconv.Itoa(m.CompatibleDevice)}, "privateSelfCheck": {m.PrivateSelfCheck}}
	for key, value := range map[string]string{"detailDesc": m.DetailDesc, "simpleDesc": m.SimpleDesc, "remark": m.Remark, "customerService": m.CustomerService, "icon": v.Serials["icon"]} {
		if value != "" {
			values.Set(key, value)
		}
	}
	if len(m.Screenshots) > 0 {
		var serials []string
		for i := range m.Screenshots {
			serials = append(serials, v.Serials[fmt.Sprintf("screenshot%d", i+1)])
		}
		values.Set("screenshot", strings.Join(serials, ","))
	}
	if m.RateAge != nil {
		values.Set("rateAge", strconv.Itoa(*m.RateAge))
	}
	if len(m.TestAccount) > 0 {
		b, _ := json.Marshal(m.TestAccount)
		values.Set("testAccount", string(b))
	}
	if err = client.call(ctx, "app.sync.update.app", values, nil, nil); err != nil {
		return Result{}, err
	}
	result := Result{ExternalID: vivoExternalID(v), State: "awaiting_review", UpstreamState: "UPDATE_ACCEPTED", Reason: "vivo 已接受同步更新；审核、正式版本上架和独立真机验收仍需核对"}
	if p.Environment == "sandbox" {
		result.UpstreamState = "SANDBOX_UPDATE_ACCEPTED"
		result.Reason = "vivo 沙箱已接受同步更新；沙箱结果不能用于正式发布或生产验收"
	}
	if err = e.finishVivo(ctx, r, hash, v, result); err != nil {
		return Result{}, fmt.Errorf("vivo update accepted but local completion is uncertain; inspect without replay")
	}
	return result, nil
}

func (e *Engine) checkpointVivo(ctx context.Context, r Request, hash string, v *vivoReceipt) error {
	encoded, _ := json.Marshal(Receipt{Vivo: v})
	updated, err := e.db.ExecContext(ctx, `UPDATE operations SET receipt=?,updated_at=? WHERE operation_key=? AND request_hash=? AND state='pending'
	AND EXISTS(SELECT 1 FROM vivo_destinations WHERE environment=? AND package_name=? AND operation_key=?)
	AND EXISTS(SELECT 1 FROM vivo_versions WHERE environment=? AND package_name=? AND version_code=? AND operation_key=?)`,
		string(encoded), time.Now().UTC().Format(time.RFC3339Nano), r.OperationKey, hash,
		v.Environment, v.PackageName, r.OperationKey, v.Environment, v.PackageName, v.VersionCode, r.OperationKey)
	if err != nil {
		return err
	}
	if count, err := updated.RowsAffected(); err != nil || count != 1 {
		return fmt.Errorf("vivo approved operation or package/version ownership changed before external write")
	}
	return nil
}

func (e *Engine) finishVivo(ctx context.Context, r Request, hash string, v *vivoReceipt, result Result) error {
	v.Stage = "submitted"
	encoded, _ := json.Marshal(Receipt{Vivo: v})
	out, _ := json.Marshal(result)
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	updated, err := tx.ExecContext(ctx, "UPDATE operations SET state='complete',receipt=?,result=?,updated_at=? WHERE operation_key=? AND request_hash=? AND state='pending'", string(encoded), string(out), time.Now().UTC().Format(time.RFC3339Nano), r.OperationKey, hash)
	if err != nil {
		return err
	}
	if count, err := updated.RowsAffected(); err != nil || count != 1 {
		return fmt.Errorf("vivo operation changed before completion")
	}
	deleted, err := tx.ExecContext(ctx, "DELETE FROM vivo_destinations WHERE environment=? AND package_name=? AND operation_key=?", v.Environment, v.PackageName, r.OperationKey)
	if err != nil {
		return err
	}
	if count, err := deleted.RowsAffected(); err != nil || count != 1 {
		return fmt.Errorf("vivo package ownership changed before completion")
	}
	return tx.Commit()
}
