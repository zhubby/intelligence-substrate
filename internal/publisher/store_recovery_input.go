package publisher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

// DecodeStoreReconciliationInput is the bounded local administrator boundary.
// Recovery is deliberately absent from the ordinary Request action schema.
func DecodeStoreReconciliationInput(reader io.Reader) (StoreReconciliationRequest, error) {
	var in StoreReconciliationRequest
	body, err := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
		return in, fmt.Errorf("administrator recovery input exceeds 64 KiB or is incomplete")
	}
	if err = decodeStoreRecoveryJSON(body, &in); err != nil {
		return in, err
	}
	return in, nil
}

func decodeStoreRecoveryJSON(body []byte, out any) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' || !utf8.Valid(body) {
		return fmt.Errorf("recovery input must be one UTF-8 JSON object")
	}
	// Reuse the existing bounded, Unicode-aware duplicate-field scanner. Go's
	// normal decoder would otherwise silently accept the last repeated field.
	if err := checkXiaomiJSONValue(json.NewDecoder(bytes.NewReader(body)), 0); err != nil {
		return fmt.Errorf("recovery JSON contains invalid, repeated or excessively nested fields")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fmt.Errorf("invalid administrator recovery JSON: %w", err)
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("recovery input must contain exactly one JSON object")
	}
	return nil
}
