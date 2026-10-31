//go:build !linux && !windows

package index

import "os"

func isLink(st os.FileInfo) bool   { return st.Mode()&os.ModeSymlink != 0 }
func isHidden(st os.FileInfo) bool { return false }
func BlockedMounts() []string      { return nil }
