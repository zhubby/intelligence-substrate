package updater

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMSIXIdentityMustMatchApprovedApplicationAndVersion(t *testing.T) {
	for _, test := range []struct {
		name, xml string
		valid     bool
	}{
		{"matching", `<Package xmlns="http://schemas.microsoft.com/appx/manifest/foundation/windows10"><Identity Name="com.company.feedback" Version="2.0.0.0" ProcessorArchitecture="neutral" Publisher="CN=Company"/></Package>`, true},
		{"wrong-app", `<Package><Identity Name="com.company.other" Version="2.0.0.0" ProcessorArchitecture="neutral"/></Package>`, false},
		{"wrong-version", `<Package><Identity Name="com.company.feedback" Version="1.0.0.0" ProcessorArchitecture="neutral"/></Package>`, false},
		{"revision", `<Package><Identity Name="com.company.feedback" Version="2.0.0.1" ProcessorArchitecture="neutral"/></Package>`, false},
		{"no-identity", `<Package/>`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var archive bytes.Buffer
			writer := zip.NewWriter(&archive)
			entry, _ := writer.Create("AppxManifest.xml")
			_, _ = entry.Write([]byte(test.xml))
			_ = writer.Close()
			name := filepath.Join(t.TempDir(), "app.msix")
			_ = os.WriteFile(name, archive.Bytes(), 0600)
			err := verifyMSIX(name, "com.company.feedback", "2.0.0", "CN=Company")
			if (err == nil) != test.valid {
				t.Fatalf("package identity result: %v", err)
			}
		})
	}
}

func TestMSIXInstalledReceiptRequiresFixedPublisher(t *testing.T) {
	valid := []byte(`{"name":"com.company.feedback","publisher":"CN=Company","version":"2.0.0.0"}`)
	if got, err := msixReceiptVersion(valid, "com.company.feedback", "CN=Company"); err != nil || got != "2.0.0" {
		t.Fatalf("valid receipt: %q %v", got, err)
	}
	for _, body := range []string{
		`{"name":"com.company.feedback","publisher":"CN=Other","version":"2.0.0.0"}`,
		`{"name":"com.company.other","publisher":"CN=Company","version":"2.0.0.0"}`,
		`{"name":"com.company.feedback","publisher":"CN=Company","version":"2.0.0.1"}`,
		`{"version":"2.0.0.0"}`,
	} {
		if _, err := msixReceiptVersion([]byte(body), "com.company.feedback", "CN=Company"); err == nil {
			t.Fatal("foreign or incomplete installed receipt accepted")
		}
	}
}

func TestPKGAndInstalledReceiptIdentityAreBoundToVersion(t *testing.T) {
	if err := verifyPackageInfo([]byte(`<pkg-info identifier="com.company.feedback" version="2.0.0"/>`), "com.company.feedback", "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := verifyPackageInfo([]byte(`<pkg-info identifier="com.company.other" version="2.0.0"/>`), "com.company.feedback", "2.0.0"); err == nil {
		t.Fatal("wrong PKG receipt identity accepted")
	}
	if err := verifyPackageInfo([]byte(`<pkg-info identifier="com.company.feedback" version="1.0.0"/>`), "com.company.feedback", "2.0.0"); err == nil {
		t.Fatal("wrong PKG version accepted")
	}
	valid := []byte(`<?xml version="1.0"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>pkgid</key><string>com.company.feedback</string><key>pkg-version</key><string>2.0.0</string><key>install-time</key><integer>1234</integer></dict></plist>`)
	if v, err := receiptVersion(valid, "com.company.feedback"); err != nil || v != "2.0.0" {
		t.Fatalf("receipt: %q %v", v, err)
	}
	if _, err := receiptVersion(valid, "com.company.other"); err == nil {
		t.Fatal("foreign installed receipt accepted")
	}
}

func TestNativeBackendRejectsUnsignedOrWrongSystemBeforeInstallation(t *testing.T) {
	f := fixture(t)
	name := filepath.Join(t.TempDir(), "unsigned.pkg")
	_ = os.WriteFile(name, []byte("not a signed installer"), 0600)
	backend := systemNative{}
	if err := backend.Verify(context.Background(), f.config, name, f.feed); err == nil {
		t.Fatal("unsigned package accepted by actual native verifier")
	}
	other := f.config
	if runtime.GOOS == "windows" {
		other.Target = "macos"
	} else {
		other.Target = "windows"
	}
	if _, err := backend.Installed(context.Background(), other); err == nil {
		t.Fatal("wrong native system reported an installed receipt")
	}
}
