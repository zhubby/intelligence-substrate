package platform

import (
	"regexp"
	"strconv"
	"strings"
)

var windowsPackagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{1,48}[A-Za-z0-9]$`)
var windowsThumbprintPattern = regexp.MustCompile(`^[A-F0-9]{40}$`)
var windowsSigningFields = []string{"expectedWindowsPublisher", "expectedWindowsPackage", "expectedWindowsVersion"}

func windowsVersion(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 4 {
		return false
	}
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 16)
		if err != nil || strconv.FormatUint(n, 10) != part || (i == 0 && n == 0) {
			return false
		}
	}
	return true
}

func windowsPublisher(value string) bool {
	if !strings.HasPrefix(value, "CN=") || len(value) < 4 || len(value) > 1024 {
		return false
	}
	for _, c := range value {
		if c < 32 || c == 127 {
			return false
		}
	}
	return strings.TrimSpace(value) == value
}

func validateWindowsSigningTask(m M) error {
	if S(m, "target") != "windows" || S(m, "signingMode") == "" {
		for _, key := range windowsSigningFields {
			if _, ok := m[key]; ok {
				return invalid("Windows身份仅用于受控MSIX签名")
			}
		}
		return nil
	}
	if S(m, "runtime") != "windows" || !windowsPublisher(S(m, "expectedWindowsPublisher")) || !windowsPackagePattern.MatchString(S(m, "expectedWindowsPackage")) || !windowsVersion(S(m, "expectedWindowsVersion")) {
		return invalid("Windows签名需要原生节点及准确发布者、包标识和四段版本")
	}
	return nil
}

func validateWindowsSigningResult(binding, artifact, result M) error {
	for _, key := range windowsSigningFields {
		if S(artifact, key) != S(binding, key) {
			return conflict("MSIX制品丢失批准的Windows身份")
		}
	}
	if S(result, "publisher") != S(binding, "expectedWindowsPublisher") || S(result, "packageName") != S(binding, "expectedWindowsPackage") || S(result, "version") != S(binding, "expectedWindowsVersion") || S(result, "architecture") != "x64" || !windowsThumbprintPattern.MatchString(S(result, "certificateSHA1")) {
		return conflict("MSIX回执与批准发布者、包版本或证书不一致")
	}
	return nil
}
