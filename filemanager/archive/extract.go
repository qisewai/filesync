package archive

import (
	"context"
	"errors"
	"io"
	"sort"
)

// Extract visits selected directories first, then file bodies in archive order.
// The caller owns disk writes and must verify each body's size and EOF before
// committing a file. Links are excluded by Selection; skipped bodies need not
// be consumed. Selection is processed in one traversal instead of a separate
// Copy call for each member.
func (idx *Index) Extract(ctx context.Context, filename string, selected map[string]string, put func(Entry, io.Reader) error) error {
	if err := idx.unchanged(filename); err != nil {
		return err
	}
	release, err := acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	dirs := []string{}
	for p := range selected {
		if idx.Entries[p].IsDir {
			dirs = append(dirs, p)
		}
	}
	sort.Strings(dirs)
	remaining := len(selected)
	for _, p := range dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		e := idx.Entries[p]
		e.Path = selected[p]
		if err := put(e, nil); err != nil {
			return err
		}
		remaining--
	}
	if remaining == 0 {
		return idx.unchanged(filename)
	}
	err = walk(ctx, filename, true, func(e Entry, r io.Reader) error {
		p, err := Clean(e.Path)
		if err != nil {
			return err
		}
		name, ok := selected[p]
		if !ok || e.IsDir {
			return nil
		}
		e = idx.Entries[p]
		e.Path = name
		if err := put(e, r); err != nil {
			return err
		}
		remaining--
		if remaining == 0 {
			return errStop
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStop) {
		return err
	}
	if remaining != 0 {
		return errors.New("压缩包解压不完整")
	}
	return idx.unchanged(filename)
}
