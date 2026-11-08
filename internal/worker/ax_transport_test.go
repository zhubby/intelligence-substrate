package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/testutil/axfixture"
)

func TestWorkerAXSecurePersistsAuthenticatedBackendBeforeMutation(t *testing.T) {
	for _, mode := range []string{"normal", "nonzero", "partial", "cleanup-denied"} {
		t.Run(mode, func(t *testing.T) {
			legacyMode := "normal"
			if mode == "nonzero" {
				legacyMode = mode
			}
			f, runner, _, cliDir := sandboxRunner(t, legacyMode)
			remote, e := axfixture.New(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			defer remote.Close()
			remote.SetMode(mode)
			body, _ := json.Marshal(remote.Config())
			runner.Config.AX.AllowLegacyCLI = false
			if e = json.Unmarshal(body, &runner.Config.AX); e != nil {
				t.Fatal(e)
			}
			before := 0
			remote.BeforeMutation = func(ctx context.Context, space, name string) error {
				items, e := f.svc.Store.List(f.project.ID, "sandbox", f.goal.ID)
				if e != nil {
					return e
				}
				if len(items) != 1 || items[0].Status != "prepared" || p.S(items[0].Data, "name") != name || p.S(p.Obj(items[0].Data["backend"]), "atespace") != space || p.S(p.Obj(p.Obj(items[0].Data["backend"])["transport"]), "version") != "mtls-v1" {
					return fmt.Errorf("missing durable authenticated ownership before remote mutation")
				}
				before++
				return nil
			}
			if worked, e := runner.RunOnce(context.Background()); e != nil || !worked {
				t.Fatal(worked, e)
			}
			items, e := f.svc.Store.List(f.project.ID, "sandbox", f.goal.ID)
			if e != nil || len(items) != 1 {
				t.Fatal(len(items), e)
			}
			want := "stopped"
			if mode == "cleanup-denied" || mode == "partial" {
				want = "unresolved"
			}
			if items[0].Status != want {
				t.Fatal(mode, items[0].Status, items[0].Data)
			}
			runs, _ := f.svc.Store.List(f.project.ID, "run", f.goal.ID)
			if len(runs) != 1 || mode == "normal" && runs[0].Status != "succeeded" || mode != "normal" && runs[0].Status == "succeeded" {
				t.Fatal(mode, runs)
			}
			if before != 2 {
				t.Fatal("both mutating requests did not observe persisted ownership", before)
			}
			if _, e := os.Stat(filepath.Join(cliDir, "calls.jsonl")); !os.IsNotExist(e) {
				t.Fatal("secure execution invoked legacy CLI", e)
			}
			if mode == "cleanup-denied" {
				if _, e = f.svc.Command(context.Background(), f.owner, "tasks.retry", p.M{"id": p.S(runs[0].Data, "taskId")}); e == nil {
					t.Fatal("unknown authenticated guest did not fence replacement")
				}
			}
			public, _ := json.Marshal(items[0])
			for _, sensitive := range []string{"PRIVATE KEY", "CERTIFICATE-----", remote.Control["clientKeyFile"].(string), remote.Guest["clientKeyFile"].(string), "clientKeyFile", "clientCertFile", "caFile"} {
				if strings.Contains(string(public), sensitive) {
					t.Fatal("credential material in durable record", sensitive)
				}
			}
			if e = f.svc.VerifyIntegrity(context.Background()); e != nil {
				t.Fatal(e)
			}
			if evidence := os.Getenv("IS_AX_TRANSPORT_EVIDENCE_DIR"); evidence != "" {
				root := filepath.Join(evidence, "worker-"+mode)
				if e = os.Mkdir(root, 0700); e != nil {
					t.Fatal(e)
				}
				if e = f.svc.Backup(context.Background(), filepath.Join(root, "backup.tar.gz")); e != nil {
					t.Fatal(e)
				}
				if e = p.Restore(context.Background(), filepath.Join(root, "backup.tar.gz"), filepath.Join(root, "restored"), ""); e != nil {
					t.Fatal(e)
				}
				proof := p.M{"passed": true, "mode": mode, "sandboxId": items[0].ID, "runId": runs[0].ID, "sandboxStatus": items[0].Status, "runStatus": runs[0].Status, "mutationsObservingPreparedOwnership": before, "calls": remote.Calls(), "realAXCluster": false, "realSubstrate": false}
				body, _ = json.MarshalIndent(proof, "", "  ")
				if e = os.WriteFile(filepath.Join(root, "proof.json"), append(body, '\n'), 0600); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}

func TestWorkerAXSecureAuthenticationFailureCreatesNoSandbox(t *testing.T) {
	f, runner, _, cliDir := sandboxRunner(t, "normal")
	remote, e := axfixture.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer remote.Close()
	config := remote.Config()
	secure := config["secure"].(map[string]any)
	secure["guest"].(map[string]any)["serverSPKI"] = strings.Repeat("f", 64)
	body, _ := json.Marshal(config)
	runner.Config.AX.AllowLegacyCLI = false
	if e = json.Unmarshal(body, &runner.Config.AX); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Call the actual execution boundary with a current, scoped run; connection
	// rejection must precede ownership and any external application RPC.
	run, e := runner.Client.Command(context.Background(), "tasks.claim", p.M{})
	if e != nil {
		t.Fatal(e)
	}
	if exit, e := runner.execRun(ctx, run, t.TempDir(), []string{"/bin/true"}, io.Discard); e == nil || exit == 0 {
		t.Fatal(exit, e)
	}
	items, _ := f.svc.Store.List(f.project.ID, "sandbox", f.goal.ID)
	if len(items) != 0 || len(remote.Calls()) != 0 {
		t.Fatal("authentication failure created external intent", items, remote.Calls())
	}
	if _, e := os.Stat(filepath.Join(cliDir, "calls.jsonl")); !os.IsNotExist(e) {
		t.Fatal("TLS error fell back to CLI", e)
	}
}
