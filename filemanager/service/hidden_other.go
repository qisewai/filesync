//go:build !windows

package service

import "io/fs"

func hiddenFileInfo(fi fs.FileInfo) bool { return false }

func metadataLocked(err error) bool { return false }
