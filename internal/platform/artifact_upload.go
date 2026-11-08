package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Content is private until a governed registration transaction succeeds.
func (s *Service) uploadContent(body io.Reader, limit int64) (string, int64, error) {
	dir := filepath.Join(s.Root, "uploads")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", 0, err
	}
	file, err := os.CreateTemp(dir, "upload-")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(file.Name())
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, h), io.LimitReader(body, limit+1))
	closeErr := file.Close()
	if err != nil {
		return "", 0, err
	}
	if closeErr != nil {
		return "", 0, closeErr
	}
	if size == 0 || size > limit {
		return "", 0, invalid(fmt.Sprintf("制品大小必须在 1 B 与 %d MiB 之间", limit>>20))
	}
	digest := hex.EncodeToString(h.Sum(nil))
	dest := s.ArtifactPath(digest)
	if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return "", 0, err
	}
	if err = os.Rename(file.Name(), dest); err != nil {
		return "", 0, err
	}
	return digest, size, nil
}
