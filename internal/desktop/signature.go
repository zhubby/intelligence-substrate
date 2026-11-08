// Package desktop verifies operating-system installer signatures independently of feeds.
package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/intelligence-substrate/platform/internal/process"
)

func Verify(ctx context.Context, target, filename, expectedSigner, artifact string) (string, error) {
	check, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	switch target {
	case "windows":
		if runtime.GOOS != "windows" {
			return "", fmt.Errorf("Windows signature verification requires a Windows execution node")
		}
		if !filepath.IsAbs(os.Getenv("SystemRoot")) {
			return "", fmt.Errorf("absolute Windows SystemRoot is required for trusted signature verification")
		}
		if !regexp.MustCompile(`^[A-Fa-f0-9]{40}$`).MatchString(expectedSigner) {
			return "", fmt.Errorf("expected Windows certificate SHA1 thumbprint required")
		}
		ext := strings.ToLower(filepath.Ext(filename))
		if ext != ".exe" && ext != ".msi" && ext != ".msix" {
			return "", fmt.Errorf("Windows distribution requires a signed exe/msi/msix installer")
		}
		name := artifact + ext
		if err := os.Rename(artifact, name); err != nil {
			return "", err
		}
		defer os.Rename(name, artifact)
		script := `$ErrorActionPreference='Stop'; $s=Get-AuthenticodeSignature -LiteralPath $env:SUBSTRATE_SIGNED_PACKAGE; @{status=$s.Status.ToString(); thumbprint=$s.SignerCertificate.Thumbprint} | ConvertTo-Json -Compress`
		cmd := exec.CommandContext(check, filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoProfile", "-NonInteractive", "-Command", script)
		cmd.Env = []string{"SystemRoot=" + os.Getenv("SystemRoot"), "TEMP=" + os.TempDir(), "SUBSTRATE_SIGNED_PACKAGE=" + name}
		process.Protect(cmd)
		cmd.WaitDelay = 5 * time.Second
		output, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("Windows Authenticode verification failed")
		}
		var result struct {
			Status     string `json:"status"`
			Thumbprint string `json:"thumbprint"`
		}
		if err = json.Unmarshal(output, &result); err != nil || result.Status != "Valid" || !strings.EqualFold(result.Thumbprint, expectedSigner) {
			return "", fmt.Errorf("Windows installer is not trusted or signed by the configured certificate")
		}
		return strings.ToUpper(result.Thumbprint), nil
	case "macos":
		if runtime.GOOS != "darwin" {
			return "", fmt.Errorf("macOS signature verification requires a macOS execution node")
		}
		if !regexp.MustCompile(`^[A-Z0-9]{10}$`).MatchString(expectedSigner) || !strings.HasSuffix(filename, ".pkg") {
			return "", fmt.Errorf("macOS requires a pkg installer and expected Developer ID team")
		}
		pkg := exec.CommandContext(check, "/usr/sbin/pkgutil", "--check-signature", artifact)
		pkg.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"}
		process.Protect(pkg)
		pkg.WaitDelay = 5 * time.Second
		output, err := pkg.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("macOS installer signature verification failed")
		}
		identity := regexp.MustCompile(`Developer ID Installer: [^\r\n]+ \(([A-Z0-9]{10})\)`).FindStringSubmatch(string(output))
		if len(identity) != 2 || identity[1] != expectedSigner {
			return "", fmt.Errorf("macOS installer belongs to an unexpected Developer ID team")
		}
		gatekeeper := exec.CommandContext(check, "/usr/sbin/spctl", "--assess", "--type", "install", "--verbose=2", artifact)
		gatekeeper.Env = pkg.Env
		process.Protect(gatekeeper)
		gatekeeper.WaitDelay = 5 * time.Second
		output, err = gatekeeper.CombinedOutput()
		if err != nil || !strings.Contains(string(output), "source=Notarized Developer ID") {
			return "", fmt.Errorf("macOS installer must pass Gatekeeper with a notarized Developer ID")
		}
		return expectedSigner, nil
	}
	return "", fmt.Errorf("unsupported desktop target")
}
