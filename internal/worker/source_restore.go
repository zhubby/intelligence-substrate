package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/source"
)

// Tools cannot choose a version or provide bytes. The only source is the exact
// claim binding, kept outside the executable workspace and fetched read-only.
func (r *Runner) restoreBase(ctx context.Context, run p.Resource, state *sourceWorkspace, root, repo, base string, action p.M) error {
	for key := range action {
		if key != "action" && key != "path" {
			return fmt.Errorf("restore_base accepts only path, never a version or content")
		}
	}
	task := p.Obj(run.Data["task"])
	binding := p.Obj(task["sourceBinding"])
	if state == nil || p.S(task, "kind") != "code" || p.S(binding, "repositoryId") != repo || p.S(binding, "commit") != base || !p.CommitPattern.MatchString(base) {
		return fmt.Errorf("restore_base requires the exact approved code source binding")
	}
	name := p.S(action, "path")
	dest, err := source.Path(root, name)
	if err != nil {
		return err
	}
	if state.baseVersion == nil {
		cache, err := os.MkdirTemp(state.private, "base-")
		if err != nil {
			return err
		}
		response, err := r.Client.request(ctx, "GET", "repositories/"+repo+"/archive?commit="+base, nil)
		if err != nil {
			_ = os.RemoveAll(cache)
			return err
		}
		entries, _, err := source.Extract(response.Body, cache)
		_ = response.Body.Close()
		if err != nil {
			_ = os.RemoveAll(cache)
			return err
		}
		version := &inputVersion{root: cache, entries: map[string]source.Entry{}}
		for _, entry := range entries {
			version.entries[entry.Name] = entry
		}
		state.baseVersion = version
	}
	version := state.baseVersion
	input, err := source.Path(version.root, name)
	if err != nil {
		return err
	}
	entry, exists := version.entries[name]
	if !exists {
		// On a case-insensitive filesystem an alias can resolve to a real
		// baseline entry. It must never be interpreted as baseline absence.
		if _, err := os.Lstat(input); err == nil || !os.IsNotExist(err) {
			return fmt.Errorf("restore_base requires one exact file path, not a directory or alias")
		}
		if err = state.delete(root, name); err != nil {
			return err
		}
		delete(state.tracked, name)
	} else {
		if info, err := os.Lstat(dest); err == nil && !info.Mode().IsRegular() || err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("restore_base only replaces one regular file")
		}
		body, err := os.ReadFile(input)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(dest), ".is-restore-")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		mode := os.FileMode(0600)
		if entry.Executable {
			mode |= 0100
		}
		_, err = file.Write(body)
		if err == nil {
			err = file.Chmod(mode)
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err = os.Rename(file.Name(), dest); err != nil {
			return err
		}
		state.tracked[name], state.modes[name] = entry.Executable, entry.Executable
	}
	if state.integration != nil && p.Has(state.integration.conflicts, name) {
		return r.resolveIntegration(ctx, run, state, root, []string{name})
	}
	return nil
}
