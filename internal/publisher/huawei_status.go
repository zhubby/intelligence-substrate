package publisher

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (e *Engine) huaweiWaiting(p Profile, receipt *huaweiReceipt) (Result, bool, error) {
	result := Result{ExternalID: huaweiExternalID(p, receipt), State: "processing", UpstreamState: "PACKAGE_PROCESSING", Reason: "等待华为解析完整 APK，尚未提审"}
	registered, err := time.Parse(time.RFC3339Nano, receipt.RegisteredAt)
	if err != nil {
		return Result{}, false, fmt.Errorf("Huawei processing timestamp is incomplete; inspect the journal")
	}
	return result, e.huaweiNow().Before(registered.Add(2 * time.Minute)), nil
}

func (c *huaweiClient) approvedPackage(ctx context.Context, p Profile, r Request, receipt *huaweiReceipt) (bool, error) {
	packages, err := c.packages(ctx, p)
	if err != nil {
		return false, err
	}
	return huaweiApprovedPackage(packages, r, receipt)
}

func huaweiApprovedPackage(packages []huaweiPackage, r Request, receipt *huaweiReceipt) (bool, error) {
	var approved *huaweiPackage
	codeCount := 0
	for i := range packages {
		pkg := &packages[i]
		if pkg.VersionCode == receipt.VersionCode {
			codeCount++
		}
		if pkg.PackageID == receipt.PackageID {
			approved = pkg
		}
		if (pkg.VersionCode == receipt.VersionCode) != (pkg.PackageID == receipt.PackageID) {
			return false, fmt.Errorf("Huawei package ID and versionCode differ from the persisted approved upload")
		}
	}
	if codeCount > 1 {
		return false, fmt.Errorf("Huawei versionCode is ambiguous across uploaded packages")
	}
	if approved == nil {
		return false, nil
	}
	digest := strings.ToLower(approved.SHA256)
	if approved.FileName != receipt.FileName || approved.PackageSize != r.ArtifactSize || (approved.VersionName != "" && approved.VersionName != receipt.Version) || (digest != "" && digest != r.ArtifactDigest) {
		return false, fmt.Errorf("Huawei parsed APK differs from the approved filename, version, size or SHA256")
	}
	return digest == r.ArtifactDigest && approved.VersionName == receipt.Version, nil
}

func (c *huaweiClient) ready(ctx context.Context, p Profile, r Request, receipt *huaweiReceipt) (*huaweiAppInfo, bool, error) {
	app, err := c.app(ctx, p)
	if err != nil {
		return nil, false, err
	}
	confirmed, err := c.approvedPackage(ctx, p, r, receipt)
	if err != nil || !confirmed {
		return app, false, err
	}
	if app.VersionCode != receipt.VersionCode || app.VersionID == "" {
		return app, false, nil
	}
	if app.VersionNumber != receipt.Version || !huaweiResourcePattern.MatchString(app.VersionID) || !huaweiEditable(app) {
		return app, false, fmt.Errorf("Huawei current business version is different or no longer editable; reconcile before submission")
	}
	return app, true, nil
}

func (e *Engine) huaweiStatus(ctx context.Context, p Profile, r Request, receipt *huaweiReceipt) (Result, error) {
	if receipt.Stage == "processing" {
		result, waiting, err := e.huaweiWaiting(p, receipt)
		if err != nil || waiting {
			return result, err
		}
		client, err := e.huaweiClient(ctx, p)
		if err != nil {
			return Result{}, err
		}
		_, ready, err := client.ready(ctx, p, r, receipt)
		if err != nil {
			return Result{}, err
		}
		if ready {
			result.ContinueRequired, result.UpstreamState, result.Reason = true, "PACKAGE_PARSED", "准确 APK 已解析，可在原审批下继续提审；尚未上架"
		}
		return result, nil
	}
	if receipt.Stage != "submitted" || !huaweiResourcePattern.MatchString(receipt.VersionID) {
		return Result{}, fmt.Errorf("Huawei stage is not safely observable; inspect without replay")
	}
	client, err := e.huaweiClient(ctx, p)
	if err != nil {
		return Result{}, err
	}
	app, err := client.app(ctx, p)
	if err != nil {
		return Result{}, err
	}
	confirmed, err := client.approvedPackage(ctx, p, r, receipt)
	if err != nil {
		return Result{}, err
	}
	result := huaweiObservedStatus(p, receipt, app, confirmed)
	if result.State == "published" || result.State == "rejected" || result.State == "halted" {
		// Status performs no upstream mutation. Recovery uses the pure observer
		// and releases its own lock only inside the journal CAS transaction.
		if _, err := e.db.ExecContext(ctx, "DELETE FROM huawei_destinations WHERE app_id=? AND operation_key=?", p.AppID, r.OperationKey); err != nil {
			return Result{}, err
		}
	}
	return result, nil
}

