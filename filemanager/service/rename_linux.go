package service

import (
	"golang.org/x/sys/unix"
	"os"
)

// Parent directories are already open and confined to their configured roots.
func renameExclusive(from *os.File, old string, to *os.File, name string) error {
	return unix.Renameat2(int(from.Fd()), old, int(to.Fd()), name, unix.RENAME_NOREPLACE)
}
