// Package fs contains filesystem primitives shared by desktop and server.
package fs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

var ErrSourceChanged = errors.New("source changed during transfer")
var ErrUnsupported = errors.New("unsupported file type")

type sourceEntry struct {
	path string
	info fs.FileInfo
}

// Receipt records the source BEFORE copying. It is only eligible for deletion
// after every recorded entry was copied successfully, with no skipped entries.
// It is not a filesystem snapshot and cannot protect against hostile writers.
type Receipt struct{ entries []sourceEntry }

func Capture(ctx context.Context, source string) (*Receipt, error) {
	r := &Receipt{}
	err := filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%w: %s", ErrUnsupported, path)
		}
		r.entries = append(r.entries, sourceEntry{path, info})
		return nil
	})
	return r, err
}

func unchanged(e sourceEntry) error {
	now, err := os.Lstat(e.path)
	if err != nil {
		return err
	}
	if !os.SameFile(e.info, now) || e.info.Mode().Type() != now.Mode().Type() ||
		(!now.IsDir() && (e.info.Size() != now.Size() || !e.info.ModTime().Equal(now.ModTime()))) {
		return fmt.Errorf("%w: %s", ErrSourceChanged, e.path)
	}
	return nil
}

// RemoveCopied removes only recorded entries, children first, never RemoveAll.
// New children make directory removal fail and survive. Validate the whole
// receipt first, then recheck each entry immediately before removing it.
func (r *Receipt) RemoveCopied(ctx context.Context) error {
	for _, e := range r.entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := unchanged(e); err != nil {
			return err
		}
	}
	for i := len(r.entries) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		e := r.entries[i]
		if err := unchanged(e); err != nil {
			return err
		}
		if err := os.Remove(e.path); err != nil {
			return err
		}
	}
	return nil
}
