package service

import (
	"errors"
	"io/fs"
	"syscall"

	"golang.org/x/sys/windows"
)

func metadataLocked(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

func hiddenFileInfo(fi fs.FileInfo) bool {
	data, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_HIDDEN != 0
}
