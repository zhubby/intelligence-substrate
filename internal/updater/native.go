package updater

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/intelligence-substrate/platform/internal/desktop"
	"github.com/intelligence-substrate/platform/internal/distribution"
	"github.com/intelligence-substrate/platform/internal/process"
)

type systemNative struct{}

func nativeSystem(config Config) error {
	if config.Target != "windows" && config.Target != "macos" {
		return fmt.Errorf("unsupported native update target")
	}
	if (config.Target == "macos" && runtime.GOOS != "darwin") || (config.Target == "windows" && runtime.GOOS != "windows") {
		return fmt.Errorf("native update operation requires the configured target operating system")
	}
	return nil
}

func nativeRun(ctx context.Context, program string, args []string, environment ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}, environment...)
	process.Protect(cmd)
	return cmd.CombinedOutput()
}

func powershell(ctx context.Context, script string, environment ...string) ([]byte, error) {
	root := os.Getenv("SystemRoot")
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("absolute Windows SystemRoot required")
	}
	script = `[Console]::OutputEncoding=[System.Text.UTF8Encoding]::new($false); ` + script
	return nativeRun(ctx, filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), []string{"-NoProfile", "-NonInteractive", "-Command", script}, append([]string{"SystemRoot=" + root, "TEMP=" + os.TempDir(), "TMP=" + os.TempDir()}, environment...)...)
}

func (systemNative) Verify(ctx context.Context, config Config, name string, feed distribution.Feed) error {
	if err := nativeSystem(config); err != nil {
		return err
	}
	if _, err := desktop.Verify(ctx, config.Target, feed.Filename, config.ExpectedSigner, name); err != nil {
		return err
	}
	if config.Target == "windows" {
		return verifyMSIX(name, config.PackageID, feed.Version, config.WindowsPublisher)
	}
	dir, err := os.MkdirTemp(filepath.Dir(name), "inspect-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	expanded := filepath.Join(dir, "expanded")
	if _, err = nativeRun(ctx, "/usr/sbin/pkgutil", []string{"--expand", name, expanded}); err != nil {
		return fmt.Errorf("cannot inspect signed component PKG")
	}
	info := filepath.Join(expanded, "PackageInfo")
	stat, err := os.Lstat(info)
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > 64000 {
		return fmt.Errorf("only a single-component PKG with a bounded PackageInfo is supported")
	}
	b, err := os.ReadFile(info)
	if err != nil {
		return err
	}
	return verifyPackageInfo(b, config.PackageID, feed.Version)
}

func verifyPackageInfo(body []byte, packageID, expected string) error {
	var info struct {
		XMLName    xml.Name `xml:"pkg-info"`
		Identifier string   `xml:"identifier,attr"`
		Version    string   `xml:"version,attr"`
	}
	if len(body) > 64000 || xml.Unmarshal(body, &info) != nil || info.Identifier != packageID || info.Version != expected {
		return fmt.Errorf("PKG identity/version differs from the approved application")
	}
	return nil
}

func verifyMSIX(name, packageID, expected, publisher string) error {
	reader, err := zip.OpenReader(name)
	if err != nil {
		return fmt.Errorf("invalid MSIX archive")
	}
	defer reader.Close()
	var manifest *zip.File
	for _, file := range reader.File {
		if strings.EqualFold(file.Name, "AppxManifest.xml") {
			if manifest != nil || file.Name != "AppxManifest.xml" {
				return fmt.Errorf("ambiguous MSIX manifest")
			}
			manifest = file
		}
	}
	if manifest == nil || manifest.UncompressedSize64 > 64000 {
		return fmt.Errorf("missing or oversized MSIX manifest")
	}
	stream, err := manifest.Open()
	if err != nil {
		return err
	}
	defer stream.Close()
	body, err := io.ReadAll(io.LimitReader(stream, 64001))
	if err != nil || len(body) > 64000 {
		return fmt.Errorf("invalid MSIX manifest bytes")
	}
	var pkg struct {
		XMLName  xml.Name `xml:"Package"`
		Identity struct {
			Name         string `xml:"Name,attr"`
			Version      string `xml:"Version,attr"`
			Architecture string `xml:"ProcessorArchitecture,attr"`
			Publisher    string `xml:"Publisher,attr"`
		} `xml:"Identity"`
	}
	if xml.Unmarshal(body, &pkg) != nil || pkg.Identity.Name != packageID || pkg.Identity.Version != expected+".0" || pkg.Identity.Publisher != publisher {
		return fmt.Errorf("MSIX identity/version differs from the approved application")
	}
	arch := map[string]string{"amd64": "x64", "arm64": "arm64", "386": "x86"}[runtime.GOARCH]
	if pkg.Identity.Architecture != "neutral" && (arch == "" || pkg.Identity.Architecture != arch) {
		return fmt.Errorf("MSIX architecture does not match the current system")
	}
	return nil
}

func (systemNative) Installed(ctx context.Context, config Config) (string, error) {
	if err := nativeSystem(config); err != nil {
		return "", err
	}
	if config.Target == "windows" {
		script := `$ErrorActionPreference='Stop'; $p=@(Get-AppxPackage -Name $env:SUBSTRATE_PACKAGE_ID); if($p.Count -eq 0){@{version=''} | ConvertTo-Json -Compress; exit}; if($p.Count -ne 1){throw 'Ambiguous installed package'}; @{name=$p[0].Name; publisher=$p[0].Publisher; version=$p[0].Version.ToString()} | ConvertTo-Json -Compress`
		out, err := powershell(ctx, script, "SUBSTRATE_PACKAGE_ID="+config.PackageID)
		if err != nil {
			return "", fmt.Errorf("cannot read installed MSIX receipt")
		}
		return msixReceiptVersion(out, config.PackageID, config.WindowsPublisher)
	}
	out, err := nativeRun(ctx, "/usr/sbin/pkgutil", []string{"--pkg-info-plist", config.PackageID})
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && strings.Contains(string(out), "No receipt for '") {
			return "", nil
		}
		return "", fmt.Errorf("cannot read installed PKG receipt")
	}
	return receiptVersion(out, config.PackageID)
}

