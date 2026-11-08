//go:build ignore

// Explicit local contract/CLI proof. No live Apple call or signed IPA is used.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/intelligence-substrate/platform/internal/process"
	"github.com/intelligence-substrate/platform/internal/publisher"
)

func main() {
	if err := verify(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func verify() (resultErr error) {
	programFlag := flag.String("publisher", "", "actual publisher executable")
	stateFlag := flag.String("state", "", "new evidence directory; must not exist")
	flag.Parse()
	if *programFlag == "" || *stateFlag == "" {
		return fmt.Errorf("-publisher and a new -state directory are required")
	}
	program, err := filepath.Abs(*programFlag)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(*stateFlag)
	if err != nil {
		return err
	}
	if err = os.Mkdir(root, 0700); err != nil {
		return err
	}
	report := map[string]any{"kind": "actual-apple-recovery-contract-and-cli", "startedAt": time.Now().UTC(), "liveApple": false, "realSignedIPA": false, "packageVerification": "test-only substituted dependency; no production bypass", "cliRemoteRecoveryExecuted": false, "cliReplaysActualRecoveredContractJournal": true, "productPlanComplete": false}
	defer func() {
		report["passed"], report["finishedAt"] = resultErr == nil, time.Now().UTC()
		if resultErr != nil {
			report["error"] = resultErr.Error()
		}
		b, _ := json.MarshalIndent(report, "", "  ")
		_ = os.WriteFile(filepath.Join(root, "result.json"), append(b, '\n'), 0600)
		fmt.Println("Evidence:", filepath.Join(root, "result.json"))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	commands := []map[string]any{}
	run := func(name string, args []string, input []byte, environment []string, wantSuccess bool) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "rtk", append([]string{"proxy"}, args...)...)
		process.Protect(cmd)
		if environment != nil {
			cmd.Env = environment
		}
		cmd.Stdin = bytes.NewReader(input)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		callErr := cmd.Run()
		exit := 0
		if callErr != nil {
			exit = -1
			if e, ok := callErr.(*exec.ExitError); ok {
				exit = e.ExitCode()
			}
		}
		_ = os.WriteFile(filepath.Join(root, name+".stdout"), stdout.Bytes(), 0600)
		_ = os.WriteFile(filepath.Join(root, name+".stderr"), stderr.Bytes(), 0600)
		commands = append(commands, map[string]any{"name": name, "exitCode": exit, "successExpected": wantSuccess})
		report["commands"] = commands
		if (callErr == nil) != wantSuccess || ctx.Err() != nil {
			return nil, fmt.Errorf("%s unexpected exit %d; inspect private evidence", name, exit)
		}
		return stdout.Bytes(), nil
	}
	contract := filepath.Join(root, "contract")
	if err = os.Mkdir(contract, 0700); err != nil {
		return err
	}
	environment := append(os.Environ(), "IS_APPLE_RECOVERY_EVIDENCE_DIR="+contract)
	if _, err = run("accepted-write-loss-contract", []string{"go", "test", "-race", "./internal/publisher", "-run", "^TestAppleRecoveryAcceptedWritesAreReadOnlyAndDurable$", "-count=1", "-v"}, nil, environment, true); err != nil {
		return err
	}
	for _, phase := range []string{"upload", "review"} {
		dir := filepath.Join(contract, phase)
		config := filepath.Join(dir, "config.json")
		body, err := os.ReadFile(filepath.Join(dir, "recovery.json"))
		if err != nil {
			return err
		}
		var in publisher.AppleReconciliationRequest
		if err = json.Unmarshal(body, &in); err != nil {
			return err
		}
		opBody, err := os.ReadFile(filepath.Join(dir, "operation.json"))
		if err != nil {
			return err
		}
		var expected publisher.Operation
		if err = json.Unmarshal(opBody, &expected); err != nil {
			return err
		}
		result, err := run(phase+"-inspect", []string{program, "-config", config, "-inspect", in.Request.OperationKey}, nil, nil, true)
		if err != nil {
			return err
		}
		var inspected publisher.Operation
		if json.Unmarshal(result, &inspected) != nil || inspected.JournalDigest != expected.JournalDigest {
			return fmt.Errorf("%s CLI inspection changed the recovered snapshot", phase)
		}
		result, err = run(phase+"-replay", []string{program, "-config", config, "-reconcile-apple"}, body, nil, true)
		if err != nil {
			return err
		}
		var replay publisher.Operation
		if json.Unmarshal(result, &replay) != nil || replay.JournalDigest != expected.JournalDigest || replay.State != "complete" {
			return fmt.Errorf("%s CLI did not replay exact contract recovery", phase)
		}
		for _, mode := range []string{"-status-only", "-continue-only", "-renew-feeds"} {
			if _, err = run(phase+"-deny"+mode, []string{program, "-config", config, "-reconcile-apple", mode}, body, nil, false); err != nil {
				return err
			}
		}
		for _, scenario := range []string{"missing-stop", "stale-snapshot", "changed-binding", "changed-reason", "unknown-field", "replacement-id", "trailing-json"} {
			changed := in
			switch scenario {
			case "missing-stop":
				changed.ConfirmStoppedOperationKey = ""
			case "stale-snapshot":
				changed.JournalDigest = strings.Repeat("a", 64)
			case "changed-binding":
				changed.Request.Binding = strings.Repeat("f", 64)
			case "changed-reason":
				changed.Reason = "A different assertion cannot borrow prior recovery"
			case "replacement-id":
				changed.Request.ExternalID = "apple:1234567890:other-upload"
			}
			input, _ := json.Marshal(changed)
			if scenario == "unknown-field" {
				input = append([]byte(`{"uploadId":"other",`), input[1:]...)
			}
			if scenario == "trailing-json" {
				input = append(input, []byte("\n{}")...)
			}
			if _, err = run(phase+"-deny-"+scenario, []string{program, "-config", config, "-reconcile-apple"}, input, nil, false); err != nil {
				return err
			}
		}
		ordinary := in.Request
		ordinary.Action = "reconcile"
		input, _ := json.Marshal(ordinary)
		if _, err = run(phase+"-deny-normal-protocol", []string{program, "-config", config}, input, nil, false); err != nil {
			return err
		}
		result, err = run(phase+"-final-inspect", []string{program, "-config", config, "-inspect", in.Request.OperationKey}, nil, nil, true)
		if err != nil {
			return err
		}
		if json.Unmarshal(result, &inspected) != nil || inspected.JournalDigest != expected.JournalDigest {
			return fmt.Errorf("%s rejection cases altered publisher journal", phase)
		}
	}
	binary, err := os.ReadFile(program)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(binary)
	report["binary"] = map[string]any{"sha256": hex.EncodeToString(digest[:]), "goos": runtime.GOOS, "goarch": runtime.GOARCH}
	report["checks"] = []string{"actual SQLite and TLS accepted-write/lost-response recovery", "recovery makes no external write", "real binary inspection and credential-free replay", "worker/status/continuation modes cannot recover", "stopped confirmation and exact binding required", "unknown fields and trailing input rejected", "failed inputs preserve exact journal"}
	return nil
}
