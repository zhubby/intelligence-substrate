// is-publisher implements the trusted stdin/stdout release contract. Install its
// executable and configuration separately from repository workspaces.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/intelligence-substrate/platform/internal/distribution"
	"github.com/intelligence-substrate/platform/internal/publisher"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	configFile := flag.String("config", "", "Administrator-owned publisher JSON")
	statusOnly := flag.Bool("status-only", false, "Refuse all mutating publication actions")
	continueOnly := flag.Bool("continue-only", false, "Allow only continuation of a persisted approved upload")
	materialsOnly := flag.Bool("materials-only", false, "Allow only an independently approved App Store text revision")
	inspect := flag.String("inspect", "", "Inspect a persisted operation without contacting a channel")
	inspectGoogleQuery := flag.String("inspect-google-query", "", "Inspect an exact persisted Google read query without contacting a channel")
	reconcileApple := flag.Bool("reconcile-apple", false, "Administrator-only read reconciliation of a stopped, uncertain App Store operation")
	reconcileStore := flag.Bool("reconcile-store", false, "Administrator-only evidence reconciliation of a stopped, uncertain domestic store operation")
	reconcileGoogle := flag.Bool("reconcile-google", false, "Administrator-only reconciliation of a stopped, uncertain Google Play operation; requires an exclusive app edit window")
	reconcileGoogleQuery := flag.Bool("reconcile-google-query", false, "Administrator-only cleanup of a stopped Google read query; never completes or resubmits a publication")
	initKey := flag.String("init-key", "", "Create a new Ed25519 update seed; refuses overwrite")
	renew := flag.Bool("renew-feeds", false, "Renew signed indexes of configured hosted channels; does not change packages")
	flag.Parse()
	if *materialsOnly && (*statusOnly || *continueOnly || *reconcileApple || *reconcileStore || *reconcileGoogle || *reconcileGoogleQuery || *renew || *inspect != "" || *inspectGoogleQuery != "" || *initKey != "") {
		return fmt.Errorf("material provider mode is exclusive")
	}
	if err := publisherModes(*statusOnly, *continueOnly, *reconcileApple, *reconcileStore, *reconcileGoogle, *reconcileGoogleQuery, *renew, *inspect, *inspectGoogleQuery, *initKey); err != nil {
		return err
	}
	if *initKey != "" {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(*initKey, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.WriteString(base64.StdEncoding.EncodeToString(private.Seed()) + "\n")
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"publicKey": base64.StdEncoding.EncodeToString(public), "keyId": distribution.KeyID(public)})
	}
	if *configFile == "" {
		return fmt.Errorf("-config is required")
	}
	file, err := os.Open(*configFile)
	if err != nil {
		return err
	}
	defer file.Close()
	var config publisher.Config
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&config); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fmt.Errorf("configuration must contain one JSON object")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	engine, err := publisher.Open(config)
	if err != nil {
		return err
	}
	defer engine.Close()
	if *inspect != "" {
		op, err := engine.Inspect(ctx, *inspect)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(op)
	}
	if *inspectGoogleQuery != "" {
		query, err := engine.InspectGoogleQuery(ctx, *inspectGoogleQuery)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(query)
	}
	if *renew {
		if *statusOnly || *continueOnly {
			return fmt.Errorf("read-only mode cannot renew signed indexes")
		}
		for _, profile := range config.Profiles {
			if profile.Driver != "hosted" {
				continue
			}
			key, err := publisher.LoadSigningKey(profile.SigningKeyFile)
			if err != nil {
				return err
			}
			store, err := distribution.Open(profile.DistributionRoot)
			if err != nil {
				return err
			}
			err = store.Renew(ctx, profile.AppID, profile.Target, profile.Channel, profile.PublicURL, key)
			store.Close()
			if err != nil {
				return err
			}
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "renewed"})
	}
	if *reconcileApple {
		var request publisher.AppleReconciliationRequest
		decoder = json.NewDecoder(io.LimitReader(os.Stdin, 65537))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&request); err != nil {
			return fmt.Errorf("invalid administrator recovery input: %w", err)
		}
		if decoder.Decode(&extra) != io.EOF {
			return fmt.Errorf("recovery input must contain one JSON object")
		}
		op, err := engine.ReconcileApple(ctx, request)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(op)
	}
	if *reconcileStore {
		request, err := publisher.DecodeStoreReconciliationInput(os.Stdin)
		if err != nil {
			return err
		}
		op, err := engine.ReconcileStore(ctx, request)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(op)
	}
	if *reconcileGoogle {
		request, err := publisher.DecodeGoogleReconciliationInput(os.Stdin)
		if err != nil {
			return err
		}
		op, err := engine.ReconcileGoogle(ctx, request)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(op)
	}
	if *reconcileGoogleQuery {
		request, err := publisher.DecodeGoogleQueryRecoveryInput(os.Stdin)
		if err != nil {
			return err
		}
		query, err := engine.ReconcileGoogleQuery(ctx, request)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(query)
	}
	var request publisher.Request
	decoder = json.NewDecoder(io.LimitReader(os.Stdin, 65537))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&request); err != nil {
		return fmt.Errorf("invalid trusted publisher input: %w", err)
	}
	if decoder.Decode(&extra) != io.EOF {
		return fmt.Errorf("publisher input must contain one JSON object")
	}
	if err = publisherActionMode(*statusOnly, *continueOnly, request.Action); err != nil {
		return err
	}
	if *materialsOnly && request.Action != "materials" {
		return fmt.Errorf("material provider refuses other actions")
	}
	result, err := engine.Execute(ctx, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func publisherModes(statusOnly, continueOnly, reconcileApple, reconcileStore, reconcileGoogle, reconcileGoogleQuery, renew bool, inspect, inspectGoogleQuery, initKey string) error {
	active := 0
	for _, enabled := range []bool{statusOnly, continueOnly, reconcileApple, reconcileStore, reconcileGoogle, reconcileGoogleQuery, renew, inspect != "", inspectGoogleQuery != "", initKey != ""} {
		if enabled {
			active++
		}
	}
	if active > 1 {
		return fmt.Errorf("publisher status, continuation, recovery, inspection, key creation and renewal modes are mutually exclusive")
	}
	return nil
}

func publisherActionMode(statusOnly, continueOnly bool, action string) error {
	if statusOnly && continueOnly {
		return fmt.Errorf("publisher action modes are mutually exclusive")
	}
	if statusOnly && action != "status" {
		return fmt.Errorf("read-only status provider refuses mutation")
	}
	if continueOnly && action != "continue" {
		return fmt.Errorf("continuation provider refuses other actions")
	}
	return nil
}
