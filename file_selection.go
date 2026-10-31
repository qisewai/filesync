package main

import (
	fileindex "filemanager/remoteclient"
	"github.com/egoist/mygo/ui"
)

// Cache positions, not entry copies: metadata stays current and callers own
// their result slices. Bound the cache so Select All doesn't duplicate a large
// selection map. Empty selections never traverse the directory.
type fileSelectionCache struct {
	first     *fileindex.Entry
	count     int
	revision  uint64
	keys      map[string]struct{}
	positions []int
}

func (cache *fileSelectionCache) chosen(items []fileindex.Entry, selected *ui.Selection[string], revision uint64) []fileindex.Entry {
	if selected.Len() == 0 || len(items) == 0 {
		*cache = fileSelectionCache{}
		return nil
	}
	const maxCachedSelection = 256
	if selected.Len() > maxCachedSelection {
		*cache = fileSelectionCache{}
		var out []fileindex.Entry
		for _, entry := range items {
			if selected.Has(entry.Path) {
				out = append(out, entry)
			}
		}
		return out
	}
	valid := cache.first == &items[0] && cache.count == len(items) && cache.revision == revision && len(cache.keys) == selected.Len()
	if valid {
		for key := range cache.keys {
			if !selected.Has(key) {
				valid = false
				break
			}
		}
		for _, i := range cache.positions {
			if !selected.Has(items[i].Path) {
				valid = false
				break
			}
		}
	}
	if !valid {
		cache.first, cache.count, cache.revision = &items[0], len(items), revision
		cache.keys = make(map[string]struct{}, selected.Len())
		for key := range selected.All() {
			cache.keys[key] = struct{}{}
		}
		cache.positions = cache.positions[:0]
		for i, entry := range items {
			if selected.Has(entry.Path) {
				cache.positions = append(cache.positions, i)
			}
		}
	}
	out := make([]fileindex.Entry, len(cache.positions))
	for i, at := range cache.positions {
		out[i] = items[at]
	}
	return out
}
