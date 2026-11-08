package webpreview

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func bundleTar(t *testing.T, entries []*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, h := range entries {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeRegA {
			if _, err := tw.Write(bytes.Repeat([]byte{'x'}, int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func readBundle(t *testing.T, body []byte) (*Bundle, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "web.tar")
	if err := os.WriteFile(p, body, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return Read(p, hex.EncodeToString(sum[:]), int64(len(body)))
}

func TestWebBundleServesOnlyExactFilesFromVerifiedTarAndGzip(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		body := bundleTar(t, []*tar.Header{{Name: "index.html", Typeflag: tar.TypeReg, Size: 1}, {Name: "./assets/", Typeflag: tar.TypeDir}, {Name: "./assets/app.js", Typeflag: tar.TypeReg, Size: 3}})
		if compressed {
			var b bytes.Buffer
			z := gzip.NewWriter(&b)
			_, _ = z.Write(body)
			if err := z.Close(); err != nil {
				t.Fatal(err)
			}
			body = b.Bytes()
		}
		b, err := readBundle(t, body)
		if err != nil {
			t.Fatal(err)
		}
		if file, ok := b.File("assets/app.js"); !ok || string(file) != "xxx" || b.FileCount != 2 {
			t.Fatal("lost exact asset", file, ok, b.FileCount)
		}
		for _, name := range []string{"../index.html", "/index.html", "assets/../index.html", "missing", "assets/", ".git/config"} {
			if _, ok := b.File(name); ok {
				t.Fatal("unsafe asset lookup", name)
			}
		}
	}
}

func TestWebBundleRejectsUnsafeOrAmbiguousArchive(t *testing.T) {
	for _, h := range []*tar.Header{
		{Name: "../outside", Typeflag: tar.TypeReg, Size: 1}, {Name: "/absolute", Typeflag: tar.TypeReg, Size: 1},
		{Name: "assets/../../outside", Typeflag: tar.TypeReg, Size: 1}, {Name: "assets\\outside", Typeflag: tar.TypeReg, Size: 1},
		{Name: ".env", Typeflag: tar.TypeReg, Size: 1}, {Name: "assets/.hidden", Typeflag: tar.TypeReg, Size: 1},
		{Name: "assets/a%2fb", Typeflag: tar.TypeReg, Size: 1}, {Name: "assets/a?b", Typeflag: tar.TypeReg, Size: 1},
		{Name: "linked", Typeflag: tar.TypeSymlink, Linkname: "index.html"}, {Name: "hard", Typeflag: tar.TypeLink, Linkname: "index.html"},
		{Name: "device", Typeflag: tar.TypeChar}, {Name: "index.html", Typeflag: tar.TypeReg, Size: 1},
	} {
		t.Run(h.Name+string(h.Typeflag), func(t *testing.T) {
			body := bundleTar(t, []*tar.Header{{Name: "index.html", Typeflag: tar.TypeReg, Size: 1}, h})
			if _, err := readBundle(t, body); err == nil {
				t.Fatal("unsafe archive accepted", h)
			}
		})
	}
	body := bundleTar(t, []*tar.Header{{Name: "app.js", Typeflag: tar.TypeReg, Size: 1}})
	if _, err := readBundle(t, body); err == nil {
		t.Fatal("missing entry document accepted")
	}
	body = bundleTar(t, []*tar.Header{{Name: "index.html", Typeflag: tar.TypeReg, Size: 1}})
	if _, err := readBundle(t, append(body, []byte("unverified trailing bytes")...)); err == nil {
		t.Fatal("tar trailer accepted")
	}
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	_, _ = z.Write(body)
	_ = z.Close()
	if _, err := readBundle(t, append(b.Bytes(), 1)); err == nil {
		t.Fatal("gzip trailer accepted")
	}
	corrupt := append([]byte{}, b.Bytes()...)
	corrupt[len(corrupt)-5] ^= 0xff
	if _, err := readBundle(t, corrupt); err == nil {
		t.Fatal("invalid gzip checksum accepted")
	}
}

func TestWebBundleRejectsDigestSizeAndExpansionAbuse(t *testing.T) {
	body := bundleTar(t, []*tar.Header{{Name: "index.html", Typeflag: tar.TypeReg, Size: 1}})
	path := filepath.Join(t.TempDir(), "web.tar")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	if _, err := Read(path, digest, int64(len(body)+1)); err == nil {
		t.Fatal("size mismatch accepted")
	}
	if _, err := Read(path, hex.EncodeToString(make([]byte, 32)), int64(len(body))); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	if _, err := Read(path, digest, 65<<20); err == nil {
		t.Fatal("oversized metadata accepted")
	}
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	_, _ = z.Write(bytes.Repeat([]byte{0}, 65<<20))
	_ = z.Close()
	if _, err := readBundle(t, compressed.Bytes()); err == nil {
		t.Fatal("unbounded expansion accepted")
	}
	entries := []*tar.Header{{Name: "index.html", Typeflag: tar.TypeReg, Size: 1}}
	for i := 0; i < 10000; i++ {
		entries = append(entries, &tar.Header{Name: filepath.ToSlash(filepath.Join("assets", string(rune(0x4e00+i)))), Typeflag: tar.TypeReg})
	}
	if _, err := readBundle(t, bundleTar(t, entries)); err == nil {
		t.Fatal("unbounded entry count accepted")
	}
}
