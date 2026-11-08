package publisher

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

func validateXiaomiProfile(p Profile) error {
	if p.Track != "" || p.Channel != "" || p.DistributionRoot != "" || p.PublicURL != "" || p.SigningKeyFile != "" || p.InstallerFilename != "" || len(p.UploadHosts) != 0 || p.ChineseMainland != nil {
		return fmt.Errorf("Xiaomi profile cannot use unrelated destination selectors")
	}
	if p.Target != "android-xiaomi" || !packagePattern.MatchString(p.AppID) || p.BundleID != p.AppID || !hashPattern.MatchString(p.ExpectedSigner) || !filepath.IsAbs(p.CredentialsFile) || !filepath.IsAbs(p.AAPTPath) || !filepath.IsAbs(p.APKSignerPath) || !filepath.IsAbs(p.MetadataFile) || !hashPattern.MatchString(p.MetadataDigest) {
		return fmt.Errorf("Xiaomi profile requires a fixed Android package, release signer SHA256, absolute credential/SDK/material paths and reviewed metadata SHA256")
	}
	return nil
}

type xiaomiAppInfo struct {
	AppName       string            `json:"appName"`
	PublisherName string            `json:"publisherName,omitempty"`
	Category      int               `json:"category,omitempty"`
	KeyWords      string            `json:"keyWords,omitempty"`
	Description   string            `json:"desc,omitempty"`
	UpdateDesc    string            `json:"updateDesc,omitempty"`
	Brief         string            `json:"brief,omitempty"`
	PrivacyURL    string            `json:"privacyUrl"`
	TestAccount   map[string]string `json:"testAccount,omitempty"`
}

type xiaomiAsset struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type xiaomiMetadata struct {
	SynchroType *int          `json:"synchroType"`
	AppInfo     xiaomiAppInfo `json:"appInfo"`
	Icon        xiaomiAsset   `json:"icon"`
	Screenshots []xiaomiAsset `json:"screenshots,omitempty"`
}

type xiaomiFile struct {
	Name, Path, Filename string
}

type xiaomiMaterial struct {
	Metadata xiaomiMetadata
	Dir      string
	Files    []xiaomiFile
}

func decodeXiaomiJSON(body []byte, out any, strict bool) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' || !utf8.Valid(body) {
		return fmt.Errorf("Xiaomi JSON must be one UTF-8 object")
	}
	// encoding/json otherwise silently keeps the last occurrence of a field.
	// In particular, {"result":-7,"result":0} must never confirm a write.
	check := json.NewDecoder(bytes.NewReader(body))
	if err := checkXiaomiJSONValue(check, 0); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("invalid Xiaomi JSON structure")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fmt.Errorf("Xiaomi JSON must contain exactly one object")
	}
	return nil
}

func checkXiaomiJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return fmt.Errorf("Xiaomi JSON exceeds nesting bound")
	}
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("invalid Xiaomi JSON structure")
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			// Match encoding/json's Unicode-insensitive field matching too.
			name = strings.Map(func(ch rune) rune {
				canonical := ch
				for folded := unicode.SimpleFold(ch); folded != ch; folded = unicode.SimpleFold(folded) {
					if folded < canonical {
						canonical = folded
					}
				}
				return canonical
			}, name)
			if err != nil || !ok || seen[name] {
				return fmt.Errorf("Xiaomi JSON has invalid or duplicate fields")
			}
			seen[name] = true
			if err := checkXiaomiJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		if _, err = decoder.Token(); err != nil {
			return fmt.Errorf("invalid Xiaomi JSON object")
		}
	case json.Delim('['):
		for decoder.More() {
			if err := checkXiaomiJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		if _, err = decoder.Token(); err != nil {
			return fmt.Errorf("invalid Xiaomi JSON array")
		}
	}
	return nil
}

func readXiaomiPrivateFile(name string) ([]byte, error) {
	stat, err := os.Lstat(name)
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("Xiaomi credentials and reviewed metadata must be regular private service-owner files")
	}
	return readPrivateFile(name)
}

