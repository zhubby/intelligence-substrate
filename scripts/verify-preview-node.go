//go:build ignore

// Real Docker deadline/lease validation with a previously built exact package.
// It never uses another node's state, containers, images, networks or volumes.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/previewnode"
	"github.com/intelligence-substrate/platform/internal/webpreview"
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func main() {
	root := flag.String("work-dir", "", "New absolute private node directory")
	artifact := flag.String("artifact", "", "Exact built preview package")
	digest := flag.String("digest", "", "Exact artifact SHA256")
	size := flag.Int64("size", 0, "Exact artifact bytes")
	relay := flag.String("relay", "", "Administrator relay executable")
	pin := flag.String("relay-sha256", "", "Approved relay SHA256")
	docker := flag.String("docker", "", "Absolute Docker CLI")
	host := flag.String("docker-host", "", "Local Docker Unix endpoint")
	arch := flag.String("arch", "arm64", "Linux architecture")
	flag.Parse()
	if *root == "" || !filepath.IsAbs(*root) {
		panic("new absolute node directory required")
	}
	must(os.Mkdir(*root, 0700))
	bundle, err := webpreview.Read(*artifact, *digest, *size)
	must(err)
	if bundle.Runtime() == nil {
		panic("isolated runtime package required")
	}
	driver, err := previewnode.NewDocker(previewnode.DockerConfig{StateDir: *root, RelayPath: *relay, RelaySHA256: *pin, Arch: *arch, DockerBinary: *docker, DockerHost: *host})
	must(err)
	engine, err := previewnode.Open(*root, driver, 2)
	must(err)
	report := p.M{"passed": false, "kind": "actual-docker-preview-deadline-and-lease", "startedAt": time.Now().UTC(), "externalPublication": false}
	save := func() {
		raw, _ := json.MarshalIndent(report, "", "  ")
		os.WriteFile(filepath.Join(*root, "result.json"), append(raw, '\n'), 0600)
	}
	defer save()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	bindings := []previewnode.Binding{}
	defer func() {
		for _, b := range bindings {
			engine.Stop(context.Background(), b)
		}
	}()
	ownerBytes, err := os.ReadFile(filepath.Join(*root, "owner"))
	must(err)
	owner := string(ownerBytes)
	shell := func(args ...string) string {
		cmd := exec.CommandContext(ctx, "rtk", append([]string{"proxy", *docker}, args...)...)
		out, err := cmd.CombinedOutput()
		must(err)
		return strings.TrimSpace(string(out))
	}
	binding := func(expires time.Time) previewnode.Binding {
		return previewnode.Binding{PreviewID: p.NewID("preview"), Binding: strings.Repeat("d", 64), ArtifactDigest: *digest, ExpiresAt: expires, Runtime: *bundle.Runtime()}
	}
	absolute := binding(time.Now().UTC().Add(12 * time.Second))
	bindings = append(bindings, absolute)
	_, err = engine.Ensure(ctx, absolute, bundle.Backend())
	must(err)
	container := "ispv-" + owner + "-" + absolute.PreviewID
	if shell("container", "inspect", "--format", "{{.State.Running}}", container) != "true" {
		panic("deadline container did not start")
	}
	// No node janitor or Reap runs until after the container has exited itself.
	fmt.Println("Observe absolute deadline without node maintenance")
	until := absolute.ExpiresAt.Add(8 * time.Second)
	for shell("container", "inspect", "--format", "{{.State.Running}}", container) == "true" {
		if time.Now().After(until) {
			panic("container exceeded absolute preview expiry")
		}
		time.Sleep(250 * time.Millisecond)
	}
	if shell("container", "inspect", "--format", "{{.State.ExitCode}}", container) != "0" {
		panic("deadline supervisor did not exit cleanly")
	}
	report["absoluteDeadlineEnforcedWithoutNodeMaintenance"] = true
	must(engine.Stop(ctx, absolute))
	leased := binding(time.Now().UTC().Add(time.Hour))
	bindings = append(bindings, leased)
	record, err := engine.Ensure(ctx, leased, bundle.Backend())
	must(err)
	result, err := engine.Request(ctx, leased, previewnode.Request{Method: "GET", Path: "/api/health"})
	must(err)
	if result.Status != http.StatusOK {
		panic("lease backend unhealthy")
	}
	fmt.Println("Observe real 60-second controller lease expiry")
	for time.Now().Before(record.LeaseUntil) {
		time.Sleep(500 * time.Millisecond)
	}
	if err = engine.Renew(ctx, leased); err == nil {
		panic("expired controller lease revived")
	}
	must(engine.Reap(ctx))
	if len(engine.List()) != 0 {
		panic("expired lease not reclaimed")
	}
	report["leaseElapsedSeconds"] = time.Since(record.LeaseUntil.Add(-time.Minute)).Seconds()
	report["expiredLeaseCannotRenew"] = true
	label := "org.intelligence-substrate.preview-node=" + owner
	if shell("container", "ls", "--all", "--filter", "label="+label, "--format", "{{.ID}}") != "" || shell("image", "ls", "--filter", "label="+label, "--format", "{{.ID}}") != "" {
		panic("owned deadline/lease resources remain")
	}
	report["ownedContainersAndImagesReclaimed"] = true
	report["passed"] = true
	report["finishedAt"] = time.Now().UTC()
	save()
	fmt.Println("Passed:", filepath.Join(*root, "result.json"))
}
