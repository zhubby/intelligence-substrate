package previewnode

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type DockerConfig struct{ StateDir, RelayPath, RelaySHA256, Arch, DockerBinary, DockerHost string }
type Docker struct {
	config DockerConfig
	owner  string
	relay  []byte
}
type boundedOutput struct {
	bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, fmt.Errorf("command output limit exceeded")
	}
	return b.Buffer.Write(p)
}
func staticELF(body []byte, arch string) error {
	f, err := elf.NewFile(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("preview executable must be a static 64-bit Linux ELF")
	}
	defer f.Close()
	expected := elf.EM_NONE
	if arch == "arm64" {
		expected = elf.EM_AARCH64
	} else if arch == "amd64" {
		expected = elf.EM_X86_64
	}
	if f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || f.Machine != expected || expected == elf.EM_NONE || f.Type != elf.ET_EXEC {
		return fmt.Errorf("preview executable architecture or type differs")
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return fmt.Errorf("dynamic preview executables are unsupported")
		}
	}
	return nil
}
func NewDocker(c DockerConfig) (*Docker, error) {
	if !filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.RelayPath) || !filepath.IsAbs(c.DockerBinary) || !strings.HasPrefix(c.DockerHost, "unix:///") || !hashPattern.MatchString(c.RelaySHA256) {
		return nil, fmt.Errorf("explicit private state, pinned relay, Docker binary and local engine required")
	}
	if err := os.MkdirAll(c.StateDir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(c.StateDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("preview node directory must be private 0700")
	}
	info, err = os.Lstat(c.RelayPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return nil, fmt.Errorf("regular bounded relay required")
	}
	relay, err := os.ReadFile(c.RelayPath)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(relay)
	if hex.EncodeToString(sum[:]) != c.RelaySHA256 {
		return nil, fmt.Errorf("trusted preview relay digest mismatch")
	}
	if err = staticELF(relay, c.Arch); err != nil {
		return nil, err
	}
	ownerPath := filepath.Join(c.StateDir, "owner")
	owner, err := os.ReadFile(ownerPath)
	if os.IsNotExist(err) {
		raw := make([]byte, 16)
		if _, err = rand.Read(raw); err != nil {
			return nil, err
		}
		owner = []byte(hex.EncodeToString(raw))
		var f *os.File
		f, err = os.OpenFile(ownerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			_, err = f.Write(owner)
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
		}
	}
	if err != nil || len(owner) != 32 || strings.Trim(string(owner), "0123456789abcdef") != "" {
		return nil, fmt.Errorf("preview resource owner unavailable")
	}
	d := &Docker{config: c, owner: string(owner), relay: relay}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	body, err := d.run(ctx, nil, "info", "--format", "{{.OSType}}/{{.Architecture}}")
	want := "linux/x86_64"
	if c.Arch == "arm64" {
		want = "linux/aarch64"
	}
	if err != nil || strings.TrimSpace(string(body)) != want {
		return nil, fmt.Errorf("Docker Linux architecture must match relay and backend")
	}
	return d, nil
}
func (d *Docker) run(ctx context.Context, input io.Reader, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, d.config.DockerBinary, args...)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "DOCKER_HOST=" + d.config.DockerHost, "DOCKER_CONFIG=" + filepath.Join(d.config.StateDir, "docker-config")}
	command.Stdin = input
	out := &boundedOutput{limit: 2 << 20}
	stderr := &boundedOutput{limit: 64 << 10}
	command.Stdout = out
	command.Stderr = stderr
	command.WaitDelay = 3 * time.Second
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("preview Docker operation failed; inspect node state")
	}
	return out.Bytes(), nil
}
func (d *Docker) name(b Binding) string { return "ispv-" + d.owner + "-" + b.PreviewID }
func (d *Docker) tag(b Binding) string  { return "ispv/" + d.owner + ":" + b.PreviewID }
func (d *Docker) labels(b Binding) map[string]string {
	return map[string]string{"org.intelligence-substrate.preview-node": d.owner, "org.intelligence-substrate.preview-id": b.PreviewID, "org.intelligence-substrate.preview-binding": b.Binding, "org.intelligence-substrate.preview-artifact": b.ArtifactDigest}
}
func (d *Docker) owned(labels map[string]string, b Binding) bool {
	for k, v := range d.labels(b) {
		if labels[k] != v {
			return false
		}
	}
	return true
}
func (d *Docker) find(ctx context.Context, kind string, b Binding) (string, error) {
	filter := "reference=" + d.tag(b)
	format := "{{.ID}}"
	if kind == "container" {
		filter = "name=^/" + d.name(b) + "$"
	}
	args := []string{kind, "ls", "--no-trunc", "--filter", filter, "--format", format}
	if kind == "container" {
		args = append(args, "--all")
	}
	body, err := d.run(ctx, nil, args...)
	if err != nil {
		return "", err
	}
	lines := strings.Fields(string(body))
	if len(lines) == 0 {
		return "", nil
	}
	if len(lines) != 1 {
		return "", conflict()
	}
	body, err = d.run(ctx, nil, kind, "inspect", lines[0])
	if err != nil {
		return "", err
	}
	var values []struct {
		ID     string `json:"Id"`
		Config struct{ Labels map[string]string }
	}
	if json.Unmarshal(body, &values) != nil || len(values) != 1 || !d.owned(values[0].Config.Labels, b) {
		return "", fmt.Errorf("preview resource collision; existing resource preserved")
	}
	return values[0].ID, nil
}
func (d *Docker) Start(ctx context.Context, b Binding, binary []byte) error {
	if !b.live() || !b.verify(binary) || b.Runtime.Goarch != d.config.Arch {
		return conflict()
	}
	if err := staticELF(binary, d.config.Arch); err != nil {
		return err
	}
	for _, kind := range []string{"container", "image"} {
		id, err := d.find(ctx, kind, b)
		if err != nil {
			return err
		}
		if id != "" {
			return fmt.Errorf("preview start already has resources; reconcile")
		}
	}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	for _, file := range []struct {
		name string
		body []byte
	}{{"substrate-backend", binary}, {"substrate-relay", d.relay}} {
		if err := tw.WriteHeader(&tar.Header{Name: file.name, Mode: 0555, Size: int64(len(file.body)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := tw.Write(file.body); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	args := []string{"image", "import", "--platform", "linux/" + d.config.Arch, "--change", `ENTRYPOINT ["/substrate-relay","serve"]`, "--change", "USER 10001:10001"}
	for k, v := range d.labels(b) {
		args = append(args, "--change", "LABEL "+k+"="+v)
	}
	args = append(args, "-", d.tag(b))
	if _, err := d.run(ctx, &archive, args...); err != nil {
		return err
	}
	image, err := d.find(ctx, "image", b)
	if err != nil || image == "" {
		return conflict()
	}
	args = []string{"container", "create", "--pull=never", "--name", d.name(b), "--network", "none", "--read-only", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "64", "--memory", "256m", "--cpus", "0.5", "--restart", "no", "--log-driver", "local", "--log-opt", "max-size=1m", "--log-opt", "max-file=2", "--tmpfs", "/data:rw,nosuid,nodev,noexec,size=16m,uid=10001,gid=10001,mode=0700", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=4m,uid=10001,gid=10001,mode=0700", "--env", "APP_MODE=" + b.Runtime.Mode, "--env", "APP_LISTEN=127.0.0.1:8081", "--env", "APP_STATE_FILE=/data/state.json", "--env", "SUBSTRATE_PREVIEW_EXPIRES_AT=" + b.ExpiresAt.UTC().Format(time.RFC3339Nano)}
	if b.Runtime.Mode == "booking" {
		args = append(args, "--env", `APP_SLOTS=[{"id":"preview-slot","startsAt":"2026-12-01T09:00:00+08:00","reserved":false}]`)
	}
	for k, v := range d.labels(b) {
		args = append(args, "--label", k+"="+v)
	}
	args = append(args, image)
	if _, err = d.run(ctx, nil, args...); err != nil {
		return err
	}
	if _, err = d.run(ctx, nil, "container", "start", d.name(b)); err != nil {
		return err
	}
	readyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		if r, x := d.Request(readyCtx, b, Request{Method: "GET", Path: "/api/health"}); x == nil && r.Status == 200 {
			return nil
		}
		select {
		case <-readyCtx.Done():
			return fmt.Errorf("preview backend did not become ready")
		case <-time.After(250 * time.Millisecond):
		}
	}
}
func (d *Docker) Request(ctx context.Context, b Binding, q Request) (Response, error) {
	if !q.Valid(b.Runtime.Mode) {
		return Response{}, conflict()
	}
	id, err := d.find(ctx, "container", b)
	if err != nil || id == "" {
		return Response{}, conflict()
	}
	raw, err := json.Marshal(q)
	if err != nil {
		return Response{}, err
	}
	body, err := d.run(ctx, bytes.NewReader(raw), "container", "exec", "-i", id, "/substrate-relay")
	if err != nil {
		return Response{}, err
	}
	var result Response
	if json.Unmarshal(body, &result) != nil || !result.Valid() {
		return Response{}, conflict()
	}
	return result, nil
}
func (d *Docker) Stop(ctx context.Context, b Binding) error {
	container, err := d.find(ctx, "container", b)
	if err != nil {
		return err
	}
	image, err := d.find(ctx, "image", b)
	if err != nil {
		return err
	}
	if container != "" {
		if _, err = d.run(ctx, nil, "container", "stop", "--time", "3", container); err != nil {
			return err
		}
		if _, err = d.run(ctx, nil, "container", "rm", container); err != nil {
			return err
		}
	}
	if image != "" {
		if _, err = d.run(ctx, nil, "image", "rm", image); err != nil {
			return err
		}
	}
	return nil
}
