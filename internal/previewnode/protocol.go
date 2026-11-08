// Package previewnode executes immutable preview backends behind a local Unix
// service. It never accepts arbitrary endpoints, host paths or commands.
package previewnode

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/intelligence-substrate/platform/internal/webpreview"
)

const MaxRequest = 16 << 10
const MaxResponse = 512 << 10

var previewID = regexp.MustCompile(`^preview_[a-f0-9]{24}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Binding struct {
	PreviewID      string             `json:"previewId"`
	Binding        string             `json:"binding"`
	ArtifactDigest string             `json:"artifactDigest"`
	ExpiresAt      time.Time          `json:"expiresAt"`
	Runtime        webpreview.Runtime `json:"runtime"`
}

func (b Binding) valid() bool {
	return previewID.MatchString(b.PreviewID) && hashPattern.MatchString(b.Binding) && hashPattern.MatchString(b.ArtifactDigest) && b.Runtime.Valid() && !b.ExpiresAt.IsZero()
}
func (b Binding) live() bool {
	return b.valid() && b.ExpiresAt.After(time.Now()) && time.Until(b.ExpiresAt) <= 4*time.Hour
}
func (b Binding) matches(other Binding) bool {
	return b.PreviewID == other.PreviewID && b.Binding == other.Binding && b.ArtifactDigest == other.ArtifactDigest && b.ExpiresAt.Equal(other.ExpiresAt) && b.Runtime == other.Runtime
}
func (b Binding) verify(binary []byte) bool {
	sum := sha256.Sum256(binary)
	return int64(len(binary)) == b.Runtime.BackendSize && hex.EncodeToString(sum[:]) == b.Runtime.BackendSHA256
}

type Request struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   []byte `json:"body,omitempty"`
}
type Response struct {
	Status int    `json:"status"`
	Body   []byte `json:"body"`
}

func (r Request) Valid(mode string) bool {
	if len(r.Body) > MaxRequest {
		return false
	}
	if r.Method == "GET" && len(r.Body) == 0 {
		return r.Path == "/api/health" || mode == "feedback" && r.Path == "/api/feedback" || mode == "booking" && r.Path == "/api/slots"
	}
	if r.Method != "POST" || !json.Valid(r.Body) || !bytes.HasPrefix(bytes.TrimSpace(r.Body), []byte("{")) {
		return false
	}
	return mode == "feedback" && r.Path == "/api/feedback" || mode == "booking" && (r.Path == "/api/bookings" || r.Path == "/api/bookings/cancel")
}
func (r Response) Valid() bool {
	return r.Status >= 200 && r.Status <= 599 && len(r.Body) <= MaxResponse && json.Valid(r.Body)
}
func conflict() error {
	return fmt.Errorf("预览节点状态不匹配或结果不明；请核对后创建新预览")
}
