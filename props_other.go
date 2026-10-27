//go:build !linux && !windows

package main

import "os"

// statMore reads what os.FileInfo leaves to the platform.
func statMore(fi os.FileInfo) fileStatMore { return fileStatMore{} }
