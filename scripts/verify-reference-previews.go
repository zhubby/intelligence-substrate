//go:build ignore

// Real Git -> Worker -> Flutter/Go package -> isolated node -> Chrome probe.
// Synthetic local approvals only; no model calls or public delivery evidence.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/previewnode"
	"github.com/intelligence-substrate/platform/internal/webpreview"
	"github.com/intelligence-substrate/platform/internal/worker"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// TLSIngress is emitted only by the local verification orchestrator. The
// external TLS listeners belong to Caddy; these two upstreams stay loopback.
type TLSIngress struct {
	ControlListen string `json:"controlListen"`
	PreviewListen string `json:"previewListen"`
	Origin        string `json:"origin"`
	PreviewOrigin string `json:"previewOrigin"`
	Pin           string `json:"pin"`
	CAFile        string `json:"caFile"`
}

func main() {
	output := flag.String("work-dir", "", "New private directory under work/")
	flutter := flag.String("flutter", "", "Absolute installed Flutter executable")
	goTool := flag.String("go", "", "Absolute installed Go executable")
	docker := flag.String("docker", "", "Absolute Docker executable")
	host := flag.String("docker-host", "", "Local Docker Unix endpoint")
	arch := flag.String("arch", "arm64", "Linux node architecture")
	wrapper := flag.String("tool-wrapper", "", "Optional administrator native environment Python wrapper")
	ingressConfig := flag.String("tls-ingress", "", "Optional private local HTTPS verification configuration")
	flag.Parse()
	var ingress *TLSIngress
	if *ingressConfig != "" {
		raw, err := os.ReadFile(*ingressConfig)
		must(err)
		ingress = &TLSIngress{}
		must(json.Unmarshal(raw, ingress))
		for _, address := range []string{ingress.ControlListen, ingress.PreviewListen} {
			host, port, err := net.SplitHostPort(address)
			if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || port == "" || port == "0" {
				panic("TLS verification upstream must be a fixed loopback address")
			}
		}
		for _, origin := range []string{ingress.Origin, ingress.PreviewOrigin} {
			u, err := url.Parse(origin)
			if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
				panic("TLS verification origin must be an explicit HTTPS origin")
			}
		}
		pin, err := base64.StdEncoding.DecodeString(ingress.Pin)
		if err != nil || len(pin) != sha256.Size || ingress.ControlListen == ingress.PreviewListen {
			panic("invalid TLS verification pin or upstream")
		}
		if info, err := os.Stat(ingress.CAFile); err != nil || !info.Mode().IsRegular() {
			panic("TLS verification requires its private CA file")
		}
	}
	repo, err := os.Getwd()
	must(err)
	root, err := filepath.Abs(*output)
	must(err)
	rel, err := filepath.Rel(filepath.Join(repo, "work"), root)
	must(err)
	if *output == "" || rel == "." || strings.HasPrefix(rel, "..") {
		panic("new work directory required")
	}
	must(os.Mkdir(root, 0700))
	report := p.M{"kind": "real-reference-fullstack-previews", "passed": false, "syntheticApprovals": true, "modelCalls": 0, "storePublished": false, "physicalDevice": false, "startedAt": time.Now().UTC(), "applications": []any{}}
	report["httpsIngress"] = ingress != nil
	report["workerTransport"] = "loopback-http"
	save := func() {
		raw, _ := json.MarshalIndent(report, "", "  ")
		must(os.WriteFile(filepath.Join(root, "result.json"), append(raw, '\n'), 0600))
	}
	save()
	defer save()
	shell := func(dir string, argv ...string) string {
		cmd := exec.Command("rtk", append([]string{"proxy"}, argv...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			panic(fmt.Sprintf("%s failed: %v: %s", argv[0], err, out))
		}
		return strings.TrimSpace(string(out))
	}
	native := func(argv ...string) []string {
		if *wrapper != "" {
			return append([]string{"python3", *wrapper, "rtk", "proxy"}, argv...)
		}
		return argv
	}
	relay := filepath.Join(root, "relay-linux")
	cmd := exec.Command("rtk", "proxy", *goTool, "build", "-trimpath", "-ldflags=-s -w", "-o", relay, "./cmd/preview-relay")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+*arch, "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		panic(fmt.Sprintf("relay build: %v: %s", err, out))
	}
	relayBytes, err := os.ReadFile(relay)
	must(err)
	sum := sha256.Sum256(relayBytes)
	relayDigest := hex.EncodeToString(sum[:])
	nodeBinary := filepath.Join(root, "preview-node")
	shell(repo, *goTool, "build", "-trimpath", "-o", nodeBinary, "./cmd/preview-node")
	nodeState := filepath.Join(root, "node")
	if ingress != nil {
		nodeState = filepath.Join(root, "n")
	}
	socket := filepath.Join(nodeState, "node.sock")
	nodeClient, err := previewnode.NewClient(socket)
	must(err)
	var node *exec.Cmd
	var nodeLog *os.File
	startNode := func(label string) {
		report["nodeStopped"] = false
		nodeLog, err = os.OpenFile(filepath.Join(root, label+".log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		must(err)
		// Start the owned executable directly so signals target the service,
		// without an intermediary CLI process swallowing or changing them.
		node = exec.Command(nodeBinary, "-state", nodeState, "-relay", relay, "-relay-sha256", relayDigest, "-arch", *arch, "-docker", *docker, "-docker-host", *host)
		node.Stdout = nodeLog
		node.Stderr = nodeLog
		must(node.Start())
		deadline := time.Now().Add(20 * time.Second)
		for {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, x := nodeClient.List(ctx)
			cancel()
			if x == nil {
				break
			}
			if time.Now().After(deadline) {
				panic("preview node startup failed; inspect private log")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	stopNode := func() {
		if node == nil {
			return
		}
		must(node.Process.Signal(os.Interrupt))
		done := make(chan error, 1)
		go func() { done <- node.Wait() }()
		select {
		case x := <-done:
			must(x)
		case <-time.After(15 * time.Second):
			node.Process.Kill()
			panic("node shutdown timed out")
		}
		nodeLog.Close()
		node = nil
		report["nodeStopped"] = true
	}
	startNode("node-initial")
	defer stopNode()
	var store *p.Store
	var svc *p.Service
	var h *httpapi.Handler
	var control, preview *http.Server
	var controlAddress, previewAddress, origin, previewOrigin string
	if ingress != nil {
		controlAddress, previewAddress = ingress.ControlListen, ingress.PreviewListen
	}
	var cancelMaintenance context.CancelFunc
	var maintenanceDone chan struct{}
	startControl := func() {
		report["httpServersStopped"] = false
		var err error
		store, err = p.Open(filepath.Join(root, "control.db"))
		must(err)
		svc = p.NewService(store)
		h = httpapi.New(svc, ingress != nil)
		c, err := net.Listen("tcp", func() string {
			if controlAddress != "" {
				return controlAddress
			}
			return "127.0.0.1:0"
		}())
		must(err)
		v, err := net.Listen("tcp", func() string {
			if previewAddress != "" {
				return previewAddress
			}
			return "127.0.0.1:0"
		}())
		must(err)
		controlAddress = c.Addr().String()
		previewAddress = v.Addr().String()
		origin = "http://" + controlAddress
		_, port, _ := net.SplitHostPort(previewAddress)
		previewOrigin = "http://localhost:" + port
		if ingress != nil {
			origin, previewOrigin = ingress.Origin, ingress.PreviewOrigin
		}
		h.PublicURL = origin
		must(h.ConfigurePreviews(previewAddress, previewOrigin, ingress == nil))
		must(h.ConfigurePreviewNode(socket))
		h.Static = filepath.Join(root, "empty-static")
		control = &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
		preview = &http.Server{Handler: h.PreviewGateway(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second}
		go control.Serve(c)
		go preview.Serve(v)
		var ctx context.Context
		ctx, cancelMaintenance = context.WithCancel(context.Background())
		maintenanceDone = make(chan struct{})
		handler := h
		go func() {
			defer close(maintenanceDone)
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					check, cancel := context.WithTimeout(ctx, 10*time.Second)
					_ = handler.ReconcilePreviewRuntimes(check)
					cancel()
				}
			}
		}()
	}
	stopControl := func() {
		if store == nil {
			return
		}
		cancelMaintenance()
		<-maintenanceDone
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		must(control.Shutdown(ctx))
		must(preview.Shutdown(ctx))
		must(store.Close())
		store = nil
		for _, address := range []string{controlAddress, previewAddress} {
			connection, x := net.DialTimeout("tcp", address, 200*time.Millisecond)
			if x == nil {
				connection.Close()
				panic("verification HTTP endpoint still open")
			}
		}
		report["httpServersStopped"] = true
		report["httpPortsClosed"] = true
	}
	startControl()
	defer stopControl()
	password := p.Secret()
	must(h.Auth.Bootstrap("reference-preview@example.test", password))
	identity, err := h.Auth.IdentityByEmail("reference-preview@example.test")
	must(err)
	owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	command := func(name string, in p.M) p.Resource { r, e := svc.Command(ctx, owner, name, in); must(e); return r }
	// Best-effort authorized cleanup also runs when a business/browser assertion fails.
	defer func() {
		cleanupService, cleanupHandler := svc, h
		if store == nil {
			// A failed offline probe may leave the normal store closed. Reopen
			// only this test's database and stop its previews before the node.
			cleanupStore, e := p.Open(filepath.Join(root, "control.db"))
			if e != nil {
				report["failureCleanupFailed"] = true
				return
			}
			defer cleanupStore.Close()
			cleanupService = p.NewService(cleanupStore)
			cleanupHandler = httpapi.New(cleanupService, ingress != nil)
			cleanupHandler.PublicURL = origin
			if e = cleanupHandler.ConfigurePreviews(previewAddress, previewOrigin, ingress == nil); e == nil {
				e = cleanupHandler.ConfigurePreviewNode(socket)
			}
			if e != nil {
				report["failureCleanupFailed"] = true
				return
			}
		}
		rows, e := cleanupService.Store.List("", "preview", "")
		if e != nil {
			return
		}
		for _, r := range rows {
			if r.Status == "active" {
				if _, e = cleanupService.Command(context.Background(), owner, "previews.stop", p.M{"id": r.ID, "expectedRevision": r.Revision, "reason": "Local verification ended", "key": "cleanup-" + r.ID}); e != nil {
					report["failureCleanupFailed"] = true
				}
			}
		}
		if e = cleanupHandler.ReconcilePreviewRuntimes(context.Background()); e != nil {
			report["failureCleanupFailed"] = true
		}
	}()
	apps := []any{}
	for _, mode := range []string{"feedback", "booking"} {
		fmt.Println("Generate actual current application:", mode)
		generated := filepath.Join(root, mode+"-generated")
		shell(repo, native("python3", "scripts/new-app.py", generated, "--name", "preview_"+mode, "--mode", mode, "--organization", "com.intelligencesubstrate.preview", "--create-platforms")...)
		project := command("projects.create", p.M{"title": "Real fullstack preview - " + mode})
		goal := command("goals.create", p.M{"projectId": project.ID, "title": "Business preview - " + mode, "outcome": "Review actual isolated backend business interactions", "targets": []string{"web"}, "criteria": []any{p.M{"id": "business", "text": "Actual preview business operation"}}})
		command("goals.approve", p.M{"id": goal.ID})
		repository := command("repositories.create", p.M{"projectId": project.ID, "title": "Current Flutter Go " + mode})
		source := filepath.Join(root, mode+"-source")
		shell(repo, "git", "clone", svc.RepoPath(repository), source)
		shell(source, "git", "checkout", "-b", "reference-preview")
		shell(repo, "python3", "-c", `import shutil,sys;shutil.copytree(sys.argv[1],sys.argv[2],dirs_exist_ok=True,ignore=shutil.ignore_patterns('.git','.dart_tool','build','.gradle','Pods','ephemeral','.symlinks','local.properties','.flutter-plugins-dependencies','Generated.xcconfig','flutter_export_environment.sh'))`, generated, source)
		shell(source, "git", "add", ".")
		shell(source, "git", "-c", "user.name=Reference preview", "-c", "user.email=preview@example.test", "commit", "-m", "Import current reference preview source")
		commit := shell(source, "git", "rev-parse", "HEAD")
		shell(source, "git", "push", "origin", "reference-preview")
		build := native("python3", "scripts/build-reference-client.py", "--application", ".", "--target", "web-preview", "--preview-arch", *arch, "--flutter", *flutter, "--go", *goTool, "--output", "artifacts/preview.tar.gz")
		argv := []any{"rtk", "proxy"}
		for _, v := range build {
			argv = append(argv, v)
		}
		plan := command("plans.propose", p.M{"goalId": goal.ID, "summary": "Build exact immutable frontend and backend for isolated preview", "tasks": []any{p.M{"id": "preview-build", "title": "Compile actual preview " + mode, "role": "developer", "kind": "build", "runtime": runtime.GOOS, "repositoryId": repository.ID, "commit": commit, "branch": "reference-preview", "command": argv, "outputPath": "artifacts/preview.tar.gz", "target": "web", "timeoutSeconds": 900}}})
		command("plans.approve", p.M{"id": plan.ID})
		nodeResource := command("workers.register", p.M{"projectId": project.ID, "title": "Actual preview build worker", "runtime": runtime.GOOS, "roles": []any{"developer"}, "capabilities": []any{"build"}})
		token, _, e := h.Auth.IssueToken(ctx, owner, p.M{"projectId": project.ID, "workerId": nodeResource.ID})
		must(e)
		client, e := worker.NewClient("http://"+controlAddress, token, true)
		must(e)
		runner := &worker.Runner{Client: client, Config: worker.Config{Driver: "native", AllowNative: true, StateDir: filepath.Join(root, mode+"-worker")}}
		fmt.Println("Real Worker build:", mode, commit)
		worked, e := runner.RunOnce(ctx)
		must(e)
		if !worked {
			panic("build not claimed")
		}
		runs, e := store.List(project.ID, "run", goal.ID)
		must(e)
		raw, _ := json.MarshalIndent(runs, "", "  ")
		must(os.WriteFile(filepath.Join(root, mode+"-runs.json"), raw, 0600))
		artifacts, e := store.List(project.ID, "artifact", goal.ID)
		must(e)
		if len(artifacts) != 1 {
			panic("exact preview build failed; inspect run diagnostics")
		}
		artifact := artifacts[0]
		must(svc.VerifyArtifact(artifact))
		bundle, e := webpreview.Read(svc.ArtifactPath(p.S(artifact.Data, "digest")), p.S(artifact.Data, "digest"), int64(p.N(artifact.Data, "size")))
		must(e)
		if bundle.Runtime() == nil || p.S(artifact.Data, "commit") != commit {
			panic("preview provenance mismatch")
		}
		preview := command("previews.create", p.M{"artifactId": artifact.ID, "expectedRequirementRevision": 1, "title": "实际应用预览 · " + mode, "reason": "Real source-to-browser verification", "key": "real-preview"})
		if p.S(preview.Data, "sourceKind") != "worker-build" {
			panic("preview lost worker provenance")
		}
		apps = append(apps, p.M{"mode": mode, "projectId": project.ID, "goalId": goal.ID, "repositoryId": repository.ID, "commit": commit, "artifact": artifact, "preview": preview, "runs": runs, "runtime": bundle.Runtime()})
		report["applications"] = apps
		save()
	}
	config := p.M{"repo": repo, "root": root, "origin": origin, "previewOrigin": previewOrigin, "email": "reference-preview@example.test", "password": password, "applications": apps}
	if ingress != nil {
		config["tls"] = ingress
	}
	configPath := filepath.Join(root, "browser-config.json")
	raw, _ := json.Marshal(config)
	must(os.WriteFile(configPath, raw, 0600))
	fmt.Println("Real Docker and Chrome business verification")
	shell(repo, "node", "scripts/verify-reference-previews.mjs", configPath, "healthy")
	instances, err := nodeClient.List(ctx)
	must(err)
	if len(instances) != 3 {
		panic("expected two applications and one independent-data preview")
	}
	ownerBytes, err := os.ReadFile(filepath.Join(nodeState, "owner"))
	must(err)
	label := "org.intelligence-substrate.preview-node=" + string(ownerBytes)
	ids := strings.Fields(shell(repo, *docker, "container", "ls", "--all", "--filter", "label="+label, "--format", "{{.ID}}"))
	if len(ids) != 3 {
		panic("unexpected preview container count")
	}
	inspection := shell(repo, append([]string{*docker, "container", "inspect"}, ids...)...)
	must(os.WriteFile(filepath.Join(root, "docker-inspect-private.json"), []byte(inspection), 0600))
	var inspected []map[string]any
	must(json.Unmarshal([]byte(inspection), &inspected))
	for _, instance := range inspected {
		hostConfig := p.Obj(instance["HostConfig"])
		configuration := p.Obj(instance["Config"])
		if p.S(hostConfig, "NetworkMode") != "none" || hostConfig["ReadonlyRootfs"] != true || hostConfig["Privileged"] == true || p.S(configuration, "User") != "10001:10001" || p.N(hostConfig, "Memory") != 256<<20 || p.N(hostConfig, "NanoCpus") != 500000000 || p.N(hostConfig, "PidsLimit") != 64 || !p.Has(p.Strings(hostConfig["CapDrop"]), "ALL") || !p.Has(p.Strings(hostConfig["SecurityOpt"]), "no-new-privileges") {
			panic("actual Docker isolation differs from preview contract")
		}
		if len(p.Strings(hostConfig["Binds"])) != 0 || len(p.Obj(hostConfig["PortBindings"])) != 0 || len(p.Obj(hostConfig["Tmpfs"])) != 2 {
			panic("unexpected host exposure or missing temporary storage")
		}
		mounts, _ := instance["Mounts"].([]any)
		for _, raw := range mounts {
			mount := p.Obj(raw)
			if p.S(mount, "Type") != "tmpfs" || (p.S(mount, "Destination") != "/data" && p.S(mount, "Destination") != "/tmp") {
				panic("unexpected preview mount")
			}
		}
		for _, value := range p.Strings(configuration["Env"]) {
			if !strings.HasPrefix(value, "APP_MODE=") && !strings.HasPrefix(value, "APP_LISTEN=") && !strings.HasPrefix(value, "APP_STATE_FILE=") && !strings.HasPrefix(value, "APP_SLOTS=") && !strings.HasPrefix(value, "SUBSTRATE_PREVIEW_EXPIRES_AT=") {
				panic("unexpected inherited preview environment")
			}
		}
	}
	report["actualContainerIsolationVerified"] = true
	report["containerCountBeforeRecovery"] = len(ids)
	fmt.Println("Restart node and control plane with existing containers and original grants")
	stopNode()
	startNode("node-recovered")
	stopControl()
	if ingress != nil {
		shell(repo, "node", "scripts/verify-reference-previews.mjs", configPath, "unavailable")
	}
	startControl()
	shell(repo, "node", "scripts/verify-reference-previews.mjs", configPath, "recovered")
	report["nodeAndControlRestartVerified"] = true
	afterIDs := strings.Fields(shell(repo, *docker, "container", "ls", "--all", "--filter", "label="+label, "--format", "{{.ID}}"))
	if len(afterIDs) != len(ids) {
		panic("restart replaced an instance")
	}
	for _, id := range ids {
		if !p.Has(afterIDs, id) {
			panic("restart changed original container identity")
		}
	}
	// All resources remain scoped to this verification's owner and exact bindings.
	rows, e := store.List("", "preview", "")
	must(e)
	for _, r := range rows {
		if r.Status == "active" {
			command("previews.stop", p.M{"id": r.ID, "expectedRevision": r.Revision, "reason": "Actual verification complete", "key": "end-" + r.ID})
		}
	}
	must(h.ReconcilePreviewRuntimes(ctx))
	shell(repo, "node", "scripts/verify-reference-previews.mjs", configPath, "stopped")
	instances, err = nodeClient.List(ctx)
	must(err)
	if len(instances) != 0 {
		panic("preview runtime cleanup incomplete")
	}
	if shell(repo, *docker, "container", "ls", "--all", "--filter", "label="+label, "--format", "{{.ID}}") != "" || shell(repo, *docker, "image", "ls", "--filter", "label="+label, "--format", "{{.ID}}") != "" {
		panic("owned Docker resources remain")
	}
	report["ownedContainersAndImagesReclaimed"] = true
	must(svc.VerifyIntegrity(ctx))
	for _, item := range apps {
		app := p.Obj(item)
		for _, kind := range []string{"evidence", "release", "measurement"} {
			rows, x := store.List(p.S(app, "projectId"), kind, p.S(app, "goalId"))
			must(x)
			if len(rows) != 0 {
				panic("development preview fabricated formal quality or delivery evidence")
			}
		}
	}
	report["formalQualityOrReleaseEvidenceCreated"] = false
	report["relaySHA256"] = relayDigest
	report["passed"] = true
	report["finishedAt"] = time.Now().UTC()
	save()
	fmt.Println("Passed:", filepath.Join(root, "result.json"))
}
