package publisher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

type appleVersionText struct {
	Description     string `json:"description"`
	Keywords        string `json:"keywords"`
	SupportURL      string `json:"supportUrl"`
	MarketingURL    string `json:"marketingUrl"`
	PromotionalText string `json:"promotionalText"`
	WhatsNew        string `json:"whatsNew"`
}
type appleInfoText struct {
	Name              string `json:"name"`
	Subtitle          string `json:"subtitle"`
	PrivacyPolicyURL  string `json:"privacyPolicyUrl"`
	PrivacyChoicesURL string `json:"privacyChoicesUrl"`
	PrivacyPolicyText string `json:"privacyPolicyText"`
}
type appleMaterialImage struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	SpecID string `json:"specId"`
}
type appleMaterialGroup struct {
	PlacementGroup string               `json:"placementGroup"`
	Images         []appleMaterialImage `json:"images"`
}
type appleMaterialLocale struct {
	Locale      string               `json:"locale"`
	VersionText appleVersionText     `json:"versionText"`
	AppInfo     appleInfoText        `json:"appInfo"`
	Screenshots []appleMaterialGroup `json:"screenshots"`
}
type appleMaterialManifest struct {
	SchemaVersion int                   `json:"schemaVersion"`
	AppID         string                `json:"appId"`
	BundleID      string                `json:"bundleId"`
	Version       string                `json:"version"`
	Locales       []appleMaterialLocale `json:"locales"`
}
type appleImageSnapshot struct {
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
	SpecID      string `json:"specId"`
	Filename    string `json:"filename"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Alpha       bool   `json:"alpha"`
	Format      string `json:"format"`
	ImageID     string `json:"imageId,omitempty"`
	PlacementID string `json:"placementId,omitempty"`
}
type appleGroupSnapshot struct {
	PlacementGroup string               `json:"placementGroup"`
	Images         []appleImageSnapshot `json:"images"`
}
type appleLocaleSnapshot struct {
	Locale                string               `json:"locale"`
	VersionText           appleVersionText     `json:"versionText"`
	AppInfo               appleInfoText        `json:"appInfo"`
	VersionLocalizationID string               `json:"versionLocalizationId,omitempty"`
	InfoLocalizationID    string               `json:"infoLocalizationId,omitempty"`
	Screenshots           []appleGroupSnapshot `json:"screenshots"`
}
type appleMaterialSnapshot struct {
	Digest    string                `json:"digest"`
	AppID     string                `json:"appId"`
	LibraryID string                `json:"libraryId,omitempty"`
	AppInfoID string                `json:"appInfoId,omitempty"`
	Locales   []appleLocaleSnapshot `json:"locales"`
}
type appleMaterial struct {
	Dir      string
	Snapshot *appleMaterialSnapshot
}

var appleLocalePattern = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8}){0,2}$`)
var appleGroupPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

func appleMaterialBinding(p Profile, r Request) error {
	if r.MetadataDigest != p.MetadataDigest || (p.MetadataFile != "" && !hashPattern.MatchString(r.MetadataDigest)) {
		return fmt.Errorf("App Store metadata digest must match the approved release and administrator manifest")
	}
	return nil
}
func validAppleMaterialURL(value string, required bool) bool {
	if value == "" {
		return !required
	}
	u, err := url.Parse(value)
	return err == nil && len(value) <= 4096 && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && !strings.ContainsAny(value, "\r\n\x00")
}
func appleBoundedText(value string, max int, required bool) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= max && (!required || strings.TrimSpace(value) != "")
}

