package index

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// directoryTree encodes directory names with parent IDs. Existing IDs remain
// unchanged because file chunks refer to them; missing ancestors can occur
// when a partial scan preserves an unreadable range and are appended.
func directoryTree(r *rootData) ([]string, []uint32, []int64, []byte, error) {
	if len(r.dirs) == 0 || r.dirs[0] != r.path || len(r.dirStamp) != len(r.dirs) || len(r.dirReady) != len(r.dirs) {
		return nil, nil, nil, nil, ErrSnapshotFormat
	}
	dirs := append([]string(nil), r.dirs...)
	ids := make(map[string]uint32, len(dirs))
	for i, p := range dirs {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || !Within(p, r.path) {
			return nil, nil, nil, nil, ErrSnapshotFormat
		}
		if _, ok := ids[p]; ok {
			return nil, nil, nil, nil, fmt.Errorf("重复的索引目录: %s", p)
		}
		ids[p] = uint32(i)
	}
	names := make([]string, len(dirs))
	parents := make([]uint32, len(dirs))
	stamps := append([]int64(nil), r.dirStamp...)
	ready := append([]byte(nil), r.dirReady...)
	for i := 1; i < len(dirs); i++ {
		p := dirs[i]
		name, parent := filepath.Base(p), filepath.Dir(p)
		if p == r.path || name == "." || parent == p || !Within(parent, r.path) {
			return nil, nil, nil, nil, ErrSnapshotFormat
		}
		id, ok := ids[parent]
		if !ok {
			id = uint32(len(dirs))
			ids[parent] = id
			dirs = append(dirs, parent)
			names = append(names, "")
			parents = append(parents, 0)
			stamps = append(stamps, 0)
			ready = append(ready, 0)
		}
		names[i], parents[i] = name, id
	}
	return names, parents, stamps, ready, nil
}

// dirPath resolves a directory ID without storing complete paths for every
// node. Directory links have been validated when built or loaded.
func (r *rootData) dirPath(id uint32) string {
	parts := make([]string, 0, 8)
	for id != 0 {
		parts = append(parts, r.dirs[id])
		id = r.dirParents[id]
	}
	slices.Reverse(parts)
	return filepath.Join(append([]string{r.path}, parts...)...)
}

// directoryPaths validates references and reconstructs the existing in-memory
// directory cache. The on-disk format does not duplicate ancestor paths.
func directoryPaths(root string, names []string, parents []uint32) ([]string, error) {
	return directoryPathsContext(context.Background(), root, names, parents)
}

func directoryPathsContext(ctx context.Context, root string, names []string, parents []uint32) ([]string, error) {
	if len(names) == 0 || len(names) != len(parents) || names[0] != "" || parents[0] != 0 || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, ErrSnapshotFormat
	}
	paths := make([]string, len(names))
	state := make([]byte, len(names))
	paths[0], state[0] = root, 2
	seen := map[string]bool{root: true}
	for i := 1; i < len(names); i++ {
		if i%256 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var chain []int
		for j := i; state[j] != 2; {
			if len(chain)%256 == 0 && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if state[j] == 1 || int(parents[j]) >= len(names) {
				return nil, ErrSnapshotFormat
			}
			name := names[j]
			if name == "" || name == "." || name == ".." || filepath.Base(name) != name || filepath.VolumeName(name) != "" || strings.ContainsRune(name, 0) {
				return nil, ErrSnapshotFormat
			}
			state[j] = 1
			chain = append(chain, j)
			j = int(parents[j])
		}
		for k := len(chain) - 1; k >= 0; k-- {
			j := chain[k]
			paths[j] = filepath.Join(paths[parents[j]], names[j])
			if !Within(paths[j], root) || seen[paths[j]] {
				return nil, ErrSnapshotFormat
			}
			seen[paths[j]], state[j] = true, 2
		}
	}
	return paths, nil
}

// The cache belongs to an immutable snapshot, not individual search results.
// Cancellation must never leave a partial cache installed.
const directoryCacheBudget = 64 << 20 // retained bytes per immutable snapshot

type resolvedDirectories struct {
	paths, folded []string
	hidden        []bool
	restricted    bool
	names         []map[string]bool
}

func (s *snapshot) directories(ctx context.Context, r *rootData) (*resolvedDirectories, error) {
	s.pathsMu.Lock()
	defer s.pathsMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cached := s.paths[r]; cached != nil {
		return cached, nil
	}
	paths, err := directoryPathsContext(ctx, r.path, r.dirs, r.dirParents)
	if err != nil {
		return nil, err
	}
	cached := &resolvedDirectories{paths: paths, folded: make([]string, len(paths)), hidden: make([]bool, len(paths)), names: make([]map[string]bool, len(paths))}
	byParent := map[string]map[string]bool{}
	for removed := range allRemovedNames(r) {
		parent := pathKey(filepath.Dir(removed))
		if byParent[parent] == nil {
			byParent[parent] = map[string]bool{}
		}
		byParent[parent][pathKey(filepath.Base(removed))] = true
	}
	cached.restricted = len(r.removed) > 0 || len(r.replacedNames) > 0
	for i, p := range paths {
		if i%256 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		cached.folded[i] = strings.ToLower(p)
		if cached.restricted {
			for q := p; Within(q, r.path); q = filepath.Dir(q) {
				if r.removed[pathKey(q)] {
					cached.hidden[i] = true
					break
				}
				if q == r.path {
					break
				}
			}
			cached.names[i] = byParent[pathKey(p)]
		}
	}
	if s.paths == nil {
		s.paths = make(map[*rootData]*resolvedDirectories)
	}
	var bytes int64
	for i, p := range cached.paths {
		bytes += int64(32 + len(p))
		if cached.folded[i] != p {
			bytes += int64(len(cached.folded[i]))
		}
	}
	// Oversized directory trees stay query-local instead of permanently
	// retaining complete paths alongside the parent/name representation.
	bytes += int64(len(cached.hidden) + len(cached.names)*8)
	if bytes <= directoryCacheBudget-s.pathBytes.Load() {
		s.pathBytes.Add(bytes)
		s.paths[r] = cached
	}
	return cached, nil
}

func (c *resolvedDirectories) visible(parent uint32, name string) bool {
	return !c.hidden[parent] && !c.names[parent][pathKey(name)]
}

func allRemovedNames(r *rootData) map[string]bool {
	out := make(map[string]bool, len(r.removed)+len(r.replacedNames))
	for p := range r.removed {
		out[p] = true
	}
	for p := range r.replacedNames {
		out[p] = true
	}
	return out
}
