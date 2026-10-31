package fs

import (
	"filemanager/internal/winrename"
	"os"
	"path/filepath"
)

func rootRename(src *os.Root, from string, dst *os.Root, to string, replace bool) error {
	a, err := src.Open(filepath.Dir(from))
	if err != nil {
		return err
	}
	defer a.Close()
	b, err := dst.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer b.Close()
	return winrename.At(a, filepath.Base(from), b, filepath.Base(to), replace)
}
