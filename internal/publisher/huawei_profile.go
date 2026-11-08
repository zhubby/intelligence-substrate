package publisher

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

var huaweiIDPattern = regexp.MustCompile(`^[0-9]{1,32}$`)

func validateHuaweiProfile(p Profile) error {
	if p.Track != "" || p.Channel != "" || p.DistributionRoot != "" || p.PublicURL != "" || p.SigningKeyFile != "" || p.InstallerFilename != "" {
		return fmt.Errorf("Huawei profile cannot use unrelated track, channel or hosted destination selectors")
	}
	if p.Target != "android-huawei" || !huaweiIDPattern.MatchString(p.AppID) || !packagePattern.MatchString(p.BundleID) || !hashPattern.MatchString(p.ExpectedSigner) || !filepath.IsAbs(p.CredentialsFile) || !filepath.IsAbs(p.AAPTPath) || !filepath.IsAbs(p.APKSignerPath) || p.ChineseMainland == nil || len(p.UploadHosts) == 0 || len(p.UploadHosts) > 16 {
		return fmt.Errorf("Huawei profile requires fixed appId, Android bundleId, signer SHA256, credential/SDK paths, mainland choice and exact uploadHosts")
	}
	seen := map[string]bool{}
	for _, host := range p.UploadHosts {
		u, err := url.Parse("https://" + host)
		if err != nil || u.Host != host || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || host != strings.ToLower(host) || strings.ContainsAny(host, "*[] \t\r\n") || seen[host] {
			return fmt.Errorf("Huawei uploadHosts must be distinct exact lowercase HTTPS hosts with optional ports")
		}
		seen[host] = true
	}
	return nil
}
