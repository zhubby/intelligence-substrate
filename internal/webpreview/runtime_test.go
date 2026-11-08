package webpreview

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"testing"
)

func runtimeArchive(t *testing.T, mutate func(map[string][]byte, map[string]any)) []byte {
	t.Helper()
	binary := []byte("exact backend bytes")
	sum := sha256.Sum256(binary)
	m := map[string]any{"schemaVersion": 1, "profile": "flutter-go-v1", "mode": "feedback", "goarch": "arm64", "backendSHA256": hex.EncodeToString(sum[:]), "backendSize": len(binary)}
	files := map[string][]byte{"index.html": []byte("<main>preview</main>"), "substrate-preview/backend": binary}
	if mutate != nil {
		mutate(files, m)
	}
	body, _ := json.Marshal(m)
	files["substrate-preview/manifest.json"] = body
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for n, v := range files {
		if e := tw.WriteHeader(&tar.Header{Name: n, Size: int64(len(v)), Mode: 0644}); e != nil {
			t.Fatal(e)
		}
		if _, e := tw.Write(v); e != nil {
			t.Fatal(e)
		}
	}
	if e := tw.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}

func TestPreviewRuntimePrivateMembersAreNeverStaticFiles(t *testing.T) {
	b, e := readBundle(t, runtimeArchive(t, nil))
	if e != nil {
		t.Fatal(e)
	}
	for _, n := range []string{"substrate-preview/backend", "substrate-preview/manifest.json"} {
		if _, ok := b.File(n); ok {
			t.Fatalf("private executable metadata served: %s", n)
		}
	}
	if _, ok := b.File("index.html"); !ok {
		t.Fatal("lost public page")
	}
}
func TestPreviewRuntimeRejectsAmbiguousOrMismatchedManifests(t *testing.T) {
	cases := map[string]func(map[string][]byte, map[string]any){
		"digest":          func(f map[string][]byte, m map[string]any) { m["backendSHA256"] = hex.EncodeToString(make([]byte, 32)) },
		"size":            func(f map[string][]byte, m map[string]any) { m["backendSize"] = 1 },
		"unknown-field":   func(f map[string][]byte, m map[string]any) { m["backendURL"] = "http://127.0.0.1/" },
		"arch":            func(f map[string][]byte, m map[string]any) { m["goarch"] = "other" },
		"version":         func(f map[string][]byte, m map[string]any) { m["schemaVersion"] = 2 },
		"mode":            func(f map[string][]byte, m map[string]any) { m["mode"] = "other" },
		"missing-backend": func(f map[string][]byte, m map[string]any) { delete(f, "substrate-preview/backend") },
		"hidden-extra":    func(f map[string][]byte, m map[string]any) { f["substrate-preview/credential"] = []byte("private") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, e := readBundle(t, runtimeArchive(t, mutate)); e == nil {
				t.Fatal("unsafe runtime manifest accepted")
			}
		})
	}
}

func TestPreviewRuntimeRejectsDuplicateManifestKeys(t *testing.T) {
	body := runtimeArchive(t, nil)
	// Rebuild the same archive with an ambiguous duplicate mode, independently
	// of the parser under test; a malicious publisher cannot choose the winner.
	var out bytes.Buffer
	tr := tar.NewReader(bytes.NewReader(body))
	tw := tar.NewWriter(&out)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		v, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if h.Name == "substrate-preview/manifest.json" {
			v = append([]byte(`{"mode":"booking",`), v[1:]...)
			h.Size = int64(len(v))
		}
		if err = tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		tw.Write(v)
	}
	tw.Close()
	if _, err := readBundle(t, out.Bytes()); err == nil {
		t.Fatal("duplicate manifest key accepted")
	}
}
