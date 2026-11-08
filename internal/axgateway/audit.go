package axgateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type auditLog struct{ file *os.File }

func openAudit(path string) (*auditLog, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("audit requires an absolute private-node path")
	}
	before, e := os.Lstat(path)
	var file *os.File
	if os.IsNotExist(e) {
		file, e = os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL, 0600)
	} else if e == nil && before.Mode().IsRegular() && before.Mode().Perm()&0077 == 0 {
		file, e = os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	} else {
		return nil, fmt.Errorf("audit path is unavailable or unsafe")
	}
	if e != nil {
		return nil, fmt.Errorf("audit path could not be opened")
	}
	after, e := file.Stat()
	if e != nil || !after.Mode().IsRegular() || after.Mode().Perm()&0077 != 0 || before != nil && !os.SameFile(before, after) {
		file.Close()
		return nil, fmt.Errorf("audit path changed while opening")
	}
	return &auditLog{file: file}, nil
}
func (a *auditLog) Append(event Event) error {
	body, e := json.Marshal(event)
	if e != nil {
		return e
	}
	body = append(body, '\n')
	n, e := a.file.Write(body)
	if e != nil {
		return e
	}
	if n != len(body) {
		return fmt.Errorf("incomplete audit write")
	}
	return a.file.Sync()
}
func (a *auditLog) Close() error { return a.file.Close() }
