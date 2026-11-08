//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIPrivateRoundTripAndGenericFailures(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	key, input := filepath.Join(dir, "key"), filepath.Join(dir, "input")
	cipher, output := filepath.Join(dir, "cipher"), filepath.Join(dir, "output")
	private := []byte("test private archive value")
	if err := os.WriteFile(input, private, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"keygen", "--output", key},
		{"seal", "--key-file", key, "--input", input, "--output", cipher},
		{"open", "--key-file", key, "--input", cipher, "--output", output},
	} {
		var diagnostics bytes.Buffer
		if run(args, &diagnostics) != 0 {
			t.Fatalf("command %s failed: %s", args[0], &diagnostics)
		}
	}
	body, _ := os.ReadFile(output)
	if !bytes.Equal(body, private) {
		t.Fatal("CLI did not recover exact input")
	}
	for _, args := range [][]string{
		{"seal", "--key-file", key, "--input", input, "--output", cipher},
		{"open", "--key-file", input, "--input", cipher, "--output", filepath.Join(dir, "bad")},
		{"reset", "--output", filepath.Join(dir, "reset")},
		{"open", "--key-file", key, "--input", cipher, "--output", filepath.Join(dir, "bad"), "unexpected"},
	} {
		var diagnostics bytes.Buffer
		if run(args, &diagnostics) == 0 {
			t.Fatal("invalid command succeeded")
		}
		if strings.Contains(diagnostics.String(), string(private)) || strings.Contains(diagnostics.String(), dir) {
			t.Fatal("CLI exposed private input or internal path diagnostics")
		}
	}
}