func stageXiaomiMaterial(p Profile) (*xiaomiMaterial, error) {
	body, err := readXiaomiPrivateFile(p.MetadataFile)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != p.MetadataDigest {
		return nil, fmt.Errorf("Xiaomi metadata differs from the administrator-reviewed SHA256")
	}
	var m xiaomiMetadata
	if err = decodeXiaomiJSON(body, &m, true); err != nil {
		return nil, err
	}
	if m.SynchroType == nil || (*m.SynchroType != 0 && *m.SynchroType != 1) || strings.TrimSpace(m.AppInfo.AppName) == "" || len(m.Screenshots) > 5 {
		return nil, fmt.Errorf("Xiaomi requires an app name, new/update type and at most five screenshots")
	}
	info := m.AppInfo
	u, err := url.Parse(info.PrivacyURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(info.PrivacyURL) > 4096 {
		return nil, fmt.Errorf("Xiaomi privacy policy must be a reviewed HTTPS URL without credentials or fragment")
	}
	for _, value := range []string{info.AppName, info.PublisherName, info.KeyWords, info.Description, info.UpdateDesc, info.Brief} {
		if len(value) > 32768 || strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("Xiaomi app information exceeds supported text bounds")
		}
	}
	if *m.SynchroType == 0 && (info.Category < 1 || strings.TrimSpace(info.KeyWords) == "" || strings.TrimSpace(info.Description) == "" || strings.TrimSpace(info.Brief) == "" || len(m.Screenshots) < 3) {
		return nil, fmt.Errorf("a new Xiaomi application requires category, keywords, description, brief and three to five screenshots")
	}
	if info.Category < 0 || (*m.SynchroType == 1 && strings.TrimSpace(info.UpdateDesc) == "") {
		return nil, fmt.Errorf("Xiaomi updates require an update description and a valid optional category")
	}
	for locale, account := range info.TestAccount {
		if locale != "zh_CN" || strings.TrimSpace(account) == "" || len(account) > 8192 || strings.ContainsRune(account, 0) {
			return nil, fmt.Errorf("Xiaomi testAccount supports only bounded reviewed zh_CN text")
		}
	}
	dir, err := os.MkdirTemp("", "is-xiaomi-material-")
	if err != nil {
		return nil, err
	}
	staged := &xiaomiMaterial{Metadata: m, Dir: dir}
	assets := append([]xiaomiAsset{m.Icon}, m.Screenshots...)
	for i, asset := range assets {
		name := "icon"
		if i > 0 {
			name = fmt.Sprintf("screenshot_%d", i)
		}
		file, err := stageXiaomiImage(dir, name, asset)
		if err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
		staged.Files = append(staged.Files, file)
	}
	return staged, nil
}

func stageXiaomiImage(dir, name string, asset xiaomiAsset) (xiaomiFile, error) {
	var file xiaomiFile
	if !filepath.IsAbs(asset.Path) || !hashPattern.MatchString(asset.SHA256) || asset.Bytes < 1 || asset.Bytes > 8<<20 {
		return file, fmt.Errorf("Xiaomi images require absolute regular paths, reviewed SHA256 and size up to 8 MiB")
	}
	stat, err := os.Lstat(asset.Path)
	if err != nil || !stat.Mode().IsRegular() {
		return file, fmt.Errorf("Xiaomi image must be a regular file")
	}
	in, err := os.Open(asset.Path)
	if err != nil {
		return file, fmt.Errorf("cannot open reviewed Xiaomi image")
	}
	defer in.Close()
	body, err := io.ReadAll(io.LimitReader(in, asset.Bytes+1))
	digest := sha256.Sum256(body)
	if err != nil || int64(len(body)) != asset.Bytes || hex.EncodeToString(digest[:]) != asset.SHA256 {
		return file, fmt.Errorf("Xiaomi image differs from the reviewed SHA256 or size")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || (format != "png" && format != "jpeg") || config.Width < 1 || config.Height < 1 || config.Width > 8192 || config.Height > 8192 || int64(config.Width)*int64(config.Height) > 16<<20 {
		return file, fmt.Errorf("Xiaomi image must be a bounded PNG or JPEG")
	}
	if _, _, err = image.Decode(bytes.NewReader(body)); err != nil {
		return file, fmt.Errorf("Xiaomi image data is incomplete or invalid")
	}
	extension := ".png"
	if format == "jpeg" {
		extension = ".jpg"
	}
	file = xiaomiFile{Name: name, Path: filepath.Join(dir, name+extension), Filename: name + extension}
	if err = os.WriteFile(file.Path, body, 0600); err != nil {
		return xiaomiFile{}, err
	}
	return file, nil
}
