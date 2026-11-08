package runtimehost

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type Engine struct {
	Store  *Store
	Driver ContainerDriver
}

func (e *Engine) requestHash(r Request) string {
	r.RunID = ""
	r.ArtifactPath = ""
	r.Action = ""
	r.ExternalID = ""
	destination := e.Store.Config
	destination.StateDir = ""
	destination.EnvironmentFile = ""
	destination.PlatformURL = ""
	destination.WorkerTokenFile = ""
	body, _ := json.Marshal(struct {
		Request     Request
		Destination Config
	}{r, destination})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
func (e *Engine) Execute(ctx context.Context, r Request) (Result, error) {
	c := e.Store.Config
	if r.ProtocolVersion != "1" || r.ProjectID != c.ProjectID || r.GoalID != c.GoalID || r.Target != "web" || !hashRE.MatchString(r.OperationKey) || !hashRE.MatchString(r.Binding) || !hashRE.MatchString(r.ArtifactDigest) || r.ArtifactSize < 1 || !commitRE.MatchString(r.Commit) || len(r.Version) == 0 || len(r.Version) > 80 || !idRE.MatchString(r.ReleaseID) || !idRE.MatchString(r.ArtifactID) {
		return Result{}, fmt.Errorf("complete approved runtime binding required")
	}
	if r.Action == "status" {
		return e.status(ctx, r)
	}
	if r.Action != "publish" && r.Action != "rollback" && r.Action != "" {
		return Result{}, fmt.Errorf("unsupported runtime action")
	}
	hash := e.requestHash(r)
	op, err := e.Store.reserve(ctx, r.OperationKey, hash)
	if err != nil {
		return Result{}, err
	}
	if !op.New {
		if op.State == "complete" && op.Result != nil {
			return *op.Result, nil
		}
		return Result{}, fmt.Errorf("runtime operation %s requires inspection and explicit reconciliation; never replay", op.State)
	}
	knownFailure := func(err error) (Result, error) {
		if finishErr := e.Store.finish(context.WithoutCancel(ctx), r.OperationKey, Result{}, err.Error()); finishErr != nil {
			return Result{}, fmt.Errorf("runtime journal could not confirm failure; reconcile")
		}
		return Result{}, err
	}
	previous, err := e.Store.Active(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return knownFailure(err)
	}
	dep := Deployment{ID: r.OperationKey, ReleaseID: r.ReleaseID, ArtifactID: r.ArtifactID, ArtifactDigest: r.ArtifactDigest, ArtifactSize: r.ArtifactSize, Commit: r.Commit, Binding: r.Binding, Version: r.Version, Container: "isrt-" + c.owner() + "-" + r.OperationKey[:16]}
	if r.Action == "rollback" {
		expected, err := c.externalKey(r.ExpectedActive)
		if err != nil || previous.ID != expected {
			return knownFailure(fmt.Errorf("active version differs from approved rollback source"))
		}
		destination, err := c.externalKey(r.RollbackTo)
		if err != nil || destination == expected {
			return knownFailure(fmt.Errorf("distinct historical rollback destination required"))
		}
		old, err := e.Store.Lookup(ctx, destination)
		if err != nil || old.ReleaseID != r.ReleaseID || old.ArtifactID != r.ArtifactID || old.ArtifactDigest != r.ArtifactDigest || old.Commit != r.Commit || old.Binding != r.Binding {
			return knownFailure(fmt.Errorf("rollback destination differs from persisted approved artifact"))
		}
		dep.ImageID = old.ImageID
	} else {
		image, cleanup, err := prepareArchive(r.Request, c)
		if err != nil {
			return knownFailure(err)
		}
		defer cleanup()
		if err = e.Driver.Load(ctx, image.Archive, image.ID); err != nil {
			return knownFailure(err)
		}
		dep.ImageID = image.ID
	}
	if err = e.Store.intent(ctx, r.OperationKey, dep, previous.ID); err != nil {
		return Result{}, err
	}
	if previous.ID != "" {
		if err = e.Driver.Stop(ctx, previous); err != nil {
			return Result{}, fmt.Errorf("previous writer could not be confirmed stopped; reconcile runtime operation")
		}
	}
	dep, err = e.Driver.Start(ctx, dep)
	if err == nil {
		err = e.waitReady(ctx, dep)
	}
	if err != nil {
		recovery, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer stop()
		if stopErr := e.Driver.Stop(recovery, dep); stopErr != nil {
			return Result{}, fmt.Errorf("candidate result uncertain; reconcile before starting another writer")
		}
		if previous.ID != "" {
			restored, restoreErr := e.Driver.Start(recovery, previous)
			if restoreErr != nil {
				return Result{}, fmt.Errorf("previous version could not be restored; reconcile")
			}
			if checkErr := e.waitReady(recovery, restored); checkErr != nil {
				return Result{}, fmt.Errorf("previous version is not healthy after restore; reconcile")
			}
			if journalErr := e.Store.activate(recovery, restored, previous.ID); journalErr != nil {
				return Result{}, fmt.Errorf("restored endpoint could not be persisted; reconcile")
			}
		}
		return knownFailure(fmt.Errorf("candidate failed; previous active version retained"))
	}
	if err = e.Store.activate(ctx, dep, previous.ID); err != nil {
		return Result{}, fmt.Errorf("container started but activation journal uncertain; reconcile")
	}
	result := e.result(dep)
	if err = e.Store.finish(ctx, r.OperationKey, result, ""); err != nil {
		return Result{}, fmt.Errorf("application activated but receipt uncertain; reconcile")
	}
	return result, nil
}
func (e *Engine) result(d Deployment) Result {
	return Result{ExternalID: e.Store.Config.external(d.ID), State: "published", URL: e.Store.Config.PublicURL, Runtime: map[string]any{"driver": "docker", "application": e.Store.Config.AppID, "activationId": e.Store.Config.external(d.ID), "imageId": d.ImageID, "dataContract": e.Store.Config.DataContract}}
}
func (e *Engine) status(ctx context.Context, r Request) (Result, error) {
	key, err := e.Store.Config.externalKey(r.ExternalID)
	if err != nil {
		return Result{}, err
	}
	d, err := e.Store.Lookup(ctx, key)
	if err != nil || d.ReleaseID != r.ReleaseID || d.ArtifactDigest != r.ArtifactDigest || d.Binding != r.Binding || d.Commit != r.Commit {
		return Result{}, fmt.Errorf("runtime query does not match persisted publication")
	}
	active, err := e.Store.Active(ctx)
	if err != nil {
		return Result{}, err
	}
	result := e.result(d)
	if active.ID != d.ID {
		result.State = "halted"
		result.Reason = "a different approved activation is serving this application"
		return result, nil
	}
	probe := e.Probe(ctx, d)
	result.UpstreamState = probe.Health
	if probe.Health != "healthy" {
		result.State = "unchanged"
		result.Reason = "active application health requires attention; health observations are separate from acceptance"
	}
	return result, nil
}

type Observation struct {
	Health     string `json:"health"`
	StatusCode int    `json:"statusCode"`
	LatencyMS  int64  `json:"latencyMs"`
	CheckedAt  string `json:"checkedAt"`
}

func (e *Engine) Probe(ctx context.Context, d Deployment) Observation {
	started := time.Now()
	sample := Observation{Health: "unhealthy", CheckedAt: started.UTC().Format(time.RFC3339Nano)}
	address, err := backendAddress(d)
	if err != nil {
		return sample
	}
	request, err := http.NewRequestWithContext(ctx, "GET", "http://"+address+e.Store.Config.HealthPath, nil)
	if err != nil {
		return sample
	}
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	response, err := client.Do(request)
	sample.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		return sample
	}
	response.Body.Close()
	sample.StatusCode = response.StatusCode
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		sample.Health = "healthy"
	}
	return sample
}
func (e *Engine) waitReady(ctx context.Context, d Deployment) error {
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		if e.Probe(ctx, d).Health == "healthy" {
			return nil
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return fmt.Errorf("application did not become healthy")
		case <-timer.C:
		}
	}
}
