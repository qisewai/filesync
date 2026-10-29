//go:build !windows

package localindex

import "os"

func openSnapshot(path string) (*os.File, error) { return os.Open(path) }
