package localindex

import (
	"bytes"
	"cmp"
	"context"
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
}
type Results struct {
	snap  *snapshot
	hits  []uint64
	query Query
}

func (r *Results) Total() int {
	if r == nil {
		return 0
	}
	return len(r.hits)
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
	}
	var tasks []task
	for ri, r := range s.roots {
		var dirs []bool
		if q.Dir != "" {
			dirs = make([]bool, len(r.dirs))
			for i, p := range r.dirs {
				if i%256 == 0 && ctx.Err() != nil {
					return nil, ctx.Err()
				}
				dirs[i] = Within(p, q.Dir)
			}
		}
		for ci := range r.chunks {
			tasks = append(tasks, task{ri, ci, dirs})
		}
	}
	w := min(max(1, runtime.GOMAXPROCS(0)), 4, len(tasks))
	partial := make([][]uint64, w)
	var wg sync.WaitGroup
	for worker := range w {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ti := worker; ti < len(tasks); ti += w {
				if ctx.Err() != nil {
					return
				}
				t := tasks[ti]
				r, c := s.roots[t.root], s.roots[t.root].chunks[t.part]
				keep := func(i int) {
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
					for _, term := range terms[min(1, len(terms)):] {
						if !bytes.Contains(c.lower(i), []byte(term)) {
							return
						}
					}
					partial[worker] = append(partial[worker], uint64(t.root)<<48|uint64(t.part)<<16|uint64(i))
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out.hits = slices.Concat(partial...)
	return out, nil
}

func (r *Results) compare(a, b uint64) int {
	ra, ca, ia := r.snap.decode(a)
	rb, cb, ib := r.snap.decode(b)
	da, db := ca.flags[ia]&flagDir != 0, cb.flags[ib]&flagDir != 0
	if da != db {
		if da {
			return -1
		}
		return 1
	}
	var n int
	switch r.query.Sort {
	case "size":
		n = cmp.Compare(ca.size[ia], cb.size[ib])
	case "mtime":
		n = cmp.Compare(ca.mtime[ia], cb.mtime[ib])
	case "path":
		n = compareLower(ra.foldDirs[ca.parent[ia]], rb.foldDirs[cb.parent[ib]])
	}
	if n == 0 {
		n = compareLower(ca.lower(ia), cb.lower(ib))
	}
	if r.query.Desc {
		n = -n
	}
	if n == 0 {
		n = compareLower(ra.foldDirs[ca.parent[ia]], rb.foldDirs[cb.parent[ib]])
	}
	if n == 0 {
		n = cmp.Compare(a, b)
	}
	return n
}

// Page selects a globally sorted prefix using a bounded max heap. It retains
// the immutable version from Search, even if a new scan completes meanwhile.
func (r *Results) Page(ctx context.Context, offset, limit int) ([]Entry, error) {
	if r == nil || limit <= 0 || offset >= len(r.hits) {
		return nil, ctx.Err()
	}
	offset = max(offset, 0)
	k := offset + min(limit, len(r.hits)-offset)
	h := make([]uint64, 0, k)
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
	for i, ref := range r.hits {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if len(h) < k {
			h = append(h, ref)
			for p := len(h) - 1; p > 0; {
				parent := (p - 1) / 2
				if r.compare(h[p], h[parent]) <= 0 {
					break
				}
				h[p], h[parent] = h[parent], h[p]
				p = parent
			}
		} else if r.compare(ref, h[0]) < 0 {
			h[0] = ref
			down(0)
		}
	}
	// The prefix is already a max heap. Drain it in place so sorting also
	// responds to cancellation when a deep page requests a large prefix.
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
	out := make([]Entry, 0, len(h)-offset)
	for _, ref := range h[offset:] {
		root, c, i := r.snap.decode(ref)
		out = append(out, root.entry(c, i))
	}
	return out, nil
}

// CompareNames agrees with the desktop's natural, case-insensitive order.
func CompareNames(a, b string) int {
	return compareLower([]byte(strings.ToLower(a)), []byte(strings.ToLower(b)))
}
func compareLower(a, b []byte) int {
	for len(a) > 0 && len(b) > 0 {
		// Paths often share a long ASCII prefix. Skip equal nonnumeric bytes
		// without decoding; digit runs must still be compared as whole numbers.
		if a[0] == b[0] && a[0] < utf8.RuneSelf && (a[0] < '0' || a[0] > '9') {
			a, b = a[1:], b[1:]
			continue
		}
		ra, na := utf8.DecodeRune(a)
		rb, nb := utf8.DecodeRune(b)
		if ra >= '0' && ra <= '9' && rb >= '0' && rb <= '9' {
			i, j := 0, 0
			for i < len(a) && a[i] >= '0' && a[i] <= '9' {
				i++
			}
			for j < len(b) && b[j] >= '0' && b[j] <= '9' {
				j++
			}
			ta, tb := bytes.TrimLeft(a[:i], "0"), bytes.TrimLeft(b[:j], "0")
			if n := cmp.Compare(len(ta), len(tb)); n != 0 {
				return n
			}
			if n := bytes.Compare(ta, tb); n != 0 {
				return n
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
