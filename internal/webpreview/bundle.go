// Package webpreview reads bounded, immutable static application bundles. It
// never extracts files, runs build commands, or contacts a network service.
package webpreview

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode"
)

const MaxSize = 64 << 20

type Bundle struct {
	FileCount int
	files     map[string][]byte
	runtime   *Runtime
}

// File returns verified bytes for an exact, normalized relative path. Callers
// must treat the bytes as immutable; HTTP readers never modify them.
func (b *Bundle) File(name string) ([]byte, bool) {
	if !validPath(name) || strings.HasPrefix(name, "substrate-preview/") {
		return nil, false
	}
	body, ok := b.files[name]
	return body, ok
}

func validPath(name string) bool {
	if name == "" || len(name) > 2048 || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\%?#:") || strings.ContainsFunc(name, unicode.IsControl) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

func Read(filename, digest string, size int64) (*Bundle, error) {
	if size < 1 || size > MaxSize {
		return nil, fmt.Errorf("预览归档须在 64 MiB 以内")
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("预览制品内容不存在")
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if int64(len(body)) != size || hex.EncodeToString(sum[:]) != digest {
		return nil, fmt.Errorf("预览制品完整性验证失败")
	}
	if len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b {
		source := bytes.NewReader(body)
		z, err := gzip.NewReader(source)
		if err != nil {
			return nil, fmt.Errorf("预览 gzip 归档无效")
		}
		z.Multistream(false)
		decoded, readErr := io.ReadAll(io.LimitReader(z, MaxSize+1))
		closeErr := z.Close()
		if readErr != nil || closeErr != nil || len(decoded) > MaxSize || source.Len() != 0 {
			return nil, fmt.Errorf("预览 gzip 校验失败、包含尾随内容或展开超限")
		}
		body = decoded
	}
	reader := bytes.NewReader(body)
	tr := tar.NewReader(reader)
	b := &Bundle{files: map[string][]byte{}}
	seen := map[string]bool{}
	count, total := 0, int64(0)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("预览 tar 归档无效")
		}
		count++
		if count > 10000 {
			return nil, fmt.Errorf("预览归档条目超过 10000")
		}
		name := strings.TrimPrefix(h.Name, "./")
		if h.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
			if name == "" || name == "." {
				continue
			}
		}
		if !validPath(name) || seen[name] {
			return nil, fmt.Errorf("预览归档存在不安全或重复路径")
		}
		seen[name] = true
		if h.Typeflag == tar.TypeDir && h.Size == 0 {
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("预览归档只允许普通文件和目录")
		}
		if h.Size < 0 || h.Size > 32<<20 || h.Size > MaxSize-total {
			return nil, fmt.Errorf("预览单文件或展开总量超限")
		}
		file, err := io.ReadAll(io.LimitReader(tr, h.Size+1))
		if err != nil || int64(len(file)) != h.Size {
			return nil, fmt.Errorf("预览文件内容不完整")
		}
		total += h.Size
		b.files[name] = file
	}
	// archive/tar deliberately ignores data after the end marker. Never let a
	// second hidden archive or arbitrary trailer share the approved digest.
	trailer := body[len(body)-reader.Len():]
	for _, v := range trailer {
		if v != 0 {
			return nil, fmt.Errorf("预览 tar 含尾随内容")
		}
	}
	if len(b.files["index.html"]) == 0 {
		return nil, fmt.Errorf("预览包根目录需要非空 index.html")
	}
	b.FileCount = len(b.files)
	if err := b.readRuntime(); err != nil {
		return nil, err
	}
	return b, nil
}
