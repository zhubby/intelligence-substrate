package source

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const MaxBytes int64 = 64 << 20
const MaxEntries = 20000

type Entry struct {
	Name       string
	Size       int64
	Executable bool
}

// Extract hashes the entire bounded input, including archive padding. The
// caller owns a new private root and discards it on any error.
func Extract(body io.Reader, root string) ([]Entry, string, error) {
	limited := &io.LimitedReader{R: body, N: MaxBytes + 1}
	hash := sha256.New()
	input := io.TeeReader(limited, hash)
	reader := tar.NewReader(input)
	entries := []Entry{}
	var seen names
	var total int64
	for count := 0; ; count++ {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}
		if count >= MaxEntries {
			return nil, "", fmt.Errorf("too many source entries")
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeDir {
			return nil, "", fmt.Errorf("source links and special entries are forbidden")
		}
		name := strings.TrimPrefix(header.Name, "./")
		if header.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
			if name == "" || name == "." {
				continue
			}
		}
		destination, err := Path(root, name)
		if err != nil {
			return nil, "", err
		}
		if err = seen.add(name); err != nil {
			return nil, "", err
		}
		if header.Typeflag == tar.TypeDir {
			if err = os.MkdirAll(destination, 0700); err != nil {
				return nil, "", err
			}
			continue
		}
		total += header.Size
		if header.Size < 0 || total > MaxBytes {
			return nil, "", fmt.Errorf("source bytes exceed 64 MiB")
		}
		if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return nil, "", err
		}
		mode := os.FileMode(0600) | os.FileMode(header.Mode)&0100
		file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return nil, "", err
		}
		copied, copyErr := io.Copy(file, reader)
		closeErr := file.Close()
		if copyErr != nil {
			return nil, "", copyErr
		}
		if closeErr != nil {
			return nil, "", closeErr
		}
		if copied != header.Size {
			return nil, "", fmt.Errorf("incomplete source file")
		}
		entries = append(entries, Entry{Name: name, Size: header.Size, Executable: header.Mode&0100 != 0})
	}
	if _, err := io.Copy(paddingWriter{}, input); err != nil {
		return nil, "", err
	}
	if limited.N == 0 || MaxBytes+1-limited.N < 1024 {
		return nil, "", fmt.Errorf("invalid or oversized complete source archive")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, hex.EncodeToString(hash.Sum(nil)), nil
}

// Write produces stable bytes and only uses explicitly selected regular files.
func Write(output io.Writer, root string, entries []Entry) error {
	bounded := &boundedWriter{output: output}
	writer := tar.NewWriter(bounded)
	var seen names
	if len(entries) > MaxEntries {
		return fmt.Errorf("too many source entries")
	}
	files := append([]Entry(nil), entries...)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	for _, entry := range files {
		name, err := Path(root, entry.Name)
		if err != nil {
			return err
		}
		if err = seen.add(entry.Name); err != nil {
			return err
		}
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() != entry.Size {
			file.Close()
			return fmt.Errorf("source file changed or is not regular")
		}
		mode := int64(0644)
		if entry.Executable {
			mode = 0755
		}
		err = writer.WriteHeader(&tar.Header{Name: entry.Name, Mode: mode, Size: entry.Size, Typeflag: tar.TypeReg, Format: tar.FormatPAX})
		if err == nil {
			_, err = io.Copy(writer, file)
		}
		closed := file.Close()
		if err != nil {
			return err
		}
		if closed != nil {
			return closed
		}
	}
	return writer.Close()
}

type boundedWriter struct {
	output  io.Writer
	written int64
}
type paddingWriter struct{}

func (paddingWriter) Write(b []byte) (int, error) {
	for _, value := range b {
		if value != 0 {
			return 0, fmt.Errorf("unexpected content after source archive")
		}
	}
	return len(b), nil
}
func (w *boundedWriter) Write(b []byte) (int, error) {
	if w.written+int64(len(b)) > MaxBytes {
		return 0, fmt.Errorf("source archive exceeds 64 MiB")
	}
	n, err := w.output.Write(b)
	w.written += int64(n)
	return n, err
}
