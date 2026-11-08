package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/process"
)

var windowsIdentityFields = []string{"expectedWindowsPublisher", "expectedWindowsPackage", "expectedWindowsVersion"}
var windowsThumbprint = regexp.MustCompile(`^[A-F0-9]{40}$`)

func (r *Runner) signWindows(ctx context.Context, run p.Resource) (p.M, int, error) {
	in, err := signingInput(run)
	if err != nil {
		return p.M{"blocked": true}, 1, err
	}
	return r.signWindowsArtifact(ctx, run, in)
}

// The transport is separate from the native host guard; it never executes
// repository code and accepts only the authoritative signingInput above.
func (r *Runner) signWindowsArtifact(ctx context.Context, run p.Resource, in p.M) (result p.M, exit int, err error) {
	result, exit = p.M{"blocked": true}, 1
	defer func() {
		if err != nil {
			result = p.M{"blocked": true}
		}
	}()
	argv := r.Config.Signers["windows"]
	if len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return result, exit, fmt.Errorf("absolute administrator Windows signing program required")
	}
	dir, err := os.MkdirTemp("", "is-windows-signing-")
	if err != nil {
		return result, exit, err
	}
	defer os.RemoveAll(dir)
	inputPath := filepath.Join(dir, "unsigned.msix")
	if err = r.download(ctx, p.S(in, "artifactId"), inputPath); err != nil {
		return result, exit, err
	}
	file, err := os.Open(inputPath)
	if err != nil {
		return result, exit, err
	}
	h := sha256.New()
	size, readErr := io.Copy(h, io.LimitReader(file, 256<<20+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || size != int64(p.N(in, "artifactSize")) || hex.EncodeToString(h.Sum(nil)) != p.S(in, "artifactDigest") {
		return result, exit, fmt.Errorf("unsigned MSIX differs from authoritative input")
	}
	request := p.M{"protocolVersion": "windows-1", "action": "sign", "operationKey": p.S(in, "operationKey"), "projectId": run.ProjectID, "goalId": run.GoalID, "runId": run.ID, "taskId": p.S(in, "taskId"), "planId": p.S(in, "planId"), "requirementRevision": p.N(in, "requirementRevision"), "signingInputDigest": p.S(in, "digest"), "sourceBindingDigest": p.S(in, "sourceBindingDigest"), "repositoryId": p.S(in, "repositoryId"), "commit": p.S(in, "commit"), "target": "windows", "artifactId": p.S(in, "artifactId"), "artifactDigest": p.S(in, "artifactDigest"), "artifactSize": size, "artifactPath": inputPath, "expectedSigner": p.S(in, "expectedSigner")}
	for _, key := range windowsIdentityFields {
		request[key] = in[key]
	}
	input, err := json.Marshal(request)
	if err != nil {
		return result, exit, err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	process.Protect(cmd)
	cmd.Dir, cmd.Stdin = dir, bytes.NewReader(input)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + dir, "LC_ALL=C", "LANG=C"}
	if runtime.GOOS == "windows" {
		for _, key := range []string{"SystemRoot", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "PROGRAMDATA"} {
			if value := os.Getenv(key); value != "" {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
		}
		cmd.Env = append(cmd.Env, "TEMP="+dir, "TMP="+dir)
	}
	var output signingOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	err = cmd.Run()
	_ = cmd.Cancel()
	if err != nil {
		return result, exit, fmt.Errorf("Windows signing is unconfirmed; inspect private operation by operationKey")
	}
	receipt, err := decodeSigningReceipt(output.Bytes())
	if err != nil {
		return result, exit, fmt.Errorf("Windows signer must return one bounded unambiguous JSON object")
	}
	fields := []string{"protocolVersion", "state", "operationKey", "requestBinding", "payloadDigest", "certificateSHA256", "certificateSHA1", "publisher", "packageName", "version", "architecture", "inputArtifactId", "inputArtifactDigest", "artifactPath", "artifactDigest", "artifactSize"}
	if len(receipt) != len(fields) {
		return result, exit, fmt.Errorf("unexpected Windows signing receipt fields")
	}
	for _, key := range fields {
		if _, exists := receipt[key]; !exists {
			return result, exit, fmt.Errorf("incomplete Windows signing receipt")
		}
	}
	sizeValue := p.N(receipt, "artifactSize")
	if p.S(receipt, "protocolVersion") != "windows-1" || p.S(receipt, "state") != "complete" || p.S(receipt, "operationKey") != p.S(in, "operationKey") || p.S(receipt, "certificateSHA256") != p.S(in, "expectedSigner") || !windowsThumbprint.MatchString(p.S(receipt, "certificateSHA1")) || p.S(receipt, "publisher") != p.S(in, "expectedWindowsPublisher") || p.S(receipt, "packageName") != p.S(in, "expectedWindowsPackage") || p.S(receipt, "version") != p.S(in, "expectedWindowsVersion") || p.S(receipt, "architecture") != "x64" || p.S(receipt, "inputArtifactId") != p.S(in, "artifactId") || p.S(receipt, "inputArtifactDigest") != p.S(in, "artifactDigest") || !signingDigest.MatchString(p.S(receipt, "requestBinding")) || !signingDigest.MatchString(p.S(receipt, "payloadDigest")) || !signingDigest.MatchString(p.S(receipt, "artifactDigest")) || p.S(receipt, "artifactDigest") == p.S(in, "artifactDigest") || !filepath.IsAbs(p.S(receipt, "artifactPath")) || sizeValue <= 0 || sizeValue > 256<<20 || math.Trunc(sizeValue) != sizeValue {
		return result, exit, fmt.Errorf("Windows receipt differs from approved identity, exact input or bounded MSIX")
	}
	path := p.S(receipt, "artifactPath")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return result, exit, fmt.Errorf("signed MSIX must be a regular file")
	}
	signed, err := os.Open(path)
	if err != nil {
		return result, exit, err
	}
	defer signed.Close()
	opened, err := signed.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return result, exit, fmt.Errorf("signed MSIX changed before readback")
	}
	copyPath := filepath.Join(dir, "signed.msix")
	copied, err := os.OpenFile(copyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, exit, err
	}
	h.Reset()
	size, err = io.Copy(io.MultiWriter(copied, h), io.LimitReader(signed, 256<<20+1))
	closeErr = copied.Close()
	if err != nil || closeErr != nil || size != int64(sizeValue) || hex.EncodeToString(h.Sum(nil)) != p.S(receipt, "artifactDigest") {
		return result, exit, fmt.Errorf("signed MSIX bytes differ from trusted receipt")
	}
	artifact, err := r.upload(ctx, run, dir, "signed.msix", p.S(in, "repositoryId"), p.S(in, "commit"), "windows", "package")
	if err != nil {
		return result, exit, err
	}
	if p.S(artifact.Data, "digest") != p.S(receipt, "artifactDigest") || p.S(artifact.Data, "signingInputDigest") != p.S(in, "digest") || p.S(artifact.Data, "inputArtifactId") != p.S(in, "artifactId") || p.S(artifact.Data, "expectedSigner") != p.S(in, "expectedSigner") {
		return result, exit, fmt.Errorf("uploaded MSIX lost authoritative signing binding")
	}
	for _, key := range windowsIdentityFields {
		if p.S(artifact.Data, key) != p.S(in, key) {
			return result, exit, fmt.Errorf("uploaded MSIX lost approved package identity")
		}
	}
	result = p.M{"artifactId": artifact.ID, "repositoryId": p.S(in, "repositoryId"), "commit": p.S(in, "commit"), "signingInputDigest": p.S(in, "digest"), "operationKey": p.S(in, "operationKey")}
	for _, key := range []string{"certificateSHA256", "certificateSHA1", "publisher", "packageName", "version", "architecture", "payloadDigest", "requestBinding"} {
		result[key] = receipt[key]
	}
	return result, 0, nil
}
