package previewnode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPreviewDockerRejectsUnverifiedRelayBeforeContactingEngine(t *testing.T) {
	dir := privateDir(t)
	relay := filepath.Join(dir, "relay")
	os.WriteFile(relay, []byte("not an ELF"), 0700)
	_, err := NewDocker(DockerConfig{StateDir: dir, RelayPath: relay, RelaySHA256: "0000000000000000000000000000000000000000000000000000000000000000", Arch: "arm64", DockerBinary: "/nonexistent/docker", DockerHost: "unix:///nonexistent/docker.sock"})
	if err == nil {
		t.Fatal("unverified relay accepted")
	}
	d := &Docker{config: DockerConfig{Arch: "arm64", DockerBinary: "/nonexistent/docker"}}
	b, bin := fixtureBinding(t)
	if err = d.Start(context.Background(), b, bin); err == nil {
		t.Fatal("non-ELF backend executed")
	}
}
