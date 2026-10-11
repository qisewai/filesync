package localindex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"filesync/engine"
)

// Directory caches contain only references into an immutable version. Large
// directories use the streaming enumerator instead of accumulating metadata.
const maxCachedDirectory = 4096

type directoryCache struct {
	root *rootData
	ids  map[string]int
	refs [][]uint64
}

func cacheDirectories(ctx context.Context, r *rootData) (*directoryCache, error) {
	c := &directoryCache{root: r, ids: make(map[string]int, len(r.dirs)), refs: make([][]uint64, len(r.dirs))}
	for i, p := range r.dirs {
		c.ids[p] = i
	}
	for ci, ch := range r.chunks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for i, parent := range ch.parent {
			c.refs[parent] = append(c.refs[parent], uint64(ci)<<16|uint64(i))
		}
	}
	return c, nil
}

func (c *directoryCache) read(ctx context.Context, t directoryTask, filter *engine.Filter, root string, blocked func(string) bool, out chan<- scanBatch) bool {
	id, ok := c.ids[t.path]
	if !ok || c.root.dirReady[id] == 0 || len(c.refs[id]) > maxCachedDirectory {
		return false
	}
	before, err := os.Lstat(t.path)
	if err != nil || !before.IsDir() || isLink(before) || before.ModTime().UnixNano() != c.root.dirStamp[id] {
		return false
	}
	entries := make([]Entry, 0, len(c.refs[id]))
	for _, ref := range c.refs[id] {
		if ctx.Err() != nil {
			return true
		}
		ch, row := c.root.chunks[ref>>16], int(uint16(ref))
		name := ch.name(row)
		p := filepath.Join(t.path, name)
		if blocked(p) {
			continue
		}
		st, err := os.Lstat(p)
		// A name disappeared even though the timestamp was unchanged (or was
		// restored by another program). Enumerate this directory afresh.
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		if err != nil {
			return false
		} // full path preserves unreadable old records
		rel, _ := filepath.Rel(root, p)
		if filter.Excluded(filepath.ToSlash(rel), st.IsDir()) {
			continue
		}
		if !st.Mode().IsRegular() && !st.IsDir() && !isLink(st) {
			continue
		}
		e := metadataEntry(name, p, t.path, t.hidden, st)
		entries = append(entries, e)
	}
	after, err := os.Lstat(t.path)
	if err != nil || !os.SameFile(before, after) || before.ModTime() != after.ModTime() {
		return false
	}
	for start := 0; ; start += 256 {
		end := min(start+256, len(entries))
		last := end == len(entries)
		batch := scanBatch{task: t, entries: entries[start:end], done: last, stamp: after.ModTime().UnixNano(), ready: true, reused: true}
		select {
		case out <- batch:
		case <-ctx.Done():
			return true
		}
		if last {
			return true
		}
	}
}

func metadataEntry(name, p, parent string, hidden bool, st os.FileInfo) Entry {
	e := Entry{Name: name, Path: p, Parent: parent, IsDir: st.IsDir(), Size: st.Size(), MTime: st.ModTime().Unix(), Link: isLink(st), Hidden: hidden || strings.HasPrefix(name, ".") || isHidden(st)}
	if e.IsDir {
		e.Size = 0
	} else {
		e.Ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	}
	return e
}
