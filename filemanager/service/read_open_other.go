//go:build !linux

package service

import "os"

func openReadFile(root *os.Root, path string) (*os.File, error) {
	return root.Open(path)
}
