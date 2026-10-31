//go:build linux

package catalog

import (
	"golang.org/x/sys/unix"
	"os"
)

func openHash(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
}
