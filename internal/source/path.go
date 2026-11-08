// Package source handles bounded complete source trees, never Git metadata.
package source

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Path accepts portable, canonical repository paths and refuses existing links.
func Path(root, name string) (string, error) {
	if info, err := os.Lstat(root); err != nil {
		return "", err
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("invalid source root")
	}
	if err := validateName(name); err != nil {
		return "", err
	}
	current := root
	for _, part := range strings.Split(name, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("source path traverses a link")
		}
	}
	return current, nil
}

// validateName is filesystem independent so approval and execution use the
// same portable, literal repository path syntax.
func validateName(name string) error {
	if name == "" || name == "." || len(name) > 1024 || !utf8.ValidString(name) || path.Clean(name) != name || path.IsAbs(name) || strings.ContainsAny(name, "\\:*?\"<>|") {
		return fmt.Errorf("invalid source path")
	}
	for _, part := range strings.Split(name, "/") {
		lower := strings.ToLower(part)
		device := strings.Split(strings.ToUpper(part), ".")[0]
		if part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || lower == ".git" || strings.HasPrefix(lower, ".git~") || lower == ".intelligence-artifact" || device == "CON" || device == "PRN" || device == "AUX" || device == "NUL" || (len(device) == 4 && (strings.HasPrefix(device, "COM") || strings.HasPrefix(device, "LPT")) && device[3] >= '1' && device[3] <= '9') {
			return fmt.Errorf("reserved source path")
		}
		for _, r := range part {
			if unicode.IsControl(r) {
				return fmt.Errorf("control character in source path")
			}
		}
	}
	return nil
}

type names struct {
	aliases  map[string]string
	explicit map[string]bool
}

func (n *names) add(name string) error {
	if n.aliases == nil {
		n.aliases, n.explicit = map[string]string{}, map[string]bool{}
	}
	if n.explicit[name] {
		return fmt.Errorf("duplicate source entry")
	}
	n.explicit[name] = true
	parts := strings.Split(name, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		alias := strings.ToLower(norm.NFC.String(prefix))
		if previous, ok := n.aliases[alias]; ok && previous != prefix {
			return fmt.Errorf("ambiguous source path")
		}
		n.aliases[alias] = prefix
	}
	return nil
}
