package integrations

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

// RunCommand uses the opt-in AX guest transport under an administrator-controlled
// atespace. No platform/model credential is copied to the guest. AX phase alone
// is never interpreted as command success.
func (a AX) RunCommand(ctx context.Context, dir string, argv []string, output io.Writer) (int, error) {
	exit, _, e := a.RunOwnedCommand(ctx, p.NewID("sandbox"), dir, argv, output)
	return exit, e
}

// The identity is persisted by the trusted controller before any apply. A true
// stopped result means either no external request occurred, or cleanup and both
// exact absence observations succeeded; command success is independent.
func (a AX) RunOwnedCommand(ctx context.Context, id, dir string, argv []string, output io.Writer) (exit int, stopped bool, err error) {
	stopped = true
	if !regexp.MustCompile(`^sandbox_[a-f0-9]{24}$`).MatchString(id) {
		return 1, true, fmt.Errorf("AX command needs an owned sandbox identity")
	}
	prepared, _, close, e := a.Open(ctx)
	if e != nil {
		return 1, true, e
	}
	defer close()
	a = prepared
	exit, err = a.runOwnedCommand(ctx, id, dir, argv, output, &stopped)
	return exit, stopped, err
}

func (a AX) runOwnedCommand(ctx context.Context, id, dir string, argv []string, output io.Writer, stopped *bool) (exit int, err error) {
	var source bytes.Buffer
	tw := tar.NewWriter(&source)
	e := filepath.Walk(dir, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		relative, e := filepath.Rel(dir, path)
		if e != nil {
			return e
		}
		if relative == "." {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("AX source refuses links and special files")
		}
		if int64(source.Len())+info.Size() > 64<<20 {
			return fmt.Errorf("AX source exceeds 64 MiB")
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
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		_, e = io.Copy(tw, f)
		f.Close()
		return e
	})
	if e != nil {
		return 1, e
	}
	if e = tw.Close(); e != nil {
		return 1, e
	}
	run := p.Resource{ID: id, Data: p.M{"task": p.M{}}}
	name, e := a.Submit(ctx, run, "")
	if name != "" {
		*stopped = false
		defer func() {
			clean, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if e := a.cleanupCommand(clean, name); e != nil {
				fmt.Fprintf(output, "AX cleanup requires attention: %s\n", name)
				err = errors.Join(err, fmt.Errorf("AX cleanup %s: %w", name, e))
				if exit == 0 {
					exit = 1
				}
			} else {
				*stopped = true
			}
		}()
	}
	if e != nil {
		return 1, e
	}
	ready, stop := context.WithTimeout(ctx, 3*time.Minute)
	defer stop()
	for {
		description, e := a.Describe(ready, name)
		if e != nil {
			return 1, e
		}
		var task struct {
			Status struct {
				Phase      string `yaml:"phase"`
				Conditions []struct {
					Reason  string `yaml:"reason"`
					Message string `yaml:"message"`
				} `yaml:"conditions"`
			} `yaml:"status"`
		}
		if e = yaml.Unmarshal([]byte(description), &task); e != nil {
			return 1, fmt.Errorf("AX task status invalid: %w", e)
		}
		if task.Status.Phase == "Failed" || task.Status.Phase == "Terminating" {
			var reasons []string
			for _, condition := range task.Status.Conditions {
				reasons = append(reasons, condition.Reason+": "+condition.Message)
			}
			return 1, fmt.Errorf("AX sandbox %s is %s: %s", name, task.Status.Phase, strings.Join(reasons, "; "))
		}
		if task.Status.Phase == "Running" {
			var out bytes.Buffer
			exit, e := a.Exec(ready, name, []string{"/bin/true"}, &out)
			if e == nil && exit == 0 {
				break
			}
		}
		select {
		case <-ready.Done():
			return 1, fmt.Errorf("AX sandbox did not become ready: %s: %w", name, ready.Err())
		case <-time.After(2 * time.Second):
		}
	}
	remote := func(script string, out io.Writer) (int, error) {
		return a.Exec(ctx, name, []string{"/bin/sh", "-c", script}, out)
	}
	if exit, e := remote("mkdir -p /workspace/job && : > /workspace/input.b64", output); e != nil || exit != 0 {
		return exit, e
	}
	encoded := base64.StdEncoding.EncodeToString(source.Bytes())
	for offset := 0; offset < len(encoded); offset += 48000 {
		end := offset + 48000
		if end > len(encoded) {
			end = len(encoded)
		}
		if exit, e := remote("printf '%s' "+shellQuote(encoded[offset:end])+" >> /workspace/input.b64", output); e != nil || exit != 0 {
			return exit, e
		}
	}
	// Host uid/gid values (for example macOS 501:50) are not identities in the
	// guest. Its restricted root lacks CAP_CHOWN, so preserve content/mode only.
	if exit, e := remote("base64 -d < /workspace/input.b64 > /workspace/input.tar && tar --no-same-owner -xf /workspace/input.tar -C /workspace/job", output); e != nil || exit != 0 {
		return exit, e
	}
	quoted := []string{}
	for _, arg := range argv {
		quoted = append(quoted, shellQuote(arg))
	}
	script := "cd /workspace/job && env -i PATH=/usr/local/bin:/usr/bin:/bin HOME=/tmp IS_ARTIFACT_PATH=/workspace/job/.intelligence-artifact " + strings.Join(quoted, " ")
	exit, executeErr := remote(script, output)
	if executeErr != nil {
		return exit, executeErr
	}
	// Transfer results separately from command logs so they cannot be mistaken for
	// a reported exit code. Every extracted path is checked again on the host.
	var result boundedBuffer
	copyExit, e := remote("cd /workspace/job && tar -cf /workspace/result.tar . && base64 < /workspace/result.tar", &result)
	if e != nil || copyExit != 0 {
		return 1, fmt.Errorf("AX result transfer failed")
	}
	raw, e := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(result.String()), ""))
	if e != nil {
		return 1, fmt.Errorf("AX result encoding invalid")
	}
	staging, e := os.MkdirTemp(filepath.Dir(dir), ".is-ax-result-")
	if e != nil {
		return 1, e
	}
	defer os.RemoveAll(staging)
	tr := tar.NewReader(bytes.NewReader(raw))
	total := int64(0)
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return 1, e
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		total += h.Size
		if total > 64<<20 {
			return 1, fmt.Errorf("AX output exceeds 64 MiB")
		}
		path, e := p.SafePath(staging, h.Name)
		if e != nil {
			return 1, e
		}
		if h.Typeflag == tar.TypeDir {
			if e = os.MkdirAll(path, 0700); e != nil {
				return 1, e
			}
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return 1, fmt.Errorf("AX output contains unsupported links")
		}
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return 1, e
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600|os.FileMode(h.Mode)&0100)
		if e != nil {
			return 1, e
		}
		_, e = io.Copy(f, tr)
		f.Close()
		if e != nil {
			return 1, e
		}
	}
	if e = os.RemoveAll(dir); e != nil {
		return 1, e
	}
	if e = os.Rename(staging, dir); e != nil {
		return 1, e
	}
	return exit, nil
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 90<<20 {
		return 0, fmt.Errorf("AX transfer exceeds limit")
	}
	return b.Buffer.Write(data)
}
