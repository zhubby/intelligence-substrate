package publisher

import (
	"context"
	"fmt"
	"net/url"
)

type vivoDetails struct {
	PackageName string `json:"packageName"`
	AppType     *int   `json:"appType"`
	SaleStatus  *int   `json:"saleStatus"`
	Status      *int   `json:"status"`
	VersionCode int64  `json:"versionCode"`
	VersionName string `json:"versionName"`
	APKSize     int64  `json:"apkSize"`
}

func (c *vivoClient) details(ctx context.Context, packageName string) (vivoDetails, error) {
	var reply vivoDetails
	err := c.call(ctx, "app.query.details", url.Values{"packageName": {packageName}}, nil, &reply)
	return reply, err
}

func (c *vivoClient) editable(ctx context.Context, p Profile, pkg androidPackage) error {
	d, err := c.details(ctx, p.AppID)
	if err != nil {
		return err
	}
	if d.PackageName != p.AppID || d.AppType == nil || *d.AppType != 1 || d.VersionCode < 1 || d.VersionCode >= pkg.VersionCode || d.VersionName == "" || d.APKSize < 1 || d.Status == nil || d.SaleStatus == nil || *d.SaleStatus < 0 || *d.SaleStatus > 2 || (*d.Status != 3 && *d.Status != 4 && *d.Status != 5) || (*d.Status == 3 && *d.SaleStatus != 1) {
		return fmt.Errorf("vivo existing application is unrelated, incomplete, busy or not editable with a higher versionCode; inspect the portal")
	}
	var lastAccepted int64
	err = c.engine.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(v.version_code),0) FROM vivo_versions v JOIN operations o ON o.operation_key=v.operation_key WHERE v.environment=? AND v.package_name=? AND o.state='complete'`, p.Environment, p.AppID).Scan(&lastAccepted)
	if err != nil {
		return err
	}
	if lastAccepted > d.VersionCode {
		return fmt.Errorf("vivo has not yet exposed the last accepted local version; inspect before another update")
	}
	return nil
}

func (c *vivoClient) status(ctx context.Context, v *vivoReceipt) (Result, error) {
	d, err := c.details(ctx, v.PackageName)
	if err != nil {
		return Result{}, err
	}
	manual := Result{ExternalID: vivoExternalID(v), State: "unchanged", UpstreamState: "MANUAL_VERIFICATION_REQUIRED", Reason: "vivo 详情不足以证明该制品已在正式渠道上架；请授权人员核对门户准确版本并登记证明，随后独立真机验收"}
	if v.Environment != "production" {
		manual.Reason = "vivo 沙箱仅用于协议调试，不能确认正式上架；请在正式配置下单独执行批准发布"
		return manual, nil
	}
	if d.PackageName != v.PackageName || d.AppType == nil || *d.AppType != 1 || d.VersionCode != v.VersionCode || d.VersionName != v.VersionName || d.APKSize != v.APKSize || d.Status == nil || d.SaleStatus == nil || *d.SaleStatus < 0 || *d.SaleStatus > 2 {
		return manual, nil
	}
	result := Result{ExternalID: vivoExternalID(v)}
	switch *d.Status {
	case 2:
		result.State, result.UpstreamState = "awaiting_review", "REVIEW_PENDING"
	case 4:
		result.State, result.UpstreamState = "rejected", "REVIEW_REJECTED"
		result.Reason = "vivo 目标版本审核未通过；请核对门户原因和相关资质，修复后按新候选版本审批"
	case 3:
		if *d.SaleStatus == 0 {
			result.State, result.UpstreamState = "awaiting_release", "REVIEW_PASSED_AWAITING_SALE"
		} else if *d.SaleStatus == 2 {
			result.State, result.UpstreamState = "halted", "OFF_SALE"
		} else {
			// app.query.details exposes general saleStatus without an APK hash
			// or an explicit on-sale version. An old live APK may still exist.
			return manual, nil
		}
	default:
		return manual, nil
	}
	return result, nil
}
