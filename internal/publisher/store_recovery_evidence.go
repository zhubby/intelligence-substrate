package publisher

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type StoreEvidenceAttachment struct {
	Name   string `json:"name"`
	Path   string `json:"path,omitempty"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// This is an operator assertion. Its hash binds the statement and attachments;
// neither a hash nor a screenshot makes it independently verified API evidence.
type StorePortalEvidence struct {
	ProtocolVersion string                    `json:"protocolVersion"`
	Driver          string                    `json:"driver"`
	Environment     string                    `json:"environment"`
	AppID           string                    `json:"appId"`
	PackageName     string                    `json:"packageName"`
	OperationKey    string                    `json:"operationKey"`
	JournalDigest   string                    `json:"journalDigest"`
	ArtifactDigest  string                    `json:"artifactDigest"`
	ArtifactSize    int64                     `json:"artifactSize"`
	MetadataDigest  string                    `json:"metadataDigest"`
	Binding         string                    `json:"binding"`
	Version         string                    `json:"version"`
	VersionCode     int64                     `json:"versionCode"`
	State           string                    `json:"state"`
	Operator        string                    `json:"operator"`
	Reference       string                    `json:"reference"`
	ObservedAt      string                    `json:"observedAt"`
	Summary         string                    `json:"summary"`
	Attachments     []StoreEvidenceAttachment `json:"attachments"`
}

type storeEvidenceBody struct {
	name, digest string
	body         []byte
}
type storePortalBundle struct {
	report StorePortalEvidence
	bodies []storeEvidenceBody
}

func privateStoreEvidenceState(dir string) error {
	stat, err := os.Lstat(dir)
	if err != nil || !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 || stat.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("portal evidence requires a private publisher state directory (0700)")
	}
	stat, err = os.Lstat(filepath.Join(dir, "publisher.db"))
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("portal evidence requires a private publisher database (0600); fix permissions as its service owner")
	}
	return nil
}

func readStoreEvidenceFile(name, digest string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(name) || !hashPattern.MatchString(digest) {
		return nil, fmt.Errorf("private absolute evidence file and SHA256 required")
	}
	stat, err := os.Lstat(name)
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 || stat.Size() < 1 || stat.Size() > limit {
		return nil, fmt.Errorf("evidence must be a bounded regular private service-owner file")
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("cannot open private evidence file")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(stat, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("private evidence file changed before reading")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) != stat.Size() || int64(len(b)) > limit {
		return nil, fmt.Errorf("evidence size changed or exceeded limit")
	}
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != digest {
		return nil, fmt.Errorf("evidence SHA256 differs from administrator input")
	}
	return b, nil
}

func readStorePortalEvidence(in StoreReconciliationRequest, p Profile, code int64) (*storePortalBundle, error) {
	b, err := readStoreEvidenceFile(in.EvidenceFile, in.EvidenceDigest, 64<<10)
	if err != nil {
		return nil, err
	}
	var proof StorePortalEvidence
	if err = decodeStoreRecoveryJSON(b, &proof); err != nil {
		return nil, fmt.Errorf("invalid portal report: %w", err)
	}
	r := in.Request
	if proof.ProtocolVersion != "1" || proof.Driver != p.Driver || proof.Environment != storeEnvironment(p) || proof.AppID != p.AppID || proof.PackageName != p.BundleID || proof.OperationKey != r.OperationKey || proof.JournalDigest != in.JournalDigest || proof.ArtifactDigest != r.ArtifactDigest || proof.ArtifactSize != r.ArtifactSize || proof.MetadataDigest != p.MetadataDigest || (r.MetadataDigest != "" && r.MetadataDigest != p.MetadataDigest) || proof.Binding != r.Binding || proof.Version != strings.TrimPrefix(r.Version, "v") || proof.VersionCode != code || proof.Operator != in.Operator || proof.State != "accepted" || !boundedStoreText(proof.Reference, 512) || !boundedStoreText(proof.Summary, 4096) {
		return nil, fmt.Errorf("portal report differs from the exact operation, profile, approved bytes/material or acceptance-only assertion")
	}
	observed, err := time.Parse(time.RFC3339Nano, proof.ObservedAt)
	now := time.Now()
	if err != nil || observed.After(now.Add(5*time.Minute)) || observed.Before(now.Add(-24*time.Hour)) {
		return nil, fmt.Errorf("portal observation must be recent and not in the future")
	}
	if len(proof.Attachments) < 1 || len(proof.Attachments) > 8 {
		return nil, fmt.Errorf("one to eight private portal attachments required")
	}
	bundle := &storePortalBundle{report: proof, bodies: []storeEvidenceBody{{name: "report.json", digest: in.EvidenceDigest, body: b}}}
	seen := map[string]bool{"report.json": true}
	var total int64
	for _, a := range proof.Attachments {
		if !identifierPattern.MatchString(a.Name) || seen[a.Name] || a.Bytes < 1 || a.Bytes > 8<<20 {
			return nil, fmt.Errorf("portal attachment names must be unique and byte sizes bounded")
		}
		seen[a.Name] = true
		total += a.Bytes
		if total > 16<<20 {
			return nil, fmt.Errorf("portal attachments exceed total byte limit")
		}
		body, err := readStoreEvidenceFile(a.Path, a.SHA256, a.Bytes)
		if err != nil {
			return nil, err
		}
		if int64(len(body)) != a.Bytes {
			return nil, fmt.Errorf("portal attachment byte count differs")
		}
		bundle.bodies = append(bundle.bodies, storeEvidenceBody{name: a.Name, digest: a.SHA256, body: body})
	}
	return bundle, nil
}
