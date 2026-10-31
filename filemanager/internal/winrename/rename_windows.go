// Package winrename provides handle-relative atomic rename on Windows.
package winrename

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// At accepts only leaf names under already-confined, pinned parent directories.
// Opening the leaf as a reparse point renames links rather than their targets.
func At(from *os.File, old string, to *os.File, name string, replace bool) error {
	valid := func(s string) bool {
		return s != "" && s != "." && s != ".." && filepath.Base(s) == s && !strings.ContainsAny(s, `\/:`)
	}
	if !valid(old) || !valid(name) {
		return syscall.EINVAL
	}
	objectName, err := windows.NewNTUnicodeString(old)
	if err != nil {
		return err
	}
	oa := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(from.Fd()), ObjectName: objectName, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var h windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, windows.DELETE|windows.SYNCHRONIZE, &oa, &status, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return linkError(old, name, err)
	}
	defer windows.CloseHandle(h)
	utf16, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	// FILE_RENAME_INFORMATION uses a BOOLEAN followed by pointer alignment.
	type renameInfo struct {
		Replace uint32
		Root    windows.Handle
		Length  uint32
		Name    [1]uint16
	}
	var layout renameInfo
	offset := unsafe.Offsetof(layout.Name)
	buf := make([]byte, int(offset)+2*len(utf16))
	info := (*renameInfo)(unsafe.Pointer(&buf[0]))
	if replace {
		info.Replace = 1
	}
	info.Root = windows.Handle(to.Fd())
	info.Length = uint32(2 * (len(utf16) - 1))
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buf[offset])), len(utf16)), utf16)
	err = windows.NtSetInformationFile(h, &status, &buf[0], uint32(len(buf)), windows.FileRenameInformation)
	if err != nil {
		return linkError(old, name, err)
	}
	return nil
}
func linkError(old, name string, err error) error {
	if status, ok := err.(windows.NTStatus); ok {
		err = status.Errno()
	}
	if err == windows.ERROR_NOT_SAME_DEVICE {
		err = syscall.EXDEV
	}
	return &os.LinkError{Op: "rename", Old: old, New: name, Err: err}
}
