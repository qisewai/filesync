package localtrash

import (
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	shell32      = syscall.NewLazyDLL("shell32.dll")
	shFileOpProc = shell32.NewProc("SHFileOperationW")
)

// shFileOpStruct is SHFILEOPSTRUCTW, as laid out on 64-bit Windows.
type shFileOpStruct struct {
	hwnd                  uintptr
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

const (
	foDelete          = 0x3
	fofSilent         = 0x4
	fofNoConfirmation = 0x10
	fofAllowUndo      = 0x40
	fofNoErrorUI      = 0x400
	// fofWantNukeWarning makes a file the recycle bin cannot take fail,
	// rather than be deleted for good without asking.
	fofWantNukeWarning = 0x4000
)

// move sends p to the recycle bin, as Explorer deletes.
func move(p string) error {
	p, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	from, err := syscall.UTF16FromString(p)
	if err != nil {
		return err
	}
	from = append(from, 0) // the list ends with an empty name
	op := shFileOpStruct{
		wFunc:  foDelete,
		pFrom:  &from[0],
		fFlags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI | fofWantNukeWarning,
	}
	r, _, _ := shFileOpProc.Call(uintptr(unsafe.Pointer(&op)))
	if r != 0 {
		return fmt.Errorf("无法移入回收站：%s（错误 %#x）", p, r)
	}
	if op.fAnyOperationsAborted != 0 {
		return fmt.Errorf("没有移入回收站：%s", p)
	}
	return nil
}
