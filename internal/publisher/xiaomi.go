package publisher

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type xiaomiReceipt struct {
	Stage       string `json:"stage"`
	PackageName string `json:"packageName"`
	VersionCode int64  `json:"versionCode"`
	Version     string `json:"version"`
	SynchroType int    `json:"synchroType"`
}

func xiaomiExternalID(receipt *xiaomiReceipt) string {
	// Local tracking identity, not an upstream submission ID or shelf evidence.
	return "xiaomi:" + receipt.PackageName + ":" + strconv.FormatInt(receipt.VersionCode, 10)
}

func (e *Engine) executeXiaomi(ctx context.Context, p Profile, r Request, hash string) (Result, error) {
	if err := validateXiaomiProfile(p); err != nil {
		return Result{}, err
	}
	if r.Action == "continue" {
		return Result{}, fmt.Errorf("Xiaomi does not support automated submission continuation")
	}
	journal, err := e.inspectJournal(ctx, r.OperationKey)
	if err == nil {
		if journal.requestHash != hash {
			return Result{}, fmt.Errorf("Xiaomi operation differs from the approved binding, material or administrator destination")
		}
		receipt := journal.Receipt.Xiaomi
		if receipt == nil || journal.State != "complete" || journal.Result == nil || receipt.Stage != "submitted" {
			return Result{}, fmt.Errorf("Xiaomi operation is pending or uncertain; inspect without resubmitting")
		}
		if r.Action == "status" {
			if r.ExternalID != xiaomiExternalID(receipt) {
				return Result{}, fmt.Errorf("Xiaomi external ID differs from the persisted approved package")
			}
			return Result{ExternalID: r.ExternalID, State: "unchanged", UpstreamState: "MANUAL_VERIFICATION_REQUIRED", Reason: "小米官方未提供审核或上架状态接口；请由授权人员人工核对开发者后台并登记证据，最新推送包不代表上架"}, nil
		}
		return *journal.Result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}
	if r.Action == "status" {
		return Result{}, fmt.Errorf("no persisted approved Xiaomi submission")
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
	material, err := stageXiaomiMaterial(p)
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(material.Dir)
	client, err := e.xiaomiClient(p)
	if err != nil {
		return Result{}, err
	}
	if err = client.editable(ctx, p, pkg, *material.Metadata.SynchroType); err != nil {
		return Result{}, err
	}
	receipt := &xiaomiReceipt{Stage: "permission_check", PackageName: p.AppID, Version: pkg.Version, VersionCode: pkg.VersionCode, SynchroType: *material.Metadata.SynchroType}
	encoded, _ := json.Marshal(Receipt{Xiaomi: receipt})
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	if err = checkRecoveredStoreVersion(ctx, tx, p, r.OperationKey, pkg.VersionCode); err != nil {
		return Result{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO xiaomi_destinations(package_name,operation_key) VALUES(?,?)", p.AppID, r.OperationKey); err != nil {
		return Result{}, fmt.Errorf("Xiaomi package has an active or uncertain operation; inspect the persisted journal")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(operation_key,request_hash,state,receipt,updated_at) VALUES(?,?,'pending',?,?)", r.OperationKey, hash, string(encoded), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return Result{}, fmt.Errorf("Xiaomi operation is already reserved; inspect before retry")
	}
	if err = tx.Commit(); err != nil {
		return Result{}, err
	}
	// Permission may change between preparation and acquisition of our package
	// reservation. Recheck while holding it, before creating any external write.
	if err = client.editable(ctx, p, pkg, receipt.SynchroType); err != nil {
		return Result{}, err
	}
	receipt.Stage = "push_intent"
	if err = e.checkpoint(ctx, r.OperationKey, Receipt{Xiaomi: receipt}); err != nil {
		return Result{}, err
	}
	info := struct {
		xiaomiAppInfo
		PackageName string `json:"packageName"`
		VersionName string `json:"versionName"`
		TestAccount string `json:"testAccount,omitempty"`
	}{xiaomiAppInfo: material.Metadata.AppInfo, PackageName: p.AppID, VersionName: pkg.Version}
	if len(info.xiaomiAppInfo.TestAccount) != 0 {
		testAccount, _ := json.Marshal(info.xiaomiAppInfo.TestAccount)
		info.TestAccount = string(testAccount)
	}
	input := struct {
		UserName    string `json:"userName"`
		SynchroType int    `json:"synchroType"`
		AppInfo     any    `json:"appInfo"`
	}{client.userName, receipt.SynchroType, info}
	files := append([]xiaomiFile{{Name: "apk", Path: artifact, Filename: "release.apk"}}, material.Files...)
	if _, err = client.call(ctx, "/dev/push", input, files); err != nil {
		return Result{}, err
	}
	result := Result{ExternalID: xiaomiExternalID(receipt), State: "awaiting_review", UpstreamState: "PUSH_ACCEPTED", Reason: "小米已接受新应用或版本更新推送；审核、上架和真机验收仍需独立核对"}
	if err = e.finishXiaomi(ctx, r, hash, receipt, result); err != nil {
		return Result{}, fmt.Errorf("Xiaomi push accepted but local completion is uncertain; inspect without replay")
	}
	return result, nil
}

func (c *xiaomiClient) editable(ctx context.Context, p Profile, pkg androidPackage, synchroType int) error {
	reply, err := c.call(ctx, "/dev/query", map[string]string{"userName": c.userName, "packageName": p.AppID}, nil)
	if err != nil {
		return err
	}
	if reply.PackageInfo != nil && (reply.PackageInfo.PackageName != p.AppID || reply.PackageInfo.VersionCode < 1 || reply.PackageInfo.VersionCode >= pkg.VersionCode) {
		return fmt.Errorf("Xiaomi latest pushed package is unrelated or has an equal/newer versionCode")
	}
	if synchroType == 0 {
		if reply.Create == nil || !*reply.Create {
			return fmt.Errorf("Xiaomi has not authorized creation of this package")
		}
	} else if reply.UpdateVersion == nil || !*reply.UpdateVersion || reply.PackageInfo == nil {
		return fmt.Errorf("Xiaomi has not authorized a newer version of this existing package")
	}
	return nil
}

func (e *Engine) finishXiaomi(ctx context.Context, r Request, hash string, receipt *xiaomiReceipt, result Result) error {
	receipt.Stage = "submitted"
	encoded, _ := json.Marshal(Receipt{Xiaomi: receipt})
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
	if count, err := updated.RowsAffected(); err != nil || count != 1 {
		return fmt.Errorf("Xiaomi intent changed before local completion")
	}
	deleted, err := tx.ExecContext(ctx, "DELETE FROM xiaomi_destinations WHERE package_name=? AND operation_key=?", receipt.PackageName, r.OperationKey)
	if err != nil {
		return err
	}
	if count, err := deleted.RowsAffected(); err != nil || count != 1 {
		return fmt.Errorf("Xiaomi package reservation changed before local completion")
	}
	return tx.Commit()
}