func huaweiObservedStatus(p Profile, receipt *huaweiReceipt, app *huaweiAppInfo, confirmed bool) Result {
	result := Result{ExternalID: huaweiExternalID(p, receipt), State: "unchanged", UpstreamState: "releaseState=" + strconv.Itoa(*app.ReleaseState), Reason: "当前渠道信息未能确认本次准确版本的状态，保留已知结论并人工核对"}
	if !confirmed {
		return result
	}
	current := app.VersionID == receipt.VersionID && app.VersionCode == receipt.VersionCode && app.VersionNumber == receipt.Version
	shelf := app.OnShelfVersionID == receipt.VersionID && app.OnShelfVersionCode == receipt.VersionCode && app.OnShelfVersionNumber == receipt.Version
	switch *app.ReleaseState {
	case 0:
		if shelf {
			result.State, result.Reason = "published", "华为报告本次准确版本已上架；仍需目标地区和真实设备独立验收"
		}
	case 1, 8, 13:
		if current {
			result.State, result.Reason = "rejected", "华为拒绝了本次准确版本，需核对渠道原因并提交新的已批准修复版本"
		}
	case 2, 10:
		if current || shelf {
			result.State, result.Reason = "halted", "华为报告本次准确版本已下架，需人工处理"
		}
	case 11:
		if current {
			result.State, result.Reason = "halted", "本次准确版本的发布已被取消"
		}
	case 3:
		if current {
			result.State, result.Reason = "awaiting_release", "本次准确版本正在发布，尚未确认上架"
		}
	case 4, 5, 12:
		if current {
			result.State, result.Reason = "awaiting_review", "本次准确版本正在审核或预审，尚未确认上架"
		}
	}
	return result
}

func (e *Engine) huaweiContinue(ctx context.Context, p Profile, r Request, journal operationJournal) (Result, error) {
	receipt := journal.Receipt.Huawei
	if receipt.Stage != "processing" {
		return Result{}, fmt.Errorf("Huawei operation is not an approved processing upload")
	}
	result, waiting, err := e.huaweiWaiting(p, receipt)
	if err != nil || waiting {
		return result, err
	}
	client, err := e.huaweiClient(ctx, p)
	if err != nil {
		return Result{}, err
	}
	app, ready, err := client.ready(ctx, p, r, receipt)
	if err != nil || !ready {
		return result, err
	}
	var owner string
	if err = e.db.QueryRowContext(ctx, "SELECT operation_key FROM huawei_destinations WHERE app_id=?", p.AppID).Scan(&owner); err != nil || owner != r.OperationKey {
		return Result{}, fmt.Errorf("Huawei operation no longer owns the destination; inspect before submission")
	}
	receipt.VersionID, receipt.Stage = app.VersionID, "submission_check_intent"
	reserved, err := e.db.ExecContext(ctx, "UPDATE operations SET state='pending',receipt=?,updated_at=? WHERE operation_key=? AND request_hash=? AND state='complete' AND receipt=?", huaweiReceiptJSON(journal.Receipt), time.Now().UTC().Format(time.RFC3339Nano), r.OperationKey, journal.requestHash, journal.receipt)
	if err != nil {
		return Result{}, err
	}
	count, _ := reserved.RowsAffected()
	if count != 1 {
		return Result{}, fmt.Errorf("Huawei continuation is already reserved; inspect before retry")
	}
	// The upstream submit API has no version compare-and-swap. The administrator
	// must prevent other AGC writers; re-read all available identity just before it.
	app, ready, err = client.ready(ctx, p, r, receipt)
	if err != nil {
		return Result{}, err
	}
	if !ready || app.VersionID != receipt.VersionID {
		return Result{}, fmt.Errorf("Huawei selected version changed before submission; reconcile without replay")
	}
	if err = e.huaweiCheckpoint(ctx, r, receipt, "submit_intent"); err != nil {
		return Result{}, err
	}
	if _, err = client.call(ctx, "POST", "/api/publish/v2/app-submit?"+huaweiAppQuery(p).Encode(), nil); err != nil {
		return Result{}, err
	}
	if err = e.huaweiCheckpoint(ctx, r, receipt, "submitted"); err != nil {
		return Result{}, err
	}
	result = Result{ExternalID: huaweiExternalID(p, receipt), State: "awaiting_review", UpstreamState: "SUBMIT_ACCEPTED", Reason: "华为已接受提审，尚未确认审核通过或上架"}
	if err = e.finish(ctx, r.OperationKey, result); err != nil {
		return Result{}, fmt.Errorf("Huawei review accepted but local completion uncertain; reconcile without replay")
	}
	return result, nil
}
