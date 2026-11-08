package webpreview

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Runtime contains no command, URL, host path or unrestricted environment.
type Runtime struct {
	SchemaVersion int    `json:"schemaVersion"`
	Profile       string `json:"profile"`
	Mode          string `json:"mode"`
	Goarch        string `json:"goarch"`
	BackendSHA256 string `json:"backendSHA256"`
	BackendSize   int64  `json:"backendSize"`
}

func (r Runtime) Valid() bool {
	_, err := hex.DecodeString(r.BackendSHA256)
	return r.SchemaVersion == 1 && r.Profile == "flutter-go-v1" && (r.Mode == "feedback" || r.Mode == "booking") && (r.Goarch == "arm64" || r.Goarch == "amd64") && len(r.BackendSHA256) == 64 && strings.ToLower(r.BackendSHA256) == r.BackendSHA256 && err == nil && r.BackendSize > 0 && r.BackendSize <= 32<<20
}
func (b *Bundle) readRuntime() error {
	raw, exists := b.files["substrate-preview/manifest.json"]
	for name := range b.files {
		if strings.HasPrefix(name, "substrate-preview/") && (!exists || (name != "substrate-preview/manifest.json" && name != "substrate-preview/backend")) {
			return fmt.Errorf("预览包保留目录存在未知文件或缺少清单")
		}
	}
	if !exists {
		return nil
	}
	if len(raw) > 4096 {
		return fmt.Errorf("预览运行清单超限")
	}
	keys := json.NewDecoder(bytes.NewReader(raw))
	first, err := keys.Token()
	if err != nil || first != json.Delim('{') {
		return fmt.Errorf("预览运行清单需要唯一字段的对象")
	}
	seen := map[string]bool{}
	for keys.More() {
		token, err := keys.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return fmt.Errorf("预览运行清单包含重复字段")
		}
		seen[name] = true
		var value json.RawMessage
		if keys.Decode(&value) != nil {
			return fmt.Errorf("预览运行清单值无效")
		}
	}
	var r Runtime
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&r) != nil || decoder.Decode(new(any)) != io.EOF || !r.Valid() {
		return fmt.Errorf("预览运行清单无效")
	}
	binary := b.files["substrate-preview/backend"]
	sum := sha256.Sum256(binary)
	if int64(len(binary)) != r.BackendSize || hex.EncodeToString(sum[:]) != r.BackendSHA256 {
		return fmt.Errorf("预览后端完整性验证失败")
	}
	b.runtime = &r
	return nil
}
func (b *Bundle) Runtime() *Runtime {
	if b.runtime == nil {
		return nil
	}
	r := *b.runtime
	return &r
}

// Backend returns immutable, verified executable bytes for the trusted node.
func (b *Bundle) Backend() []byte { return b.files["substrate-preview/backend"] }
