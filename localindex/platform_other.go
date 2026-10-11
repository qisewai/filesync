//go:build !linux && !windows

package localindex

import "os"

func isLink(st os.FileInfo) bool   { return st.Mode()&os.ModeSymlink != 0 }
func isHidden(st os.FileInfo) bool { return false }
func BlockedMounts() []string      { return nil }
