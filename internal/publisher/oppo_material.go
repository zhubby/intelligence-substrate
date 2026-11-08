package publisher

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var oppoIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

func prepareOppoState(dir string) error {
	stat, err := os.Lstat(dir)
	if err != nil || !stat.IsDir() || stat.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("OPPO token cache requires a regular private state directory (0700)")
	}
	name := filepath.Join(dir, "publisher.db")
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		return file.Close()
	}
	if !os.IsExist(err) {
		return fmt.Errorf("cannot prepare private OPPO token state")
	}
	stat, err = os.Lstat(name)
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("OPPO token cache database must be a regular private service-owner file (0600)")
	}
	return nil
}

func validateOppoProfile(p Profile) error {
	if p.Target != "android-oppo" || !oppoIDPattern.MatchString(p.AppID) || !packagePattern.MatchString(p.BundleID) || !hashPattern.MatchString(p.ExpectedSigner) || !filepath.IsAbs(p.CredentialsFile) || !filepath.IsAbs(p.AAPTPath) || !filepath.IsAbs(p.APKSignerPath) || !filepath.IsAbs(p.MetadataFile) || !hashPattern.MatchString(p.MetadataDigest) || len(p.UploadHosts) == 0 || len(p.UploadHosts) > 8 {
		return fmt.Errorf("OPPO requires a fixed app ID/package/release signer, absolute private credentials/SDK/material paths, reviewed metadata SHA256 and exact HTTPS upload hosts")
	}
	if p.Track != "" || p.Channel != "" || p.DistributionRoot != "" || p.PublicURL != "" || p.SigningKeyFile != "" || p.InstallerFilename != "" || p.ChineseMainland != nil {
		return fmt.Errorf("OPPO profile cannot use unrelated destination selectors")
	}
	seen := map[string]bool{}
	for _, host := range p.UploadHosts {
		u, err := url.Parse("https://" + host)
		if err != nil || host != strings.ToLower(host) || host == "" || u.Host != host || u.Hostname() == "" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(host, "*\\% \t\r\n") || u.Opaque != "" || seen[host] || host == "oop-openapi-cn.heytapmobi.com" || (u.Port() != "" && u.Port() != "443") {
			return fmt.Errorf("OPPO upload hosts must be distinct exact HTTPS authorities without wildcards or API origin")
		}
		seen[host] = true
	}
	return nil
}

type oppoMetadata struct {
	AppName           string        `json:"appName"`
	SecondCategoryID  int           `json:"secondCategoryId"`
	ThirdCategoryID   int           `json:"thirdCategoryId"`
	Summary           string        `json:"summary"`
	DetailDesc        string        `json:"detailDesc"`
	UpdateDesc        string        `json:"updateDesc"`
	PrivacyURL        string        `json:"privacyURL"`
	TestDesc          string        `json:"testDesc"`
	BusinessUsername  string        `json:"businessUsername"`
	BusinessEmail     string        `json:"businessEmail"`
	BusinessMobile    string        `json:"businessMobile"`
	AgeLevel          *int          `json:"ageLevel"`
	AdaptiveEquipment int           `json:"adaptiveEquipment"`
	AdaptiveType      int           `json:"adaptiveType,omitempty"`
	ICP               string        `json:"icp,omitempty"`
	Icon              xiaomiAsset   `json:"icon"`
	Screenshots       []xiaomiAsset `json:"screenshots"`
	Copyright         xiaomiAsset   `json:"copyright"`
	Special           []xiaomiAsset `json:"special,omitempty"`
}

type oppoFile struct {
	Name, Path, Filename, Type string
	Bytes                      int64
}
type oppoMaterial struct {
	Metadata oppoMetadata
	Dir      string
	Files    []oppoFile
}

func decodeOppoJSON(body []byte, out any, strict bool) error {
	// Share the bounded UTF-8/duplicate-field scanner with the preceding channel.
	if err := decodeXiaomiJSON(body, out, strict); err != nil {
		return fmt.Errorf("invalid, ambiguous or incomplete OPPO JSON")
	}
	return nil
}

func readOppoPrivateFile(name string) ([]byte, error) {
	if body, err := readXiaomiPrivateFile(name); err == nil {
		return body, nil
	}
	return nil, fmt.Errorf("OPPO credentials and metadata must be bounded regular private service-owner files")
}

