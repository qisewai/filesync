//go:build !linux

package service

import (
	"filemanager/api"
	"os"
)

func renameExclusive(from *os.File, old string, to *os.File, name string) error {
	return &api.Error{Code: "unsupported", Message: "atomic no-replace rename is not implemented on this platform"}
}
