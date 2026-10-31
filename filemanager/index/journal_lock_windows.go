package index

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockJournal(f *os.File) error {
	var overlap windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlap)
}

// Directory fsync is unavailable on Windows; the file itself is synced.
func syncJournalDirectory(string) error { return nil }
