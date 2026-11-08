package updater

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

func (c *Client) Install(ctx context.Context, approvedDigest string) (Snapshot, error) {
	tx, r, err := c.begin(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	if !hash.MatchString(approvedDigest) || approvedDigest != r.Feed.Digest {
		return r.Snapshot, fmt.Errorf("explicit approval of the exact staged SHA256 is required")
	}
	if r.State == "installed" {
		return r.Snapshot, nil
	}
	if r.State != "staged" {
		return r.Snapshot, fmt.Errorf("installation must be staged and not already executing or uncertain")
	}
	if err = c.verify(ctx, r); err != nil {
		return r.Snapshot, err
	}
	installed, err := c.native.Installed(ctx, c.config)
	if err != nil {
		return r.Snapshot, err
	}
	if installed != "" && (!version.MatchString(installed) || compare(installed, r.Feed.Version) > 0 || (r.InstalledVersion != "" && compare(installed, r.InstalledVersion) < 0)) {
		return r.Snapshot, fmt.Errorf("installed package regressed or is newer than the approved update")
	}
	if installed == r.Feed.Version {
		r.State = "installed"
		r.InstalledVersion = installed
		r.Error = ""
		return c.save(ctx, tx, r)
	}
	attempt := make([]byte, 24)
	if _, err = rand.Read(attempt); err != nil {
		return r.Snapshot, err
	}
	r.AttemptID = hex.EncodeToString(attempt)
	r.State = "installing"
	r.Error = ""
	reserved, err := c.save(ctx, tx, r)
	if err != nil {
		return reserved, err
	}
	// Reserve durably before the native side effect. Unknown results are read
	// from installed receipts, never recovered by executing the installer again.
	installErr := c.native.Install(ctx, c.config, r.PackagePath)
	observed := ""
	if installErr == nil {
		observed, installErr = c.native.Installed(ctx, c.config)
		if installErr == nil && observed != r.Feed.Version {
			installErr = fmt.Errorf("installer exited without the exact installed receipt")
		}
	}
	finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, current, err := c.begin(finishCtx)
	if err != nil {
		return reserved, fmt.Errorf("installer dispatched but completion journal is uncertain: %w", err)
	}
	defer tx.Rollback()
	if current.AttemptID != r.AttemptID {
		return current.Snapshot, fmt.Errorf("installation attempt changed; operator investigation required")
	}
	if current.State == "installed" {
		return current.Snapshot, nil
	}
	if installErr != nil {
		current.State = "reconciliation_required"
		current.Error = "native installation outcome is uncertain; reconcile installed receipts before any further action"
	} else {
		current.State = "installed"
		current.InstalledVersion = observed
		current.Error = ""
	}
	s, err := c.save(finishCtx, tx, current)
	if err != nil {
		return s, err
	}
	if installErr != nil {
		return s, fmt.Errorf("%s: %w", current.Error, installErr)
	}
	return s, nil
}

func (c *Client) Reconcile(ctx context.Context, confirmedStoppedAttempt string) (Snapshot, error) {
	tx, r, err := c.begin(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	if r.State == "installed" {
		return r.Snapshot, nil
	}
	if !busy(r) {
		return r.Snapshot, fmt.Errorf("no uncertain installation to reconcile")
	}
	// A receipt may appear before an installer/service finishes. The caller must
	// first confirm termination of this exact attempt using native diagnostics.
	if r.AttemptID == "" || confirmedStoppedAttempt != r.AttemptID {
		return r.Snapshot, fmt.Errorf("confirm the exact installer attempt has stopped before receipt reconciliation")
	}
	installed, err := c.native.Installed(ctx, c.config)
	if err != nil || installed != r.Feed.Version {
		r.State = "reconciliation_required"
		r.Error = "the exact candidate version is not confirmed by the native installed receipt; do not retry the installer"
		s, saveErr := c.save(ctx, tx, r)
		if saveErr != nil {
			return s, saveErr
		}
		return s, fmt.Errorf("%s", r.Error)
	}
	r.State = "installed"
	r.InstalledVersion = installed
	r.Error = ""
	return c.save(ctx, tx, r)
}
