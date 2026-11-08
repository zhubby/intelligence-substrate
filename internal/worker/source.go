package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/source"
)

type sourceWorkspace struct {
	private         string
	tracked         map[string]bool
	modes           map[string]bool
	integration     *integrationWorkspace
	inputDigest     string
	commitTried     bool
	commitRejected  bool
	baseVersion     *inputVersion
	checkpointTried bool
}

func newSourceWorkspace(entries []source.Entry) (*sourceWorkspace, error) {
	private, e := os.MkdirTemp("", "is-source-controller-")
	if e != nil {
		return nil, e
	}
	state := &sourceWorkspace{private: private, tracked: map[string]bool{}, modes: map[string]bool{}}
	for _, entry := range entries {
		state.tracked[entry.Name] = entry.Executable
	}
	if _, e = state.git(context.Background(), "", "init", "--bare", filepath.Join(private, "selection.git")); e != nil {
		state.close()
		return nil, e
	}
	return state, nil
}
func (s *sourceWorkspace) close() { _ = os.RemoveAll(s.private) }
func (s *sourceWorkspace) executable(name string, native, windows bool) bool {
	if !windows {
		return native
	}
	if explicit, ok := s.modes[name]; ok {
		return explicit
	}
	return s.tracked[name]
}
func (s *sourceWorkspace) git(ctx context.Context, root string, args ...string) ([]byte, error) {
	timeout, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	prefix := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.excludesFile=" + os.DevNull}
	if root != "" {
		prefix = append(prefix, "--git-dir", filepath.Join(s.private, "selection.git"), "--work-tree", root)
	}
	cmd := exec.CommandContext(timeout, "git", append(prefix, args...)...)
	cmd.Env = []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	for _, key := range []string{"PATH", "TMPDIR", "TMP", "TEMP", "SystemRoot", "SYSTEMROOT", "LANG", "LC_ALL"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	output := &sourceListBuffer{}
	stderr := &cappedLog{}
	cmd.Stdout = output
	cmd.Stderr = stderr
	if e := cmd.Run(); e != nil {
		return nil, fmt.Errorf("source selection failed: %w: %s", e, stderr.String())
	}
	return output.Bytes(), nil
}

type sourceListBuffer struct{ bytes.Buffer }

func (b *sourceListBuffer) Write(data []byte) (int, error) {
	if b.Len()+len(data) > source.MaxEntries*1026 {
		return 0, fmt.Errorf("too many source paths")
	}
	return b.Buffer.Write(data)
}

func (s *sourceWorkspace) selectEntries(ctx context.Context, root string) ([]source.Entry, error) {
	selected := map[string]bool{}
	for name := range s.tracked {
		selected[name] = true
	}
	raw, e := s.git(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if e != nil {
		return nil, e
	}
	for _, name := range strings.Split(string(raw), "\x00") {
		if name != "" {
			selected[name] = true
		}
	}
	entries := []source.Entry{}
	var total int64
	for name := range selected {
		// Downloaded quality artifacts and Git metadata belong to the controller,
		// never to the produced application's source snapshot.
		reserved := false
		for _, part := range strings.Split(name, "/") {
			if strings.EqualFold(part, ".git") || strings.EqualFold(part, ".intelligence-artifact") {
				reserved = true
				break
			}
		}
		if reserved {
			continue
		}
		path, e := source.Path(root, name)
		if e != nil {
			return nil, e
		}
		info, e := os.Lstat(path)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if info.IsDir() {
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("source must contain regular files")
		}
		total += info.Size()
		if total > source.MaxBytes || len(entries) >= source.MaxEntries {
			return nil, fmt.Errorf("source snapshot exceeds limits")
		}
		entries = append(entries, source.Entry{Name: name, Size: info.Size(), Executable: s.executable(name, info.Mode()&0100 != 0, runtime.GOOS == "windows")})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}
func (s *sourceWorkspace) write(root string, action p.M) error {
	name := p.S(action, "path")
	path, e := source.Path(root, name)
	if e != nil {
		return e
	}
	content, ok := action["content"].(string)
	if !ok || len(content) > 1<<20 {
		return fmt.Errorf("write requires text no larger than 1 MiB; tools may generate larger binary files")
	}
	var executable bool
	_, explicit := action["executable"]
	if explicit {
		var ok bool
		executable, ok = action["executable"].(bool)
		if !ok {
			return fmt.Errorf("executable must be boolean")
		}
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	if e = os.WriteFile(path, []byte(content), 0600); e != nil {
		return e
	}
	if explicit {
		bits := os.FileMode(0600)
		if executable {
			bits |= 0100
		}
		if e = os.Chmod(path, bits); e != nil {
			return e
		}
		s.modes[name] = executable
	}
	return nil
}
func (s *sourceWorkspace) delete(root, name string) error {
	path, e := source.Path(root, name)
	if e != nil {
		return e
	}
	info, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("delete accepts one regular file")
	}
	if e = os.Remove(path); e != nil {
		return e
	}
	delete(s.modes, name)
	return nil
}
func (r *Runner) commitSource(ctx context.Context, run p.Resource, state *sourceWorkspace, root, repo, base, branch, expected string) (p.Resource, error) {
	state.commitTried = true
	state.commitRejected = false
	entries, e := state.selectEntries(ctx, root)
	if e != nil {
		return p.Resource{}, e
	}
	file, e := os.CreateTemp(state.private, "snapshot-*.tar")
	if e != nil {
		return p.Resource{}, e
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}()
	if e = source.Write(file, root, entries); e != nil {
		return p.Resource{}, e
	}
	if e = file.Sync(); e != nil {
		return p.Resource{}, e
	}
	return r.submitSourceArchive(ctx, run, state, file, repo, base, branch, expected)
}

func (r *Runner) submitSourceArchive(ctx context.Context, run p.Resource, state *sourceWorkspace, file io.ReadSeeker, repo, base, branch, expected string) (p.Resource, error) {
	state.commitTried = true
	state.commitRejected = false
	meta := p.M{"key": run.ID, "runId": run.ID, "leaseToken": p.S(run.Data, "leaseToken"), "branch": branch, "baseCommit": base, "expectedHead": expected, "message": run.Title}
	if state.integration != nil {
		meta["integrationId"], meta["resolutionDigest"] = state.integration.id, state.integration.digest
	}
	raw, e := json.Marshal(meta)
	if e != nil {
		return p.Resource{}, e
	}
	header := base64.StdEncoding.EncodeToString(raw)
	// A bounded exact replay also recovers a response lost after the Git CAS.
	for attempt := 0; attempt < 3; attempt++ {
		if _, e = file.Seek(0, io.SeekStart); e != nil {
			return p.Resource{}, e
		}
		response, err := r.Client.requestHeaders(ctx, "POST", "repositories/"+repo+"/snapshot", struct{ io.Reader }{file}, map[string]string{"Content-Type": "application/x-tar", "X-IS-Source": header})
		e = err
		if e == nil {
			var op p.Resource
			e = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&op)
			response.Body.Close()
			if e == nil {
				return op, nil
			}
		}
		var api *APIError
		if ctx.Err() != nil || errors.As(e, &api) && api.Status < 500 {
			state.commitRejected = ctx.Err() == nil && api != nil && api.Status == 409 && api.Code == "source_write_scope_rejected"
			break
		}
	}
	return p.Resource{}, e
}