func stageAppleMaterial(p Profile, r Request) (*appleMaterial, error) {
	if err := appleMaterialBinding(p, r); err != nil {
		return nil, err
	}
	body, err := readXiaomiPrivateFile(p.MetadataFile)
	if err != nil {
		return nil, fmt.Errorf("App Store reviewed material must be one private regular file")
	}
	h := sha256.Sum256(body)
	if hex.EncodeToString(h[:]) != p.MetadataDigest {
		return nil, fmt.Errorf("App Store material differs from the approved SHA256")
	}
	var manifest appleMaterialManifest
	if err = decodeXiaomiJSON(body, &manifest, true); err != nil {
		return nil, fmt.Errorf("invalid or duplicate App Store material JSON")
	}
	if manifest.SchemaVersion != 1 || manifest.AppID != p.AppID || manifest.BundleID != p.BundleID || manifest.Version != strings.TrimPrefix(r.Version, "v") || len(manifest.Locales) < 1 || len(manifest.Locales) > 8 {
		return nil, fmt.Errorf("App Store manifest must match the exact app/version and contain one to eight locales")
	}
	dir, err := os.MkdirTemp("", "is-apple-material-")
	if err != nil {
		return nil, err
	}
	material := &appleMaterial{Dir: dir, Snapshot: &appleMaterialSnapshot{Digest: p.MetadataDigest, AppID: p.AppID}}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(dir)
		}
	}()
	locales := map[string]bool{}
	total := int64(0)
	count := 0
	for _, locale := range manifest.Locales {
		v, a := locale.VersionText, locale.AppInfo
		if !appleLocalePattern.MatchString(locale.Locale) || locales[locale.Locale] || len(locale.Screenshots) < 1 || len(locale.Screenshots) > 4 {
			return nil, fmt.Errorf("distinct App Store locales with one to four screenshot groups required")
		}
		locales[locale.Locale] = true
		if !appleBoundedText(v.Description, 4000, true) || !appleBoundedText(v.Keywords, 100, false) || !appleBoundedText(v.PromotionalText, 170, false) || !appleBoundedText(v.WhatsNew, 4000, false) || !appleBoundedText(a.Name, 30, true) || !appleBoundedText(a.Subtitle, 30, false) || !appleBoundedText(a.PrivacyPolicyText, 4000, false) || !validAppleMaterialURL(v.SupportURL, true) || !validAppleMaterialURL(v.MarketingURL, false) || !validAppleMaterialURL(a.PrivacyPolicyURL, true) || !validAppleMaterialURL(a.PrivacyChoicesURL, false) {
			return nil, fmt.Errorf("App Store requires bounded complete text and reviewed HTTPS support/privacy URLs")
		}
		entry := appleLocaleSnapshot{Locale: locale.Locale, VersionText: v, AppInfo: a}
		groups := map[string]bool{}
		for _, group := range locale.Screenshots {
			if !appleGroupPattern.MatchString(group.PlacementGroup) || groups[group.PlacementGroup] || len(group.Images) < 1 || len(group.Images) > 10 {
				return nil, fmt.Errorf("distinct App Store screenshot groups with one to ten images required")
			}
			groups[group.PlacementGroup] = true
			g := appleGroupSnapshot{PlacementGroup: group.PlacementGroup}
			for _, asset := range group.Images {
				count++
				total += asset.Bytes
				if count > 40 || asset.Bytes < 1 || asset.Bytes > 20<<20 || total > 128<<20 || !filepath.IsAbs(asset.Path) || !hashPattern.MatchString(asset.SHA256) || !appleResourceID.MatchString(asset.SpecID) {
					return nil, fmt.Errorf("App Store screenshot path/digest/spec/size exceeds supported bounds")
				}
				staged, cleanup, err := stageArtifact(Request{ArtifactPath: asset.Path, ArtifactDigest: asset.SHA256, ArtifactSize: asset.Bytes})
				if err != nil {
					return nil, err
				}
				imageEntry, err := inspectAppleImage(staged, asset, count)
				if err == nil {
					err = os.Rename(staged, filepath.Join(dir, imageEntry.Filename))
				}
				cleanup()
				if err != nil {
					return nil, err
				}
				g.Images = append(g.Images, imageEntry)
			}
			entry.Screenshots = append(entry.Screenshots, g)
		}
		material.Snapshot.Locales = append(material.Snapshot.Locales, entry)
	}
	success = true
	return material, nil
}
func inspectAppleImage(name string, asset appleMaterialImage, index int) (appleImageSnapshot, error) {
	var result appleImageSnapshot
	f, err := os.Open(name)
	if err != nil {
		return result, err
	}
	defer f.Close()
	config, format, err := image.DecodeConfig(f)
	if err != nil || (format != "png" && format != "jpeg") || config.Width < 1 || config.Height < 1 || config.Width > 8192 || config.Height > 8192 || int64(config.Width)*int64(config.Height) > 32_000_000 {
		return result, fmt.Errorf("App Store screenshots require a bounded complete PNG/JPEG image")
	}
	if _, err = f.Seek(0, 0); err != nil {
		return result, err
	}
	decoded, actual, err := image.Decode(f)
	if err != nil || actual != format || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return result, fmt.Errorf("App Store screenshot cannot be completely decoded")
	}
	opaque, ok := decoded.(interface{ Opaque() bool })
	if !ok {
		return result, fmt.Errorf("App Store screenshot alpha cannot be determined")
	}
	ext := ".png"
	if format == "jpeg" {
		ext = ".jpg"
	}
	result = appleImageSnapshot{SHA256: asset.SHA256, Bytes: asset.Bytes, SpecID: asset.SpecID, Filename: fmt.Sprintf("screenshot-%03d%s", index, ext), Width: config.Width, Height: config.Height, Alpha: !opaque.Opaque(), Format: format}
	return result, nil
}

func appleMaterialJSON(value any) map[string]any {
	b, _ := json.Marshal(value)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}
