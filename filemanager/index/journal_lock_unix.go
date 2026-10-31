//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package index

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockJournal(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func syncJournalDirectory(path string) error {
	parent, err := os.Open(path)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
