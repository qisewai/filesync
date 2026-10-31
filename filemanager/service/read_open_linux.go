//go:build linux

package service

import (
	"golang.org/x/sys/unix"
	"os"
)

// Nonblocking open prevents a concurrently substituted FIFO from hanging a
// request. The caller verifies the opened descriptor is a regular file.
func openReadFile(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
}
