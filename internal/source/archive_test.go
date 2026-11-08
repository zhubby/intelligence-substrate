package source

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rawArchive(t *testing.T, headers []*tar.Header) []byte {
	t.Helper()
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	for _, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = io.CopyN(tw, strings.NewReader(strings.Repeat("x", int(h.Size))), h.Size)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestSnapshotPreservesBinaryBytesAndExecutable(t *testing.T) {
	root := t.TempDir()
	data := append([]byte{0, 255, 0, 128}, bytes.Repeat([]byte("raw\r\n"), 250000)...)
	if err := os.WriteFile(filepath.Join(root, "asset.bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "build.sh"), []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	entries := []Entry{{Name: "asset.bin", Size: int64(len(data))}, {Name: "build.sh", Size: 10, Executable: true}}
	var archive bytes.Buffer
	if err := Write(&archive, root, entries); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	got, digest, err := Extract(bytes.NewReader(archive.Bytes()), destination)
	if err != nil || len(got) != 2 || len(digest) != 64 {
		t.Fatalf("snapshot result: %v %v %s", err, got, digest)
	}
	read, _ := os.ReadFile(filepath.Join(destination, "asset.bin"))
	if !bytes.Equal(read, data) || !got[1].Executable {
		t.Fatal("snapshot changed raw bytes or executable mode")
	}
	var second bytes.Buffer
	if err = Write(&second, destination, got); err != nil || !bytes.Equal(second.Bytes(), archive.Bytes()) {
		t.Fatal("identical snapshots have different encoding", err)
	}
}

func TestSnapshotRejectsUnsafeAndAmbiguousPaths(t *testing.T) {
	for _, names := range [][]string{
		{"../escape"}, {"/absolute"}, {".git/config"}, {".GiT/config"}, {".git./config"},
		{"x\\y"}, {"file:stream"}, {"dir/CON.txt"}, {"file", "./file"},
		{"Readme", "README"}, {"dir/a", "Dir/b"}, {"caf\u00e9.txt", "cafe\u0301.txt"},
	} {
		t.Run(strings.Join(names, ","), func(t *testing.T) {
			headers := []*tar.Header{}
			for _, name := range names {
				headers = append(headers, &tar.Header{Name: name, Size: 1, Mode: 0600, Typeflag: tar.TypeReg})
			}
			if _, _, err := Extract(bytes.NewReader(rawArchive(t, headers)), t.TempDir()); err == nil {
				t.Fatal("unsafe source accepted")
			}
		})
	}
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeChar, tar.TypeFifo} {
		body := rawArchive(t, []*tar.Header{{Name: "unsafe", Typeflag: kind, Linkname: "outside"}})
		if _, _, err := Extract(bytes.NewReader(body), t.TempDir()); err == nil {
			t.Fatalf("accepted special entry %d", kind)
		}
	}
}

func TestSnapshotBoundsWholeArchiveAndAcceptsExplicitRoot(t *testing.T) {
	archive := rawArchive(t, []*tar.Header{{Name: "./", Typeflag: tar.TypeDir, Mode: 0700}, {Name: "./empty.txt", Typeflag: tar.TypeReg, Mode: 0600}})
	files, _, err := Extract(bytes.NewReader(archive), t.TempDir())
	if err != nil || len(files) != 1 || files[0].Name != "empty.txt" {
		t.Fatal("valid tar root failed", err)
	}
	if _, _, err = Extract(bytes.NewReader(nil), t.TempDir()); err == nil {
		t.Fatal("missing stream treated as delete-all")
	}
	oversized := io.MultiReader(bytes.NewReader(archive), io.LimitReader(zeroReader{}, MaxBytes))
	if _, _, err = Extract(oversized, t.TempDir()); err == nil {
		t.Fatal("oversized archive padding accepted")
	}
	if _, _, err = Extract(io.MultiReader(bytes.NewReader(archive), strings.NewReader("unexpected appended content")), t.TempDir()); err == nil {
		t.Fatal("nonzero trailing content accepted as one complete snapshot")
	}
}

func TestSourcePathRejectsReplacedRootSymlink(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("requires POSIX symlink creation")
	}
	root := filepath.Join(t.TempDir(), "source")
	if e := os.Symlink(t.TempDir(), root); e != nil {
		t.Fatal(e)
	}
	if _, e := Path(root, "file"); e == nil {
		t.Fatal("replaced root escaped source workspace")
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
