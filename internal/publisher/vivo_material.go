package publisher

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func validateVivoProfile(p Profile) error {
	if p.Target != "android-vivo" || (p.Environment != "production" && p.Environment != "sandbox") || !packagePattern.MatchString(p.AppID) || p.BundleID != p.AppID || !hashPattern.MatchString(p.ExpectedSigner) || !filepath.IsAbs(p.CredentialsFile) || !filepath.IsAbs(p.AAPTPath) || !filepath.IsAbs(p.APKSignerPath) || !filepath.IsAbs(p.MetadataFile) || !hashPattern.MatchString(p.MetadataDigest) {
		return fmt.Errorf("vivo requires an explicit production/sandbox environment, fixed package/release signer, absolute private credential/SDK/material paths and reviewed metadata SHA256")
	}
	if p.Track != "" || p.Channel != "" || p.DistributionRoot != "" || p.PublicURL != "" || p.SigningKeyFile != "" || p.InstallerFilename != "" || len(p.UploadHosts) != 0 || p.ChineseMainland != nil {
		return fmt.Errorf("vivo profile cannot use unrelated destination selectors")
	}
	return nil
}

type vivoMetadata struct {
	UpdateDesc       string        `json:"updateDesc"`
	CompatibleDevice int           `json:"compatibleDevice"`
	PrivateSelfCheck string        `json:"privateSelfCheck"`
	DetailDesc       string        `json:"detailDesc,omitempty"`
	SimpleDesc       string        `json:"simpleDesc,omitempty"`
	Remark           string        `json:"remark,omitempty"`
	CustomerService  string        `json:"customerService,omitempty"`
	RateAge          *int          `json:"rateAge,omitempty"`
	TestAccount      []vivoAccount `json:"testAccount,omitempty"`
	Icon             *xiaomiAsset  `json:"icon,omitempty"`
	Screenshots      []xiaomiAsset `json:"screenshots,omitempty"`
}

type vivoAccount struct {
	Account  string `json:"testAccount"`
	Password string `json:"testPassword"`
}

type vivoFile struct {
	Name, Method, Path, Filename string
}

type vivoMaterial struct {
	Metadata vivoMetadata
	Dir      string
	Files    []vivoFile
}

func decodeVivoJSON(body []byte, out any, strict bool) error {
	if err := decodeXiaomiJSON(body, out, strict); err != nil {
		return fmt.Errorf("invalid, ambiguous or incomplete vivo JSON")
	}
	return nil
}

func readVivoPrivateFile(name string) ([]byte, error) {
	if body, err := readXiaomiPrivateFile(name); err == nil {
		return body, nil
	}
	return nil, fmt.Errorf("vivo credentials and reviewed metadata must be bounded regular private service-owner files")
}

func stageVivoMaterial(p Profile) (*vivoMaterial, error) {
	body, err := readVivoPrivateFile(p.MetadataFile)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(body)
	if hex.EncodeToString(h[:]) != p.MetadataDigest {
		return nil, fmt.Errorf("vivo metadata differs from administrator-reviewed SHA256")
	}
	var m vivoMetadata
	if err = decodeVivoJSON(body, &m, true); err != nil {
		return nil, err
	}
	if m.CompatibleDevice < 1 || m.CompatibleDevice > 3 || len(m.TestAccount) > 5 || (len(m.Screenshots) != 0 && (len(m.Screenshots) < 3 || len(m.Screenshots) > 5)) {
		return nil, fmt.Errorf("vivo requires reviewed target devices, at most five accounts and optionally three to five screenshots")
	}
	if m.RateAge != nil && *m.RateAge != 3 && *m.RateAge != 8 && *m.RateAge != 12 && *m.RateAge != 16 && *m.RateAge != 18 {
		return nil, fmt.Errorf("vivo age must be one of the supported store age levels")
	}
	for _, text := range []struct {
		value    string
		min, max int
		optional bool
	}{{m.UpdateDesc, 5, 200, false}, {m.DetailDesc, 50, 1000, true}, {m.SimpleDesc, 1, 50, true}, {m.Remark, 10, 200, true}, {m.CustomerService, 3, 254, true}} {
		n := utf8.RuneCountInString(text.value)
		if (n == 0 && !text.optional) || (n != 0 && (n < text.min || n > text.max)) || strings.TrimSpace(text.value) != text.value || strings.ContainsAny(text.value, "\x00\r") {
			return nil, fmt.Errorf("vivo reviewed text is missing or exceeds supported channel bounds")
		}
	}
	u, err := url.Parse(m.PrivateSelfCheck)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(m.PrivateSelfCheck) > 4096 {
		return nil, fmt.Errorf("vivo privacy self-check must be a reviewed HTTPS URL")
	}
	for _, account := range m.TestAccount {
		for _, text := range []string{account.Account, account.Password} {
			if len(text) == 0 || len(text) > 1024 || strings.TrimSpace(text) != text || strings.ContainsAny(text, "\x00\r\n") {
				return nil, fmt.Errorf("vivo review accounts must be bounded nonempty private values")
			}
		}
	}
	dir, err := os.MkdirTemp("", "is-vivo-material-")
	if err != nil {
		return nil, err
	}
	material := &vivoMaterial{Metadata: m, Dir: dir}
	add := func(name, method string, asset xiaomiAsset, icon bool) error {
		limit := int64(2 << 20)
		if icon {
			limit = 500 << 10
		}
		if asset.Bytes >= limit {
			return fmt.Errorf("vivo reviewed image exceeds the channel size bound")
		}
		file, err := stageXiaomiImage(dir, name, asset)
		if err != nil {
			return fmt.Errorf("vivo image is invalid or differs from reviewed SHA256/size")
		}
		in, err := os.Open(file.Path)
		if err != nil {
			return err
		}
		cfg, format, err := image.DecodeConfig(in)
		_ = in.Close()
		if err != nil || (icon && (format != "png" || cfg.Width != cfg.Height || cfg.Width < 256 || cfg.Width > 512)) || (!icon && (cfg.Width != 1080 || cfg.Height != 1920)) {
			return fmt.Errorf("vivo icon must be square 256–512 PNG; screenshots must be 1080x1920 PNG/JPEG")
		}
		material.Files = append(material.Files, vivoFile{Name: name, Method: method, Path: file.Path, Filename: file.Filename})
		return nil
	}
	if m.Icon != nil {
		if err = add("icon", "app.upload.icon", *m.Icon, true); err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
	}
	for i, asset := range m.Screenshots {
		if err = add(fmt.Sprintf("screenshot%d", i+1), "app.upload.screenshot", asset, false); err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
	}
	return material, nil
}
