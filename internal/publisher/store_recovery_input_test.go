package publisher

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStoreRecoveryInputStrictSingleObjectAndBounds(t *testing.T) {
	_, request := publisherFixture(t)
	in := StoreReconciliationRequest{Request: request, JournalDigest: strings.Repeat("a", 64), ConfirmStoppedOperationKey: request.OperationKey, Operator: "admin", Reason: "stopped", Proof: "api"}
	body, _ := json.Marshal(in)
	decoded, err := DecodeStoreReconciliationInput(strings.NewReader(string(body)))
	if err != nil || decoded != in {
		t.Fatal(decoded, err)
	}
	for _, tc := range []struct{ name, body string }{
		{"unknown", `{"unknown":true}`},
		{"null", `null`},
		{"array", `[]`},
		{"trailing", string(body) + ` {}`},
		{"duplicate", `{"proof":"portal","proof":"api"}`},
		{"case duplicate", `{"proof":"portal","Proof":"api"}`},
		{"nested duplicate", `{"request":{"binding":"wrong","binding":"right"}}`},
		{"oversize", string(body) + strings.Repeat(" ", 65536)},
		{"non UTF-8", `{"operator":"` + string([]byte{255}) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeStoreReconciliationInput(strings.NewReader(tc.body)); err == nil {
				t.Fatal("invalid administrator JSON accepted")
			}
		})
	}
}
