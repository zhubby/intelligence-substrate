package publisher

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestHuaweiSDKOutputLimitSurvivesStreamingCopy(t *testing.T) {
	var output androidSDKOutput
	// exec copies command pipes through io.Copy. Embedding bytes.Buffer would
	// promote ReaderFrom and let this stream bypass the capped Write method.
	_, err := io.Copy(&output, io.LimitReader(strings.NewReader(strings.Repeat("x", (1<<20)+1)), (1<<20)+1))
	if err == nil || len(output.Bytes()) > 1<<20 {
		t.Fatal("SDK output stream bypassed its memory bound")
	}
}

// Removing signature/identity validation, or passing a task-selected tool or
// file to the SDK, must fail these tests. The SDK is the sole replaced boundary.
func TestHuaweiAPKRequiresApprovedIdentityAndReleaseSignature(t *testing.T) {
	for _, tc := range []struct {
		name, signing, badging string
		failSDK                bool
		wantOK                 bool
	}{
		{"exact release", "Signer #1 certificate SHA-256 digest: " + strings.Repeat("c", 64), "package: name='com.company.feedback' versionCode='42' versionName='1.0.0' platformBuildVersionName='16'", false, true},
		{"wrong signer", "Signer #1 certificate SHA-256 digest: " + strings.Repeat("d", 64), "package: name='com.company.feedback' versionCode='42' versionName='1.0.0'", false, false},
		{"multiple signers", "Signer #1 certificate SHA-256 digest: " + strings.Repeat("c", 64) + "\nSigner #2 certificate SHA-256 digest: " + strings.Repeat("d", 64), "package: name='com.company.feedback' versionCode='42' versionName='1.0.0'", false, false},
		{"wrong package", "Signer #1 certificate SHA-256 digest: " + strings.Repeat("c", 64), "package: name='com.company.other' versionCode='42' versionName='1.0.0'", false, false},
		{"wrong version", "Signer #1 certificate SHA-256 digest: " + strings.Repeat("c", 64), "package: name='com.company.feedback' versionCode='42' versionName='2.0.0'", false, false},
		{"zero code", "Signer #1 certificate SHA-256 digest: " + strings.Repeat("c", 64), "package: name='com.company.feedback' versionCode='0' versionName='1.0.0'", false, false},
		{"oversized code", "Signer #1 certificate SHA-256 digest: " + strings.Repeat("c", 64), "package: name='com.company.feedback' versionCode='2100000001' versionName='1.0.0'", false, false},
		{"debuggable", "Signer #1 certificate SHA-256 digest: " + strings.Repeat("c", 64), "package: name='com.company.feedback' versionCode='42' versionName='1.0.0'\napplication-debuggable", false, false},
		{"signature verification failure", "Signer #1 certificate SHA-256 digest: " + strings.Repeat("c", 64), "package: name='com.company.feedback' versionCode='42' versionName='1.0.0'", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, request := huaweiConfig(t)
			profile := config.Profiles[0]
			run := func(_ context.Context, program string, args ...string) ([]byte, error) {
				if program == profile.APKSignerPath {
					if strings.Join(args, " ") != "verify --verbose --print-certs /private/approved-copy" {
						t.Fatal("signature SDK did not receive the exact staged APK")
					}
					if tc.failSDK {
						return []byte(tc.signing), errors.New("SDK rejected signature")
					}
					return []byte(tc.signing), nil
				}
				if program != profile.AAPTPath || strings.Join(args, " ") != "dump badging /private/approved-copy" {
					t.Fatal("package metadata SDK command is not administrator-fixed")
				}
				return []byte(tc.badging), nil
			}
			pkg, err := inspectAndroidPackage(context.Background(), profile, request, "/private/approved-copy", run)
			if (err == nil) != tc.wantOK {
				t.Fatalf("release validation got %+v, %v", pkg, err)
			}
			if tc.wantOK && (pkg.Version != "1.0.0" || pkg.VersionCode != 42) {
				t.Fatalf("wrong parsed version: %+v", pkg)
			}
		})
	}
}
