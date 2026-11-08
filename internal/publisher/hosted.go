package publisher

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"strings"

	"github.com/intelligence-substrate/platform/internal/distribution"
)

func hostedID(p Profile, key string) string { return "hosted:" + p.AppID + ":" + p.Target + ":" + key }
func (e *Engine) hostedPublish(ctx context.Context, p Profile, r Request, hash, source, signer string, key ed25519.PrivateKey) (Result, error) {
	store, err := distribution.Open(p.DistributionRoot)
	if err != nil {
		return Result{}, err
	}
	defer store.Close()
	filename := p.InstallerFilename
	if p.Target == "web" {
		filename = p.AppID + "-" + r.Version + ".tar.gz"
	}
	release := distribution.Release{ID: r.OperationKey, RequestHash: hash, App: p.AppID, Target: p.Target, Channel: p.Channel, Version: r.Version, Digest: r.ArtifactDigest, Size: r.ArtifactSize, Filename: filename, ProjectID: r.ProjectID, GoalID: r.GoalID, ReleaseID: r.ReleaseID, ArtifactID: r.ArtifactID, Commit: r.Commit, Binding: r.Binding, Signer: signer}
	if _, err = store.Publish(ctx, release, source, p.PublicURL, key); err != nil {
		return Result{}, err
	}
	origin := strings.TrimRight(p.PublicURL, "/")
	url := origin + "/updates/" + p.AppID + "/" + p.Target + "/" + p.Channel + ".json"
	if p.Target == "web" {
		url = origin + "/apps/" + p.AppID + "/web/"
	}
	return Result{ExternalID: hostedID(p, r.OperationKey), State: "published", UpstreamState: "active", URL: url}, nil
}
func (e *Engine) hostedStatus(ctx context.Context, p Profile, r Request) (Result, error) {
	if r.ExternalID != hostedID(p, r.OperationKey) {
		return Result{}, fmt.Errorf("hosted external ID does not match operation")
	}
	store, err := distribution.OpenReadOnly(p.DistributionRoot)
	if err != nil {
		return Result{}, err
	}
	defer store.Close()
	release, err := store.Lookup(ctx, r.OperationKey)
	if err != nil {
		return Result{}, err
	}
	if release.App != p.AppID || release.Target != p.Target || release.Channel != p.Channel {
		return Result{}, fmt.Errorf("hosted receipt does not match destination")
	}
	current, err := store.Current(ctx, p.AppID, p.Target, p.Channel)
	if err != nil {
		return Result{}, err
	}
	state, upstream := "published", "active"
	if current.ID != release.ID {
		state, upstream = "halted", "superseded"
	}
	return Result{ExternalID: r.ExternalID, State: state, UpstreamState: upstream}, nil
}
