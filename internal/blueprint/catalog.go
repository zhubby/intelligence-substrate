// Package blueprint freezes administrator-installed starting sources. It never
// runs a generator, resolves a user path, or treats a template as quality proof.
package blueprint

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/intelligence-substrate/platform/internal/source"
)

type Descriptor struct {
	ID          string   `json:"id"`
	Version     int      `json:"version"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Digest      string   `json:"digest"`
	Targets     []string `json:"targets"`
	Modes       []string `json:"modes"`
	Limitations []string `json:"limitations"`
}

type Options struct {
	Name         string `json:"name"`
	Organization string `json:"organization"`
	Mode         string `json:"mode"`
}

type file struct {
	name       string
	body       []byte
	executable bool
}

type Catalog struct {
	descriptor Descriptor
	files      []file
}

var appName = regexp.MustCompile(`^[a-z][a-z0-9_]{1,49}$`)
var organization = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9]*)+$`)

func ValidateOptions(o Options) error {
	if !appName.MatchString(o.Name) || len(o.Organization) > 160 || !organization.MatchString(o.Organization) || (o.Mode != "feedback" && o.Mode != "booking") {
		return fmt.Errorf("需要有效的小写应用名、反向域名和 feedback/booking 业务模式")
	}
	return nil
}

// Load accepts the release root from startup configuration, not request data.
// A changed asset set gets a new digest even if blueprint.json was not bumped.
func Load(root string) (*Catalog, error) {
	if root == "-" {
		return nil, nil
	}
	c := &Catalog{}
	template := filepath.Join(root, "blueprints", "flutter-go")
	var total int64
	add := func(path, name string) error {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("blueprint asset is not a regular file: %s", name)
		}
		total += info.Size()
		if total > source.MaxBytes-(1<<20) || len(c.files) >= 1000 {
			return fmt.Errorf("blueprint assets exceed limits")
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		c.files = append(c.files, file{name, body, info.Mode()&0100 != 0})
		return nil
	}
	err := filepath.WalkDir(template, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("blueprint assets cannot contain links")
		}
		if entry.IsDir() {
			if path != template && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "build" || entry.Name() == "__pycache__") {
				return fs.SkipDir
			}
			return nil
		}
		name, err := filepath.Rel(template, path)
		if err != nil {
			return err
		}
		return add(path, filepath.ToSlash(name))
	})
	if err != nil {
		return nil, fmt.Errorf("load installed application blueprint: %w", err)
	}
	for _, name := range []string{"build-reference-client.py", "android_bundle.py", "reference_preview.py", "windows_msix.py", "scaffold-reference-platforms.py"} {
		if err := add(filepath.Join(root, "scripts", name), "scripts/"+name); err != nil {
			return nil, err
		}
	}
	sort.Slice(c.files, func(i, j int) bool { return c.files[i].name < c.files[j].name })
	var manifest struct {
		ID          string            `json:"id"`
		Version     int               `json:"version"`
		Description string            `json:"description"`
		Runtimes    map[string]string `json:"runtimes"`
	}
	var hashes []any
	for _, f := range c.files {
		if f.name == "blueprint.json" {
			if err := json.Unmarshal(f.body, &manifest); err != nil {
				return nil, err
			}
		}
		hashes = append(hashes, map[string]any{"path": f.name, "sha256": digest(f.body), "executable": f.executable})
	}
	if manifest.ID != "flutter-go-v1" || manifest.Version != 1 || len(manifest.Runtimes) != 5 {
		return nil, fmt.Errorf("unsupported installed blueprint manifest")
	}
	bound, err := json.Marshal(map[string]any{"generatorVersion": 1, "files": hashes})
	if err != nil {
		return nil, err
	}
	c.descriptor = Descriptor{ID: manifest.ID, Version: manifest.Version, Title: "Flutter + Go 多端应用", Description: manifest.Description,
		Digest: digest(bound), Targets: []string{"web", "ios-appstore", "android-google-play", "android-huawei", "android-xiaomi", "android-oppo", "android-vivo", "windows", "macos"}, Modes: []string{"feedback", "booking"},
		Limitations: []string{"后端为单实例 JSON 状态参考实现", "原生工程准备、构建、签名、真机验收和正式发布分别执行", "模板版本不代表生产业务、安全或容量验收"}}
	return c, nil
}

func (c *Catalog) List() []Descriptor {
	if c == nil {
		return []Descriptor{}
	}
	d := c.descriptor
	d.Targets, d.Modes, d.Limitations = append([]string(nil), d.Targets...), append([]string(nil), d.Modes...), append([]string(nil), d.Limitations...)
	return []Descriptor{d}
}

func (c *Catalog) Generate(id, expected string, options Options) ([]byte, int, error) {
	if c == nil || id != c.descriptor.ID || expected != c.descriptor.Digest {
		return nil, 0, fmt.Errorf("蓝图目录版本不可用或已变化，请重新读取目录")
	}
	if err := ValidateOptions(options); err != nil {
		return nil, 0, err
	}
	files := append([]file(nil), c.files...)
	for i, f := range files {
		body := f.body
		if f.name == "client/pubspec.yaml" {
			body = bytes.ReplaceAll(body, []byte("name: delivery_app"), []byte("name: "+options.Name))
		}
		if strings.HasSuffix(f.name, ".dart") {
			body = bytes.ReplaceAll(body, []byte("package:delivery_app/"), []byte("package:"+options.Name+"/"))
		}
		if f.name == "client/lib/main.dart" {
			body = bytes.ReplaceAll(body, []byte("defaultValue: 'feedback'"), []byte("defaultValue: '"+options.Mode+"'"))
		}
		files[i].body = body
	}
	configuration := map[string]any{"name": options.Name, "organization": options.Organization, "mode": options.Mode,
		"blueprint": id, "blueprintVersion": c.descriptor.Version, "catalogDigest": expected, "nativeScaffolded": false, "androidUnsignedRelease": true, "macosDirectDistribution": false, "gradleVersionOverride": nil}
	config, err := json.MarshalIndent(configuration, "", "  ")
	if err != nil {
		return nil, 0, err
	}
	env := "APP_MODE=" + options.Mode + "\nAPP_WEB_ORIGIN=https://app.company.example\nAPP_STATE_FILE=data/state.json\n"
	if options.Mode == "booking" {
		env += "APP_SLOTS=[{\"id\":\"sample-slot\",\"startsAt\":\"2026-12-01T09:00:00+08:00\",\"reserved\":false}]\n"
	}
	files = append(files, file{"application.json", append(config, '\n'), false}, file{"application.env.example", []byte(env), false})
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	for _, f := range files {
		mode := int64(0644)
		if f.executable {
			mode = 0755
		}
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Size: int64(len(f.body)), Mode: mode, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return nil, 0, err
		}
		if _, err := tw.Write(f.body); err != nil {
			return nil, 0, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, 0, err
	}
	if int64(archive.Len()) > source.MaxBytes {
		return nil, 0, fmt.Errorf("blueprint archive exceeds source limits")
	}
	return archive.Bytes(), len(files), nil
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
