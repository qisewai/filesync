package service

import (
	"filemanager/internal/winrename"
	"os"
)

func renameExclusive(from *os.File, old string, to *os.File, name string) error {
	return winrename.At(from, old, to, name, false)
}
