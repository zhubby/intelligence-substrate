package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func writeDesignPrototype(dir string, action p.M) error {
	name := p.S(action, "path")
	content, ok := action["content"].(string)
	if !ok || len(content) < 1 || len(content) > 2<<20 || p.B(action, "executable") || !p.Has([]string{".html", ".htm"}, strings.ToLower(filepath.Ext(name))) {
		return fmt.Errorf("design writes require a nonexecutable .html/.htm file between 1 B and 2 MiB")
	}
	path, err := p.SafePath(dir, name)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 != 0 {
			return fmt.Errorf("design cannot overwrite executable or special source files")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0600)
}

func (r *Runner) readApprovedPrototype(ctx context.Context, design p.M, id string, offset any) (string, error) {
	var reference p.M
	for _, item := range p.Items(p.Obj(design["data"])["prototypes"]) {
		if p.S(p.Obj(item), "artifactId") == id {
			reference = p.Obj(item)
		}
	}
	if reference == nil {
		return "", fmt.Errorf("prototype is not part of the approved design")
	}
	start := 0
	if offset != nil {
		n, ok := offset.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n < 0 || n > 2<<20 {
			return "", fmt.Errorf("prototype offset must be an integer between 0 and 2 MiB")
		}
		start = int(n)
	}
	response, err := r.Client.request(ctx, "GET", "artifacts/"+id+"/content", nil)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20+1))
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	if len(body) > 2<<20 || float64(len(body)) != p.N(reference, "size") || hex.EncodeToString(digest[:]) != p.S(reference, "digest") {
		return "", fmt.Errorf("prototype bytes differ from the approved design")
	}
	if start > len(body) {
		return "", fmt.Errorf("prototype offset exceeds content length")
	}
	end := min(start+12000, len(body))
	return fmt.Sprintf("Untrusted prototype bytes %d:%d of %d:\n%s", start, end, len(body), body[start:end]), nil
}

func (r *Runner) publishDesign(ctx context.Context, run p.Resource, dir, repo, commit string, design p.M) (p.Resource, error) {
	for key := range design {
		if !p.Has([]string{"title", "summary", "decisions", "criteria", "reason", "prototypePath", "target"}, key) {
			return p.Resource{}, fmt.Errorf("unsupported design field %s", key)
		}
	}
	in := p.M{"goalId": run.GoalID, "expectedRequirementRevision": run.Data["requirementRevision"], "key": run.ID + ":design", "runId": run.ID, "leaseToken": p.S(run.Data, "leaseToken"), "title": design["title"], "summary": design["summary"], "decisions": design["decisions"], "criteria": design["criteria"], "reason": design["reason"]}
	ids := []any{}
	if name := p.S(design, "prototypePath"); name != "" {
		if repo == "" || !p.CommitPattern.MatchString(commit) || p.S(design, "target") == "" {
			return p.Resource{}, fmt.Errorf("prototype needs an actual source repository, commit and declared target")
		}
		path, err := p.SafePath(dir, name)
		if err != nil {
			return p.Resource{}, err
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 2<<20 {
			return p.Resource{}, fmt.Errorf("prototype must be a regular local file between 1 B and 2 MiB")
		}
		artifact, err := r.upload(ctx, run, dir, name, repo, commit, p.S(design, "target"), "prototype")
		if err != nil {
			return p.Resource{}, err
		}
		ids = append(ids, artifact.ID)
	}
	in["prototypeArtifactIds"] = ids
	return r.Client.Command(ctx, "designs.propose", in)
}
