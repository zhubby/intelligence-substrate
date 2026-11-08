package publisher

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/intelligence-substrate/platform/internal/process"
)

type androidPackage struct {
	Version     string
	VersionCode int64
}

type androidSDKRunner func(context.Context, string, ...string) ([]byte, error)

var androidSignerPattern = regexp.MustCompile(`(?m)^Signer #([1-9][0-9]*) certificate SHA-256 digest: ([a-fA-F0-9]{64})\r?$`)
var androidBadgingPattern = regexp.MustCompile(`(?m)^package: name='([^'\r\n]+)' versionCode='([0-9]+)' versionName='([^'\r\n]+)'(?:[^\r\n]*)\r?$`)

func inspectAndroidPackage(ctx context.Context, p Profile, r Request, artifact string, run androidSDKRunner) (androidPackage, error) {
	var pkg androidPackage
	signing, err := run(ctx, p.APKSignerPath, "verify", "--verbose", "--print-certs", artifact)
	if err != nil {
		return pkg, fmt.Errorf("APK signature verification failed; check the administrator SDK and approved release package")
	}
	signers := androidSignerPattern.FindAllSubmatch(signing, -1)
	if len(signers) != 1 || string(signers[0][1]) != "1" || strings.ToLower(string(signers[0][2])) != p.ExpectedSigner || strings.Contains(strings.ToLower(string(signing)), "cn=android debug") {
		return pkg, fmt.Errorf("APK must have exactly the administrator-approved release signer")
	}
	badging, err := run(ctx, p.AAPTPath, "dump", "badging", artifact)
	if err != nil {
		return pkg, fmt.Errorf("APK metadata verification failed; check the administrator SDK")
	}
	matches := androidBadgingPattern.FindAllSubmatch(badging, -1)
	if len(matches) != 1 || string(matches[0][1]) != p.BundleID || string(matches[0][3]) != strings.TrimPrefix(r.Version, "v") || strings.Contains(string(badging), "application-debuggable") {
		return pkg, fmt.Errorf("APK package, version or debuggable flag differs from the approved release")
	}
	code, err := strconv.ParseInt(string(matches[0][2]), 10, 64)
	if err != nil || code < 1 || code > 2100000000 {
		return pkg, fmt.Errorf("APK requires an Android release versionCode between 1 and 2100000000")
	}
	return androidPackage{Version: string(matches[0][3]), VersionCode: code}, nil
}

func verifyAndroidPackage(ctx context.Context, p Profile, r Request, artifact string) (androidPackage, error) {
	check, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return inspectAndroidPackage(check, p, r, artifact, runAndroidSDK)
}

// Do not embed bytes.Buffer: its promoted ReaderFrom would bypass Write's cap
// when os/exec streams a command's pipe with io.Copy.
type androidSDKOutput struct{ body bytes.Buffer }

func (b *androidSDKOutput) Bytes() []byte { return b.body.Bytes() }

func (b *androidSDKOutput) Write(p []byte) (int, error) {
	if len(p) > (1<<20)-b.body.Len() {
		return 0, io.ErrShortBuffer
	}
	return b.body.Write(p)
}

func runAndroidSDK(ctx context.Context, program string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, program, args...)
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	// apksigner's administrator-owned launcher may require the provisioned JDK.
	if java := os.Getenv("JAVA_HOME"); java != "" {
		command.Env = append(command.Env, "JAVA_HOME="+java)
	}
	process.Protect(command)
	var output androidSDKOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	err := command.Run()
	return output.Bytes(), err
}