func msixReceiptVersion(body []byte, packageID, publisher string) (string, error) {
	var result struct {
		Name      string  `json:"name"`
		Publisher string  `json:"publisher"`
		Version   *string `json:"version"`
	}
	if json.Unmarshal(body, &result) != nil || result.Version == nil {
		return "", fmt.Errorf("invalid installed MSIX receipt")
	}
	if *result.Version == "" {
		return "", nil
	}
	if result.Name != packageID || result.Publisher != publisher || !strings.HasSuffix(*result.Version, ".0") || !version.MatchString(strings.TrimSuffix(*result.Version, ".0")) {
		return "", fmt.Errorf("installed MSIX identity/publisher/version differs from the profile")
	}
	return strings.TrimSuffix(*result.Version, ".0"), nil
}

func receiptVersion(body []byte, packageID string) (string, error) {
	if len(body) > 64000 {
		return "", fmt.Errorf("oversized native receipt")
	}
	decoder := xml.NewDecoder(bytes.NewReader(body))
	values := map[string]string{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "key" {
			continue
		}
		var key string
		if err = decoder.DecodeElement(&key, &start); err != nil {
			return "", err
		}
		for {
			token, err = decoder.Token()
			if err != nil {
				return "", err
			}
			if _, ok = token.(xml.CharData); ok {
				continue
			}
			break
		}
		element, ok := token.(xml.StartElement)
		if !ok {
			return "", fmt.Errorf("invalid receipt plist")
		}
		if element.Name.Local == "string" {
			var value string
			if err = decoder.DecodeElement(&value, &element); err != nil {
				return "", err
			}
			if _, duplicate := values[key]; duplicate {
				return "", fmt.Errorf("duplicate receipt value")
			}
			values[key] = value
		} else if err = decoder.Skip(); err != nil {
			return "", err
		}
	}
	if values["pkgid"] != packageID || !version.MatchString(values["pkg-version"]) {
		return "", fmt.Errorf("installed receipt identity/version is invalid")
	}
	return values["pkg-version"], nil
}

func (systemNative) Install(ctx context.Context, config Config, name string) error {
	if err := nativeSystem(config); err != nil {
		return err
	}
	if config.Target == "windows" {
		_, err := powershell(ctx, `$ErrorActionPreference='Stop'; Add-AppxPackage -Path $env:SUBSTRATE_PACKAGE -ForceApplicationShutdown`, "SUBSTRATE_PACKAGE="+name)
		if err != nil {
			return fmt.Errorf("native MSIX installation did not confirm completion")
		}
		return nil
	}
	if _, err := nativeRun(ctx, "/usr/sbin/installer", []string{"-pkg", name, "-target", "/"}); err != nil {
		return fmt.Errorf("native PKG installation did not confirm completion; authorized enterprise installer privileges are required")
	}
	return nil
}
