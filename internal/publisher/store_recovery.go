package publisher

import (
	"context"
	"crypto/md5" // OPPO/vivo report MD5; SHA256 verifies approved local bytes first.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// StoreReconciliationRequest is local administrator input, never a Worker action.
// The operator is a local attestation, not an authenticated platform identity.
type StoreReconciliationRequest struct {
	Request                    Request `json:"request"`
	JournalDigest              string  `json:"journalDigest"`
	ConfirmStoppedOperationKey string  `json:"confirmStoppedOperationKey"`
	Operator                   string  `json:"operator"`
	Reason                     string  `json:"reason"`
	Proof                      string  `json:"proof"`
	EvidenceFile               string  `json:"evidenceFile,omitempty"`
	EvidenceDigest             string  `json:"evidenceDigest,omitempty"`
}

type StoreReconciliation struct {
	JournalDigest                string                    `json:"journalDigest"`
	ResultJournalDigest          string                    `json:"resultJournalDigest"`
	InputDigest                  string                    `json:"inputDigest"`
	Driver                       string                    `json:"driver"`
	Environment                  string                    `json:"environment"`
	AppID                        string                    `json:"appId"`
	VersionCode                  int64                     `json:"versionCode"`
	Proof                        string                    `json:"proof"`
	Operator                     string                    `json:"operator"`
	Reason                       string                    `json:"reason"`
	ConfirmedStoppedOperationKey string                    `json:"confirmedStoppedOperationKey"`
	FromStage                    string                    `json:"fromStage"`
	ToStage                      string                    `json:"toStage"`
	ResultState                  string                    `json:"resultState"`
	ObservedAt                   string                    `json:"observedAt"`
	EvidenceDigest               string                    `json:"evidenceDigest,omitempty"`
	Attachments                  []StoreEvidenceAttachment `json:"attachments,omitempty"`
	PortalReference              string                    `json:"portalReference,omitempty"`
	PortalObservedAt             string                    `json:"portalObservedAt,omitempty"`
	ObservedResult               Result                    `json:"observedResult"`
}

func storeEnvironment(p Profile) string {
	if p.Environment != "" {
		return p.Environment
	}
	return "production"
}

func boundedStoreText(s string, limit int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= limit && !strings.ContainsRune(s, '\x00')
}

func (e *Engine) ReconcileStore(ctx context.Context, in StoreReconciliationRequest) (Operation, error) {
	if err := validateRequest(in.Request); err != nil {
		return Operation{}, err
	}
	if !hashPattern.MatchString(in.JournalDigest) || in.ConfirmStoppedOperationKey != in.Request.OperationKey || !boundedStoreText(in.Operator, 256) || !boundedStoreText(in.Reason, 4096) {
		return Operation{}, fmt.Errorf("exact journal digest, stopped operation confirmation, operator and bounded reason required")
	}
	p, err := e.profile(in.Request)
	if err != nil {
		return Operation{}, err
	}
	switch p.Driver {
	case "huawei-appgallery", "oppo-store":
		if in.Proof != "api" || in.EvidenceFile != "" || in.EvidenceDigest != "" {
			return Operation{}, fmt.Errorf("this store requires exact API proof")
		}
	case "xiaomi-market", "vivo-store":
		if in.Proof != "portal" || in.EvidenceFile == "" || !hashPattern.MatchString(in.EvidenceDigest) {
			return Operation{}, fmt.Errorf("this store requires explicit digest-bound portal evidence")
		}
	default:
		return Operation{}, fmt.Errorf("domestic recovery requires a fixed domestic store profile")
	}
	j, err := e.inspectJournal(ctx, in.Request.OperationKey)
	if err != nil {
		return Operation{}, err
	}
	if j.requestHash != requestHash(in.Request, p) {
		return Operation{}, fmt.Errorf("recovery differs from the approved binding, material or administrator destination")
	}
	stage, code, externalID, err := storeRecoveryBinding(p, in.Request, j.Receipt)
	if err != nil {
		return Operation{}, err
	}
	if in.Request.ExternalID != "" && in.Request.ExternalID != externalID {
		return Operation{}, fmt.Errorf("recovery external ID differs from the persisted operation")
	}
	b, _ := json.Marshal([]string{j.requestHash, in.JournalDigest, in.ConfirmStoppedOperationKey, in.Operator, in.Reason, in.Proof, in.EvidenceDigest})
	h := sha256.Sum256(b)
	inputDigest := hex.EncodeToString(h[:])
	for _, previous := range j.StoreReconciliations {
		if previous.JournalDigest == in.JournalDigest {
			if previous.InputDigest == inputDigest && j.JournalDigest == previous.ResultJournalDigest {
				return j.Operation, nil
			}
			return Operation{}, fmt.Errorf("recovery input or subsequent operation state changed; inspect the current journal")
		}
	}
	if j.State != "pending" || j.JournalDigest != in.JournalDigest {
		return Operation{}, fmt.Errorf("only the exact pending snapshot can be reconciled")
	}
	if err = checkStoreRecoveryOwners(ctx, e.db, p, in.Request.OperationKey, code); err != nil {
		return Operation{}, err
	}
	artifact, cleanup, err := stageArtifact(in.Request)
	if err != nil {
		return Operation{}, err
	}
	defer cleanup()
	pkg, err := e.verifyAndroidPackage(ctx, p, in.Request, artifact)
	if err != nil {
		return Operation{}, err
	}
	if pkg.VersionCode != code || pkg.Version != strings.TrimPrefix(in.Request.Version, "v") {
		return Operation{}, fmt.Errorf("recovery APK differs from persisted version")
	}
	f, err := os.Open(artifact)
	if err != nil {
		return Operation{}, err
	}
	checksum := md5.New()
	_, err = io.Copy(checksum, f)
	f.Close()
	if err != nil {
		return Operation{}, err
	}
	md5sum := hex.EncodeToString(checksum.Sum(nil))
	if j.Receipt.Oppo != nil && j.Receipt.Oppo.APKMD5 != md5sum || j.Receipt.Vivo != nil && j.Receipt.Vivo.APKMD5 != md5sum {
		return Operation{}, fmt.Errorf("recovery APK MD5 differs from persisted upload")
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	var result Result
	var evidence *storePortalBundle
	if in.Proof == "portal" {
		if err = privateStoreEvidenceState(e.Config.StateDir); err != nil {
			return Operation{}, err
		}
		evidence, err = readStorePortalEvidence(in, p, code)
		if err == nil {
			result = Result{ExternalID: externalID, State: "processing", UpstreamState: "PORTAL_ACCEPTED_UNVERIFIED", Reason: "管理员凭据确认渠道接受原提交；此为人工声明，审核、上架和独立设备验收仍待核对"}
			if p.Driver == "xiaomi-market" {
				j.Receipt.Xiaomi.Stage = "submitted"
			} else {
				j.Receipt.Vivo.Stage = "submitted"
				if p.Environment == "sandbox" {
					result.Reason = "管理员凭据确认沙箱接受原提交；不构成正式渠道发布或交付证据"
				}
			}
		}
	} else if p.Driver == "huawei-appgallery" {
		result, err = e.recoverHuawei(ctx, p, in.Request, j.Receipt.Huawei)
	} else {
		q := in.Request
		q.ExternalID = externalID
		result, err = e.oppoObserve(ctx, p, q, j.Receipt.Oppo, true)
		if err == nil && result.State == "unchanged" {
			err = fmt.Errorf("OPPO has not positively confirmed the exact submitted artifact; retain pending operation")
		}
		if err == nil {
			j.Receipt.Oppo.Stage = "submitted"
		}
	}
	if err != nil {
		return Operation{}, err
	}
	record := StoreReconciliation{JournalDigest: in.JournalDigest, InputDigest: inputDigest, Driver: p.Driver, Environment: storeEnvironment(p), AppID: p.AppID, VersionCode: code, Proof: in.Proof, Operator: in.Operator, Reason: in.Reason, ConfirmedStoppedOperationKey: in.ConfirmStoppedOperationKey, FromStage: stage, ResultState: result.State, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), EvidenceDigest: in.EvidenceDigest, ObservedResult: result}
	switch p.Driver {
	case "huawei-appgallery":
		record.ToStage = j.Receipt.Huawei.Stage
	case "oppo-store":
		record.ToStage = j.Receipt.Oppo.Stage
	default:
		record.ToStage = "submitted"
	}
	if evidence != nil {
		record.PortalReference = evidence.report.Reference
		record.PortalObservedAt = evidence.report.ObservedAt
		for _, a := range evidence.report.Attachments {
			a.Path = ""
			record.Attachments = append(record.Attachments, a)
		}
	}
	return e.commitStoreRecovery(ctx, p, j, record, result, evidence)
}

func storeRecoveryBinding(p Profile, r Request, receipt Receipt) (string, int64, string, error) {
	bad := func() (string, int64, string, error) {
		return "", 0, "", fmt.Errorf("this stage lacks sufficient persisted identity or recoverable final intent; retain the blocked operation")
	}
	count := 0
	for _, present := range []bool{receipt.Apple != nil, receipt.Huawei != nil, receipt.Oppo != nil, receipt.Xiaomi != nil, receipt.Vivo != nil} {
		if present {
			count++
		}
	}
	if count != 1 || receipt.EditID != "" || receipt.VersionCode != "" {
		return bad()
	}
	version := strings.TrimPrefix(r.Version, "v")
	switch p.Driver {
	case "huawei-appgallery":
		v := receipt.Huawei
		if v == nil || v.Version != version || v.VersionCode < 1 || v.FileName != "release-"+r.ArtifactDigest[:24]+".apk" || !huaweiObjectPattern.MatchString(v.ObjectID) {
			return bad()
		}
		switch v.Stage {
		case "file_registration_intent":
		case "processing", "submission_check_intent", "submit_intent", "submitted":
			if !huaweiResourcePattern.MatchString(v.PackageID) {
				return bad()
			}
		default:
			return bad()
		}
		if (v.Stage == "submit_intent" || v.Stage == "submitted" || v.Stage == "submission_check_intent") && !huaweiResourcePattern.MatchString(v.VersionID) {
			return bad()
		}
		return v.Stage, v.VersionCode, huaweiExternalID(p, v), nil
	case "oppo-store":
		v := receipt.Oppo
		if v == nil || v.Version != version || v.VersionCode < 1 || (v.Stage != "submit_intent" && v.Stage != "submitted") {
			return bad()
		}
		return v.Stage, v.VersionCode, oppoExternalID(p, v), nil
	case "xiaomi-market":
		v := receipt.Xiaomi
		if v == nil || v.PackageName != p.AppID || v.Version != version || v.VersionCode < 1 || (v.SynchroType != 0 && v.SynchroType != 1) || (v.Stage != "push_intent" && v.Stage != "submitted") {
			return bad()
		}
		return v.Stage, v.VersionCode, xiaomiExternalID(v), nil
	case "vivo-store":
		v := receipt.Vivo
		if v == nil || v.Environment != p.Environment || v.PackageName != p.AppID || v.VersionName != version || v.VersionCode < 1 || v.APKSize != r.ArtifactSize || (v.Stage != "submit_intent" && v.Stage != "submitted") {
			return bad()
		}
		return v.Stage, v.VersionCode, vivoExternalID(v), nil
	}
	return bad()
}
