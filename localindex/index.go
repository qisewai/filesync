// Package localindex indexes names and metadata through explicit scans and
// operation-driven reconciliation. Columnar chunks are immutable; queries retain a version
// so directory changes cannot mix pages from different scans.
package localindex

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"filesync/engine"
)

const chunkSize = 4096

type Entry struct {
	Name, Path, Parent, Ext string
	Size, MTime             int64
	IsDir, Hidden, Link     bool
}

type chunk struct {
	file                     *os.File
	offset, length           int64
	names, folded            []byte
	start, foldStart, parent []uint32
	size, mtime              []int64
	ext                      []uint16
	flags, kind              []uint8
}

const (
	flagDir = 1 << iota
	flagHidden
	flagLink
)

type rootData struct {
	replacedNames map[string]bool
	removed       map[string]bool // immutable replacement regions in the base view
	path          string
	dirs, exts    []string // directory names; the root name is empty
	dirParents    []uint32 // parent directory ID; root points to itself
	dirStamp      []int64  // nanoseconds, only trusted when dirReady is set
	dirReady      []byte
	excludes      []string
	chunks        []*chunk
	count         int
}
type snapshot struct {
	delta      bool
	exactCount atomic.Int64
	pathsMu    sync.Mutex
	pathBytes  atomic.Int64
	paths      map[*rootData]*resolvedDirectories
	roots      []*rootData
	version    uint64
	updated    time.Time
}
type Index struct {
	base          atomic.Pointer[snapshot]
	deltaMu       sync.Mutex
	regions       map[string]region
	ancestorRoots map[string]*rootData
	snap          atomic.Pointer[snapshot]
	writer        sync.Mutex
	disk          sync.Mutex
	version       atomic.Uint64
}

func New() *Index { return &Index{} }

type Status struct {
	Delta, Approximate bool
	Count              int
	Version            uint64
	Updated            time.Time
	Bytes              int64
}

func (x *Index) Status() Status {
	s := x.snap.Load()
	if s == nil {
		return Status{}
	}
	out := Status{Delta: s.delta, Approximate: s.delta && s.exactCount.Load() < 0, Version: s.version, Updated: s.updated}
	for _, r := range s.roots {
		out.Count += r.count
		for _, d := range r.dirs {
			out.Bytes += int64(len(d) + 16)
		}
		out.Bytes += int64(cap(r.dirParents) * 4)
		out.Bytes += int64(cap(r.dirStamp)*8 + cap(r.dirReady))
		for _, c := range r.chunks {
			if c.file != nil {
				out.Bytes += 32
				continue
			}
			out.Bytes += int64(cap(c.names) + cap(c.start)*4 + cap(c.parent)*4 + cap(c.size)*8 + cap(c.mtime)*8 + cap(c.ext)*2 + cap(c.flags) + cap(c.kind))
			if !sameStorage(c.names, c.folded) {
				out.Bytes += int64(cap(c.folded))
			}
			if !sameStorage(c.start, c.foldStart) {
				out.Bytes += int64(cap(c.foldStart) * 4)
			}
		}
	}
	if s.delta && s.exactCount.Load() >= 0 {
		out.Count = int(s.exactCount.Load())
	}
	out.Bytes += s.pathBytes.Load()
	return out
}
func (x *Index) publish(roots []*rootData, updated time.Time) {
	x.deltaMu.Lock()
	defer x.deltaMu.Unlock()
	base := &snapshot{roots: slices.Clone(roots), updated: updated}
	x.base.Store(base)
	// Scope changes invalidate patches from the previous settings.
	for key, patch := range x.regions {
		found := false
		for _, r := range roots {
			if r.path == patch.root && slices.Equal(r.excludes, patch.excludes) {
				found = true
				break
			}
		}
		if !found {
			delete(x.regions, key)
		}
	}
	for root, meta := range x.ancestorRoots {
		found := false
		for _, r := range roots {
			if r.path == root && slices.Equal(r.excludes, meta.excludes) {
				found = true
				break
			}
		}
		if !found {
			delete(x.ancestorRoots, root)
		}
	}
	x.snap.Store(x.composeLocked(base, updated))
}

