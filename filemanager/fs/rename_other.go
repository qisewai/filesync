//go:build !linux && !windows

package fs

func renameNoReplace(from, to string) error { return ErrUnsupported }
