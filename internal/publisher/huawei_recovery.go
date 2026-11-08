package publisher

import (
	"context"
	"fmt"
	"time"
)

func (e *Engine) recoverHuawei(ctx context.Context, p Profile, r Request, v *huaweiReceipt) (Result, error) {
	c, err := e.huaweiClient(ctx, p)
	if err != nil {
		return Result{}, err
	}
	app, err := c.app(ctx, p)
	if err != nil {
		return Result{}, err
	}
	packages, err := c.packages(ctx, p)
	if err != nil {
		return Result{}, err
	}
	if v.PackageID == "" && v.Stage == "file_registration_intent" {
		for _, pkg := range packages {
			if pkg.VersionCode == v.VersionCode {
				if v.PackageID != "" {
					return Result{}, fmt.Errorf("Huawei recovery package identity is ambiguous")
				}
				v.PackageID = pkg.PackageID
			}
		}
	}
	confirmed, err := huaweiApprovedPackage(packages, r, v)
	if err != nil {
		return Result{}, err
	}
	if !confirmed {
		return Result{}, fmt.Errorf("Huawei has not confirmed the exact approved registered APK; retain pending operation")
	}
	if v.Stage == "submit_intent" || v.Stage == "submitted" {
		result := huaweiObservedStatus(p, v, app, true)
		if result.State == "unchanged" {
			return Result{}, fmt.Errorf("Huawei has not confirmed the persisted business version was submitted; retain pending operation")
		}
		v.Stage = "submitted"
		return result, nil
	}
	if app.VersionCode != v.VersionCode || app.VersionNumber != v.Version || !huaweiResourcePattern.MatchString(app.VersionID) || !huaweiEditable(app) || (v.VersionID != "" && v.VersionID != app.VersionID) {
		return Result{}, fmt.Errorf("Huawei current business version is different or not editable; retain pending operation")
	}
	// Registration may have succeeded before its timestamp was checkpointed. A
	// new local wait is conservative; no upload or submission is replayed here.
	if v.RegisteredAt == "" {
		v.RegisteredAt = e.huaweiNow().UTC().Format(time.RFC3339Nano)
	}
	if _, err = time.Parse(time.RFC3339Nano, v.RegisteredAt); err != nil {
		return Result{}, fmt.Errorf("Huawei registration timestamp is invalid")
	}
	v.Stage = "processing"
	result, waiting, err := e.huaweiWaiting(p, v)
	if err != nil {
		return Result{}, err
	}
	if !waiting {
		result.ContinueRequired = true
		result.UpstreamState = "PACKAGE_PARSED"
		result.Reason = "已只读核对准确 APK 及可编辑业务版本；原审批下可继续提审，尚未上架"
	}
	return result, nil
}
