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

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/process"
)

func (r *Runner) signIOS(ctx context.Context, run p.Resource) (result p.M, exit int, err error) {
	result, exit = p.M{"blocked": true}, 1
	defer func() {
		if err != nil {
			result = p.M{"blocked": true}
		}
	}()
	argv := r.Config.Signers["ios-appstore"]
	if len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return result, exit, fmt.Errorf("absolute administrator iOS signing program required")
	}
	in, err := signingInput(run)
	if err != nil {
		return result, exit, err
	}
	dir, err := os.MkdirTemp("", "is-ios-signing-")
	if err != nil {
		return result, exit, err
	}
	defer os.RemoveAll(dir)
	inputPath := filepath.Join(dir, "unsigned.app.tar.gz")
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
		return result, exit, fmt.Errorf("unsigned iOS archive differs from authoritative input")
	}
	request := p.M{"protocolVersion": "ios-1", "action": "sign", "operationKey": p.S(in, "operationKey"), "projectId": run.ProjectID, "goalId": run.GoalID, "runId": run.ID, "taskId": p.S(in, "taskId"), "planId": p.S(in, "planId"), "requirementRevision": p.N(in, "requirementRevision"), "signingInputDigest": p.S(in, "digest"), "sourceBindingDigest": p.S(in, "sourceBindingDigest"), "repositoryId": p.S(in, "repositoryId"), "commit": p.S(in, "commit"), "target": "ios-appstore", "artifactId": p.S(in, "artifactId"), "artifactDigest": p.S(in, "artifactDigest"), "artifactSize": size, "artifactPath": inputPath, "expectedSigner": p.S(in, "expectedSigner"), "expectedTeamId": p.S(in, "expectedTeamId"), "expectedProvisioningProfile": p.S(in, "expectedProvisioningProfile")}
	input, err := json.Marshal(request)
	if err != nil {
		return result, exit, err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	process.Protect(cmd)
	cmd.Dir, cmd.Stdin = dir, bytes.NewReader(input)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + dir, "LC_ALL=C", "LANG=C"}
	var output signingOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	err = cmd.Run()
	_ = cmd.Cancel()
	if err != nil {
		return result, exit, fmt.Errorf("iOS signing is unconfirmed; inspect private operation by operationKey")
	}
	receipt, err := decodeSigningReceipt(output.Bytes())
	if err != nil {
		return result, exit, fmt.Errorf("iOS signer must return one bounded unambiguous JSON object")
	}
	fields := []string{"protocolVersion", "state", "operationKey", "requestBinding", "payloadDigest", "certificateSHA256", "profileSHA256", "teamId", "inputArtifactId", "inputArtifactDigest", "artifactPath", "artifactDigest", "artifactSize", "bundleId", "version", "build"}
	if len(receipt) != len(fields) {
		return result, exit, fmt.Errorf("unexpected iOS signing receipt fields")
	}
	for _, key := range fields {
		if _, exists := receipt[key]; !exists {
			return result, exit, fmt.Errorf("incomplete iOS signing receipt")
		}
	}
	sizeValue := p.N(receipt, "artifactSize")
	if p.S(receipt, "protocolVersion") != "ios-1" || p.S(receipt, "state") != "complete" || p.S(receipt, "operationKey") != p.S(in, "operationKey") || p.S(receipt, "certificateSHA256") != p.S(in, "expectedSigner") || p.S(receipt, "profileSHA256") != p.S(in, "expectedProvisioningProfile") || p.S(receipt, "teamId") != p.S(in, "expectedTeamId") || p.S(receipt, "inputArtifactId") != p.S(in, "artifactId") || p.S(receipt, "inputArtifactDigest") != p.S(in, "artifactDigest") || !signingDigest.MatchString(p.S(receipt, "requestBinding")) || !signingDigest.MatchString(p.S(receipt, "payloadDigest")) || !signingDigest.MatchString(p.S(receipt, "artifactDigest")) || p.S(receipt, "artifactDigest") == p.S(in, "artifactDigest") || !filepath.IsAbs(p.S(receipt, "artifactPath")) || sizeValue <= 0 || sizeValue > 256<<20 || math.Trunc(sizeValue) != sizeValue || p.S(receipt, "bundleId") == "" || p.S(receipt, "version") == "" || p.S(receipt, "build") == "" {
		return result, exit, fmt.Errorf("iOS receipt differs from exact input, distribution identity or bounded IPA")
	}
	path := p.S(receipt, "artifactPath")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return result, exit, fmt.Errorf("signed IPA must be a regular file")
	}
	signed, err := os.Open(path)
	if err != nil {
		return result, exit, err
	}
	defer signed.Close()
	opened, err := signed.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return result, exit, fmt.Errorf("signed IPA changed before readback")
	}
	copyPath := filepath.Join(dir, "signed.ipa")
	copied, err := os.OpenFile(copyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, exit, err
	}
	h.Reset()
	size, err = io.Copy(io.MultiWriter(copied, h), io.LimitReader(signed, 256<<20+1))
	closeErr = copied.Close()
	if err != nil || closeErr != nil || size != int64(sizeValue) || hex.EncodeToString(h.Sum(nil)) != p.S(receipt, "artifactDigest") {
		return result, exit, fmt.Errorf("signed IPA bytes differ from trusted receipt")
	}
	artifact, err := r.upload(ctx, run, dir, "signed.ipa", p.S(in, "repositoryId"), p.S(in, "commit"), "ios-appstore", "package")
	if err != nil {
		return result, exit, err
	}
	if p.S(artifact.Data, "digest") != p.S(receipt, "artifactDigest") || p.S(artifact.Data, "signingInputDigest") != p.S(in, "digest") || p.S(artifact.Data, "inputArtifactId") != p.S(in, "artifactId") || p.S(artifact.Data, "expectedSigner") != p.S(in, "expectedSigner") || p.S(artifact.Data, "expectedTeamId") != p.S(in, "expectedTeamId") || p.S(artifact.Data, "expectedProvisioningProfile") != p.S(in, "expectedProvisioningProfile") {
		return result, exit, fmt.Errorf("uploaded IPA lost authoritative signing binding")
	}
	result = p.M{"artifactId": artifact.ID, "repositoryId": p.S(in, "repositoryId"), "commit": p.S(in, "commit"), "signingInputDigest": p.S(in, "digest"), "operationKey": p.S(in, "operationKey")}
	for _, key := range []string{"certificateSHA256", "teamId", "profileSHA256", "payloadDigest", "requestBinding"} {
		result[key] = receipt[key]
	}
	return result, 0, nil
}
