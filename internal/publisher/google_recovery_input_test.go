package publisher

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGoogleRecoveryAdministratorInputIsStrictAndBounded(t *testing.T) {
	in := GoogleReconciliationRequest{Request: Request{ProtocolVersion: "1", OperationKey: strings.Repeat("a", 64)}, JournalDigest: strings.Repeat("b", 64), ConfirmStoppedOperationKey: strings.Repeat("a", 64), Reason: "已停止原操作"}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeGoogleReconciliationInput(strings.NewReader(string(body)))
	if err != nil || decoded.Reason != "已停止原操作" || decoded.Request.OperationKey != strings.Repeat("a", 64) {
		t.Fatal(decoded, err)
	}
	for _, input := range []string{
		"", "null", "[]", string(body) + "{}", string(body) + strings.Repeat(" ", 65536),
		`{"unknown":true}`, `{"request":{"unknown":true}}`,
		`{"reason":"a","reason":"b"}`, `{"reason":"a","\u0072eason":"b"}`,
		`{"request":{"action":"publish","action":"status"}}`,
		"{\"reason\":\"\xff\"}",
	} {
		if _, err := DecodeGoogleReconciliationInput(strings.NewReader(input)); err == nil {
			t.Fatal("ambiguous or oversized recovery input accepted")
		}
	}
}
