package localindex

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"filesync/engine"
)

type region struct {
	root, path string
	excludes   []string
	data       *rootData
}

func pathKey(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

// Reconcile replaces affected subtrees with their current disk state. It does
// not execute historical operations. Failed reads leave the previous view
// intact, and old Results keep immutable chunks, paths and deletion masks.
func (x *Index) Reconcile(ctx context.Context, opts ScanOptions, paths []string) error {
	roots, err := Validate(opts.Roots, opts.Excludes)
	if err != nil {
		return err
	}
	filter, _ := engine.NewFilter(opts.Excludes)
	x.deltaMu.Lock()
	defer x.deltaMu.Unlock()
	base := x.base.Load()
	if base == nil {
		base = &snapshot{}
		for _, root := range roots {
			b := newBuilder(root)
			b.data.excludes = slices.Clone(opts.Excludes)
			base.roots = append(base.roots, b.snapshot())
		}
		x.base.Store(base)
	}
	for _, r := range base.roots {
		if !slices.Contains(roots, r.path) || !slices.Equal(r.excludes, opts.Excludes) {
			return ErrSnapshotScope
		}
	}
	next := make(map[string]region, len(x.regions))
	for k, r := range x.regions {
		next[k] = r
	}
	var affected []region
	for _, root := range roots {
		rootInfo, rootErr := os.Lstat(root)
		if rootErr != nil && !errors.Is(rootErr, os.ErrNotExist) {
			return rootErr
		}
		var wanted []string
		for _, p := range paths {
			if p == "" || !filepath.IsAbs(p) {
				continue
			}
			p = filepath.Clean(p)
			if Within(root, p) {
				p = root
			} else if !Within(p, root) {
				continue
			}
			if rootErr != nil || !rootInfo.IsDir() || isLink(rootInfo) {
				p = root
			}
			if runtime.GOOS == "windows" {
				p = actualPathCase(root, p)
			}
			// A removed/replaced ancestor must hide all of its stale descendants.
			chain := []string{}
			for q := filepath.Dir(p); q != root && Within(q, root); q = filepath.Dir(q) {
				chain = append(chain, q)
			}
			for i := len(chain) - 1; i >= 0; i-- {
				st, e := os.Lstat(chain[i])
				if errors.Is(e, os.ErrNotExist) || e == nil && (!st.IsDir() || isLink(st)) {
					p = chain[i]
					break
				}
				if e != nil {
					return e
				}
			}
			for q := p; Within(q, root); q = filepath.Dir(q) {
				if r, ok := next[pathKey(q)]; ok && r.root == root {
					p = r.path
					break
				}
				if q == root {
					break
				}
			}
			wanted = append(wanted, p)
		}
		wanted = collapsePaths(wanted)
		for _, p := range wanted {
			affected = append(affected, region{root: root, path: p, excludes: slices.Clone(opts.Excludes)})
		}
	}
	if len(affected) == 0 {
		return ctx.Err()
	}
	for _, r := range affected {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := readRegion(ctx, r.root, r.path, opts, filter)
		if err != nil {
			return err
		}
		r.data = data
		for k, old := range next {
			if old.root == r.root && Within(old.path, r.path) {
				delete(next, k)
			}
		}
		next[pathKey(r.path)] = r
	}
	if len(base.roots)+len(next) > 65535 {
		return errors.New("索引增量区域过多，请全部重建")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ancestors, err := readAncestorMetadata(ctx, next, opts, filter)
	if err != nil {
		return err
	}
	if len(base.roots)+len(next)+len(ancestors) > 65535 {
		return errors.New("索引增量区域过多，请全部重建")
	}
	x.ancestorRoots = ancestors
	x.regions = next
	view := x.composeLocked(base, time.Now())
	x.snap.Store(view)
	// Count against the same immutable view. Publish before this pass so a new
	// search can already see the change while statistics are being calculated.
	counter := New()
	counter.snap.Store(view)
	if results, err := counter.Search(ctx, Query{Hidden: true}); err == nil {
		view.exactCount.Store(int64(results.Total()))
	}
	return ctx.Err()
}

func collapsePaths(paths []string) []string {
	slices.SortStableFunc(paths, func(a, b string) int { return len(a) - len(b) })
	accepted := map[string]bool{}
	var out []string
	for _, p := range paths {
		covered := false
		for q := p; ; q = filepath.Dir(q) {
			if accepted[pathKey(q)] {
				covered = true
				break
			}
			if filepath.Dir(q) == q {
				break
			}
		}
		if !covered {
			out = append(out, p)
			accepted[pathKey(p)] = true
		}
	}
	return out
}

func readRegion(ctx context.Context, root, path string, opts ScanOptions, filter *engine.Filter) (*rootData, error) {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if path == root && (!st.IsDir() || isLink(st)) {
		return nil, nil
	}
	b := newBuilder(root)
	b.data.excludes = slices.Clone(opts.Excludes)
	blocked := func(p string) bool {
		for _, skip := range opts.Skip {
			if Within(p, skip) {
				return true
			}
		}
		return false
	}
	hidden := map[string]bool{root: false}
	// Check exclusions and hidden flags on ancestors that WalkDir won't visit.
	var ancestors []string
	for p := filepath.Dir(path); p != root && Within(p, root); p = filepath.Dir(p) {
		ancestors = append(ancestors, p)
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		p := ancestors[i]
		rel, _ := filepath.Rel(root, p)
		if blocked(p) || filter.Excluded(filepath.ToSlash(rel), true) {
			return nil, nil
		}
		st, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() || isLink(st) {
			return nil, errors.New("增量路径的上级目录已改变")
		}
		hidden[p] = hidden[filepath.Dir(p)] || strings.HasPrefix(filepath.Base(p), ".") || isHidden(st)
	}
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if blocked(p) || p != root && filter.Excluded(filepath.ToSlash(rel), d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		st, err := d.Info()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if p == root {
			if !st.IsDir() || isLink(st) {
				return filepath.SkipDir
			}
			return nil
		}
		if !st.Mode().IsRegular() && !st.IsDir() && !isLink(st) {
			return nil
		}
		e := metadataEntry(d.Name(), p, filepath.Dir(p), hidden[filepath.Dir(p)], st)
		b.add(e, b.directory(e.Parent))
		if e.Link && d.IsDir() {
			return filepath.SkipDir
		}
		if e.IsDir && !e.Link {
			b.directory(p)
			hidden[p] = e.Hidden
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return b.snapshot(), nil
}

// Caller holds deltaMu. Base snapshots alone are persisted; patches survive
// publication/loading of newer bases until a successful checkpoint clears them.
func (x *Index) composeLocked(base *snapshot, updated time.Time) *snapshot {
	out := &snapshot{version: x.version.Add(1), updated: updated, delta: len(x.regions) > 0}
	out.exactCount.Store(-1)
	for _, original := range base.roots {
		r := *original
		r.replacedNames = map[string]bool{}
		if meta := x.ancestorRoots[r.path]; meta != nil {
			for _, c := range meta.chunks {
				for i := range c.start {
					e := meta.entry(c, i)
					r.replacedNames[pathKey(e.Path)] = true
				}
			}
		}
		r.removed = map[string]bool{}
		for _, patch := range x.regions {
			if patch.root == r.path {
				r.removed[pathKey(patch.path)] = true
			}
		}
		out.roots = append(out.roots, &r)
	}
	keys := make([]string, 0, len(x.regions))
	for key := range x.regions {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		r := x.regions[key]
		if r.data != nil {
			out.roots = append(out.roots, r.data)
		}
	}
	for _, root := range base.roots {
		if meta := x.ancestorRoots[root.path]; meta != nil {
			out.roots = append(out.roots, meta)
		}
	}
	return out
}

// ClearDeltas is only valid after a successful, operation-fenced full rebuild.
func (x *Index) ClearDeltas() {
	x.deltaMu.Lock()
	defer x.deltaMu.Unlock()
	x.regions = nil
	x.ancestorRoots = nil
	if base := x.base.Load(); base != nil {
		x.snap.Store(x.composeLocked(base, base.updated))
	}
}

// RefreshPaths includes previous patches so manual scans can incorporate
// external changes even inside a region already shadowing the base.
func (x *Index) RefreshPaths() []string {
	x.deltaMu.Lock()
	defer x.deltaMu.Unlock()
	out := make([]string, 0, len(x.regions))
	for _, r := range x.regions {
		out = append(out, r.path)
	}
	return out
}

// Parent rows carry directory mtime/hidden flags and may not exist in an old
// base (e.g. a download made its parent directories). Replace those rows only,
// leaving their unchanged children in the binary base.
func readAncestorMetadata(ctx context.Context, regions map[string]region, opts ScanOptions, filter *engine.Filter) (map[string]*rootData, error) {
	builders := map[string]*builder{}
	wanted := map[string]string{}
	for _, patch := range regions {
		for p := filepath.Dir(patch.path); p != patch.root && Within(p, patch.root); p = filepath.Dir(p) {
			if _, seen := wanted[p]; seen {
				continue
			}
			covered := false
			for q := p; Within(q, patch.root); q = filepath.Dir(q) {
				if _, ok := regions[pathKey(q)]; ok {
					covered = true
					break
				}
				if q == patch.root {
					break
				}
			}
			if !covered {
				wanted[p] = patch.root
			}
		}
	}
	paths := make([]string, 0, len(wanted))
	for p := range wanted {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		root := wanted[p]
		hidden, excluded := false, false
		for q := p; q != root; q = filepath.Dir(q) {
			rel, _ := filepath.Rel(root, q)
			if filter.Excluded(filepath.ToSlash(rel), true) {
				excluded = true
				break
			}
			for _, skip := range opts.Skip {
				if Within(q, skip) {
					excluded = true
					break
				}
			}
			st, err := os.Lstat(q)
			if errors.Is(err, os.ErrNotExist) {
				excluded = true
				break
			}
			if err != nil {
				return nil, err
			}
			if !st.IsDir() || isLink(st) {
				excluded = true
				break
			}
			hidden = hidden || strings.HasPrefix(filepath.Base(q), ".") || isHidden(st)
		}
		if excluded {
			continue
		}
		st, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		b := builders[root]
		if b == nil {
			b = newBuilder(root)
			b.data.excludes = slices.Clone(opts.Excludes)
			builders[root] = b
		}
		e := metadataEntry(filepath.Base(p), p, filepath.Dir(p), hidden, st)
		b.add(e, b.directory(e.Parent))
		b.directory(p)
	}
	out := map[string]*rootData{}
	for root, b := range builders {
		out[root] = b.snapshot()
	}
	return out, nil
}

// Windows lookups ignore case, but result paths should reflect a case-only
// rename even when a historical source path was coalesced before its target.
func actualPathCase(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return path
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		next := filepath.Join(current, part)
		if st, err := os.Lstat(next); err == nil {
			next = filepath.Join(current, st.Name())
		}
		current = next
	}
	return current
}
