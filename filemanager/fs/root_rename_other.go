//go:build !linux && !windows

package fs

import "os"

func rootRename(src *os.Root, from string, dst *os.Root, to string, replace bool) error {
	return ErrUnsupported
}
