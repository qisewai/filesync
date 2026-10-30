package localindex

import (
	"bytes"
	"cmp"
	"context"
	"math/bits"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

type Query struct {
	Text, Dir, Sort, Category string
	Exts                      []string
	Desc, Hidden              bool
	// MinSize and MaxSize bound the size, in bytes, and From and To the
	// time of the last change, in Unix seconds; 0 bounds nothing.
	MinSize, MaxSize int64
	From, To         int64
}
type Results struct {
	snap     *snapshot
	foldDirs [][]string // paths resolved for this immutable result version
	matches  []chunkMatches
	total    int
	query    Query
}

// One bit per row bounds even an all-files search to one bit per indexed
// entry, rather than growing and concatenating per-worker uint64 hit lists.
type chunkMatches struct {
	base uint64
	rows [chunkSize / 64]uint64
}

func (r *Results) Total() int {
	if r == nil {
		return 0
	}
	return r.total
}
func (r *Results) Version() uint64 {
	if r == nil || r.snap == nil {
		return 0
	}
	return r.snap.version
}
func (s *snapshot) decode(ref uint64) (*rootData, *chunk, int) {
	r := s.roots[ref>>48]
	return r, r.chunks[uint32(ref>>16)], int(uint16(ref))
}

// Search runs a bounded number of CPU workers; no filesystem access occurs.
func (x *Index) Search(ctx context.Context, q Query) (*Results, error) {
	s := x.snap.Load()
	out := &Results{snap: s, query: q}
	if s == nil {
		return out, ctx.Err()
	}
	terms := strings.Fields(strings.ToLower(q.Text))
	slices.SortFunc(terms, func(a, b string) int { return len(b) - len(a) })
	for _, term := range terms {
		if strings.ContainsRune(term, 0) {
			return out, nil
		}
	}
	extSet := map[string]bool{}
	for _, ext := range q.Exts {
		extSet[strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ext)), ".")] = true
	}
	cat := categoryID(q.Category)
	if q.Category == "file" {
		cat = 0
	}
	type task struct {
		root, part int
		dirs       []bool
		visibility *resolvedDirectories
	}
	var tasks []task
	out.foldDirs = make([][]string, len(s.roots))
	for ri, r := range s.roots {
		cached, err := s.directories(ctx, r)
		if err != nil {
			return nil, err
		}
		out.foldDirs[ri] = cached.folded
		paths := cached.paths
		var dirs []bool
		if q.Dir != "" {
			dirs = make([]bool, len(r.dirs))
			for i, p := range paths {
				if i%256 == 0 && ctx.Err() != nil {
					return nil, ctx.Err()
				}
				dirs[i] = Within(p, q.Dir)
			}
		}
		for ci := range r.chunks {
			tasks = append(tasks, task{ri, ci, dirs, cached})
		}
	}
	w := min(max(1, runtime.GOMAXPROCS(0)), 4, len(tasks))
	out.matches = make([]chunkMatches, len(tasks))
	var wg sync.WaitGroup
	var readMu sync.Mutex
	var readErr error
	for worker := range w {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Give each worker a contiguous output range to keep writes local.
			matchAt := worker*(len(tasks)/w) + min(worker, len(tasks)%w)
			for ti := worker; ti < len(tasks); ti += w {
				if ctx.Err() != nil {
					return
				}
				t := tasks[ti]
				r := s.roots[t.root]
				c, err := readChunk(ctx, r, r.chunks[t.part])
				if err != nil {
					readMu.Lock()
					if readErr == nil {
						readErr = err
					}
					readMu.Unlock()
					return
				}
				match := &out.matches[matchAt]
				matchAt++
				match.base = uint64(t.root)<<48 | uint64(t.part)<<16
				keep := func(i int) {
					if t.visibility.restricted && !t.visibility.visible(c.parent[i], c.name(i)) {
						return
					}
					if q.Category == "file" && c.flags[i]&flagDir != 0 {
						return
					}
					if !q.Hidden && c.flags[i]&flagHidden != 0 || t.dirs != nil && !t.dirs[c.parent[i]] {
						return
					}
					if cat != 0 && c.kind[i] != cat {
						return
					}
					if len(extSet) > 0 && !extSet[r.exts[c.ext[i]]] {
						return
					}
					if q.MinSize > 0 && c.size[i] < q.MinSize || q.MaxSize > 0 && c.size[i] > q.MaxSize ||
						q.From > 0 && c.mtime[i] < q.From || q.To > 0 && c.mtime[i] > q.To {
						return
					}
					for _, term := range terms[min(1, len(terms)):] {
						if !bytes.Contains(c.lower(i), []byte(term)) {
							return
						}
					}
					match.rows[i/64] |= uint64(1) << (i % 64)
				}
				if len(terms) == 0 {
					for i := range c.start {
						if i%256 == 0 && ctx.Err() != nil {
							return
						}
						keep(i)
					}
					continue
				}
				lead := []byte(terms[0])
				pos := 0
				for pos < len(c.folded) {
					if ctx.Err() != nil {
						return
					}
					off := bytes.Index(c.folded[pos:], lead)
					if off < 0 {
						break
					}
					abs := uint32(pos + off)
					i := sort.Search(len(c.foldStart), func(i int) bool { return c.foldStart[i] > abs }) - 1
					pos = len(c.folded)
					if i+1 < len(c.foldStart) {
						pos = int(c.foldStart[i+1])
					}
					keep(i)
				}
			}
		}()
	}
	wg.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, match := range out.matches {
		for _, word := range match.rows {
			out.total += bits.OnesCount64(word)
		}
	}
	if s.delta && q.Hidden && len(terms) == 0 && q.Dir == "" && q.Category == "" && len(extSet) == 0 && q.MinSize == 0 && q.MaxSize == 0 && q.From == 0 && q.To == 0 {
		s.exactCount.Store(int64(out.total))
	}
	return out, nil
}

