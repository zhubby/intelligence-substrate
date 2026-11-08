package platform

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Backups omit sessions, API tokens and OIDC states. Restored deployments require
// fresh authorization, while immutable resources, identities and audit survive.
type snapshot struct {
	Version int                   `json:"version"`
	Tables  map[string][][]string `json:"tables"`
}

var backupTables = map[string][]string{
	"resources":   {"id", "kind", "project_id", "goal_id", "status", "revision", "body"},
	"identities":  {"id", "email", "body"},
	"audit":       {"id", "project_id", "at", "body"},
	"idempotency": {"scope", "key", "digest", "resource_id"},
}

func (s *Service) Backup(ctx context.Context, destination string) error {
	file, e := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	success := false
	defer func() {
		file.Close()
		if !success {
			os.Remove(destination)
		}
	}()
	e = s.Store.Update(ctx, func(tx *Tx) error {
		state := snapshot{Version: 1, Tables: map[string][][]string{}}
		for name, columns := range backupTables {
			rows, e := tx.tx.Query(`SELECT ` + strings.Join(columns, ",") + ` FROM ` + name)
			if e != nil {
				return e
			}
			values := [][]string{}
			for rows.Next() {
				row := make([]string, len(columns))
				targets := make([]any, len(columns))
				for i := range row {
					targets[i] = &row[i]
				}
				if e = rows.Scan(targets...); e != nil {
					rows.Close()
					return e
				}
				values = append(values, row)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			state.Tables[name] = values
		}
		gz := gzip.NewWriter(file)
		tw := tar.NewWriter(gz)
		b, e := json.Marshal(state)
		if e != nil {
			return e
		}
		if e = tw.WriteHeader(&tar.Header{Name: "state.json", Mode: 0600, Size: int64(len(b)), Typeflag: tar.TypeReg}); e != nil {
			return e
		}
		if _, e = tw.Write(b); e != nil {
			return e
		}
		for _, directory := range []string{"repos", "artifacts"} {
			root := filepath.Join(s.Root, directory)
			if _, e = os.Stat(root); os.IsNotExist(e) {
				continue
			}
			if e = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
					return fmt.Errorf("backup refuses special file %s", path)
				}
				relative, e := filepath.Rel(s.Root, path)
				if e != nil {
					return e
				}
				h, e := tar.FileInfoHeader(info, "")
				if e != nil {
					return e
				}
				h.Name = filepath.ToSlash(relative)
				if e = tw.WriteHeader(h); e != nil {
					return e
				}
				if info.IsDir() {
					return nil
				}
				source, e := os.Open(path)
				if e != nil {
					return e
				}
				defer source.Close()
				_, e = io.Copy(tw, source)
				return e
			}); e != nil {
				return e
			}
		}
		if e = tw.Close(); e != nil {
			return e
		}
		return gz.Close()
	})
	if e != nil {
		return e
	}
	if e = file.Sync(); e != nil {
		return e
	}
	success = true
	return nil
}
func Restore(ctx context.Context, archive, destination, databaseURL string) error {
	// Never replace existing user data. A failed restore removes only the new
	// directory this operation created; an external database must be empty.
	if e := os.Mkdir(destination, 0700); e != nil {
		return fmt.Errorf("restore requires a new destination: %w", e)
	}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(destination)
		}
	}()
	file, e := os.Open(archive)
	if e != nil {
		return e
	}
	defer file.Close()
	gz, e := gzip.NewReader(file)
	if e != nil {
		return e
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, 10<<30))
	state := snapshot{}
	seen := map[string]bool{}
	count := 0
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		count++
		if count > 100000 || h.Size > 1<<30 {
			return fmt.Errorf("backup exceeds restore limits")
		}
		if seen[h.Name] {
			return fmt.Errorf("duplicate backup entry")
		}
		seen[h.Name] = true
		if h.Name != "state.json" && !strings.HasPrefix(h.Name, "repos/") && !strings.HasPrefix(h.Name, "artifacts/") && h.Name != "repos" && h.Name != "artifacts" {
			return fmt.Errorf("unexpected backup entry %q", h.Name)
		}
		name, e := SafePath(destination, h.Name)
		if e != nil {
			return e
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if e = os.MkdirAll(name, 0700); e != nil {
				return e
			}
		case tar.TypeReg:
			if h.Name == "state.json" {
				if h.Size > 128<<20 {
					return fmt.Errorf("snapshot exceeds 128 MiB")
				}
				if e = json.NewDecoder(tr).Decode(&state); e != nil {
					return e
				}
				continue
			}
			if e = os.MkdirAll(filepath.Dir(name), 0700); e != nil {
				return e
			}
			out, e := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600|os.FileMode(h.Mode)&0100)
			if e != nil {
				return e
			}
			_, e = io.Copy(out, tr)
			ce := out.Close()
			if e != nil {
				return e
			}
			if ce != nil {
				return ce
			}
		default:
			return fmt.Errorf("backup contains an unsupported link or device")
		}
	}
	if state.Version != 1 || len(state.Tables) != len(backupTables) {
		return fmt.Errorf("backup snapshot version or tables are invalid")
	}
	dsn := databaseURL
	if dsn == "" {
		dsn = filepath.Join(destination, "platform.db")
	}
	store, e := Open(dsn)
	if e != nil {
		return e
	}
	defer store.Close()
	if e = store.Update(ctx, func(tx *Tx) error {
		for _, table := range []string{"resources", "identities", "audit", "credentials", "idempotency"} {
			var n int
			if e := tx.tx.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); e != nil {
				return e
			}
			if n > 0 {
				return fmt.Errorf("restore requires an empty database")
			}
		}
		for name, columns := range backupTables {
			rows, ok := state.Tables[name]
			if !ok {
				return fmt.Errorf("missing table %s", name)
			}
			placeholders := []string{}
			for i := range columns {
				placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
			}
			query := `INSERT INTO ` + name + ` (` + strings.Join(columns, ",") + `) VALUES (` + strings.Join(placeholders, ",") + `)`
			for _, row := range rows {
				if len(row) != len(columns) {
					return fmt.Errorf("invalid snapshot row")
				}
				values := make([]any, len(row))
				for i, v := range row {
					values[i] = v
				}
				if _, e := tx.tx.Exec(query, values...); e != nil {
					return e
				}
			}
		}
		return (&Service{Store: store, Root: destination}).verifyIntegrity(ctx, tx)
	}); e != nil {
		return e
	}
	success = true
	return nil
}
func (s *Service) VerifyIntegrity(ctx context.Context) error {
	return s.Store.Update(ctx, func(tx *Tx) error { return s.verifyIntegrity(ctx, tx) })
}
func (s *Service) verifyIntegrity(ctx context.Context, tx *Tx) error {
	audits, e := tx.Audits("")
	if e != nil {
		return e
	}
	prev := ""
	for _, a := range audits {
		hash := a.Hash
		a.Hash = ""
		if a.Previous != prev || Hash(a) != hash {
			return fmt.Errorf("audit chain broken at %s", a.ID)
		}
		prev = hash
	}
	artifacts, e := tx.List("", "artifact", "")
	if e != nil {
		return e
	}
	for _, a := range artifacts {
		if e = s.VerifyArtifact(a); e != nil {
			return fmt.Errorf("artifact %s: %w", a.ID, e)
		}
	}
	checkpoints, e := tx.List("", "workspace_checkpoint", "")
	if e != nil {
		return e
	}
	for _, checkpoint := range checkpoints {
		if e = s.VerifyArtifact(checkpoint); e != nil {
			return fmt.Errorf("workspace checkpoint %s: %w", checkpoint.ID, e)
		}
	}
	progressCheckpoints, e := tx.List("", "agent_checkpoint", "")
	if e != nil {
		return e
	}
	for _, checkpoint := range progressCheckpoints {
		if e = s.VerifyArtifact(checkpoint); e != nil {
			return fmt.Errorf("agent checkpoint %s: %w", checkpoint.ID, e)
		}
	}
	blueprints, e := tx.List("", "blueprint", "")
	if e != nil {
		return e
	}
	for _, blueprint := range blueprints {
		if e = s.VerifyArtifact(blueprint); e != nil {
			return fmt.Errorf("blueprint %s: %w", blueprint.ID, e)
		}
	}
	repos, e := tx.List("", "repository", "")
	if e != nil {
		return e
	}
	for _, repo := range repos {
		if _, e = s.Git(repo, "fsck", "--no-dangling"); e != nil {
			return fmt.Errorf("repository %s: %w", repo.ID, e)
		}
	}
	return ctx.Err()
}
