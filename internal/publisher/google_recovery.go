package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GoogleReconciliationRequest is a local administrator operation. The original
// publisher must be stopped and other publishers of this app kept quiescent:
// Google creates read snapshots through edits, which can invalidate other edits.
// It is intentionally absent from the Worker publication action schema.
type GoogleReconciliationRequest struct {
	Request                    Request `json:"request"`
	JournalDigest              string  `json:"journalDigest"`
	ConfirmStoppedOperationKey string  `json:"confirmStoppedOperationKey"`
	Reason                     string  `json:"reason"`
}

type GoogleReconciliation struct {
	JournalDigest                string              `json:"journalDigest"`
	InputDigest                  string              `json:"inputDigest"`
	ResultState                  string              `json:"resultState"`
	Reason                       string              `json:"reason"`
	ConfirmedStoppedOperationKey string              `json:"confirmedStoppedOperationKey"`
	ObservedAt                   string              `json:"observedAt"`
	Proof                        GoogleRecoveryProof `json:"proof"`
}

// Response digests cover the complete JSON values read from the two Google API
// responses (without surrounding whitespace), not an administrator's assertion.
type GoogleRecoveryProof struct {
	Source         string `json:"source"`
	QueryEditID    string `json:"queryEditId"`
	QueryID        string `json:"queryId,omitempty"`
	AppID          string `json:"appId"`
	Track          string `json:"track"`
	VersionCode    string `json:"versionCode"`
	ArtifactDigest string `json:"artifactDigest"`
	ReleaseName    string `json:"releaseName"`
	ReleaseStatus  string `json:"releaseStatus"`
	BundlesDigest  string `json:"bundlesDigest"`
	TrackDigest    string `json:"trackDigest"`
}

func DecodeGoogleReconciliationInput(reader io.Reader) (GoogleReconciliationRequest, error) {
	var in GoogleReconciliationRequest
	body, err := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
		return in, fmt.Errorf("administrator recovery input exceeds 64 KiB or is incomplete")
	}
	// Use the same duplicate-aware, UTF-8 and unknown-field checks as the other
	// administrator store recovery boundary.
	if err = decodeStoreRecoveryJSON(body, &in); err != nil {
		return in, err
	}
	return in, nil
}

