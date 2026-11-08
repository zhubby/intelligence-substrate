package runtimehost

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/intelligence-substrate/platform/internal/publisher"
)

type preparedImage struct{ ID, Archive string }

func prepareArchive(r publisher.Request, c Config) (preparedImage, func(), error) {
	noop := func() {}
	f, e := os.Open(r.ArtifactPath)
	if e != nil {
		return preparedImage{}, noop, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() != r.ArtifactSize || r.ArtifactSize < 1 || r.ArtifactSize > 512<<20 {
		return preparedImage{}, noop, fmt.Errorf("approved image archive size invalid")
	}
	dir, e := os.MkdirTemp("", "is-runtime-image-")
	if e != nil {
		return preparedImage{}, noop, e
	}
	cleanup := func() { os.RemoveAll(dir) }
	fail := func(e error) (preparedImage, func(), error) { cleanup(); return preparedImage{}, noop, e }
	// Snapshot approved bytes before parsing; a mutable source path cannot change
	// the image after the trusted controller has checked its digest.
	snapshot, e := os.OpenFile(filepath.Join(dir, "approved"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		return fail(e)
	}
	defer snapshot.Close()
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(snapshot, h), io.LimitReader(f, r.ArtifactSize+1))
	if e != nil || n != r.ArtifactSize || hex.EncodeToString(h.Sum(nil)) != r.ArtifactDigest {
		return fail(fmt.Errorf("approved image archive SHA256 mismatch"))
	}
	snapshot.Seek(0, 0)
	br := bufio.NewReader(snapshot)
	var source io.Reader = br
	magic, _ := br.Peek(2)
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, e := gzip.NewReader(br)
		if e != nil {
			return fail(e)
		}
		defer gz.Close()
		source = gz
	}
	tr := tar.NewReader(source)
	files := map[string]string{}
	seen := map[string]bool{}
	var total int64
	count := 0
	for {
		entry, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return fail(e)
		}
		name := strings.TrimSuffix(entry.Name, "/")
		if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\:\x00") || seen[name] {
			return fail(fmt.Errorf("unsafe or duplicate image archive entry"))
		}
		seen[name] = true
		count++
		total += entry.Size
		if count > 20000 || total > 512<<20 || entry.Size < 0 {
			return fail(fmt.Errorf("expanded image archive exceeds limits"))
		}
		if entry.Typeflag == tar.TypeDir {
			continue
		}
		if entry.Typeflag != tar.TypeReg && entry.Typeflag != tar.TypeRegA {
			return fail(fmt.Errorf("image archive links and special files are unsupported"))
		}
		dest := filepath.Join(dir, fmt.Sprintf("entry-%d", count))
		out, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return fail(e)
		}
		_, e = io.Copy(out, tr)
		closeErr := out.Close()
		if e != nil {
			return fail(e)
		}
		if closeErr != nil {
			return fail(closeErr)
		}
		files[name] = dest
	}
	var manifest []struct {
		Config   string   `json:"Config"`
		Layers   []string `json:"Layers"`
		RepoTags []string `json:"RepoTags"`
	}
	mf, e := os.ReadFile(files["manifest.json"])
	if e != nil || len(mf) > 1<<20 || json.Unmarshal(mf, &manifest) != nil || len(manifest) != 1 {
		return fail(fmt.Errorf("exactly one Docker-save image required"))
	}
	m := manifest[0]
	config, e := os.ReadFile(files[m.Config])
	if e != nil || len(config) > 4<<20 {
		return fail(fmt.Errorf("image config missing or too large"))
	}
	var image struct {
		OS     string `json:"os"`
		Config struct {
			Volumes map[string]any    `json:"Volumes"`
			Labels  map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if json.Unmarshal(config, &image) != nil || image.OS != "linux" || image.Config.Labels["org.intelligence-substrate.data-contract"] != c.DataContract {
		return fail(fmt.Errorf("Linux image with the approved unchanged data contract required"))
	}
	for volume := range image.Config.Volumes {
		if volume != "/data" || !c.PersistentData {
			return fail(fmt.Errorf("image declares an unmanaged anonymous volume"))
		}
	}
	// Import only the selected image's files and an untagged manifest. Docker
	// archives may otherwise replace arbitrary tags through manifest/index data.
	canonical := filepath.Join(dir, "image.tar")
	out, e := os.OpenFile(canonical, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return fail(e)
	}
	tw := tar.NewWriter(out)
	m.RepoTags = nil
	manifest[0] = m
	cleanManifest, _ := json.Marshal(manifest)
	write := func(name string, reader io.Reader, size int64) error {
		if e := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: size}); e != nil {
			return e
		}
		_, e := io.Copy(tw, reader)
		return e
	}
	written := map[string]bool{}
	for _, name := range append([]string{m.Config}, m.Layers...) {
		if written[name] {
			continue
		}
		written[name] = true
		file, e := os.Open(files[name])
		if e != nil {
			tw.Close()
			out.Close()
			return fail(fmt.Errorf("referenced image layer missing"))
		}
		stat, e := file.Stat()
		if e == nil {
			e = write(name, file, stat.Size())
		}
		file.Close()
		if e != nil {
			tw.Close()
			out.Close()
			return fail(e)
		}
	}
	e = write("manifest.json", strings.NewReader(string(cleanManifest)), int64(len(cleanManifest)))
	if e == nil {
		e = tw.Close()
	}
	if closeErr := out.Close(); e == nil {
		e = closeErr
	}
	if e != nil {
		return fail(e)
	}
	digest := sha256.Sum256(config)
	return preparedImage{ID: "sha256:" + hex.EncodeToString(digest[:]), Archive: canonical}, cleanup, nil
}