type pageCandidate struct {
	ref         uint64
	lower       []byte
	dir         string
	size, mtime int64
	isDir       bool
}

func (r *Results) compare(a, b pageCandidate) int {
	if a.isDir != b.isDir {
		if a.isDir {
			return -1
		}
		return 1
	}
	var n int
	switch r.query.Sort {
	case "size":
		n = cmp.Compare(a.size, b.size)
	case "mtime":
		n = cmp.Compare(a.mtime, b.mtime)
	case "path":
		n = compareLower(a.dir, b.dir)
	}
	if n == 0 {
		n = compareLower(a.lower, b.lower)
	}
	if r.query.Desc {
		n = -n
	}
	if n == 0 {
		n = compareLower(a.dir, b.dir)
	}
	if n == 0 {
		n = cmp.Compare(a.ref, b.ref)
	}
	return n
}

// Page scans matching blocks once and retains the globally best prefix. Heap
// keys own their names so a retained result never pins an entire file block.
func (r *Results) Page(ctx context.Context, offset, limit int) ([]Entry, error) {
	if r == nil || limit <= 0 || offset >= r.total {
		return nil, ctx.Err()
	}
	offset = max(offset, 0)
	k := offset + min(limit, r.total-offset)
	h := make([]pageCandidate, 0, k)
	down := func(i int) {
		for {
			child := i*2 + 1
			if child >= len(h) {
				break
			}
			if child+1 < len(h) && r.compare(h[child+1], h[child]) > 0 {
				child++
			}
			if r.compare(h[i], h[child]) >= 0 {
				break
			}
			h[i], h[child] = h[child], h[i]
			i = child
		}
	}
	keep := func(item pageCandidate) {
		if len(h) < k {
			item.lower = bytes.Clone(item.lower)
			h = append(h, item)
			for p := len(h) - 1; p > 0; {
				parent := (p - 1) / 2
				if r.compare(h[p], h[parent]) <= 0 {
					break
				}
				h[p], h[parent] = h[parent], h[p]
				p = parent
			}
		} else if r.compare(item, h[0]) < 0 {
			item.lower = bytes.Clone(item.lower)
			h[0] = item
			down(0)
		}
	}
	for _, match := range r.matches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hasMatch := false
		for _, w := range match.rows {
			if w != 0 {
				hasMatch = true
				break
			}
		}
		if !hasMatch {
			continue
		}
		root := r.snap.roots[match.base>>48]
		c, err := readChunk(ctx, root, root.chunks[uint32(match.base>>16)])
		if err != nil {
			return nil, err
		}
		for wi, word := range match.rows {
			if wi%4 == 0 && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			for word != 0 {
				i := wi*64 + bits.TrailingZeros64(word)
				if i >= len(c.start) {
					return nil, ErrSnapshotFormat
				}
				keep(pageCandidate{ref: match.base | uint64(i), lower: c.lower(i), dir: r.foldDirs[match.base>>48][c.parent[i]], size: c.size[i], mtime: c.mtime[i], isDir: c.flags[i]&flagDir != 0})
				word &= word - 1
			}
		}
	}
	whole := h
	for len(h) > 1 {
		if len(h)%256 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		last := len(h) - 1
		h[0], h[last] = h[last], h[0]
		h = h[:last]
		down(0)
	}
	h = whole
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	selected := h[offset:]
	// Read each selected block once, but place entries in their sorted positions.
	order := make([]int, len(selected))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool { return selected[order[i]].ref < selected[order[j]].ref })
	out := make([]Entry, len(selected))
	var current uint64 = ^uint64(0)
	var root *rootData
	var c *chunk
	for _, dest := range order {
		ref := selected[dest].ref
		base := ref &^ uint64(0xffff)
		if base != current {
			root = r.snap.roots[ref>>48]
			var err error
			c, err = readChunk(ctx, root, root.chunks[uint32(ref>>16)])
			if err != nil {
				return nil, err
			}
			current = base
		}
		i := int(uint16(ref))
		if i >= len(c.start) {
			return nil, ErrSnapshotFormat
		}
		out[dest] = root.entry(c, i)
	}
	return out, nil
}

