package fs

import (
	"golang.org/x/sys/windows"
	"os"
	"syscall"
)

func renameNoReplace(from, to string) error {
	a, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	b, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	err = windows.MoveFile(a, b)
	if err == nil {
		return nil
	}
	if err == windows.ERROR_NOT_SAME_DEVICE {
		err = syscall.EXDEV
	}
	return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
}
