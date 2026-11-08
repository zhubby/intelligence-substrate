//go:build unix

package signingarchive

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func privateDir(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func privateFixture(t *testing.T, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(privateDir(t), name)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuthenticatedRoundTripHasFreshNoncesAndPrivateOutputs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private signer vaults require POSIX ownership and no-follow semantics")
	}
	dir := privateDir(t)
	key := filepath.Join(dir, "recovery.key")
	if err := GenerateKey(key); err != nil {
		t.Fatal(err)
	}
	k, _ := os.ReadFile(key)
	if len(k) != 32 {
		t.Fatalf("key length = %d", len(k))
	}
	plain := bytes.Repeat([]byte("private-keystore-and-journal-content\x00"), 100)
	input := privateFixture(t, "archive.tar", plain)
	first, second := filepath.Join(dir, "first.enc"), filepath.Join(dir, "second.enc")
	for _, name := range []string{first, second} {
		if err := Seal(key, input, name); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(name)
		if info.Mode().Perm() != 0600 {
			t.Fatalf("ciphertext permissions = %v", info.Mode())
		}
	}
	a, _ := os.ReadFile(first)
	b, _ := os.ReadFile(second)
	if bytes.Equal(a, b) || bytes.Contains(a, k) || bytes.Contains(a, plain) {
		t.Fatal("encryption repeated a nonce or exposed private input")
	}
	output := filepath.Join(dir, "restored.tar")
	if err := Open(key, first, output); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(output)
	if !bytes.Equal(restored, plain) {
		t.Fatal("restored bytes differ")
	}
	for _, action := range []func() error{
		func() error { return GenerateKey(key) },
		func() error { return Seal(key, input, first) },
		func() error { return Open(key, first, output) },
	} {
		if action() == nil {
			t.Fatal("existing output was overwritten")
		}
	}
	after, _ := os.ReadFile(output)
	if !bytes.Equal(after, plain) {
		t.Fatal("overwrite refusal altered existing output")
	}
}

func TestAuthenticationFailuresPublishNothing(t *testing.T) {
	dir := privateDir(t)
	key, wrong := filepath.Join(dir, "key"), filepath.Join(dir, "wrong")
	if err := GenerateKey(key); err != nil {
		t.Fatal(err)
	}
	if err := GenerateKey(wrong); err != nil {
		t.Fatal(err)
	}
	input := privateFixture(t, "source", []byte("private identity and journal"))
	encrypted := filepath.Join(dir, "good.enc")
	if err := Seal(key, input, encrypted); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(encrypted)
	for _, name := range []string{"wrong-key", "header", "nonce", "payload", "tag", "truncated", "extra"} {
		t.Run(name, func(t *testing.T) {
			data, useKey := append([]byte(nil), body...), key
			switch name {
			case "wrong-key":
				useKey = wrong
			case "header":
				data[0] ^= 1
			case "nonce":
				data[len(header)] ^= 1
			case "payload":
				data[len(header)+12] ^= 1
			case "tag":
				data[len(data)-1] ^= 1
			case "truncated":
				data = data[:len(data)-1]
			case "extra":
				data = append(data, 0)
			}
			bad := privateFixture(t, "bad.enc", data)
			output := filepath.Join(privateDir(t), "unpublished")
			if Open(useKey, bad, output) == nil {
				t.Fatal("unauthenticated archive accepted")
			}
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatal("authentication failure left plaintext output")
			}
		})
	}
}

func TestPrivateFilesBoundsAndLinksFailClosed(t *testing.T) {
	dir := privateDir(t)
	key := filepath.Join(dir, "key")
	if err := GenerateKey(key); err != nil {
		t.Fatal(err)
	}
	input := privateFixture(t, "source", []byte("private source"))
	for _, name := range []string{"public-input", "public-key", "linked-input", "linked-key", "linked-parent", "public-parent", "short-key", "oversized"} {
		t.Run(name, func(t *testing.T) {
			useKey, useInput, parent := key, input, privateDir(t)
			switch name {
			case "public-input":
				useInput = privateFixture(t, "public", []byte("public"))
				_ = os.Chmod(useInput, 0644)
			case "public-key":
				useKey = privateFixture(t, "public", bytes.Repeat([]byte{1}, 32))
				_ = os.Chmod(useKey, 0644)
			case "linked-input":
				useInput = filepath.Join(privateDir(t), "link")
				if err := os.Symlink(input, useInput); err != nil {
					t.Fatal(err)
				}
			case "linked-key":
				useKey = filepath.Join(privateDir(t), "link")
				if err := os.Symlink(key, useKey); err != nil {
					t.Fatal(err)
				}
			case "linked-parent":
				link := filepath.Join(privateDir(t), "link")
				if err := os.Symlink(parent, link); err != nil {
					t.Fatal(err)
				}
				parent = link
			case "public-parent":
				_ = os.Chmod(parent, 0755)
			case "short-key":
				useKey = privateFixture(t, "short", []byte("not a random 32-byte key"))
			case "oversized":
				useInput = privateFixture(t, "large", []byte{1})
				if err := os.Truncate(useInput, maxPlaintext+1); err != nil {
					t.Fatal(err)
				}
			}
			output := filepath.Join(parent, "unpublished")
			if Seal(useKey, useInput, output) == nil {
				t.Fatal("unsafe input or destination accepted")
			}
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatal("failed seal published output")
			}
		})
	}
}