// CompareNames agrees with the desktop's natural, case-insensitive order.
func CompareNames(a, b string) int {
	return compareLower(strings.ToLower(a), strings.ToLower(b))
}
func compareLower[T string | []byte](a, b T) int {
	for len(a) > 0 && len(b) > 0 {
		// Paths often share a long ASCII prefix. Skip equal nonnumeric bytes
		// without decoding; digit runs must still be compared as whole numbers.
		if a[0] == b[0] && a[0] < utf8.RuneSelf && (a[0] < '0' || a[0] > '9') {
			a, b = a[1:], b[1:]
			continue
		}
		ra, na := utf8.DecodeRuneInString(string(a))
		rb, nb := utf8.DecodeRuneInString(string(b))
		if ra >= '0' && ra <= '9' && rb >= '0' && rb <= '9' {
			i, j := 0, 0
			for i < len(a) && a[i] >= '0' && a[i] <= '9' {
				i++
			}
			for j < len(b) && b[j] >= '0' && b[j] <= '9' {
				j++
			}
			ta, tb := a[:i], b[:j]
			for len(ta) > 0 && ta[0] == '0' {
				ta = ta[1:]
			}
			for len(tb) > 0 && tb[0] == '0' {
				tb = tb[1:]
			}
			if n := cmp.Compare(len(ta), len(tb)); n != 0 {
				return n
			}
			for k := range len(ta) {
				if n := cmp.Compare(ta[k], tb[k]); n != 0 {
					return n
				}
			}
			a, b = a[i:], b[j:]
			continue
		}
		if n := cmp.Compare(ra, rb); n != 0 {
			return n
		}
		a, b = a[na:], b[nb:]
	}
	return cmp.Compare(len(a), len(b))
}

var categoryNames = []string{"", "dir", "image", "video", "audio", "archive", "disk", "doc", "code", "other", "file"}

func categoryID(name string) uint8 {
	for i, n := range categoryNames {
		if name == n {
			return uint8(i)
		}
	}
	return 0
}

var categories = map[string]string{}

func init() {
	for name, exts := range map[string]string{
		"image": "jpg jpeg png gif bmp webp heic heif tif tiff svg raw cr2 nef arw dng psd",
		"video": "mp4 mkv avi mov wmv flv webm m4v m2ts rmvb rm 3gp mpg mpeg vob",
		"audio": "mp3 flac wav aac m4a ogg wma ape opus", "archive": "zip rar 7z tar gz tgz bz2 xz zst",
		"disk": "vmdk vdi vhd vhdx avhd avhdx qcow qcow2 qed hdd ova xva gho ghs v2i tib tibx wim esd swm iso img dmg nrg mdf",
		"doc":  "pdf doc docx xls xlsx ppt pptx txt md rtf odt ods odp csv epub wps et dps",
		"code": "go py js ts java c cpp h hpp cs rs php rb sh sql json xml yaml yml html css vue kt swift",
	} {
		for _, ext := range strings.Fields(exts) {
			categories[ext] = name
		}
	}
}
func category(ext string, dir bool) uint8 { return categoryID(Category(ext, dir)) }
func Category(ext string, dir bool) string {
	if dir {
		return "dir"
	}
	if c, ok := categories[ext]; ok {
		return c
	}
	return "other"
}
