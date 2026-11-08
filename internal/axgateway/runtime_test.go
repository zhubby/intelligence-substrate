package axgateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestGatewayRuntimeRejectsUnsafeConfigWithoutOpeningAudit(t *testing.T) {
	c := configFixture(t)
	c.ControlUpstream.PrivateNetworkNamespace = false
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := Run(ctx, c, make(chan struct{}), func(string) {}); e == nil {
		t.Fatal("unsafe native upstream started")
	}
	if matches, _ := filepath.Glob(c.AuditFile); len(matches) != 0 {
		t.Fatal("unsafe config opened audit")
	}
}
