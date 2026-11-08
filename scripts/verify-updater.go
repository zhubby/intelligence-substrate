//go:build ignore

// Run on macOS: go run scripts/verify-updater.go -updater work/bin/is-updater
// Exercises actual CLI/TLS/SQLite and native unsigned rejection. Never installs.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/intelligence-substrate/platform/internal/distribution"
	"github.com/intelligence-substrate/platform/internal/updater"
)

type commandResult struct {
	Action   string           `json:"action"`
	ExitCode int              `json:"exitCode"`
	Snapshot updater.Snapshot `json:"snapshot"`
	Stderr   string           `json:"stderr,omitempty"`
}

func main() {
	if err := verify(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func verify() (resultErr error) {
	binary := flag.String("updater", "work/bin/is-updater", "Explicit updater executable")
	flag.Parse()
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("this native unsigned-PKG CLI proof requires macOS; run updater tests for portable contract checks")
	}
	program, err := filepath.Abs(*binary)
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("work", "updater-cli-")
	if err != nil {
		return err
	}
	root, _ = filepath.Abs(root)
	report := map[string]any{"kind": "actual-updater-cli-tls-and-unsigned-native-rejection", "startedAt": time.Now().UTC().Format(time.RFC3339), "nativeInstallExecuted": false, "realSignedUpgrade": false, "syntheticPublisherAttestation": true, "storePublished": false, "productPlanComplete": false}
	defer func() {
		report["passed"] = resultErr == nil
		report["finishedAt"] = time.Now().UTC().Format(time.RFC3339)
		if resultErr != nil {
			report["error"] = resultErr.Error()
		}
		body, _ := json.MarshalIndent(report, "", "  ")
		_ = os.WriteFile(filepath.Join(root, "result.json"), append(body, '\n'), 0600)
		fmt.Println(string(body))
		fmt.Println("evidence:", root)
	}()
	store, err := distribution.Open(filepath.Join(root, "distribution"))
	if err != nil {
		return err
	}
	defer store.Close()
	var override atomic.Value
	override.Store([]byte(nil))
	handler := store.Handler()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/updates/") {
			if body := override.Load().([]byte); len(body) > 0 {
				_, _ = w.Write(body)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	ca := filepath.Join(root, "fixture-ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		return err
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	packageBytes := []byte("unsigned diagnostic fixture; not a signed or installable PKG")
	artifact := filepath.Join(root, "unsigned.pkg")
	if err = os.WriteFile(artifact, packageBytes, 0600); err != nil {
		return err
	}
	digest := sha256.Sum256(packageBytes)
	release := distribution.Release{ID: "fixture-operation", RequestHash: "synthetic-approved-fixture", App: "feedback", Target: "macos", Channel: "stable", Version: "2.0.0", Digest: hex.EncodeToString(digest[:]), Size: int64(len(packageBytes)), Filename: "feedback.pkg", Signer: "ABCDE12345", ProjectID: "project_fixture", GoalID: "goal_fixture", ReleaseID: "release_fixture", ArtifactID: "artifact_fixture", Commit: strings.Repeat("a", 40), Binding: strings.Repeat("b", 64)}
	if _, err = store.Publish(context.Background(), release, artifact, server.URL, key); err != nil {
		return err
	}
	response, err := server.Client().Get(server.URL + "/updates/feedback/macos/stable.json")
	if err != nil {
		return err
	}
	original, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		return err
	}
	config := updater.Config{StateDir: filepath.Join(root, "updater-state"), Origin: server.URL, App: "feedback", Target: "macos", Channel: "stable", PublicKey: base64.StdEncoding.EncodeToString(pub), ExpectedSigner: "ABCDE12345", PackageID: "com.intelligencesubstrate.fixture-feedback", CAFile: ca}
	profile := filepath.Join(root, "profile.json")
	body, _ := json.Marshal(config)
	if err = os.WriteFile(profile, body, 0600); err != nil {
		return err
	}
	commands := []commandResult{}
	run := func(action string) commandResult {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, program, "-config", profile, "-action", action)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		result := commandResult{Action: action, Stderr: strings.TrimSpace(stderr.String())}
		if err != nil {
			result.ExitCode = -1
			if exit, ok := err.(*exec.ExitError); ok {
				result.ExitCode = exit.ExitCode()
			}
		}
		if stdout.Len() > 0 {
			if err = json.Unmarshal(stdout.Bytes(), &result.Snapshot); err != nil {
				result.ExitCode = -1
				result.Stderr += " invalid CLI JSON"
			}
		}
		commands = append(commands, result)
		report["commands"] = commands
		return result
	}
	if got := run("status"); got.ExitCode != 0 || got.Snapshot.State != "unchecked" {
		return fmt.Errorf("initial status failed: %+v", got)
	}
	if got := run("check"); got.ExitCode != 0 || got.Snapshot.State != "available" || got.Snapshot.MinimumSequence != 1 || got.Snapshot.InstalledVersion != "" {
		return fmt.Errorf("actual signed feed check failed: %+v", got)
	}
	if got := run("stage"); got.ExitCode == 0 || !strings.Contains(got.Stderr, "signature verification failed") {
		return fmt.Errorf("actual macOS verifier did not reject unsigned download: %+v", got)
	}
	if got := run("status"); got.ExitCode != 0 || got.Snapshot.State != "available" || got.Snapshot.PackagePath != "" {
		return fmt.Errorf("unsigned package left installable state: %+v", got)
	}
	if err = store.Renew(context.Background(), "feedback", "macos", "stable", server.URL, key); err != nil {
		return err
	}
	if got := run("check"); got.ExitCode != 0 || got.Snapshot.MinimumSequence != 2 {
		return fmt.Errorf("legitimate feed renewal failed: %+v", got)
	}
	override.Store(original)
	if got := run("check"); got.ExitCode == 0 || got.Snapshot.MinimumSequence != 2 {
		return fmt.Errorf("restarted CLI accepted older signed index: %+v", got)
	}
	if got := run("install"); got.ExitCode == 0 || !strings.Contains(got.Stderr, "approve-sha256") {
		return fmt.Errorf("missing approval reached installation: %+v", got)
	}
	if got := run("status"); got.ExitCode != 0 || got.Snapshot.MinimumSequence != 2 || got.Snapshot.State != "available" {
		return fmt.Errorf("rejected operations corrupted durable state: %+v", got)
	}
	binaryBytes, err := os.ReadFile(program)
	if err != nil {
		return err
	}
	binaryDigest := sha256.Sum256(binaryBytes)
	report["binary"] = map[string]any{"path": program, "sha256": hex.EncodeToString(binaryDigest[:]), "goos": runtime.GOOS, "goarch": runtime.GOARCH}
	report["checks"] = []string{"actual CLI signed HTTPS check", "native unsigned rejection before staging", "legitimate renewal preserves artifact", "restart-persistent sequence rejects replay", "exact SHA256 consent required before install"}
	return nil
}
