package index

import (
	"os"
	"syscall"
	"unsafe"
)

func isLink(st os.FileInfo) bool {
	if st.Mode()&os.ModeSymlink != 0 {
		return true
	}
	data, ok := st.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&0x400 != 0
}
func isHidden(st os.FileInfo) bool {
	data, ok := st.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&2 != 0
}
func BlockedMounts() []string {
	getDriveType := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDriveTypeW")
	var out []string
	for d := 'A'; d <= 'Z'; d++ {
		root := string(d) + `:\`
		p, _ := syscall.UTF16PtrFromString(root)
		kind, _, _ := getDriveType.Call(uintptr(unsafe.Pointer(p)))
		if kind == 4 {
			out = append(out, root)
		}
	}
	return out
}
