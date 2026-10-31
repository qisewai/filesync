package main

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/egoist/mygo"
)

// All helpers started during this frontend session use the same binary.
var sessionCompanion string

func companionExecutable() string {
	if sessionCompanion != "" {
		return sessionCompanion
	}
	if exe := os.Getenv("FILESYNC_FILEMANAGER"); exe != "" {
		return exe
	}
	name := "filemanager"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if dir, err := mygo.App.Path(mygo.PathResources); err == nil {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(self), name)
}

func prepareCompanion() (string, func(), error) {
	return snapshotCompanion(companionExecutable())
}

func snapshotCompanion(source string) (string, func(), error) {
	noop := func() {}
	in, err := os.Open(source)
	if err != nil {
		return "", noop, err
	}
	defer in.Close()
	dir, err := os.MkdirTemp("", "filesync-session-")
	if err != nil {
		return "", noop, err
	}
	cleanup := sync.OnceFunc(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, filepath.Base(source))
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		cleanup()
		return "", noop, err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		cleanup()
		return "", noop, copyErr
	}
	if closeErr != nil {
		cleanup()
		return "", noop, closeErr
	}
	return path, cleanup, nil
}
