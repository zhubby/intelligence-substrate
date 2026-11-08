package updater

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func stageFixture(t *testing.T) (*updateFixture, *Client, Snapshot) {
	t.Helper()
	f := fixture(t)
	c := f.open()
	if _, err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, err := c.Stage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return f, c, s
}

func TestInstallationRequiresExactConsentAndFreshTrust(t *testing.T) {
	for _, name := range []string{"no-approval", "wrong-approval", "expired", "native-trust-revoked", "already-newer"} {
		t.Run(name, func(t *testing.T) {
			f, c, s := stageFixture(t)
			var installs atomic.Int32
			f.native.install = func() error { installs.Add(1); return nil }
			digest := s.Feed.Digest
			switch name {
			case "no-approval":
				digest = ""
			case "wrong-approval":
				digest = "wrong"
			case "expired":
				c.now = func() time.Time { return f.now.Add(48 * time.Hour) }
			case "native-trust-revoked":
				f.native.verifyErr = errors.New("signer no longer trusted")
			case "already-newer":
				f.native.version = "3.0.0"
			}
			if _, err := c.Install(context.Background(), digest); err == nil {
				t.Fatal("unsafe installation accepted")
			}
			if installs.Load() != 0 {
				t.Fatal("installer invoked before consent/trust/version checks")
			}
		})
	}
}

func TestInstallationReservedAcrossConnectionsAndObservedOnce(t *testing.T) {
	f, first, s := stageFixture(t)
	second := f.open()
	entered, release := make(chan struct{}), make(chan struct{})
	var installs atomic.Int32
	f.native.install = func() error {
		installs.Add(1)
		close(entered)
		<-release
		f.native.mu.Lock()
		f.native.version = "2.0.0"
		f.native.mu.Unlock()
		return nil
	}
	done := make(chan error, 1)
	go func() { _, err := first.Install(context.Background(), s.Feed.Digest); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("installer not entered")
	}
	status, err := second.Status(context.Background())
	if err != nil || status.State != "installing" || status.AttemptID == "" {
		t.Fatalf("reservation missing: %+v %v", status, err)
	}
	if _, err = second.Install(context.Background(), s.Feed.Digest); err == nil {
		t.Fatal("concurrent invocation accepted")
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	status, err = second.Status(context.Background())
	if err != nil || status.State != "installed" || status.InstalledVersion != "2.0.0" || installs.Load() != 1 {
		t.Fatalf("incorrect durable completion: %+v %v calls=%d", status, err, installs.Load())
	}
	if _, err = second.Install(context.Background(), s.Feed.Digest); err != nil || installs.Load() != 1 {
		t.Fatal("completed installation replayed or lost receipt")
	}
}

func TestUnknownInstallSurvivesRestartAndOnlyReceiptReconciliationResolves(t *testing.T) {
	f, c, s := stageFixture(t)
	var installs atomic.Int32
	f.native.install = func() error { installs.Add(1); return errors.New("installer connection lost") }
	status, err := c.Install(context.Background(), s.Feed.Digest)
	if err == nil || status.State != "reconciliation_required" {
		t.Fatalf("unknown outcome became success: %+v %v", status, err)
	}
	_ = c.Close()
	c = f.open()
	if _, err = c.Install(context.Background(), s.Feed.Digest); err == nil {
		t.Fatal("uncertain installation retried")
	}
	if _, err = c.Check(context.Background()); err == nil {
		t.Fatal("new check erased unresolved native operation")
	}
	if _, err = c.Reconcile(context.Background(), status.AttemptID); err == nil {
		t.Fatal("old installed version confirmed new package")
	}
	f.native.mu.Lock()
	f.native.version = "2.0.0"
	f.native.mu.Unlock()
	status, err = c.Reconcile(context.Background(), status.AttemptID)
	if err != nil || status.State != "installed" || installs.Load() != 1 {
		t.Fatalf("read-only receipt reconciliation failed: %+v %v calls=%d", status, err, installs.Load())
	}
}

func TestInstallerExitZeroRequiresActualInstalledReceipt(t *testing.T) {
	f, c, s := stageFixture(t)
	f.native.install = func() error { return nil }
	status, err := c.Install(context.Background(), s.Feed.Digest)
	if err == nil || status.State != "reconciliation_required" || status.InstalledVersion == "2.0.0" {
		t.Fatalf("exit zero invented installed version: %+v %v", status, err)
	}
}

func TestReconcileNeedsExplicitConfirmationThatUnknownInstallerStopped(t *testing.T) {
	f, c, s := stageFixture(t)
	f.native.install = func() error {
		f.native.mu.Lock()
		f.native.version = "2.0.0"
		f.native.mu.Unlock()
		return errors.New("native connection lost after receipt appeared")
	}
	if _, err := c.Install(context.Background(), s.Feed.Digest); err == nil {
		t.Fatal("unknown install succeeded")
	}
	if _, err := c.Reconcile(context.Background(), ""); err == nil {
		t.Fatal("receipt alone resolved an installer that may still be active")
	}
}
