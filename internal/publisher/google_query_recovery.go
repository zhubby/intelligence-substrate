package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// This administrator operation only cleans a stopped read query. It cannot
// complete, resubmit or unlock the associated publication operation.
type GoogleQueryRecoveryRequest struct {
	Request               Request `json:"request"`
	QueryID               string  `json:"queryId"`
	QueryDigest           string  `json:"queryDigest"`
	ConfirmStoppedQueryID string  `json:"confirmStoppedQueryId"`
	Reason                string  `json:"reason"`
}

type GoogleQueryReconciliation struct {
	QueryDigest           string `json:"queryDigest"`
	InputDigest           string `json:"inputDigest"`
	ConfirmedStoppedQuery string `json:"confirmedStoppedQueryId"`
	Reason                string `json:"reason"`
	ObservedAt            string `json:"observedAt"`
}

func DecodeGoogleQueryRecoveryInput(reader io.Reader) (GoogleQueryRecoveryRequest, error) {
	var in GoogleQueryRecoveryRequest
	body, err := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
		return in, fmt.Errorf("administrator query cleanup input exceeds 64 KiB or is incomplete")
	}
	if err = decodeStoreRecoveryJSON(body, &in); err != nil {
		return in, err
	}
	return in, nil
}

func (e *Engine) ReconcileGoogleQuery(ctx context.Context, in GoogleQueryRecoveryRequest) (GoogleQuery, error) {
	if err := validateRequest(in.Request); err != nil {
		return GoogleQuery{}, err
	}
	if in.Request.Action != "" && in.Request.Action != "publish" && in.Request.Action != "status" {
		return GoogleQuery{}, fmt.Errorf("query cleanup requires the original approved publish or status request")
	}
	if !hashPattern.MatchString(in.QueryID) || !hashPattern.MatchString(in.QueryDigest) || in.ConfirmStoppedQueryID != in.QueryID || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 4096 {
		return GoogleQuery{}, fmt.Errorf("exact query ID, snapshot, stopped-query confirmation and bounded reason required")
	}
	p, err := e.profile(in.Request)
	if err != nil {
		return GoogleQuery{}, err
	}
	if p.Driver != "google-play" {
		return GoogleQuery{}, fmt.Errorf("query cleanup requires the fixed Google Play profile")
	}
	j, err := e.inspectGoogleQuery(ctx, in.QueryID)
	if err != nil {
		return GoogleQuery{}, err
	}
	if j.OperationKey != in.Request.OperationKey || j.requestHash != requestHash(in.Request, p) || j.AppID != p.AppID || j.Track != p.Track {
		return GoogleQuery{}, fmt.Errorf("query cleanup differs from the approved operation or destination")
	}
	op, err := e.inspectJournal(ctx, in.Request.OperationKey)
	if err != nil {
		return GoogleQuery{}, err
	}
	if op.requestHash != j.requestHash || (in.Request.ExternalID != "" && in.Request.ExternalID != p.AppID+":"+op.Receipt.VersionCode) {
		return GoogleQuery{}, fmt.Errorf("query cleanup does not match the persisted publication")
	}
	body, _ := json.Marshal([]string{j.requestHash, in.QueryID, in.QueryDigest, in.ConfirmStoppedQueryID, in.Reason})
	digest := sha256.Sum256(body)
	inputDigest := hex.EncodeToString(digest[:])
	if j.State == "cleaned" {
		if j.Reconciliation != nil && j.Reconciliation.QueryDigest == in.QueryDigest && j.Reconciliation.InputDigest == inputDigest {
			return j.GoogleQuery, nil
		}
		return GoogleQuery{}, fmt.Errorf("query already closed under a different cleanup input; inspect its journal")
	}
	if j.QueryDigest != in.QueryDigest || !j.WindowHeld {
		return GoogleQuery{}, fmt.Errorf("only the exact owned query snapshot can be cleaned")
	}
	if (j.State != "reserved" && j.State != "created") || (j.State == "reserved" && j.EditID != "") || (j.State == "created" && j.EditID == "") {
		return GoogleQuery{}, fmt.Errorf("query edit identity is uncertain; retain its durable window without creating another edit")
	}
	if j.EditID != "" && j.EditID == op.Receipt.EditID {
		return GoogleQuery{}, fmt.Errorf("query cleanup cannot target the original publication edit; retain its durable window")
	}
	record := &GoogleQueryReconciliation{QueryDigest: in.QueryDigest, InputDigest: inputDigest, ConfirmedStoppedQuery: in.ConfirmStoppedQueryID, Reason: in.Reason}
	source := "not-started"
	if j.State == "created" {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		client, err := e.googleClient(ctx, p)
		if err != nil {
			return GoogleQuery{}, err
		}
		if source, err = deleteGoogleQueryEdit(ctx, e, client, p, j.EditID); err != nil {
			return GoogleQuery{}, err
		}
	}
	updated, err := e.finishGoogleQuery(ctx, j, j.EditID, source, record)
	return updated.GoogleQuery, err
}
