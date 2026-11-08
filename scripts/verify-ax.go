//go:build ignore

// Run explicitly against an operator-provisioned, disposable AX atespace.
// This exercises real guest commands and a local authenticated control plane;
// it does not call a model or publish an application.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	"github.com/intelligence-substrate/platform/internal/integrations"
	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/worker"
)

func main() {
	config := flag.String("config", "", "administrator-controlled worker AX JSON configuration")
	state := flag.String("state", "", "new private evidence directory; must not exist")
	flag.Parse()
	if *config == "" || *state == "" {
		fmt.Fprintln(os.Stderr, "-config and -state are required")
		os.Exit(2)
	}
	if err := os.Mkdir(*state, 0700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	report := p.M{"startedAt": time.Now().UTC(), "modelInvoked": false, "published": false, "approvalIdentity": "synthetic local acceptance owner"}
	err := verify(*config, *state, report)
	if err != nil {
		report["error"] = err.Error()
	}
	report["passed"], report["finishedAt"] = err == nil, time.Now().UTC()
	body, _ := json.MarshalIndent(report, "", "  ")
	if writeErr := os.WriteFile(filepath.Join(*state, "result.json"), append(body, '\n'), 0600); writeErr != nil {
		fmt.Fprintln(os.Stderr, writeErr)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Actual AX transport and authenticated Worker checks passed; see", filepath.Join(*state, "result.json"))
}

func verify(configPath, state string, report p.M) error {
	body, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var config worker.Config
	if err = json.Unmarshal(body, &config); err != nil {
		return err
	}
	if config.Driver != "ax" || config.AX.Binary == "" || config.AX.Atespace == "" {
		return fmt.Errorf("an explicit AX driver, trusted binary and disposable atespace are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	report["atespace"], report["image"] = config.AX.Atespace, config.AX.Image
	if err = transport(ctx, config.AX, state, report); err != nil {
		return err
	}
	return flow(ctx, config, state, report)
}

func transport(ctx context.Context, ax integrations.AX, state string, report p.M) error {
	fmt.Println("Checking actual guest source/result transfer and nonzero exit")
	dir := filepath.Join(state, "transport")
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	want := []byte{0, 1, 2, 255, '\n', '\''}
	if err := os.WriteFile(filepath.Join(dir, "binary input"), want, 0600); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(state, "transport.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	exit, err := ax.RunCommand(ctx, dir, []string{"/bin/sh", "-c", `test -z "$IS_WORKER_TOKEN" && test -z "$PGPASSWORD" && cp 'binary input' 'binary output' && rm 'binary input' && printf '#!/bin/sh\nexit 0\n' > executable && chmod 755 executable`}, log)
	if err != nil || exit != 0 {
		return fmt.Errorf("actual result transfer: exit=%d: %v", exit, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "binary output"))
	if err != nil || !bytes.Equal(got, want) {
		return fmt.Errorf("binary result differs: %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "binary input")); !os.IsNotExist(err) {
		return fmt.Errorf("guest deletion was lost: %v", err)
	}
	mode, err := os.Stat(filepath.Join(dir, "executable"))
	if err != nil || mode.Mode()&0100 == 0 {
		return fmt.Errorf("executable mode was lost: %v", err)
	}
	exit, err = ax.RunCommand(ctx, dir, []string{"/bin/sh", "-c", `printf 'failure result\n' > nonzero.txt; exit 17`}, log)
	if err != nil || exit != 17 {
		return fmt.Errorf("actual nonzero exit lost: exit=%d: %v", exit, err)
	}
	got, err = os.ReadFile(filepath.Join(dir, "nonzero.txt"))
	if err != nil || string(got) != "failure result\n" {
		return fmt.Errorf("nonzero result bytes missing: %v", err)
	}
	exit, err = ax.RunCommand(ctx, dir, []string{"/bin/sh", "-c", `printf 'must not replace input\n' > nonzero.txt; ln -s /etc/passwd forbidden-link`}, log)
	if err == nil || exit == 0 {
		return fmt.Errorf("unsafe guest output was accepted")
	}
	got, readErr := os.ReadFile(filepath.Join(dir, "nonzero.txt"))
	if readErr != nil || string(got) != "failure result\n" {
		return fmt.Errorf("rejected result altered original input: %v", readErr)
	}
	report["transport"] = p.M{"binaryRoundTrip": true, "deletion": true, "executableMode": true, "exit17": true, "unsafeLinkRejected": true, "sourcePreservedOnRejectedTransfer": true, "unsafeResultError": err.Error()}
	return nil
}

func flow(ctx context.Context, config worker.Config, state string, report p.M) error {
	fmt.Println("Checking authenticated HTTP → code → Git → build → independent QA through AX")
	store, err := p.Open(filepath.Join(state, "platform.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	svc := p.NewService(store)
	h := httpapi.New(svc, false)
	password := p.Secret()
	if err = h.Auth.Bootstrap("ax-proof@example.test", password); err != nil {
		return err
	}
	token, session, err := h.Auth.Login("ax-proof@example.test", password)
	if err != nil {
		return err
	}
	h.ModelCall = func(context.Context, p.Actor, p.M) (p.M, error) {
		report["modelInvoked"] = true
		return nil, fmt.Errorf("the fixed-command AX acceptance must not call a model")
	}
	server := httptest.NewServer(h)
	defer server.Close()
	command := func(name string, in p.M) (p.Resource, error) {
		body, _ := json.Marshal(in)
		req, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/v1/commands/"+name, bytes.NewReader(body))
		if err != nil {
			return p.Resource{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", server.URL)
		req.Header.Set("X-CSRF-Token", session.CSRF)
		req.AddCookie(&http.Cookie{Name: "is_session", Value: token})
		resp, err := server.Client().Do(req)
		if err != nil {
			return p.Resource{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			var failure p.M
			_ = json.NewDecoder(resp.Body).Decode(&failure)
			return p.Resource{}, fmt.Errorf("%s HTTP %d: %v", name, resp.StatusCode, failure)
		}
		var result p.Resource
		err = json.NewDecoder(resp.Body).Decode(&result)
		return result, err
	}
	project, err := command("projects.create", p.M{"title": "Actual AX acceptance"})
	if err != nil {
		return err
	}
	goal, err := command("goals.create", p.M{"projectId": project.ID, "title": "AX web package", "outcome": "Produce an independently verified HTML package", "targets": []any{"web"}, "criteria": []any{p.M{"id": "content", "text": "Actual package includes approved HTML and executable", "method": "automated"}}, "budget": 100})
	if err != nil {
		return err
	}
	if _, err = command("goals.approve", p.M{"id": goal.ID}); err != nil {
		return err
	}
	repo, err := command("repositories.create", p.M{"projectId": project.ID, "title": "ax-source"})
	if err != nil {
		return err
	}
	plan, err := command("plans.propose", p.M{"goalId": goal.ID, "summary": "Approved fixed commands executed in real guests", "tasks": []any{
		p.M{"id": "code", "title": "Generate source in AX", "kind": "code", "role": "developer", "runtime": "linux", "repositoryId": repo.ID, "command": []any{"/bin/sh", "-c", `test -z "$IS_WORKER_TOKEN" && printf '<!doctype html><title>AX acceptance</title><h1>Verified AX output</h1>\n' > index.html && printf '#!/bin/sh\nprintf "AX-client-ready\\n"\n' > run.sh && chmod +x run.sh`}},
		p.M{"id": "build", "title": "Build pinned source in AX", "kind": "build", "role": "developer", "runtime": "linux", "repositoryId": repo.ID, "dependsOn": []any{"code"}, "command": []any{"/bin/sh", "-c", "test -x run.sh && tar -cf package.tar index.html run.sh"}, "outputPath": "package.tar", "target": "web"},
	}})
	if err != nil {
		return err
	}
	if _, err = command("plans.approve", p.M{"id": plan.ID}); err != nil {
		return err
	}
	runner := func(role string, caps []any, quality bool) (*worker.Runner, error) {
		w, err := command("workers.register", p.M{"projectId": project.ID, "title": role + " AX acceptance", "runtime": "linux", "roles": []any{role}, "capabilities": caps, "trustedQuality": quality})
		if err != nil {
			return nil, err
		}
		token, _, err := h.Auth.IssueToken(ctx, session.Actor, p.M{"projectId": project.ID, "workerId": w.ID})
		if err != nil {
			return nil, err
		}
		client, err := worker.NewClient(server.URL, token, true)
		if err != nil {
			return nil, err
		}
		cfg := config
		cfg.StateDir = filepath.Join(state, role)
		return &worker.Runner{Client: client, Config: cfg}, nil
	}
	dev, err := runner("developer", []any{"code", "build"}, false)
	if err != nil {
		return err
	}
	for i := 0; i < 2; i++ {
		if worked, err := dev.RunOnce(ctx); err != nil || !worked {
			return fmt.Errorf("AX developer cycle %d: worked=%v: %v", i, worked, err)
		}
	}
	artifacts, err := store.List(project.ID, "artifact", goal.ID)
	if err != nil || len(artifacts) != 1 {
		return fmt.Errorf("actual AX artifact not registered: %v", err)
	}
	artifact := artifacts[0]
	if err = svc.VerifyArtifact(artifact); err != nil {
		return err
	}
	commit := p.S(artifact.Data, "commit")
	if !p.CommitPattern.MatchString(commit) {
		return fmt.Errorf("artifact lacks exact source commit")
	}
	pipeline, err := command("pipelines.create", p.M{"projectId": project.ID, "title": "Independent actual AX package check", "runtime": "linux", "method": "automated", "command": []any{"/bin/sh", "-c", `test -x run.sh && ./run.sh | grep -qx AX-client-ready && mkdir inspect && tar --no-same-owner -xf "$IS_ARTIFACT_PATH" -C inspect && cmp index.html inspect/index.html && grep -q 'Verified AX output' inspect/index.html`}})
	if err != nil {
		return err
	}
	if _, err = command("pipelines.run", p.M{"pipelineId": pipeline.ID, "artifactId": artifact.ID, "criteria": []any{"content"}}); err != nil {
		return err
	}
	qa, err := runner("quality", []any{"test"}, true)
	if err != nil {
		return err
	}
	if worked, err := qa.RunOnce(ctx); err != nil || !worked {
		return fmt.Errorf("AX quality cycle: worked=%v: %v", worked, err)
	}
	evidence, err := store.List(project.ID, "evidence", goal.ID)
	if err != nil || len(evidence) != 1 || evidence[0].Status != "passed" || p.S(evidence[0].Data, "commit") != commit {
		return fmt.Errorf("actual AX quality evidence missing or differs: %v", err)
	}
	_, releaseErr := command("releases.create", p.M{"goalId": goal.ID, "version": "1.0.0", "commit": commit, "artifactIds": []any{artifact.ID}})
	if releaseErr == nil || !strings.Contains(releaseErr.Error(), "目标环境") {
		return fmt.Errorf("automated-only AX evidence bypassed browser gate: %v", releaseErr)
	}
	runs, err := store.List(project.ID, "run", goal.ID)
	if err != nil || len(runs) != 3 {
		return fmt.Errorf("AX execution records missing: %v", err)
	}
	rows := []p.M{}
	for _, run := range runs {
		result := p.Obj(run.Data["result"])
		if run.Status != "succeeded" || p.S(result, "environment") != "ax" || p.S(result, "commit") != commit {
			return fmt.Errorf("run %s not successful on exact AX source", run.ID)
		}
		rows = append(rows, p.M{"id": run.ID, "status": run.Status, "kind": p.S(p.Obj(run.Data["task"]), "kind"), "commit": p.S(result, "commit"), "environment": "ax"})
	}
	report["workerFlow"] = p.M{"projectId": project.ID, "goalId": goal.ID, "planId": plan.ID, "repositoryId": repo.ID, "commit": commit, "artifactId": artifact.ID, "artifactDigest": p.S(artifact.Data, "digest"), "evidenceId": evidence[0].ID, "runs": rows, "independentQualityIdentity": true, "browserGateRejectedRelease": true, "releaseRejection": releaseErr.Error()}
	return nil
}
