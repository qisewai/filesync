//go:build !linux

package fs

func renameNoReplace(from, to string) error { return ErrUnsupported }
