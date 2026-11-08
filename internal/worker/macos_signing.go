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

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/process"
)

var macOSTeam = regexp.MustCompile(`^[A-Z0-9]{10}$`)
var macOSNotaryID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func (r *Runner) signMacOS(ctx context.Context, run p.Resource) (result p.M, exit int, err error) {
	result, exit = p.M{"blocked": true}, 1
	defer func() {
		if err != nil {
			result = p.M{"blocked": true}
		}
	}()
	task := p.Obj(run.Data["task"])
	argv := r.Config.Signers["macos"]
	if len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return result, exit, fmt.Errorf("an absolute administrator macOS signing program is required")
	}
	in, err := signingInput(run)
	if err != nil {
		return result, exit, err
	}
	phase := p.S(task, "signingPhase")
	if phase != "" && phase != "status" {
		return result, exit, fmt.Errorf("unsupported macOS signing phase")
	}
	dir, err := os.MkdirTemp("", "is-macos-signing-")
	if err != nil {
		return result, exit, err
	}
	defer os.RemoveAll(dir)
	request := p.M{"protocolVersion": "macos-1", "action": "sign", "operationKey": p.S(in, "operationKey"), "projectId": run.ProjectID, "goalId": run.GoalID, "runId": run.ID, "taskId": p.S(in, "taskId"), "planId": p.S(in, "planId"), "requirementRevision": p.N(in, "requirementRevision"), "signingInputDigest": p.S(in, "digest"), "sourceBindingDigest": p.S(in, "sourceBindingDigest"), "repositoryId": p.S(in, "repositoryId"), "commit": p.S(in, "commit"), "target": "macos", "artifactId": p.S(in, "artifactId"), "artifactDigest": p.S(in, "artifactDigest"), "artifactSize": p.N(in, "artifactSize"), "artifactPath": "", "expectedSigner": p.S(in, "expectedSigner"), "expectedApplicationSigner": p.S(in, "expectedApplicationSigner"), "expectedTeamId": p.S(in, "expectedTeamId")}
	if phase == "status" {
		op := p.Obj(task["signingOperation"])
		if p.S(op, "signingInputDigest") != p.S(in, "digest") || p.S(op, "operationKey") != p.S(in, "operationKey") || !macOSNotaryID.MatchString(p.S(op, "notarizationId")) || !signingDigest.MatchString(p.S(op, "requestBinding")) || !signingDigest.MatchString(p.S(op, "payloadDigest")) {
			return result, exit, fmt.Errorf("status requires the persisted exact notarization operation")
		}
		request["action"] = "status"
		for _, key := range []string{"notarizationId", "requestBinding", "payloadDigest"} {
			request[key] = op[key]
		}
	} else {
		inputPath := filepath.Join(dir, "unsigned.app.tar.gz")
		if err = r.download(ctx, p.S(in, "artifactId"), inputPath); err != nil {
			return result, exit, err
		}
		body, e := os.Open(inputPath)
		if e != nil {
			return result, exit, e
		}
		h := sha256.New()
		size, readErr := io.Copy(h, io.LimitReader(body, 256<<20+1))
		closeErr := body.Close()
		if readErr != nil || closeErr != nil || size != int64(p.N(in, "artifactSize")) || hex.EncodeToString(h.Sum(nil)) != p.S(in, "artifactDigest") {
			return result, exit, fmt.Errorf("unsigned macOS archive differs from authoritative input")
		}
		request["artifactPath"] = inputPath
	}
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
	// Reap descendants immediately after the signer exits, before package
	// readback or network uploads. All tools share this private task group.
	_ = cmd.Cancel()
	if err != nil {
		return result, exit, fmt.Errorf("macOS signing result is unconfirmed; inspect the private operation by operationKey")
	}
	receipt, err := decodeSigningReceipt(output.Bytes())
	if err != nil {
		return result, exit, fmt.Errorf("macOS signer must return one bounded unambiguous JSON object")
	}
	fields := []string{"protocolVersion", "state", "operationKey", "requestBinding", "payloadDigest", "certificateSHA256", "applicationCertificateSHA256", "teamId", "notarizationId", "inputArtifactId", "inputArtifactDigest"}
	state := p.S(receipt, "state")
	if state == "complete" {
		fields = append(fields, "artifactPath", "artifactDigest", "artifactSize", "notarized", "bundleId", "version", "build")
	}
	if len(receipt) != len(fields) {
		return result, exit, fmt.Errorf("unexpected macOS signing receipt fields")
	}
	for _, key := range fields {
		if _, ok := receipt[key]; !ok {
			return result, exit, fmt.Errorf("incomplete macOS signing receipt")
		}
	}
	if !p.Has([]string{"processing", "complete"}, state) || p.S(receipt, "protocolVersion") != "macos-1" || p.S(receipt, "operationKey") != p.S(in, "operationKey") || p.S(receipt, "certificateSHA256") != p.S(in, "expectedSigner") || p.S(receipt, "applicationCertificateSHA256") != p.S(in, "expectedApplicationSigner") || p.S(receipt, "teamId") != p.S(in, "expectedTeamId") || p.S(receipt, "inputArtifactId") != p.S(in, "artifactId") || p.S(receipt, "inputArtifactDigest") != p.S(in, "artifactDigest") || !macOSNotaryID.MatchString(p.S(receipt, "notarizationId")) || !signingDigest.MatchString(p.S(receipt, "requestBinding")) || !signingDigest.MatchString(p.S(receipt, "payloadDigest")) {
		return result, exit, fmt.Errorf("macOS signing receipt differs from the exact input or Apple identity")
	}
	if phase == "status" {
		op := p.Obj(task["signingOperation"])
		for _, key := range []string{"notarizationId", "requestBinding", "payloadDigest"} {
			if receipt[key] != op[key] {
				return result, exit, fmt.Errorf("macOS status returned a different notarization operation")
			}
		}
	}
	result = p.M{"repositoryId": p.S(in, "repositoryId"), "commit": p.S(in, "commit"), "signingInputDigest": p.S(in, "digest"), "operationKey": p.S(in, "operationKey")}
	for _, key := range []string{"certificateSHA256", "applicationCertificateSHA256", "teamId", "notarizationId", "requestBinding", "payloadDigest"} {
		result[key] = receipt[key]
	}
	if state == "processing" {
		result["signingPending"] = true
		return result, 0, nil
	}
	size := p.N(receipt, "artifactSize")
	if !filepath.IsAbs(p.S(receipt, "artifactPath")) || !signingDigest.MatchString(p.S(receipt, "artifactDigest")) || p.S(receipt, "artifactDigest") == p.S(in, "artifactDigest") || size <= 0 || size > 256<<20 || math.Trunc(size) != size || !p.B(receipt, "notarized") || p.S(receipt, "bundleId") == "" || p.S(receipt, "version") == "" || p.S(receipt, "build") == "" {
		return result, exit, fmt.Errorf("macOS completion requires a notarized bounded PKG")
	}
	path := p.S(receipt, "artifactPath")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return result, exit, fmt.Errorf("signed macOS package must be a regular file")
	}
	signed, err := os.Open(path)
	if err != nil {
		return result, exit, err
	}
	defer signed.Close()
	opened, err := signed.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return result, exit, fmt.Errorf("signed macOS package changed before readback")
	}
	copyPath := filepath.Join(dir, "signed.pkg")
	copied, err := os.OpenFile(copyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, exit, err
	}
	h := sha256.New()
	readSize, err := io.Copy(io.MultiWriter(copied, h), io.LimitReader(signed, 256<<20+1))
	closeErr := copied.Close()
	if err != nil || closeErr != nil || readSize != int64(size) || hex.EncodeToString(h.Sum(nil)) != p.S(receipt, "artifactDigest") {
		return result, exit, fmt.Errorf("signed PKG bytes differ from the trusted receipt")
	}
	artifact, err := r.upload(ctx, run, dir, "signed.pkg", p.S(in, "repositoryId"), p.S(in, "commit"), "macos", "package")
	if err != nil {
		return result, exit, err
	}
	if p.S(artifact.Data, "digest") != p.S(receipt, "artifactDigest") || p.S(artifact.Data, "signingInputDigest") != p.S(in, "digest") || p.S(artifact.Data, "expectedApplicationSigner") != p.S(in, "expectedApplicationSigner") || p.S(artifact.Data, "expectedTeamId") != p.S(in, "expectedTeamId") {
		return result, exit, fmt.Errorf("uploaded PKG lost authoritative Apple binding")
	}
	result["artifactId"], result["notarized"] = artifact.ID, true
	return result, 0, nil
}
