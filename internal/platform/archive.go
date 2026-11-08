package platform

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/intelligence-substrate/platform/internal/source"
)

func (s *Service) SourceArchive(ctx context.Context, a Actor, id, commit string) (string, error) {
	repo, e := s.GetResource(ctx, a, id)
	if e != nil || repo.Kind != "repository" {
		return "", missing()
	}
	return s.sourceArchive(ctx, repo, commit)
}

func (s *Service) sourceArchive(ctx context.Context, repo Resource, commit string) (string, error) {
	if !CommitPattern.MatchString(commit) {
		return "", invalid("需要完整源提交")
	}
	if _, e := s.rawGit(ctx, repo, "", nil, "cat-file", "-e", commit+"^{commit}"); e != nil {
		return "", e
	}
	file, e := os.CreateTemp("", "is-source-*.tar")
	if e != nil {
		return "", e
	}
	path := file.Name()
	file.Close()
	success := false
	defer func() {
		if !success {
			os.Remove(path)
		}
	}()
	// git archive applies export-ignore/export-subst attributes. Workers need the
	// complete raw tracked tree, including files marked as distribution exclusions.
	root, e := os.MkdirTemp("", "is-export-tree-")
	if e != nil {
		return "", e
	}
	defer os.RemoveAll(root)
	tree, e := s.rawGit(ctx, repo, "", nil, "ls-tree", "-r", "-l", "-z", commit)
	if e != nil {
		return "", e
	}
	entries := []source.Entry{}
	var total int64
	for _, line := range strings.Split(strings.TrimSuffix(string(tree), "\x00"), "\x00") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid Git tree")
		}
		fields := strings.Fields(parts[0])
		if len(fields) != 4 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			return "", invalid("源码仅支持普通文件，不支持链接或子模块")
		}
		size, e := strconv.ParseInt(fields[3], 10, 64)
		if e != nil || size < 0 {
			return "", fmt.Errorf("invalid Git blob size")
		}
		total += size
		if total > source.MaxBytes || len(entries) >= source.MaxEntries {
			return "", invalid("源代码归档超过限制")
		}
		dest, e := source.Path(root, parts[1])
		if e != nil {
			return "", invalid(e.Error())
		}
		if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
			return "", e
		}
		blob, e := s.rawGit(ctx, repo, "", nil, "cat-file", "blob", fields[2])
		if e != nil {
			return "", e
		}
		if int64(len(blob)) != size {
			return "", fmt.Errorf("source blob size changed")
		}
		if e = os.WriteFile(dest, blob, 0600); e != nil {
			return "", e
		}
		entries = append(entries, source.Entry{Name: parts[1], Size: size, Executable: fields[0] == "100755"})
	}
	file, e = os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
	if e != nil {
		return "", e
	}
	e = source.Write(file, root, entries)
	closeErr := file.Close()
	if e != nil {
		return "", e
	}
	if closeErr != nil {
		return "", closeErr
	}
	success = true
	return path, nil
}
