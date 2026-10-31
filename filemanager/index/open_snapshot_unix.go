//go:build !windows

package index

import "os"

func openSnapshot(path string) (*os.File, error) { return os.Open(path) }