// NormalizeRoots removes duplicate and nested scopes without touching disk.
func NormalizeRoots(roots []string) ([]string, error) {
	var out []string
	for _, p := range roots {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("索引目录必须是绝对路径：%s", p)
		}
		p = filepath.Clean(p)
		if runtime.GOOS == "windows" && strings.HasPrefix(p, `\\`) {
			return nil, fmt.Errorf("本机索引不支持网络路径：%s", p)
		}
		covered := false
		for _, old := range out {
			if Within(p, old) {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		out = slices.DeleteFunc(out, func(old string) bool { return Within(old, p) })
		out = append(out, p)
	}
	return out, nil
}
func Within(p, base string) bool {
	if runtime.GOOS == "windows" {
		p, base = strings.ToLower(p), strings.ToLower(base)
	}
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func Validate(roots, excludes []string) ([]string, error) {
	r, err := NormalizeRoots(roots)
	if err != nil {
		return nil, err
	}
	_, err = engine.NewFilter(excludes)
	return r, err
}

type builder struct {
	data    rootData
	current *chunk
	extIDs  map[string]uint16
	dirIDs  map[string]uint32
}

func newBuilder(root string) *builder {
	return &builder{data: rootData{path: root, dirs: []string{""}, dirParents: []uint32{0}, dirStamp: []int64{0}, dirReady: []byte{0}, exts: []string{""}}, extIDs: map[string]uint16{"": 0}, dirIDs: map[string]uint32{root: 0}}
}
func (b *builder) directory(p string) uint32 {
	if id, ok := b.dirIDs[p]; ok {
		return id
	}
	parent := b.directory(filepath.Dir(p))
	id := uint32(len(b.data.dirs))
	b.dirIDs[p] = id
	b.data.dirs = append(b.data.dirs, filepath.Base(p))
	b.data.dirParents = append(b.data.dirParents, parent)
	b.data.dirStamp = append(b.data.dirStamp, 0)
	b.data.dirReady = append(b.data.dirReady, 0)
	return id
}
func (b *builder) add(e Entry, parent uint32) {
	if b.current == nil {
		// Full chunks need exactly chunkSize slots. Reserve column storage
		// once instead of retaining the excess capacity of repeated growth.
		b.current = &chunk{
			names:  make([]byte, 0, min(len(e.Name)+1, 64)*chunkSize),
			folded: make([]byte, 0, min(len(e.Name)+1, 64)*chunkSize),
			start:  make([]uint32, 0, chunkSize), foldStart: make([]uint32, 0, chunkSize), parent: make([]uint32, 0, chunkSize),
			size: make([]int64, 0, chunkSize), mtime: make([]int64, 0, chunkSize), ext: make([]uint16, 0, chunkSize),
			flags: make([]uint8, 0, chunkSize), kind: make([]uint8, 0, chunkSize),
		}
	}
	c := b.current
	c.start = append(c.start, uint32(len(c.names)))
	c.names = append(c.names, e.Name...)
	c.names = append(c.names, 0)
	c.foldStart = append(c.foldStart, uint32(len(c.folded)))
	c.folded = append(c.folded, strings.ToLower(e.Name)...)
	c.folded = append(c.folded, 0)
	c.parent = append(c.parent, parent)
	c.size = append(c.size, e.Size)
	c.mtime = append(c.mtime, e.MTime)
	ext := e.Ext
	id, ok := b.extIDs[ext]
	if !ok && len(b.data.exts) < 65536 {
		id = uint16(len(b.data.exts))
		b.extIDs[ext] = id
		b.data.exts = append(b.data.exts, ext)
	}
	c.ext = append(c.ext, id)
	var flags uint8
	if e.IsDir {
		flags |= flagDir
	}
	if e.Hidden {
		flags |= flagHidden
	}
	if e.Link {
		flags |= flagLink
	}
	c.flags = append(c.flags, flags)
	c.kind = append(c.kind, category(ext, e.IsDir))
	b.data.count++
	if len(c.start) == chunkSize {
		b.seal()
	}
}
func (b *builder) seal() {
	if b.current != nil {
		b.current.compact()
		b.data.chunks = append(b.data.chunks, b.current)
		b.current = nil
	}
}

// Chunks become immutable when sealed or loaded. Identical search and display
// columns can then share storage, including offsets when case folding preserves
// byte lengths. Compact copies also discard the builder's spare name capacity.
func (c *chunk) compact() {
	if cap(c.names) > len(c.names) {
		c.names = slices.Clone(c.names)
	}
	if bytes.Equal(c.names, c.folded) {
		c.folded = c.names
	} else if cap(c.folded) > len(c.folded) {
		c.folded = slices.Clone(c.folded)
	}
	c.start = tightSlice(c.start)
	if slices.Equal(c.start, c.foldStart) {
		c.foldStart = c.start
	} else {
		c.foldStart = tightSlice(c.foldStart)
	}
	c.parent = tightSlice(c.parent)
	c.size = tightSlice(c.size)
	c.mtime = tightSlice(c.mtime)
	c.ext = tightSlice(c.ext)
	c.flags = tightSlice(c.flags)
	c.kind = tightSlice(c.kind)
}
func tightSlice[T any](s []T) []T {
	if cap(s) > len(s) {
		return slices.Clone(s)
	}
	return s
}

func sameStorage[T any](a, b []T) bool {
	return len(a) > 0 && len(b) > 0 && &a[0] == &b[0]
}
func (b *builder) snapshot() *rootData {
	b.seal()
	r := b.data
	r.chunks = slices.Clone(r.chunks)
	// These append-only slices never change existing cells. Limit their capacity
	// so consumers cannot overwrite the writer's still-growing storage.
	r.dirs = r.dirs[:len(r.dirs):len(r.dirs)]
	r.dirParents = r.dirParents[:len(r.dirParents):len(r.dirParents)]
	r.exts = r.exts[:len(r.exts):len(r.exts)]
	// A directory's final stamp arrives after its entries. Unlike the path
	// tables, these cells change, so published versions must own their copy.
	r.dirStamp = slices.Clone(r.dirStamp)
	r.dirReady = slices.Clone(r.dirReady)
	return &r
}
func (c *chunk) name(i int) string {
	end := len(c.names) - 1
	if i+1 < len(c.start) {
		end = int(c.start[i+1]) - 1
	}
	return string(c.names[c.start[i]:end])
}
func (c *chunk) lower(i int) []byte {
	end := len(c.folded) - 1
	if i+1 < len(c.foldStart) {
		end = int(c.foldStart[i+1]) - 1
	}
	return c.folded[c.foldStart[i]:end]
}
func (r *rootData) entry(c *chunk, i int) Entry {
	name, parent := c.name(i), r.dirPath(c.parent[i])
	return Entry{Name: name, Path: filepath.Join(parent, name), Parent: parent, Ext: r.exts[c.ext[i]], Size: c.size[i], MTime: c.mtime[i], IsDir: c.flags[i]&flagDir != 0, Hidden: c.flags[i]&flagHidden != 0, Link: c.flags[i]&flagLink != 0}
}
