package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/process"
)

var signingDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type signingOutput struct{ bytes.Buffer }

func (b *signingOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 65536 {
		return 0, fmt.Errorf("signing response exceeds limit")
	}
	return b.Buffer.Write(data)
}

func decodeSigningReceipt(data []byte) (p.M, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, fmt.Errorf("trusted signing response must be one JSON object")
	}
	receipt, seen := p.M{}, map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[strings.ToLower(key)] {
			return nil, fmt.Errorf("trusted signing response has ambiguous fields")
		}
		seen[strings.ToLower(key)] = true
		var value any
		if err = decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("trusted signing response contains invalid JSON")
		}
		receipt[key] = value
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return nil, fmt.Errorf("trusted signing response is incomplete")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trusted signing response contains trailing data")
	}
	return receipt, nil
}

func signingInput(run p.Resource) (p.M, error) {
	task := p.Obj(run.Data["task"])
	mode := "android-apk"
	if p.S(task, "target") == "android-google-play" {
		mode = "android-aab"
	}
	macOS := p.S(task, "target") == "macos"
	if macOS {
		mode = "macos-pkg"
	}
	iOS := p.S(task, "target") == "ios-appstore"
	if iOS {
		mode = "ios-ipa"
	}
	windows := p.S(task, "target") == "windows"
	if windows {
		mode = "windows-msix"
	}
	in := p.Obj(task["signingInput"])
	content := p.M{}
	for key, value := range in {
		if key != "digest" {
			content[key] = value
		}
	}
	if p.S(task, "signingMode") != mode || p.S(task, "kind") != "build" || p.S(task, "role") != "release" || p.S(task, "runtime") != runtime.GOOS || !p.Has([]string{"android-google-play", "android-huawei", "android-xiaomi", "android-oppo", "android-vivo", "macos", "ios-appstore", "windows"}, p.S(task, "target")) || p.N(in, "version") != 1 || p.S(in, "digest") != p.Hash(content) || p.S(in, "taskId") != p.S(run.Data, "taskId") || p.S(in, "projectId") != run.ProjectID || p.S(in, "goalId") != run.GoalID || p.S(in, "planId") != p.S(run.Data, "planId") || p.N(in, "requirementRevision") != p.N(run.Data, "requirementRevision") || p.S(in, "target") != p.S(task, "target") || p.S(in, "repositoryId") != p.S(task, "repositoryId") || p.S(in, "commit") != p.S(task, "commit") || !p.CommitPattern.MatchString(p.S(in, "commit")) || p.S(in, "expectedSigner") != p.S(task, "expectedSigner") || !signingDigest.MatchString(p.S(in, "expectedSigner")) || !signingDigest.MatchString(p.S(in, "artifactDigest")) || p.N(in, "artifactSize") <= 0 || p.N(in, "artifactSize") > 256<<20 || p.S(in, "sourceBindingDigest") != p.S(p.Obj(task["sourceBinding"]), "digest") || p.S(in, "operationKey") != "sign-"+p.S(run.Data, "taskId") {
		return nil, fmt.Errorf("invalid authoritative signing input")
	}
	if macOS && (runtime.GOOS != "darwin" || !signingDigest.MatchString(p.S(in, "expectedApplicationSigner")) || p.S(in, "expectedApplicationSigner") != p.S(task, "expectedApplicationSigner") || !macOSTeam.MatchString(p.S(in, "expectedTeamId")) || p.S(in, "expectedTeamId") != p.S(task, "expectedTeamId")) {
		return nil, fmt.Errorf("invalid native macOS application certificate or team")
	}
	if iOS && (runtime.GOOS != "darwin" || !macOSTeam.MatchString(p.S(in, "expectedTeamId")) || p.S(in, "expectedTeamId") != p.S(task, "expectedTeamId") || !signingDigest.MatchString(p.S(in, "expectedProvisioningProfile")) || p.S(in, "expectedProvisioningProfile") != p.S(task, "expectedProvisioningProfile")) {
		return nil, fmt.Errorf("invalid native iOS distribution team or provisioning profile")
	}
	if windows {
		if runtime.GOOS != "windows" {
			return nil, fmt.Errorf("Windows signing requires a native Windows node")
		}
		for _, key := range windowsIdentityFields {
			if p.S(in, key) == "" || p.S(in, key) != p.S(task, key) {
				return nil, fmt.Errorf("Windows signing lost approved package identity")
			}
		}
	}
	for _, key := range []string{"command", "outputPath", "artifactStage", "artifactId", "sourceMode", "sourceFrom", "branch"} {
		if _, exists := task[key]; exists {
			return nil, fmt.Errorf("signing tasks cannot select repository tools or alternate inputs")
		}
	}
	return in, nil
}

