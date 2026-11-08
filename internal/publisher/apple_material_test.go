package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func appleMaterialJSONFixture(t *testing.T) (map[string]any, Profile) {
	t.Helper()
	dir := t.TempDir()
	name := filepath.Join(dir, "screenshot.png")
	img := image.NewRGBA(image.Rect(0, 0, 100, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 100; x++ {
			img.SetRGBA(x, y, color.RGBA{20, 70, 130, 255})
		}
	}
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if err = png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(b)
	m := map[string]any{"schemaVersion": 1, "appId": "1234567890", "bundleId": "com.company.feedback", "version": "1.0.0", "locales": []any{map[string]any{"locale": "en-US", "versionText": map[string]any{"description": "Reviewed description", "keywords": "feedback", "supportUrl": "https://company.example/support", "marketingUrl": "", "promotionalText": "", "whatsNew": "Improved feedback"}, "appInfo": map[string]any{"name": "Feedback", "subtitle": "", "privacyPolicyUrl": "https://company.example/privacy", "privacyChoicesUrl": "", "privacyPolicyText": ""}, "screenshots": []any{map[string]any{"placementGroup": "IPHONE_TEST_PROFILE", "images": []any{map[string]any{"path": name, "sha256": hex.EncodeToString(hash[:]), "bytes": len(b), "specId": "test-spec"}}}}}}}
	return m, Profile{AppID: "1234567890", BundleID: "com.company.feedback", MetadataFile: filepath.Join(dir, "metadata.json")}
}
func writeAppleMaterialFixture(t *testing.T, p *Profile, m map[string]any) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	p.MetadataDigest = hex.EncodeToString(h[:])
	if err = os.WriteFile(p.MetadataFile, b, 0600); err != nil {
		t.Fatal(err)
	}
}

// Removing the release-to-administrator material digest check must fail before any remote write.
func TestAppleMaterialRequiresApprovedDigestBeforePublishing(t *testing.T) {
	_, r, e, state, _ := appleFixture(t)
	defer e.Close()
	m, p := appleMaterialJSONFixture(t)
	writeAppleMaterialFixture(t, &p, m)
	e.Config.Profiles[0].MetadataFile = p.MetadataFile
	e.Config.Profiles[0].MetadataDigest = p.MetadataDigest
	if _, err := e.Execute(context.Background(), r); err == nil {
		t.Fatal("managed publication accepted missing approved metadata digest")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.writes != 0 {
		t.Fatal("unapproved metadata publication reached external writes")
	}
}

func TestAppleMaterialPrivateManifestAndImageValidation(t *testing.T) {
	for _, scenario := range []string{"valid", "request-digest", "manifest-digest", "duplicate-json", "unknown-field", "wrong-app", "wrong-version", "duplicate-locale", "duplicate-group", "bad-privacy", "bad-support", "empty-description", "long-name", "changed-image", "image-symlink", "metadata-public", "too-many-locales", "unknown-image-format"} {
		t.Run(scenario, func(t *testing.T) {
			m, p := appleMaterialJSONFixture(t)
			loc := m["locales"].([]any)[0].(map[string]any)
			group := loc["screenshots"].([]any)[0].(map[string]any)
			asset := group["images"].([]any)[0].(map[string]any)
			switch scenario {
			case "wrong-app":
				m["appId"] = "987654321"
			case "wrong-version":
				m["version"] = "2.0.0"
			case "duplicate-locale":
				m["locales"] = []any{loc, loc}
			case "duplicate-group":
				loc["screenshots"] = []any{group, group}
			case "bad-privacy":
				loc["appInfo"].(map[string]any)["privacyPolicyUrl"] = "https://user:secret@company.example/privacy"
			case "bad-support":
				loc["versionText"].(map[string]any)["supportUrl"] = "http://company.example/support"
			case "empty-description":
				loc["versionText"].(map[string]any)["description"] = ""
			case "long-name":
				loc["appInfo"].(map[string]any)["name"] = strings.Repeat("中", 31)
			case "unknown-field":
				m["apiUrl"] = "https://attacker.example"
			case "changed-image":
				if err := os.WriteFile(asset["path"].(string), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "image-symlink":
				name := asset["path"].(string)
				if err := os.Rename(name, name+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(name+".original", name); err != nil {
					t.Fatal(err)
				}
			case "too-many-locales":
				m["locales"] = []any{loc, loc, loc, loc, loc, loc, loc, loc, loc}
			case "unknown-image-format":
				b := []byte("not an image")
				if err := os.WriteFile(asset["path"].(string), b, 0600); err != nil {
					t.Fatal(err)
				}
				h := sha256.Sum256(b)
				asset["sha256"] = hex.EncodeToString(h[:])
				asset["bytes"] = len(b)
			}
			writeAppleMaterialFixture(t, &p, m)
			r := Request{Version: "1.0.0", MetadataDigest: p.MetadataDigest}
			switch scenario {
			case "request-digest":
				r.MetadataDigest = strings.Repeat("a", 64)
			case "manifest-digest":
				if err := os.WriteFile(p.MetadataFile, []byte(`{"schemaVersion":1}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "metadata-public":
				if err := os.Chmod(p.MetadataFile, 0644); err != nil {
					t.Fatal(err)
				}
			case "duplicate-json":
				b, _ := os.ReadFile(p.MetadataFile)
				b = append([]byte(`{"schemaVersion":2,`), b[1:]...)
				h := sha256.Sum256(b)
				p.MetadataDigest = hex.EncodeToString(h[:])
				r.MetadataDigest = p.MetadataDigest
				if err := os.WriteFile(p.MetadataFile, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			material, err := stageAppleMaterial(p, r)
			if scenario == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(material.Dir)
				if len(material.Snapshot.Locales) != 1 || material.Snapshot.Digest != p.MetadataDigest {
					t.Fatal("staged material lost reviewed binding")
				}
				body, _ := json.Marshal(material.Snapshot)
				if strings.Contains(string(body), filepath.Dir(p.MetadataFile)) || strings.Contains(string(body), `"path"`) {
					t.Fatal("private paths leaked into receipt")
				}
			} else if err == nil {
				os.RemoveAll(material.Dir)
				t.Fatal("invalid material accepted")
			}
		})
	}
}
