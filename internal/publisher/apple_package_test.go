package publisher

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func ipaArchive(t *testing.T, extra string) string {
	t.Helper()
	var body bytes.Buffer
	zw := zip.NewWriter(&body)
	files := map[string]string{
		"Payload/Feedback.app/Info.plist": `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.company.feedback</string><key>CFBundleShortVersionString</key><string>1.0.0</string><key>CFBundleVersion</key><string>42</string><key>DTPlatformName</key><string>iphoneos</string></dict></plist>`,
		"Payload/Feedback.app/Feedback":   "unsigned fixture, not a signed application",
	}
	if extra != "" {
		files[extra] = "unsafe"
	}
	for name, value := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "unsigned.ipa")
	if err := os.WriteFile(name, body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return name
}
func TestAppleNativeVerifierRejectsActualUnsignedIPA(t *testing.T) {
	_, err := verifyApplePackage(context.Background(), Profile{BundleID: "com.company.feedback", ExpectedSigner: "ABCD123456"}, Request{Version: "1.0.0"}, ipaArchive(t, ""))
	if err == nil {
		t.Fatal("unsigned IPA accepted")
	}
	if runtime.GOOS == "darwin" && !strings.Contains(err.Error(), "signature") {
		t.Fatalf("unsigned package did not reach native signature verifier: %v", err)
	}
}
func TestAppleIPARejectsEscapesAndDuplicateApplications(t *testing.T) {
	for _, extra := range []string{"../outside", "/absolute", "Payload/Feedback.app/../escaped", "Payload/Other.app/Info.plist", "Payload/Feedback.app/info.plist"} {
		t.Run(extra, func(t *testing.T) {
			_, err := verifyApplePackage(context.Background(), Profile{}, Request{}, ipaArchive(t, extra))
			if err == nil || (!strings.Contains(err.Error(), "path") && !strings.Contains(err.Error(), "exactly one")) {
				t.Fatalf("unsafe IPA wasn't rejected at archive boundary: %v", err)
			}
		})
	}
}
