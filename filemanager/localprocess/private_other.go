//go:build !windows

package localprocess

// os.MkdirTemp creates mode 0700 on Unix.
func protectRuntimeDir(string) error { return nil }
