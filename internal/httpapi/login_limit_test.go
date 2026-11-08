package httpapi

import (
	"net/http"
	"testing"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestSuccessfulLoginsDoNotExhaustFailureLimit(t *testing.T) {
	h := setup(t)
	for i := 0; i < 12; i++ {
		w := req(h, "POST", "/api/v1/auth/login", p.M{"email": "owner@example.com", "password": "long-test-password-123"}, nil, "")
		if w.Code != http.StatusOK {
			t.Fatalf("successful login %d exhausted the failure limit: %d", i+1, w.Code)
		}
	}
}

func TestFailedLoginsRemainLimited(t *testing.T) {
	h := setup(t)
	for i := 0; i < 11; i++ {
		w := req(h, "POST", "/api/v1/auth/login", p.M{"email": "owner@example.com", "password": "invalid-password"}, nil, "")
		want := http.StatusUnauthorized
		if i == 10 {
			want = http.StatusTooManyRequests
		}
		if w.Code != want {
			t.Fatalf("failed login %d: got %d, want %d", i+1, w.Code, want)
		}
	}
}

func TestLoginLimiterReclaimsExpiredHosts(t *testing.T) {
	h := setup(t)
	for i := range 4096 {
		h.attempts[time.Unix(int64(i), 0).String()] = []time.Time{time.Now().Add(-6 * time.Minute)}
	}
	login(t, h)
	if len(h.attempts) >= 4096 {
		t.Fatal("expired remote-address buckets were retained")
	}
}
