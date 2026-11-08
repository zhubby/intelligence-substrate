package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func cliProfile(t *testing.T) (string, string, map[string]any) {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	config := map[string]any{"stateDir": state, "origin": "https://127.0.0.1:1", "app": "feedback", "target": "macos", "channel": "stable", "publicKey": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)), "expectedSigner": "ABCDE12345", "packageId": "com.company.feedback"}
	return filepath.Join(root, "profile.json"), state, config
}

func TestUpdaterCLIRejectsUnsafeActionsBeforeCreatingState(t *testing.T) {
	for _, args := range [][]string{{"-action", "unknown"}, {"-action", "install"}, {"-action", "install", "-approve-sha256", "wrong"}, {"-action", "stage", "-approve-sha256", "unexpected"}, {"-action", "status", "extra"}} {
		t.Run(args[1], func(t *testing.T) {
			profile, state, config := cliProfile(t)
			body, _ := json.Marshal(config)
			_ = os.WriteFile(profile, body, 0600)
			var output bytes.Buffer
			if err := execute(context.Background(), append([]string{"-config", profile}, args...), &output); err == nil {
				t.Fatal("invalid action/approval accepted")
			}
			if _, err := os.Stat(state); !os.IsNotExist(err) {
				t.Fatal("invalid action created updater state")
			}
		})
	}
}

func TestUpdaterCLIRejectsUnknownTrustConfiguration(t *testing.T) {
	profile, state, config := cliProfile(t)
	config["skipSignatureVerification"] = true
	body, _ := json.Marshal(config)
	_ = os.WriteFile(profile, body, 0600)
	var output bytes.Buffer
	if err := execute(context.Background(), []string{"-config", profile, "-action", "status"}, &output); err == nil {
		t.Fatal("unknown trust option accepted")
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("invalid profile created state")
	}
}

func TestUpdaterCLIStatusRequiresNoNetworkOrNativeInstallation(t *testing.T) {
	profile, _, config := cliProfile(t)
	body, _ := json.Marshal(config)
	_ = os.WriteFile(profile, body, 0600)
	var output bytes.Buffer
	if err := execute(context.Background(), []string{"-config", profile, "-action", "status"}, &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		State           string `json:"state"`
		MinimumSequence int64  `json:"minimumSequence"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.State != "unchecked" || result.MinimumSequence != 0 {
		t.Fatalf("invalid status output: %s %v", output.String(), err)
	}
}
