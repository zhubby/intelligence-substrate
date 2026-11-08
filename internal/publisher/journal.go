package publisher

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
)

// The digest describes the exact stored snapshot, including its binding and
// update time. Raw receipt/result text is retained for the atomic comparison.
type operationJournal struct {
	Operation
	requestHash string
	receipt     string
	result      sql.NullString
	updatedAt   string
}

func (e *Engine) inspectJournal(ctx context.Context, key string) (operationJournal, error) {
	var j operationJournal
	err := e.db.QueryRowContext(ctx, "SELECT operation_key,request_hash,state,receipt,result,updated_at FROM operations WHERE operation_key=?", key).Scan(&j.OperationKey, &j.requestHash, &j.State, &j.receipt, &j.result, &j.updatedAt)
	if err != nil {
		return j, err
	}
	if err = json.Unmarshal([]byte(j.receipt), &j.Receipt); err != nil {
		return j, err
	}
	if j.result.Valid {
		var result Result
		if err = json.Unmarshal([]byte(j.result.String), &result); err != nil {
			return j, err
		}
		j.Result = &result
	}
	j.JournalDigest = operationJournalDigest(j.OperationKey, j.requestHash, j.State, j.receipt, j.result, j.updatedAt)
	j.StoreReconciliations, err = e.storeReconciliations(ctx, key)
	if err == nil {
		j.GoogleReservation, err = e.googleReservation(ctx, key)
	}
	if err == nil {
		j.GoogleQuery, err = e.latestGoogleQuery(ctx, key)
	}
	return j, err
}

func operationJournalDigest(key, requestHash, state, receipt string, result sql.NullString, updatedAt string) string {
	b, _ := json.Marshal([]any{key, requestHash, state, receipt, result.String, result.Valid, updatedAt})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
