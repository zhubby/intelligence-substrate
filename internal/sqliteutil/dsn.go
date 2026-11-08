package sqliteutil

import (
	"net/url"
	"path/filepath"
	"strings"
)

// DSN keeps a Windows volume in the URI path, not its authority, and escapes
// filename query/fragment characters. Read-only consumers cannot create state.
func DSN(filename string, readOnly bool) string {
	name := filepath.ToSlash(filename)
	if len(name) >= 3 && ((name[0] >= 'A' && name[0] <= 'Z') || (name[0] >= 'a' && name[0] <= 'z')) && name[1] == ':' && (name[2] == '/' || name[2] == '\\') {
		name = "/" + strings.ReplaceAll(name, "\\", "/")
	}
	query := url.Values{"_pragma": {"busy_timeout(5000)"}}
	if readOnly {
		query.Set("mode", "ro")
	} else {
		query.Set("_txlock", "immediate")
	}
	u := url.URL{Scheme: "file", Path: name, RawQuery: query.Encode()}
	return u.String()
}
