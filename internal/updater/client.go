package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/intelligence-substrate/platform/internal/distribution"
)

func (c *Client) get(ctx context.Context, address string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	client := *c.http
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update HTTPS request failed: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("update service returned HTTP %d; redirects are not followed", response.StatusCode)
	}
	return response, nil
}

func (c *Client) validate(envelope distribution.SignedFeed, minimum int64) (distribution.Feed, error) {
	f, err := distribution.VerifyFeed(envelope, c.key, c.config.App, c.config.Target, c.config.Channel, minimum, c.now())
	if err != nil {
		return f, err
	}
	issued, _ := time.Parse(time.RFC3339, f.IssuedAt)
	expires, _ := time.Parse(time.RFC3339, f.ExpiresAt)
	if !expires.After(issued) || !version.MatchString(f.Version) || !slug.MatchString(f.ID) || !filename.MatchString(f.Filename) || f.Size > 512<<20 || !commit.MatchString(f.Commit) || !hash.MatchString(f.Binding) {
		return f, fmt.Errorf("invalid signed package provenance or lifetime")
	}
	for _, id := range []string{f.ProjectID, f.GoalID, f.ReleaseID, f.ArtifactID} {
		if !identifier.MatchString(id) {
			return f, fmt.Errorf("incomplete signed release provenance")
		}
	}
	if (c.config.Target == "macos" && (f.Signer != c.config.ExpectedSigner || filepath.Ext(f.Filename) != ".pkg")) || (c.config.Target == "windows" && (!strings.EqualFold(f.Signer, c.config.ExpectedSigner) || filepath.Ext(f.Filename) != ".msix")) {
		return f, fmt.Errorf("native signer or installer format does not match the profile")
	}
	expected := c.config.Origin + "/downloads/" + f.App + "/" + f.Target + "/" + f.ID + "/" + f.Filename
	if f.URL != expected {
		return f, fmt.Errorf("download must use the exact approved immutable same-origin URL")
	}
	return f, nil
}

func releaseIdentity(f distribution.Feed) string {
	r := f.Release
	r.Sequence = 0
	r.RequestHash = ""
	b, _ := json.Marshal(r)
	return sum(b)
}

func (c *Client) Check(ctx context.Context) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	tx, r, err := c.begin(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	if busy(r) {
		return r.Snapshot, fmt.Errorf("installation outcome requires receipt reconciliation before another check")
	}
	response, err := c.get(ctx, c.config.Origin+"/updates/"+c.config.App+"/"+c.config.Target+"/"+c.config.Channel+".json")
	if err != nil {
		return r.Snapshot, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64001))
	response.Body.Close()
	if err != nil || len(body) > 64000 {
		return r.Snapshot, fmt.Errorf("invalid or oversized signed index")
	}
	var envelope distribution.SignedFeed
	if err = json.Unmarshal(body, &envelope); err != nil {
		return r.Snapshot, fmt.Errorf("invalid signed index JSON")
	}
	f, err := c.validate(envelope, r.MinimumSequence)
	if err != nil {
		return r.Snapshot, err
	}
	payloadHash := sum([]byte(envelope.Payload))
	if f.Sequence == r.MinimumSequence && payloadHash != r.PayloadHash {
		return r.Snapshot, fmt.Errorf("same sequence has conflicting signed content")
	}
	if r.MinimumSequence > 0 && (compare(f.Version, r.Feed.Version) < 0 || (f.Version == r.Feed.Version && releaseIdentity(f) != releaseIdentity(r.Feed))) {
		return r.Snapshot, fmt.Errorf("signed update would roll back or replace an immutable package version")
	}
	installed, err := c.native.Installed(ctx, c.config)
	if err != nil {
		return r.Snapshot, err
	}
	if installed != "" && (!version.MatchString(installed) || (r.InstalledVersion != "" && compare(installed, r.InstalledVersion) < 0)) {
		return r.Snapshot, fmt.Errorf("native installed version is invalid or regressed; operator investigation required")
	}
	keepStage := r.State == "staged" && releaseIdentity(f) == releaseIdentity(r.Feed)
	r.Feed = f
	r.Envelope = envelope
	r.PayloadHash = payloadHash
	r.MinimumSequence = f.Sequence
	r.InstalledVersion = installed
	r.Error = ""
	if installed != "" && compare(f.Version, installed) <= 0 {
		r.State = "current"
		r.PackagePath = ""
	} else if !keepStage {
		r.State = "available"
		r.PackagePath = ""
		r.AttemptID = ""
	}
	return c.save(ctx, tx, r)
}

func (c *Client) fresh(r record) error {
	f, err := c.validate(r.Envelope, r.MinimumSequence)
	if err != nil {
		return err
	}
	if f.Sequence != r.MinimumSequence || releaseIdentity(f) != releaseIdentity(r.Feed) || sum([]byte(r.Envelope.Payload)) != r.PayloadHash {
		return fmt.Errorf("stored index does not match the checked package")
	}
	return nil
}

func packageBytes(name string, f distribution.Feed) error {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() != f.Size {
		return fmt.Errorf("staged package is missing, changed or not a regular file")
	}
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(file, f.Size+1))
	if err != nil || n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.Digest {
		return fmt.Errorf("package SHA256 or size differs from signed provenance")
	}
	return nil
}

func (c *Client) verify(ctx context.Context, r record) error {
	if err := c.fresh(r); err != nil {
		return err
	}
	rel, err := filepath.Rel(c.config.StateDir, r.PackagePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.Base(r.PackagePath) != r.Feed.Filename {
		return fmt.Errorf("staged package escaped private stateDir")
	}
	if err = packageBytes(r.PackagePath, r.Feed); err != nil {
		return err
	}
	return c.native.Verify(ctx, c.config, r.PackagePath, r.Feed)
}

func (c *Client) Stage(ctx context.Context) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	tx, r, err := c.begin(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	if r.State != "available" && r.State != "staged" {
		return r.Snapshot, fmt.Errorf("a checked available package is required for staging")
	}
	if err = c.fresh(r); err != nil {
		return r.Snapshot, err
	}
	if r.State == "staged" {
		if err = c.verify(ctx, r); err == nil {
			return r.Snapshot, nil
		}
		r.State = "available"
		r.PackagePath = ""
		r.Error = "staged verification failed; stage again to download a fresh copy"
		s, saveErr := c.save(ctx, tx, r)
		if saveErr != nil {
			return s, saveErr
		}
		return s, err
	}
	previous := r.Snapshot
	dir, err := os.MkdirTemp(c.config.StateDir, "stage-")
	if err != nil {
		return r.Snapshot, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	r.PackagePath = filepath.Join(dir, r.Feed.Filename)
	response, err := c.get(ctx, r.Feed.URL)
	if err != nil {
		return previous, err
	}
	defer response.Body.Close()
	file, err := os.OpenFile(r.PackagePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return previous, err
	}
	n, copyErr := io.Copy(file, io.LimitReader(response.Body, r.Feed.Size+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || n != r.Feed.Size {
		return previous, fmt.Errorf("download size or durable write failed")
	}
	if err = c.verify(ctx, r); err != nil {
		return previous, err
	}
	r.State = "staged"
	r.Error = ""
	s, err := c.save(ctx, tx, r)
	keep = err == nil
	return s, err
}
