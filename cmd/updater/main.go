// is-updater checks and stages updates without installing them. Only an explicit
// install action approving the exact SHA256 can invoke a native installer.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"syscall"

	"github.com/intelligence-substrate/platform/internal/updater"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("is-updater", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	profile := flags.String("config", "", "Administrator-owned update profile")
	action := flags.String("action", "status", "check, stage, install, status or reconcile")
	approval := flags.String("approve-sha256", "", "Explicit consent to install these exact staged bytes")
	stopped := flags.String("confirm-stopped-attempt", "", "Operator confirmation that this exact uncertain native installer has stopped")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *profile == "" {
		return fmt.Errorf("a fixed -config profile and no positional arguments are required")
	}
	if (*action == "reconcile" && !regexp.MustCompile(`^[a-f0-9]{48}$`).MatchString(*stopped)) || (*action != "reconcile" && *stopped != "") {
		return fmt.Errorf("reconcile requires -confirm-stopped-attempt for the exact attempt after checking native process termination")
	}
	switch *action {
	case "check", "stage", "status", "reconcile":
		if *approval != "" {
			return fmt.Errorf("SHA256 consent is only valid for install")
		}
	case "install":
		if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(*approval) {
			return fmt.Errorf("install requires explicit -approve-sha256 for the exact staged artifact")
		}
	default:
		return fmt.Errorf("unsupported updater action")
	}
	info, err := os.Lstat(*profile)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64000 {
		return fmt.Errorf("update profile must be a bounded regular JSON file")
	}
	body, err := os.ReadFile(*profile)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var config updater.Config
	if err = decoder.Decode(&config); err != nil {
		return fmt.Errorf("invalid update profile: %w", err)
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("profile must contain exactly one JSON object")
	}
	client, err := updater.Open(config)
	if err != nil {
		return err
	}
	defer client.Close()
	var snapshot updater.Snapshot
	switch *action {
	case "status":
		snapshot, err = client.Status(ctx)
	case "check":
		snapshot, err = client.Check(ctx)
	case "stage":
		snapshot, err = client.Stage(ctx)
	case "install":
		snapshot, err = client.Install(ctx, *approval)
	case "reconcile":
		snapshot, err = client.Reconcile(ctx, *stopped)
	}
	if encodeErr := json.NewEncoder(output).Encode(snapshot); encodeErr != nil {
		return encodeErr
	}
	return err
}
