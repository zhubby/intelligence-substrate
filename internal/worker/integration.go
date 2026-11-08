package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"unicode/utf8"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/source"
)

type integrationWorkspace struct {
	id, digest string
	workspace  string
	parents    []string
	conflicts  []string
	versions   map[string]inputVersion
}
type inputVersion struct {
	root    string
	entries map[string]source.Entry
}

func (r *Runner) prepareSourceIntegration(ctx context.Context, run p.Resource) (*integrationWorkspace, error) {
	prepared, e := r.Client.Command(ctx, "sources.prepare", p.M{"runId": run.ID, "leaseToken": p.S(run.Data, "leaseToken")})
	if e != nil {
		return nil, e
	}
	binding := p.Obj(p.Obj(run.Data["task"])["sourceBinding"])
	state := &integrationWorkspace{id: p.S(prepared.Data, "integrationId"), digest: p.S(prepared.Data, "integrationResolutionDigest"), workspace: p.S(prepared.Data, "integrationWorkspaceCommit"),
		parents: p.Strings(binding["integrationParents"]), conflicts: p.Strings(prepared.Data["integrationConflicts"]), versions: map[string]inputVersion{}}
	if prepared.ID != run.ID || state.id == "" || len(state.digest) != 64 || !p.CommitPattern.MatchString(state.workspace) || len(state.parents) == 0 {
		return nil, fmt.Errorf("invalid integration preparation response")
	}
	return state, nil
}

func (s *integrationWorkspace) context() p.M {
	return p.M{"id": s.id, "workspaceCommit": s.workspace, "parents": s.parents, "conflicts": s.conflicts, "resolutionDigest": s.digest}
}

// Fixed input versions stay in controller-owned directories, outside tools.
func (r *Runner) inputVersion(ctx context.Context, state *sourceWorkspace, repo, commit string) (inputVersion, error) {
	if state == nil || state.integration == nil || !p.Has(state.integration.parents, commit) {
		return inputVersion{}, fmt.Errorf("input commit is not selected by this integration")
	}
	if version, found := state.integration.versions[commit]; found {
		return version, nil
	}
	root, e := os.MkdirTemp(state.private, "input-")
	if e != nil {
		return inputVersion{}, e
	}
	response, e := r.Client.request(ctx, "GET", "repositories/"+repo+"/archive?commit="+commit, nil)
	if e != nil {
		os.RemoveAll(root)
		return inputVersion{}, e
	}
	entries, _, e := source.Extract(response.Body, root)
	response.Body.Close()
	if e != nil {
		os.RemoveAll(root)
		return inputVersion{}, e
	}
	version := inputVersion{root: root, entries: map[string]source.Entry{}}
	for _, entry := range entries {
		version.entries[entry.Name] = entry
	}
	state.integration.versions[commit] = version
	return version, nil
}

func (r *Runner) integrationInput(ctx context.Context, run p.Resource, state *sourceWorkspace, root, repo string, action p.M) (string, error) {
	commit := p.S(action, "commit")
	name, _ := action["path"].(string)
	version, e := r.inputVersion(ctx, state, repo, commit)
	if e != nil {
		return "", e
	}
	input, e := source.Path(version.root, name)
	if e != nil {
		return "", e
	}
	entry, exists := version.entries[name]
	if p.S(action, "action") == "read_input" {
		result := p.M{"commit": commit, "path": name, "deleted": !exists}
		if exists {
			if entry.Size > 64000 {
				return "", fmt.Errorf("input file exceeds 64 KB reading limit; take_input can select its exact bytes")
			}
			body, e := os.ReadFile(input)
			if e != nil {
				return "", e
			}
			result["executable"] = entry.Executable
			if utf8.Valid(body) && !bytes.ContainsRune(body, 0) {
				result["content"] = string(body)
			} else {
				result["encoding"], result["content"] = "base64", base64.StdEncoding.EncodeToString(body)
			}
		}
		body, _ := json.Marshal(result)
		return string(body), nil
	}
	dest, e := source.Path(root, name)
	if e != nil {
		return "", e
	}
	if !exists {
		info, err := os.Lstat(dest)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil && !info.IsDir() {
			if e = state.delete(root, name); e != nil {
				return "", e
			}
		}
	} else {
		if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
			return "", e
		}
		body, e := os.ReadFile(input)
		if e != nil {
			return "", e
		}
		mode := os.FileMode(0600)
		if entry.Executable {
			mode |= 0100
		}
		if e = os.WriteFile(dest, body, mode); e != nil {
			return "", e
		}
		if e = os.Chmod(dest, mode); e != nil {
			return "", e
		}
		state.tracked[name], state.modes[name] = entry.Executable, entry.Executable
	}
	if p.Has(state.integration.conflicts, name) {
		if e = r.resolveIntegration(ctx, run, state, root, []string{name}); e != nil {
			return "", e
		}
	}
	return "selected exact input file version or deletion", nil
}

func (r *Runner) resolveIntegration(ctx context.Context, run p.Resource, state *sourceWorkspace, root string, paths []string) error {
	if state == nil || state.integration == nil || len(paths) == 0 || len(paths) > 100 {
		return fmt.Errorf("resolve requires 1–100 conflict paths in an integration task")
	}
	decisions, seen := []any{}, map[string]bool{}
	for _, name := range paths {
		if !p.Has(state.integration.conflicts, name) || seen[name] {
			return fmt.Errorf("resolve paths must be distinct prepared conflict paths")
		}
		seen[name] = true
		path, e := source.Path(root, name)
		if e != nil {
			return e
		}
		info, e := os.Lstat(path)
		if os.IsNotExist(e) || e == nil && info.IsDir() {
			decisions = append(decisions, p.M{"path": name, "deleted": true})
			continue
		}
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || info.Size() > source.MaxBytes {
			return fmt.Errorf("conflict resolution requires a regular bounded file")
		}
		file, e := os.Open(path)
		if e != nil {
			return e
		}
		hash := sha256.New()
		_, e = io.Copy(hash, io.LimitReader(file, source.MaxBytes+1))
		file.Close()
		if e != nil {
			return e
		}
		decisions = append(decisions, p.M{"path": name, "digest": fmt.Sprintf("%x", hash.Sum(nil)), "executable": state.executable(name, info.Mode()&0100 != 0, runtime.GOOS == "windows")})
	}
	updated, e := r.Client.Command(ctx, "sources.resolve", p.M{"runId": run.ID, "leaseToken": p.S(run.Data, "leaseToken"), "resolutions": decisions})
	if e == nil {
		state.integration.digest = p.S(updated.Data, "integrationResolutionDigest")
	}
	return e
}
