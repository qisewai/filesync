//go:build linux

package index

import (
	"os"
	"runtime"
	"syscall"
)

// Snapshot files are immutable: writers publish a new inode by rename. Keeping
// this object on every block pins the mapping for old query versions too.
type snapshotMapping struct{ data []byte }

func mapSnapshot(f *os.File, size int64) *snapshotMapping {
	if size <= 0 || int64(int(size)) != size {
		return nil
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil
	}
	m := &snapshotMapping{data: data}
	runtime.SetFinalizer(m, func(m *snapshotMapping) { _ = syscall.Munmap(m.data) })
	return m
}
