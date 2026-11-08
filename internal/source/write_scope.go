package source

import (
	"fmt"
	"sort"
	"strings"
)

// ParseWritePaths validates a present approval field. Absence is deliberately
// handled by callers: null and an empty array must never grant repository scope.
func ParseWritePaths(value any) ([]string, error) {
	var values []any
	switch v := value.(type) {
	case []any:
		values = v
	case []string:
		for _, name := range v {
			values = append(values, name)
		}
	default:
		return nil, fmt.Errorf("writePaths must be an array of literal repository paths")
	}
	if len(values) < 1 || len(values) > 128 {
		return nil, fmt.Errorf("writePaths requires 1–128 paths")
	}
	paths := make([]string, 0, len(values))
	var seen names
	for _, value := range values {
		name, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("writePaths entries must be strings")
		}
		literal := strings.TrimSuffix(name, "/")
		if err := validateName(literal); err != nil {
			return nil, fmt.Errorf("invalid writePaths entry %q: %w", name, err)
		}
		if err := seen.add(literal); err != nil {
			return nil, fmt.Errorf("ambiguous or duplicate writePaths entry %q: %w", name, err)
		}
		paths = append(paths, name)
	}
	sort.Strings(paths)
	return paths, nil
}

// AllowsWrite matches files exactly and directory descendants by slash boundary.
// Callers use nil only for a genuinely absent (legacy repository-wide) field.
func AllowsWrite(paths []string, name string) bool {
	if validateName(name) != nil {
		return false
	}
	if paths == nil {
		return true
	}
	for _, entry := range paths {
		if name == entry || strings.HasSuffix(entry, "/") && strings.HasPrefix(name, entry) {
			return true
		}
	}
	return false
}
