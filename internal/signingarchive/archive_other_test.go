//go:build !unix

package signingarchive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedPlatformNeverPublishesPrivateData(t *testing.T) {
	output := filepath.Join(t.TempDir(), "unpublished")
	for _, operation := range []func() error{
		func() error { return GenerateKey(output) },
		func() error { return Seal("unavailable", "unavailable", output) },
		func() error { return Open("unavailable", "unavailable", output) },
	} {
		if operation() == nil {
			t.Fatal("unsupported private operation succeeded")
		}
		if _, err := os.Lstat(output); !os.IsNotExist(err) {
			t.Fatal("unsupported platform published private data")
		}
	}
}
