package sqliteutil

import (
	"net/url"
	"testing"
)

func TestDSNPreservesWindowsDrivesAndEscapesFilenames(t *testing.T) {
	for _, test := range []struct{ input, path string }{
		{`C:\Release Store\publisher.db`, "/C:/Release Store/publisher.db"},
		{"/private/release store/a?b#c.db", "/private/release store/a?b#c.db"},
	} {
		u, err := url.Parse(DSN(test.input, false))
		if err != nil || u.Scheme != "file" || u.Host != "" || u.Path != test.path || u.Query().Get("_txlock") != "immediate" {
			t.Fatalf("SQLite URI changed drive authority or filename: %+v %v", u, err)
		}
		readOnly, _ := url.Parse(DSN(test.input, true))
		if readOnly.Query().Get("mode") != "ro" {
			t.Fatal("public service database became writable")
		}
	}
}
