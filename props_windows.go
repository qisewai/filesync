package main

import (
	"os"
	"syscall"
	"time"
)

// statMore reads what os.FileInfo leaves to the platform.
func statMore(fi os.FileInfo) (x fileStatMore) {
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		x.atime = time.Unix(0, d.LastAccessTime.Nanoseconds())
		x.btime = time.Unix(0, d.CreationTime.Nanoseconds())
	}
	return
}
