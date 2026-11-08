package publisher

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

func (e *Engine) oppoStatus(ctx context.Context, p Profile, r Request, receipt *oppoReceipt) (Result, error) {
	return e.oppoObserve(ctx, p, r, receipt, false)
}

func (e *Engine) oppoObserve(ctx context.Context, p Profile, r Request, receipt *oppoReceipt, requireArtifact bool) (Result, error) {
	c, err := e.oppoClient(ctx, p)
	if err != nil {
		return Result{}, err
	}
	var task struct {
		Package string `json:"pkg_name"`
		Code    string `json:"version_code"`
		State   string `json:"task_state"`
	}
	code := strconv.FormatInt(receipt.VersionCode, 10)
	if err = c.call(ctx, "POST", "/resource/v1/app/task-state", url.Values{"pkg_name": {p.BundleID}, "version_code": {code}}, &task); err != nil {
		return Result{}, err
	}
	if task.Package != p.BundleID || task.Code != code {
		return Result{}, fmt.Errorf("OPPO task identity differs from persisted approved version")
	}
	var app oppoAppInfo
	if requireArtifact {
		app, err = c.app(ctx, p, code)
		if err != nil {
			return Result{}, err
		}
		if !oppoExactArtifact(p, r, receipt, app) {
			return Result{}, fmt.Errorf("OPPO recovery requires exact approved APK identity even for pending/failed tasks")
		}
	}
	if task.State == "1" {
		return Result{ExternalID: r.ExternalID, State: "processing", UpstreamState: "TASK_PENDING", Reason: "OPPO 仍在处理版本更新任务；尚不能确认提审或上架"}, nil
	}
	if task.State == "3" {
		return Result{ExternalID: r.ExternalID, State: "rejected", UpstreamState: "TASK_FAILED", Reason: "OPPO 版本更新异步任务处理失败，请由授权人员检查渠道后台；没有自动重放"}, nil
	}
	manual := Result{ExternalID: r.ExternalID, State: "unchanged", UpstreamState: "MANUAL_VERIFICATION_REQUIRED", Reason: "OPPO 精确版本或上架证据不完整，请人工核对；任务成功或旧版本在架均不能代替本次版本发布"}
	if task.State != "2" {
		return manual, nil
	}
	if !requireArtifact {
		app, err = c.app(ctx, p, code)
		if err != nil {
			return Result{}, err
		}
	}
	if !oppoExactArtifact(p, r, receipt, app) {
		return manual, nil
	}
	result := Result{ExternalID: r.ExternalID, UpstreamState: "AUDIT_" + app.Audit}
	if app.Frozen == "1" {
		result.State = "halted"
		result.UpstreamState = "APP_FROZEN"
		result.Reason = "OPPO 已冻结此应用，请人工处理渠道限制"
		return result, nil
	}
	if !oppoUnencumbered(app) {
		return manual, nil
	}
	switch app.Audit {
	case "1", "4", "00", "11", "-22":
		result.State = "awaiting_review"
		result.Reason = "OPPO 已处理版本任务，审核仍在进行；尚未确认上架"
	case "2", "6", "7":
		result.State = "awaiting_release"
		result.Reason = "OPPO 审核或运营环节已通过，仍需确认准确版本实际上架"
	case "3", "5", "-11", "444":
		result.State = "rejected"
		result.Reason = "OPPO 返回此版本的拒审或测试不通过状态，请在渠道后台检查原因"
	case "22", "222":
		result.State = "halted"
		result.Reason = "OPPO 返回此版本的冻结或下线状态"
	case "111":
		if app.State != "1" {
			return manual, nil
		}
		matches := 0
		valid := false
		for _, shelf := range app.Online {
			if shelf.AppID != p.AppID || shelf.VersionID != app.VersionID {
				continue
			}
			matches++
			online, ok := oppoStoreTime(shelf.OnlineTime)
			if !ok || shelf.State != "1" {
				continue
			}
			valid = shelf.OfflineTime == "" || shelf.OfflineTime == "0000-00-00 00:00:00"
			if !valid {
				offline, ok := oppoStoreTime(shelf.OfflineTime)
				valid = ok && online.After(offline)
			}
		}
		if matches != 1 || !valid {
			return manual, nil
		}
		result.State = "published"
		result.Reason = "OPPO 精确版本的 APK 摘要、大小和上架记录一致；仍需独立设备验收"
	default:
		return manual, nil
	}
	return result, nil
}

func oppoExactArtifact(p Profile, r Request, receipt *oppoReceipt, app oppoAppInfo) bool {
	return oppoOrdinary(p, app) && app.VersionCode == strconv.FormatInt(receipt.VersionCode, 10) && app.Version == receipt.Version && oppoIDPattern.MatchString(app.VersionID) && app.VersionType == "2" && app.MD5 == receipt.APKMD5 && app.Size == strconv.FormatInt(r.ArtifactSize, 10)
}

func oppoStoreTime(value string) (time.Time, bool) {
	// The ordinary-app API documents local portal timestamps, without an offset.
	t, err := time.ParseInLocation("2006-01-02 15:04:05", value, time.FixedZone("CST", 8*60*60))
	return t, err == nil && t.Year() >= 2000
}
