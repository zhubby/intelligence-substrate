package blueprint

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/intelligence-substrate/platform/internal/source"
)

func TestCatalogGeneratesDeterministicBinaryCompleteApplications(t *testing.T) {
	c, err := Load("../..")
	if err != nil {
		t.Fatal(err)
	}
	d := c.List()[0]
	for _, mode := range []string{"feedback", "booking"} {
		t.Run(mode, func(t *testing.T) {
			options := Options{Name: "enterprise_" + mode, Organization: "com.enterprise", Mode: mode}
			body, count, err := c.Generate(d.ID, d.Digest, options)
			if err != nil {
				t.Fatal(err)
			}
			again, _, err := c.Generate(d.ID, d.Digest, options)
			if err != nil || !bytes.Equal(body, again) {
				t.Fatal("identical selection produced different source bytes", err)
			}
			root := t.TempDir()
			entries, _, err := source.Extract(bytes.NewReader(body), root)
			if err != nil || len(entries) != count {
				t.Fatal("generated archive is not a bounded source snapshot", err)
			}
			python, err := exec.LookPath("python3")
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(python, "scripts/build-reference-client.py", "--help")
			command.Dir = root
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("generated build entry lost a runtime dependency: %v %s", err, output)
			}
			var config map[string]any
			content, err := os.ReadFile(filepath.Join(root, "application.json"))
			if err != nil || json.Unmarshal(content, &config) != nil || config["name"] != options.Name || config["catalogDigest"] != d.Digest || config["nativeScaffolded"] != false {
				t.Fatal("incorrect application identity/provenance", config, err)
			}
			main, _ := os.ReadFile(filepath.Join(root, "client/lib/main.dart"))
			if !bytes.Contains(main, []byte("defaultValue: '"+mode+"'")) {
				t.Fatal("wrong business mode")
			}
			test, _ := os.ReadFile(filepath.Join(root, "client/test/widget_test.dart"))
			if !bytes.Contains(test, []byte("package:"+options.Name+"/")) || bytes.Contains(test, []byte("package:delivery_app/")) {
				t.Fatal("widget tests still import the placeholder application")
			}
			font := "client/assets/fonts/NotoSansSC-Regular.otf"
			expected, _ := os.ReadFile(filepath.Join("../../blueprints/flutter-go", font))
			actual, _ := os.ReadFile(filepath.Join(root, font))
			if len(actual) == 0 || !bytes.Equal(expected, actual) {
				t.Fatal("binary source asset was lost")
			}
			var canonical bytes.Buffer
			if err = source.Write(&canonical, root, entries); err != nil || !bytes.Equal(body, canonical.Bytes()) {
				t.Fatal("worker's canonical source snapshot changed frozen bytes", err)
			}
		})
	}
}

func catalogCopy(t *testing.T, c *Catalog) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range c.files {
		path := filepath.Join(root, "blueprints/flutter-go", f.name)
		if strings.HasPrefix(f.name, "scripts/") {
			path = filepath.Join(root, f.name)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0600)
		if f.executable {
			mode |= 0100
		}
		if err := os.WriteFile(path, f.body, mode); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestCatalogAssetChangesCannotMutateLoadedSelections(t *testing.T) {
	c, err := Load("../..")
	if err != nil {
		t.Fatal(err)
	}
	root := catalogCopy(t, c)
	loaded, err := Load(root)
	if err != nil || loaded.List()[0].Digest != c.List()[0].Digest {
		t.Fatal("copy changed catalog", err)
	}
	d := loaded.List()[0]
	options := Options{Name: "enterprise_app", Organization: "com.example", Mode: "feedback"}
	before, _, _ := loaded.Generate(d.ID, d.Digest, options)
	path := filepath.Join(root, "scripts/scaffold-reference-platforms.py")
	if err = os.WriteFile(path, []byte("raise Exception('changed administrator asset')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after, _, err := loaded.Generate(d.ID, d.Digest, options)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("loaded catalog changed when disk assets changed", err)
	}
	reloaded, err := Load(root)
	if err != nil || reloaded.List()[0].Digest == d.Digest {
		t.Fatal("script bytes omitted from version digest", err)
	}
	if _, _, err = reloaded.Generate(d.ID, d.Digest, options); err == nil {
		t.Fatal("stale catalog digest accepted")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("/etc/hosts", path); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(root); err == nil {
		t.Fatal("linked helper was loaded")
	}
}

func TestCatalogRejectsArbitraryIdentityAndGeneratorInputs(t *testing.T) {
	c, err := Load("../..")
	if err != nil {
		t.Fatal(err)
	}
	d := c.List()[0]
	for _, options := range []Options{{"../escape", "com.example", "feedback"}, {"safe_app", "com.example;env", "feedback"}, {"safe_app", "com.example", "custom"}, {"safe_app", strings.Repeat("a", 161) + ".b", "feedback"}} {
		if _, _, err := c.Generate(d.ID, d.Digest, options); err == nil {
			t.Fatal("unbounded generator input accepted", options)
		}
	}
	if catalog, err := Load("-"); err != nil || len(catalog.List()) != 0 {
		t.Fatal("disabled catalog is not empty", err)
	}
}
