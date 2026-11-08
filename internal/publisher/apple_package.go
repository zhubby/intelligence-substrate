package publisher

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/intelligence-substrate/platform/internal/process"
)

type applePackage struct {
	Version     string `json:"version"`
	BuildNumber string `json:"buildNumber"`
}
type appleReceipt struct {
	UploadID        string                        `json:"uploadId,omitempty"`
	FileID          string                        `json:"fileId,omitempty"`
	BuildID         string                        `json:"buildId,omitempty"`
	VersionID       string                        `json:"versionId,omitempty"`
	SubmissionID    string                        `json:"submissionId,omitempty"`
	ItemID          string                        `json:"itemId,omitempty"`
	Version         string                        `json:"version"`
	BuildNumber     string                        `json:"buildNumber"`
	Stage           string                        `json:"stage"`
	Reconciliations []AppleReconciliation         `json:"reconciliations,omitempty"`
	Material        *appleMaterialSnapshot        `json:"material,omitempty"`
	Revision        *appleMaterialRevisionReceipt `json:"revision,omitempty"`
}

// This verifier runs only on the trusted macOS publisher, never in the app workspace.
func verifyApplePackage(ctx context.Context, p Profile, r Request, artifact string) (applePackage, error) {
	var result applePackage
	archive, err := zip.OpenReader(artifact)
	if err != nil {
		return result, fmt.Errorf("App Store requires an IPA ZIP archive")
	}
	defer archive.Close()
	dir, err := os.MkdirTemp("", "is-ipa-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(dir)
	seen := map[string]bool{}
	app := ""
	total := uint64(0)
	if len(archive.File) == 0 || len(archive.File) > 100000 {
		return result, fmt.Errorf("invalid IPA entry count")
	}
	for _, entry := range archive.File {
		name := entry.Name
		if strings.ContainsAny(name, "\\\x00") || filepath.IsAbs(name) || filepath.ToSlash(filepath.Clean(name)) != strings.TrimSuffix(name, "/") || strings.HasPrefix(name, "../") || name == ".." || seen[strings.ToLower(name)] || entry.Mode()&os.ModeSymlink != 0 || (!entry.FileInfo().IsDir() && !entry.Mode().IsRegular()) {
			return result, fmt.Errorf("unsafe or ambiguous IPA archive path")
		}
		seen[strings.ToLower(name)] = true
		if entry.UncompressedSize64 > 2<<30 || total > 2<<30-entry.UncompressedSize64 {
			return result, fmt.Errorf("IPA extraction exceeds 2 GiB")
		}
		total += entry.UncompressedSize64
		parts := strings.Split(name, "/")
		if len(parts) == 3 && parts[0] == "Payload" && strings.HasSuffix(parts[1], ".app") && parts[2] == "Info.plist" {
			if app != "" {
				return result, fmt.Errorf("IPA must contain exactly one application")
			}
			app = parts[0] + "/" + parts[1]
		}
		dest := filepath.Join(dir, filepath.FromSlash(name))
		if entry.FileInfo().IsDir() {
			if err = os.MkdirAll(dest, 0700); err != nil {
				return result, err
			}
			continue
		}
		if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return result, err
		}
		input, err := entry.Open()
		if err != nil {
			return result, fmt.Errorf("invalid IPA entry")
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600|entry.Mode().Perm()&0100)
		if err != nil {
			input.Close()
			return result, err
		}
		n, copyErr := io.Copy(out, io.LimitReader(input, int64(entry.UncompressedSize64)+1))
		input.Close()
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil || uint64(n) != entry.UncompressedSize64 {
			return result, fmt.Errorf("IPA entry failed integrity verification")
		}
	}
	if app == "" || runtime.GOOS != "darwin" {
		return result, fmt.Errorf("IPA verification requires one application and a dedicated macOS publisher")
	}
	check, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	run := func(program string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(check, program, args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"}
		cmd.WaitDelay = 5 * time.Second
		process.Protect(cmd)
		return cmd.Output()
	}
	root := filepath.Join(dir, filepath.FromSlash(app))
	b, err := run("/usr/bin/plutil", "-convert", "json", "-o", "-", filepath.Join(root, "Info.plist"))
	if err != nil {
		return result, fmt.Errorf("invalid IPA Info.plist")
	}
	var info struct {
		BundleID string `json:"CFBundleIdentifier"`
		Version  string `json:"CFBundleShortVersionString"`
		Build    string `json:"CFBundleVersion"`
		Platform string `json:"DTPlatformName"`
	}
	if json.Unmarshal(b, &info) != nil || info.BundleID != p.BundleID || info.Version != strings.TrimPrefix(r.Version, "v") || !appleVersionPattern.MatchString(info.Version) || !appleVersionPattern.MatchString(info.Build) || info.Platform != "iphoneos" {
		return result, fmt.Errorf("IPA bundle, device platform, version or build does not match the approved profile")
	}
	requirement := `anchor apple generic and certificate leaf[subject.OU] = "` + p.ExpectedSigner + `" and certificate 1[field.1.2.840.113635.100.6.2.1] exists and certificate leaf[field.1.2.840.113635.100.6.1.4] exists`
	if _, err = run("/usr/bin/codesign", "--verify", "--deep", "--strict", "-R", requirement, root); err != nil {
		return result, fmt.Errorf("IPA must have a valid Apple distribution signature for the configured team")
	}
	b, err = run("/usr/bin/codesign", "-d", "--entitlements", ":-", root)
	if err != nil {
		return result, fmt.Errorf("IPA entitlements unavailable")
	}
	entitlements := filepath.Join(dir, "entitlements.plist")
	if err = os.WriteFile(entitlements, b, 0600); err != nil {
		return result, err
	}
	b, err = run("/usr/bin/plutil", "-convert", "json", "-o", "-", entitlements)
	if err != nil {
		return result, fmt.Errorf("invalid IPA entitlements")
	}
	var ent struct {
		Application string `json:"application-identifier"`
		Team        string `json:"com.apple.developer.team-identifier"`
		Debug       bool   `json:"get-task-allow"`
	}
	if json.Unmarshal(b, &ent) != nil || ent.Application != p.ExpectedSigner+"."+p.BundleID || ent.Team != p.ExpectedSigner || ent.Debug {
		return result, fmt.Errorf("IPA entitlements must match the non-debug approved app/team")
	}
	return applePackage{Version: info.Version, BuildNumber: info.Build}, nil
}