func (e *Engine) ReconcileGoogle(ctx context.Context, in GoogleReconciliationRequest) (Operation, error) {
	if err := validateRequest(in.Request); err != nil {
		return Operation{}, err
	}
	if in.Request.Action != "" && in.Request.Action != "publish" {
		return Operation{}, fmt.Errorf("Google recovery requires the original publish request")
	}
	if !hashPattern.MatchString(in.JournalDigest) || in.ConfirmStoppedOperationKey != in.Request.OperationKey || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 4096 {
		return Operation{}, fmt.Errorf("exact journal digest, stopped operation confirmation and bounded reason required")
	}
	p, err := e.profile(in.Request)
	if err != nil {
		return Operation{}, err
	}
	if p.Driver != "google-play" {
		return Operation{}, fmt.Errorf("reconciliation requires the fixed Google Play profile")
	}
	j, err := e.inspectJournal(ctx, in.Request.OperationKey)
	if err != nil {
		return Operation{}, err
	}
	if j.requestHash != requestHash(in.Request, p) {
		return Operation{}, fmt.Errorf("recovery request differs from approved operation or administrator destination")
	}
	code, err := strconv.ParseInt(j.Receipt.VersionCode, 10, 32)
	if err != nil || code <= 0 || strconv.FormatInt(code, 10) != j.Receipt.VersionCode || j.Receipt.EditID == "" {
		return Operation{}, fmt.Errorf("recovery requires the persisted edit and canonical positive version code; retain the blocked operation")
	}
	externalID := p.AppID + ":" + j.Receipt.VersionCode
	if in.Request.ExternalID != "" && in.Request.ExternalID != externalID {
		return Operation{}, fmt.Errorf("recovery external ID differs from the persisted upload")
	}
	body, _ := json.Marshal([]string{j.requestHash, in.JournalDigest, in.ConfirmStoppedOperationKey, in.Reason})
	inputHash := sha256.Sum256(body)
	inputDigest := hex.EncodeToString(inputHash[:])
	if n := len(j.Receipt.GoogleReconciliations); n > 0 {
		last := j.Receipt.GoogleReconciliations[n-1]
		if last.JournalDigest == in.JournalDigest {
			if last.InputDigest == inputDigest && j.State == "complete" && j.Result != nil && j.Result.State == last.ResultState && j.Result.ExternalID == externalID {
				return j.Operation, nil
			}
			return Operation{}, fmt.Errorf("recovery input or subsequent operation state changed; inspect the current journal")
		}
	}
	if j.State != "pending" || j.JournalDigest != in.JournalDigest {
		return Operation{}, fmt.Errorf("only the exact pending journal snapshot can be reconciled")
	}
	_, cleanup, err := stageArtifact(in.Request)
	if err != nil {
		return Operation{}, err
	}
	defer cleanup()
	if err = e.reserveStoppedGoogle(ctx, p, in.Request, j); err != nil {
		return Operation{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	proof, err := e.googleRecoveryProof(ctx, p, in.Request, j.Receipt, code)
	if err != nil {
		return Operation{}, err
	}
	result := Result{ExternalID: externalID, State: "awaiting_review", UpstreamState: proof.ReleaseStatus, Reason: "准确 AAB 摘要与固定 Track 已核实；商店审核和公开可用性仍须独立确认"}
	if proof.ReleaseStatus == "halted" {
		result.State = "halted"
	}
	observedAt := time.Now().UTC().Format(time.RFC3339Nano)
	j.Receipt.GoogleReconciliations = append(j.Receipt.GoogleReconciliations, GoogleReconciliation{JournalDigest: in.JournalDigest, InputDigest: inputDigest, ResultState: result.State, Reason: in.Reason, ConfirmedStoppedOperationKey: in.ConfirmStoppedOperationKey, ObservedAt: observedAt, Proof: proof})
	receiptBody, _ := json.Marshal(j.Receipt)
	resultBody, _ := json.Marshal(result)
	var oldResult any
	if j.result.Valid {
		oldResult = j.result.String
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	updated, err := tx.ExecContext(ctx, `UPDATE operations SET state='complete',receipt=?,result=?,updated_at=? WHERE operation_key=? AND state='pending' AND request_hash=? AND receipt=? AND result IS ? AND updated_at=?
	 AND EXISTS(SELECT 1 FROM google_destinations WHERE app_id=? AND operation_key=operations.operation_key)
	 AND EXISTS(SELECT 1 FROM google_publications WHERE operation_key=operations.operation_key AND app_id=? AND request_hash=operations.request_hash AND version_code=?)
	 AND NOT EXISTS(SELECT 1 FROM google_query_locks WHERE app_id=?)`, string(receiptBody), string(resultBody), observedAt, j.OperationKey, j.requestHash, j.receipt, oldResult, j.updatedAt, p.AppID, p.AppID, j.Receipt.VersionCode, p.AppID)
	if err != nil {
		return Operation{}, err
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return Operation{}, err
	}
	if count != 1 {
		return Operation{}, fmt.Errorf("publisher journal changed during reconciliation; inspect before any further action")
	}
	if err = releaseGoogleDestination(ctx, tx, p.AppID, j.OperationKey); err != nil {
		return Operation{}, err
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, err
	}
	return e.Inspect(ctx, j.OperationKey)
}

func (e *Engine) googleRecoveryProof(ctx context.Context, p Profile, r Request, receipt Receipt, code int64) (proof GoogleRecoveryProof, resultErr error) {
	session, err := e.openGoogleQuery(ctx, p, r, "recovery", receipt.EditID)
	if err != nil {
		return proof, err
	}
	defer func() {
		if err := session.close(ctx); err != nil {
			proof = GoogleRecoveryProof{}
			if resultErr == nil {
				resultErr = err
			} else {
				resultErr = fmt.Errorf("%w; query cleanup unconfirmed, retain its durable window", resultErr)
			}
		}
	}()
	var bundlesJSON, trackJSON json.RawMessage
	if err = e.googleCall(ctx, session.client, http.MethodGet, editPath(p, session.editID)+"/bundles", nil, &bundlesJSON); err != nil {
		return proof, err
	}
	var bundles struct {
		Bundles []struct {
			VersionCode int64  `json:"versionCode"`
			SHA256      string `json:"sha256"`
		} `json:"bundles"`
	}
	if json.Unmarshal(bundlesJSON, &bundles) != nil {
		return proof, fmt.Errorf("invalid Google Play bundle evidence")
	}
	matches := 0
	for _, bundle := range bundles.Bundles {
		if bundle.VersionCode == code {
			matches++
			if !strings.EqualFold(bundle.SHA256, r.ArtifactDigest) {
				return proof, fmt.Errorf("Google Play bundle SHA256 differs from approved artifact")
			}
		}
	}
	if matches != 1 {
		return proof, fmt.Errorf("Google Play must positively identify exactly one matching bundle")
	}
	if err = e.googleCall(ctx, session.client, http.MethodGet, editPath(p, session.editID)+"/tracks/"+url.PathEscape(p.Track), nil, &trackJSON); err != nil {
		return proof, err
	}
	type release struct {
		Name         string   `json:"name"`
		VersionCodes []string `json:"versionCodes"`
		Status       string   `json:"status"`
	}
	var track struct {
		Track    string    `json:"track"`
		Releases []release `json:"releases"`
	}
	if json.Unmarshal(trackJSON, &track) != nil || track.Track != p.Track {
		return proof, fmt.Errorf("Google Play returned invalid evidence or a different track")
	}
	matches = 0
	var selected release
	for _, candidate := range track.Releases {
		refers := candidate.Name == r.Version
		for _, version := range candidate.VersionCodes {
			refers = refers || version == receipt.VersionCode
		}
		if refers {
			matches++
			selected = candidate
		}
	}
	if matches != 1 || selected.Name != r.Version || len(selected.VersionCodes) != 1 || selected.VersionCodes[0] != receipt.VersionCode || (selected.Status != "completed" && selected.Status != "halted") {
		return proof, fmt.Errorf("Google Play track does not uniquely prove the exact approved release and accepted state")
	}
	bundlesHash, trackHash := sha256.Sum256(bundlesJSON), sha256.Sum256(trackJSON)
	return GoogleRecoveryProof{Source: "google-play-edits-api", QueryEditID: session.editID, QueryID: session.journal.QueryID, AppID: p.AppID, Track: p.Track, VersionCode: receipt.VersionCode, ArtifactDigest: r.ArtifactDigest, ReleaseName: selected.Name, ReleaseStatus: selected.Status, BundlesDigest: hex.EncodeToString(bundlesHash[:]), TrackDigest: hex.EncodeToString(trackHash[:])}, nil
}