func stageOppoMaterial(p Profile) (*oppoMaterial, error) {
	body, err := readOppoPrivateFile(p.MetadataFile)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != p.MetadataDigest {
		return nil, fmt.Errorf("OPPO metadata differs from administrator-reviewed SHA256")
	}
	var m oppoMetadata
	if err = decodeOppoJSON(body, &m, true); err != nil {
		return nil, err
	}
	if m.SecondCategoryID < 1 || m.ThirdCategoryID < 1 || m.AgeLevel == nil || *m.AgeLevel < 0 || *m.AgeLevel > 18 || (m.AdaptiveEquipment != 4 && m.AdaptiveEquipment != 5 && m.AdaptiveEquipment != 6) || (m.AdaptiveType != 0 && m.AdaptiveType != 1 && m.AdaptiveType != 2) || len(m.Screenshots) < 3 || len(m.Screenshots) > 5 || len(m.Special) > 10 {
		return nil, fmt.Errorf("OPPO requires reviewed ordinary-app categories, age/target devices and three to five screenshots")
	}
	for _, v := range []struct {
		value    string
		min, max int
	}{{m.AppName, 1, 100}, {m.Summary, 1, 13}, {m.DetailDesc, 20, 10000}, {m.UpdateDesc, 5, 5000}, {m.TestDesc, 1, 400}, {m.BusinessUsername, 1, 100}, {m.BusinessEmail, 3, 254}, {m.BusinessMobile, 5, 40}, {m.ICP, 0, 1000}} {
		n := utf8.RuneCountInString(v.value)
		if n < v.min || n > v.max || strings.TrimSpace(v.value) != v.value || strings.ContainsAny(v.value, "\x00\r") {
			return nil, fmt.Errorf("OPPO reviewed text is missing or exceeds channel bounds")
		}
	}
	for _, r := range m.Summary {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			return nil, fmt.Errorf("OPPO summary cannot contain punctuation, symbols or whitespace")
		}
	}
	address, err := mail.ParseAddress(m.BusinessEmail)
	if err != nil || address.Address != m.BusinessEmail {
		return nil, fmt.Errorf("OPPO requires a reviewed contact email")
	}
	u, err := url.Parse(m.PrivacyURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(m.PrivacyURL) > 4096 {
		return nil, fmt.Errorf("OPPO privacy policy must be a reviewed HTTPS URL")
	}
	dir, err := os.MkdirTemp("", "is-oppo-material-")
	if err != nil {
		return nil, err
	}
	material := &oppoMaterial{Metadata: m, Dir: dir}
	assets := append([]xiaomiAsset{m.Icon}, m.Screenshots...)
	assets = append(assets, m.Copyright)
	assets = append(assets, m.Special...)
	for i, asset := range assets {
		name := "icon"
		if i > 0 && i <= len(m.Screenshots) {
			name = fmt.Sprintf("screen%d", i)
		} else if i == len(m.Screenshots)+1 {
			name = "copyright"
		} else if i > len(m.Screenshots)+1 {
			name = fmt.Sprintf("special%d", i-len(m.Screenshots)-1)
		}
		if asset.Bytes >= 1<<20 {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("OPPO images must be smaller than 1 MiB")
		}
		file, err := stageXiaomiImage(dir, name, asset)
		if err == nil {
			var in *os.File
			in, err = os.Open(file.Path)
			if err == nil {
				var cfg image.Config
				var format string
				cfg, format, err = image.DecodeConfig(in)
				_ = in.Close()
				if err == nil && ((i == 0 && (format != "png" || cfg.Width != 512 || cfg.Height != 512)) || (i > 0 && i <= len(m.Screenshots) && (cfg.Width != 1080 || cfg.Height != 1920))) {
					err = fmt.Errorf("OPPO icon must be 512x512 PNG; screenshots must be 1080x1920")
				}
			}
		}
		if err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("OPPO reviewed image is invalid or differs from its SHA256/size/dimensions")
		}
		material.Files = append(material.Files, oppoFile{Name: name, Path: file.Path, Filename: file.Filename, Type: "photo", Bytes: asset.Bytes})
	}
	return material, nil
}