// The signer is an administrator-configured program. No source checkout,
// repository tool, platform credential, or inherited secret environment reaches it.
func (r *Runner) signAndroid(ctx context.Context, run p.Resource) (result p.M, exit int, err error) {
	result, exit = p.M{"blocked": true}, 1
	defer func() {
		if err != nil {
			result = p.M{"blocked": true}
		}
	}()
	task := p.Obj(run.Data["task"])
	argv := r.Config.Signers[p.S(task, "target")]
	if len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return result, exit, fmt.Errorf("an absolute administrator signing program must be configured; repository fallback is forbidden")
	}
	in, err := signingInput(run)
	if err != nil {
		return result, exit, err
	}
	dir, err := os.MkdirTemp("", "is-android-signing-")
	if err != nil {
		return result, exit, err
	}
	defer os.RemoveAll(dir)
	extension := ".apk"
	if p.S(task, "signingMode") == "android-aab" {
		extension = ".aab"
	}
	inputPath := filepath.Join(dir, "unsigned"+extension)
	if err = r.download(ctx, p.S(in, "artifactId"), inputPath); err != nil {
		return result, exit, err
	}
	file, err := os.Open(inputPath)
	if err != nil {
		return result, exit, err
	}
	h := sha256.New()
	size, hashErr := io.Copy(h, file)
	closeErr := file.Close()
	if hashErr != nil || closeErr != nil || size != int64(p.N(in, "artifactSize")) || hex.EncodeToString(h.Sum(nil)) != p.S(in, "artifactDigest") {
		return result, exit, fmt.Errorf("downloaded unsigned Android package differs from authoritative signing input")
	}
	request := p.M{"protocolVersion": "1", "action": "sign", "operationKey": p.S(in, "operationKey"), "projectId": run.ProjectID, "goalId": run.GoalID, "runId": run.ID, "repositoryId": p.S(in, "repositoryId"), "commit": p.S(in, "commit"), "target": p.S(in, "target"), "artifactId": p.S(in, "artifactId"), "artifactDigest": p.S(in, "artifactDigest"), "artifactSize": size, "artifactPath": inputPath, "expectedSigner": p.S(in, "expectedSigner")}
	input, err := json.Marshal(request)
	if err != nil {
		return result, exit, err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	process.Protect(cmd)
	cmd.Dir, cmd.Stdin = dir, bytes.NewReader(input)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + os.TempDir(), "LC_ALL=C", "LANG=C"}
	if runtime.GOOS == "windows" {
		cmd.Env = append(cmd.Env, "SystemRoot="+os.Getenv("SystemRoot"), "TEMP="+os.TempDir())
	}
	var output signingOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if err = cmd.Run(); err != nil {
		return result, exit, fmt.Errorf("trusted signing did not confirm success; inspect the private operation by its operationKey")
	}
	receipt, err := decodeSigningReceipt(output.Bytes())
	if err != nil {
		return result, exit, fmt.Errorf("trusted signing response must be one bounded JSON object")
	}
	fields := []string{"artifactPath", "artifactDigest", "artifactSize", "certificateSHA256", "operationKey", "requestBinding", "payloadDigest", "packageName", "versionCode", "versionName", "minSDK", "inputArtifactId", "inputArtifactDigest"}
	if len(receipt) != len(fields) {
		return result, exit, fmt.Errorf("unexpected trusted signing response fields")
	}
	for _, key := range fields {
		if _, exists := receipt[key]; !exists {
			return result, exit, fmt.Errorf("incomplete trusted signing response")
		}
	}
	if !filepath.IsAbs(p.S(receipt, "artifactPath")) || !signingDigest.MatchString(p.S(receipt, "artifactDigest")) || !signingDigest.MatchString(p.S(receipt, "payloadDigest")) || !signingDigest.MatchString(p.S(receipt, "requestBinding")) || p.S(receipt, "certificateSHA256") != p.S(in, "expectedSigner") || p.S(receipt, "operationKey") != p.S(in, "operationKey") || p.S(receipt, "inputArtifactId") != p.S(in, "artifactId") || p.S(receipt, "inputArtifactDigest") != p.S(in, "artifactDigest") || p.N(receipt, "artifactSize") <= 0 || p.N(receipt, "artifactSize") > 256<<20 || p.S(receipt, "artifactDigest") == p.S(in, "artifactDigest") || p.S(receipt, "packageName") == "" || p.S(receipt, "versionName") == "" || p.N(receipt, "versionCode") < 1 || p.N(receipt, "minSDK") < 24 {
		return result, exit, fmt.Errorf("trusted signing receipt differs from the exact input or certificate")
	}
	path := p.S(receipt, "artifactPath")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return result, exit, fmt.Errorf("signed Android package must be a regular file")
	}
	signed, err := os.Open(path)
	if err != nil {
		return result, exit, err
	}
	defer signed.Close()
	opened, err := signed.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return result, exit, fmt.Errorf("signed Android package changed before readback")
	}
	outputName := "signed" + extension
	copyPath := filepath.Join(dir, outputName)
	copied, err := os.OpenFile(copyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, exit, err
	}
	h.Reset()
	size, err = io.Copy(io.MultiWriter(copied, h), io.LimitReader(signed, 256<<20+1))
	closeErr = copied.Close()
	if err != nil || closeErr != nil || size != int64(p.N(receipt, "artifactSize")) || hex.EncodeToString(h.Sum(nil)) != p.S(receipt, "artifactDigest") {
		return result, exit, fmt.Errorf("signed Android package readback differs from trusted signing receipt")
	}
	artifact, err := r.upload(ctx, run, dir, outputName, p.S(in, "repositoryId"), p.S(in, "commit"), p.S(in, "target"), "package")
	if err != nil {
		return result, exit, err
	}
	if p.S(artifact.Data, "digest") != p.S(receipt, "artifactDigest") || p.S(artifact.Data, "signingInputDigest") != p.S(in, "digest") || p.S(artifact.Data, "inputArtifactId") != p.S(in, "artifactId") || p.S(artifact.Data, "expectedSigner") != p.S(in, "expectedSigner") {
		return result, exit, fmt.Errorf("uploaded signing artifact differs from its authoritative binding")
	}
	return p.M{"artifactId": artifact.ID, "repositoryId": p.S(in, "repositoryId"), "commit": p.S(in, "commit"), "signingInputDigest": p.S(in, "digest"), "operationKey": p.S(in, "operationKey"), "certificateSHA256": p.S(receipt, "certificateSHA256"), "payloadDigest": p.S(receipt, "payloadDigest"), "requestBinding": p.S(receipt, "requestBinding")}, 0, nil
}
