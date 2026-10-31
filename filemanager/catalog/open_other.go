//go:build !linux

package catalog

import "os"

func openHash(root *os.Root, path string) (*os.File, error) { return root.Open(path) }
