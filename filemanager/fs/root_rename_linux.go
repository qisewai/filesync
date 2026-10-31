package fs

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

func rootRename(src *os.Root, from string, dst *os.Root, to string, replace bool) error {
	a, err := src.Open(filepath.Dir(from))
	if err != nil {
		return err
	}
	defer a.Close()
	b, err := dst.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer b.Close()
	flags := uint(unix.RENAME_NOREPLACE)
	if replace {
		flags = 0
	}
	err = unix.Renameat2(int(a.Fd()), filepath.Base(from), int(b.Fd()), filepath.Base(to), flags)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}
