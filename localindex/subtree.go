package localindex

import (
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Subtree is what the index holds below a folder.
type Subtree struct {
	Files, Dirs, Size int64
	// Updated is when the index was last brought up to date.
	Updated time.Time
}

// Subtree counts the files, their size and the folders the index holds
// below dir, dir itself left out; ok is false when the index holds no
// such folder: not indexed, or excluded.
func (x *Index) Subtree(dir string) (Subtree, bool) {
	s := x.snap.Load()
	if s == nil {
		return Subtree{}, false
	}
	dir = filepath.Clean(dir)
	for _, r := range s.roots {
		if !Within(dir, r.path) {
			continue
		}
		// The folders at dir or below it, by their ids: their paths are
		// clean, so a prefix tells.
		fold := func(p string) string { return p }
		if runtime.GOOS == "windows" {
			fold = strings.ToLower
		}
		at, under := fold(dir), fold(dir)
		if !strings.HasSuffix(under, string(filepath.Separator)) {
			under += string(filepath.Separator)
		}
		below := make([]bool, len(r.dirs))
		found := false
		for id, p := range r.dirs {
			p = fold(p)
			if p == at {
				below[id], found = true, true
			} else if strings.HasPrefix(p, under) {
				below[id] = true
			}
		}
		if !found {
			continue
		}
		out := Subtree{Updated: s.updated}
		for _, c := range r.chunks {
			for i, parent := range c.parent {
				if int(parent) >= len(below) || !below[parent] {
					continue
				}
				if c.flags[i]&flagDir != 0 {
					out.Dirs++
				} else {
					out.Files++
					out.Size += c.size[i]
				}
			}
		}
		return out, true
	}
	return Subtree{}, false
}
