package service

import (
	"errors"
	"filemanager/api"
	"filemanager/localpath"
	"golang.org/x/sys/windows"
	"os"
)

// Only RunLocal's explicit desktop sentinel invokes this constructor. Peer
// sharing and server roots continue to use New and cannot enable desktop APIs.
func newDesktopService() (*Service, error) {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil, err
	}
	var roots []api.Root
	for i := 0; i < 26; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		path := string(rune('A'+i)) + `:\`
		// Empty optical drives and unavailable mapped drives are not usable roots.
		if _, err := os.Stat(path); err != nil {
			continue
		}
		roots = append(roots, api.Root{ID: localpath.Location(path).Root, Name: path, Path: path})
	}
	if len(roots) == 0 {
		return nil, errors.New("no accessible Windows drives")
	}
	s, err := New(roots)
	if err == nil {
		s.desktop = true
	}
	return s, err
}
