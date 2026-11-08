package platform

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

type M = map[string]any
type Actor struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Admin     bool     `json:"admin"`
	ProjectID string   `json:"projectId,omitempty"`
	Roles     []string `json:"roles,omitempty"`
	WorkerID  string   `json:"workerId,omitempty"`
	// CredentialHash binds HTTP-authenticated actors to a current credential.
	// It never crosses API, message, audit or persisted Actor JSON boundaries.
	CredentialHash string `json:"-"`
}
type Resource struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	ProjectID string `json:"projectId,omitempty"`
	GoalID    string `json:"goalId,omitempty"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Revision  int    `json:"revision"`
	Data      M      `json:"data"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}
type Fault struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
}

func (f *Fault) Error() string                    { return f.Message }
func fail(code, message string, status int) error { return &Fault{code, message, status} }
func invalid(message string) error                { return fail("invalid", message, 422) }
func denied() error                               { return fail("forbidden", "当前身份无权执行此操作", 403) }
func conflict(message string) error               { return fail("conflict", message, 409) }
func missing() error                              { return fail("not_found", "资源不存在或不可访问", 404) }
func NewID(prefix string) string {
	b := make([]byte, 12)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return prefix + "_" + hex.EncodeToString(b)
}
func Secret() string { return NewID("is") + NewID("") }
func Hash(v any) string {
	b, _ := json.Marshal(v)
	x := sha256.Sum256(b)
	return hex.EncodeToString(x[:])
}
func S(m M, k string) string { v, _ := m[k].(string); return strings.TrimSpace(v) }
func N(m M, k string) float64 {
	switch v := m[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		x, _ := v.Float64()
		return x
	}
	return 0
}
func B(m M, k string) bool { v, _ := m[k].(bool); return v }
func Obj(v any) M {
	switch x := v.(type) {
	case map[string]any:
		return x
	}
	return M{}
}
func Items(v any) []any {
	if x, ok := v.([]any); ok {
		return x
	}
	if x, ok := v.([]string); ok {
		a := []any{}
		for _, s := range x {
			a = append(a, s)
		}
		return a
	}
	return []any{}
}
func Strings(v any) []string {
	o := []string{}
	for _, x := range Items(v) {
		if s, ok := x.(string); ok {
			o = append(o, s)
		}
	}
	return o
}
func Has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func resource(kind, title, project, goal string, data M) Resource {
	ts := now()
	return Resource{ID: NewID(kind), Kind: kind, Title: title, ProjectID: project, GoalID: goal, Status: "active", Revision: 1, Data: data, CreatedAt: ts, UpdatedAt: ts}
}
func copyM(in M) M            { b, _ := json.Marshal(in); out := M{}; _ = json.Unmarshal(b, &out); return out }
func cents(v float64) int64   { return int64(math.Round(v * 100)) }
func revision(r Resource) int { return int(N(r.Data, "requirementRevision")) }

var IDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,95}$`)
var CommitPattern = regexp.MustCompile(`^[a-f0-9]{40,64}$`)
var Targets = []string{"web", "ios-appstore", "android-google-play", "android-huawei", "android-xiaomi", "android-oppo", "android-vivo", "windows", "macos"}
var Roles = []string{"owner", "product", "architect", "developer", "quality", "security", "release", "viewer"}
var AgentRoles = []string{"analysis", "design", "architect", "developer", "quality", "security", "release"}

func validateText(s, label string, max int) error {
	if strings.TrimSpace(s) == "" || len(s) > max {
		return invalid(fmt.Sprintf("%s不能为空且长度不能超过 %d", label, max))
	}
	return nil
}
func noSecrets(v any) error {
	b, _ := json.Marshal(v)
	l := strings.ToLower(string(b))
	for _, k := range []string{"api_key", "apikey", "access_token", "private_key", "client_secret", "password", "authorization", "secretkey"} {
		if strings.Contains(l, `"`+k+`"`) {
			return invalid("凭据必须留在受控凭据服务，不能写入业务参数")
		}
	}
	return nil
}
