//go:build !unix

package signingarchive

import "os"

func openPrivate(string, bool) (*os.File, error) {
	return nil, errPrivate
}
