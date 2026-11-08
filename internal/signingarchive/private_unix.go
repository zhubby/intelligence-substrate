//go:build unix

package signingarchive

import (
	"os"
	"syscall"
)

func openPrivate(path string, directory bool) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil || (directory && !before.IsDir()) || (!directory && !before.Mode().IsRegular()) {
		return nil, errPrivate
	}
	flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	if directory {
		flags |= syscall.O_DIRECTORY
	}
	fd, err := syscall.Open(path, flags, 0)
	if err != nil {
		return nil, errPrivate
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, errPrivate
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0077 != 0 || !os.SameFile(before, info) {
		f.Close()
		return nil, errPrivate
	}
	if !directory && owner.Nlink != 1 {
		f.Close()
		return nil, errPrivate
	}
	return f, nil
}
