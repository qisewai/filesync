// Package archive provides read-only, streaming archive browsing. This package
// is mirrored in fileindex/internal/archive; keep both copies byte-identical.
package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bodgit/sevenzip"
	"github.com/klauspost/compress/zstd"
	"github.com/nwaples/rardecode/v2"
	"github.com/ulikunitz/xz"
	"golang.org/x/text/encoding/simplifiedchinese"
)

const MaxEntries = 1_000_000
const MaxFile = int64(8 << 30)
const MaxScan = int64(32 << 30)
const cacheBytes = int64(64 << 20)

var ErrEncrypted = errors.New("压缩包已加密，暂不支持打开")
var ErrLimit = errors.New("压缩包超过安全限制（条目数、解压量或压缩比）")
var errStop = errors.New("stop")

type Entry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Parent    string `json:"parent"`
	IsDir     bool   `json:"is_dir"`
	Size      int64  `json:"size"`
	MTime     int64  `json:"mtime"`
	Ext       string `json:"ext"`
	Link      bool   `json:"link,omitempty"`
	Encrypted bool   `json:"encrypted,omitempty"`
	solid     bool
	ordinal   int
}

type Index struct {
	Entries    map[string]Entry
	dirs       map[string][]Entry
	Version    string
	Sequential bool
	bytes      int64
}

// Clean rejects unsafe names instead of silently converting a traversal into
// a different entry. Backslashes are separators even on Unix.
func Clean(s string) (string, error) {
	s = strings.ReplaceAll(s, `\`, "/")
	if strings.HasPrefix(s, "/") || strings.ContainsAny(s, ":\x00") || len(s) > 4096 {
		return "", errors.New("压缩包内路径不安全")
	}
	for _, part := range strings.Split(s, "/") {
		if part == ".." {
			return "", errors.New("压缩包内路径越界")
		}
	}
	s = path.Clean(s)
	if s == "." {
		s = ""
	}
	return s, nil
}
func Supported(s string) bool {
	switch strings.ToLower(path.Ext(s)) {
	case ".zip", ".rar", ".7z", ".tar", ".gz", ".tgz", ".bz2", ".xz", ".zst", ".zstd", ".tbz", ".tbz2", ".txz":
		return true
	}
	return false
}
func sequential(s string) bool {
	return !strings.HasSuffix(strings.ToLower(s), ".zip") && !strings.HasSuffix(strings.ToLower(s), ".7z") && !strings.HasSuffix(strings.ToLower(s), ".rar")
}

var slots = make(chan struct{}, 2)

func acquire(ctx context.Context) (func(), error) {
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Cache stores immutable directory indexes, bounded by estimated metadata
// bytes as well as entry count. Waiting readers can cancel independently.
type Cache struct {
	mu      sync.Mutex
	lru     *list.List
	used    int64
	pending map[string]chan struct{}
}
type cached struct {
	key string
	idx *Index
}

func (c *Cache) Load(ctx context.Context, filename string) (*Index, error) {
	return c.LoadProgress(ctx, filename, nil)
}

func (c *Cache) LoadProgress(ctx context.Context, filename string, progress func(int64, int64)) (*Index, error) {
	if progress != nil {
		ctx = context.WithValue(ctx, progressKey{}, &readProgress{fn: progress})
	}
	info, err := os.Stat(filename)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("不是普通文件")
	}
	key := fmt.Sprintf("%s\x00%d\x00%d", filename, info.Size(), info.ModTime().UnixNano())
	for {
		c.mu.Lock()
		if c.lru == nil {
			c.lru = list.New()
			c.pending = map[string]chan struct{}{}
		}
		for e := c.lru.Front(); e != nil; e = e.Next() {
			v := e.Value.(cached)
			if v.key == key {
				c.lru.MoveToFront(e)
				c.mu.Unlock()
				return v.idx, nil
			}
		}
		if done := c.pending[key]; done != nil {
			c.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		done := make(chan struct{})
		c.pending[key] = done
		c.mu.Unlock()
		idx, err := build(ctx, filename, info)
		if err == nil {
			now, e := os.Stat(filename)
			if e != nil || now.Size() != info.Size() || !now.ModTime().Equal(info.ModTime()) {
				err = errors.New("压缩包读取期间发生变化，请刷新")
			}
		}
		c.mu.Lock()
		delete(c.pending, key)
		close(done)
		if err == nil && idx.bytes <= cacheBytes {
			c.lru.PushFront(cached{key, idx})
			c.used += idx.bytes
			for c.used > cacheBytes || c.lru.Len() > 16 {
				e := c.lru.Back()
				c.used -= e.Value.(cached).idx.bytes
				c.lru.Remove(e)
			}
		}
		c.mu.Unlock()
		return idx, err
	}
}
func build(ctx context.Context, filename string, info fs.FileInfo) (*Index, error) {
	release, err := acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	idx := &Index{Entries: map[string]Entry{}, dirs: map[string][]Entry{}, Version: fmt.Sprintf("%d-%d", info.Size(), info.ModTime().UnixNano()), Sequential: sequential(filename)}
	add := func(e Entry) error {
		if len(idx.Entries) >= MaxEntries {
			return ErrLimit
		}
		if _, ok := idx.Entries[e.Path]; ok {
			return errors.New("压缩包内路径重复或文件与目录冲突")
		}
		idx.Entries[e.Path] = e
		idx.bytes += int64(256 + 2*len(e.Path))
		if idx.bytes > cacheBytes {
			return ErrLimit
		}
		return nil
	}
	err = walk(ctx, filename, false, func(e Entry, r io.Reader) error {
		idx.Sequential = idx.Sequential || e.solid
		p, err := Clean(e.Path)
		if err != nil {
			return err
		}
		if p == "" {
			return nil
		}
		e.Path = p
		e.Name = path.Base(p)
		e.Parent = parent(p)
		e.Ext = strings.TrimPrefix(strings.ToLower(path.Ext(e.Name)), ".")
		for d := e.Parent; d != ""; d = parent(d) {
			if old, ok := idx.Entries[d]; ok {
				if !old.IsDir {
					return errors.New("压缩包内文件与目录冲突")
				}
				continue
			}
			if err := add(Entry{Name: path.Base(d), Path: d, Parent: parent(d), IsDir: true, ordinal: -1}); err != nil {
				return err
			}
		}
		if old, ok := idx.Entries[p]; ok && old.IsDir && old.ordinal == -1 && e.IsDir {
			idx.Entries[p] = e
			return nil
		}
		return add(e)
	})
	if err != nil {
		return nil, err
	}
	for _, e := range idx.Entries {
		idx.dirs[e.Parent] = append(idx.dirs[e.Parent], e)
	}
	for p := range idx.dirs {
		sort.Slice(idx.dirs[p], func(i, j int) bool {
			a, b := idx.dirs[p][i], idx.dirs[p][j]
			if a.IsDir != b.IsDir {
				return a.IsDir
			}
			return a.Name < b.Name
		})
	}
	return idx, nil
}
func parent(s string) string {
	p := path.Dir(s)
	if p == "." {
		return ""
	}
	return p
}
func (idx *Index) List(dir string) ([]Entry, error) {
	p, err := Clean(dir)
	if err != nil {
		return nil, err
	}
	if p != "" {
		e, ok := idx.Entries[p]
		if !ok || !e.IsDir || e.Link {
			return nil, fs.ErrNotExist
		}
	}
	out := append([]Entry{}, idx.dirs[p]...)
	return out, nil
}
func (idx *Index) File(entry string) (Entry, error) {
	p, err := Clean(entry)
	if err != nil {
		return Entry{}, err
	}
	e, ok := idx.Entries[p]
	if !ok {
		return Entry{}, fs.ErrNotExist
	}
	if e.Link {
		return Entry{}, errors.New("压缩包内符号链接只显示，不能打开")
	}
	if e.IsDir {
		return Entry{}, errors.New("不是文件")
	}
	if e.Encrypted {
		return Entry{}, ErrEncrypted
	}
	if e.Size > MaxFile {
		return Entry{}, ErrLimit
	}
	return e, nil
}

// Copy streams a single member. A positive limit intentionally returns only
// a prefix for previews. Full reads verify decoder checksums before success.
func (idx *Index) Copy(ctx context.Context, filename, entry string, w io.Writer, limit int64) error {
	if err := idx.unchanged(filename); err != nil {
		return err
	}
	e, err := idx.File(entry)
	if err != nil {
		return err
	}
	release, err := acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	found := false
	err = walk(ctx, filename, true, func(it Entry, r io.Reader) error {
		if it.ordinal != e.ordinal {
			return nil
		}
		found = true
		n := e.Size
		if limit > 0 {
			n = min(n, limit)
		}
		// Withhold the final block until size and checksum validation succeeds.
		tail := &tailWriter{w: w}
		written, err := io.CopyN(tail, r, n)
		if err != nil {
			return err
		}
		if written != n {
			return io.ErrUnexpectedEOF
		}
		if n == e.Size {
			var b [1]byte
			_, err := r.Read(b[:])
			if err != io.EOF {
				return fmt.Errorf("压缩包文件大小或校验错误: %v", err)
			}
		}
		if err := idx.unchanged(filename); err != nil {
			return err
		}
		if err := tail.flush(); err != nil {
			return err
		}
		return errStop
	})
	if errors.Is(err, errStop) {
		return nil
	}
	if err == nil && !found {
		return fs.ErrNotExist
	}
	return err
}

// Selection returns unique roots, rejecting links/encryption before writing
// headers. Descendants retain their hierarchy under the selected base name.
func (idx *Index) Selection(entries []string) (map[string]string, int64, error) {
	roots := []string{}
	for _, s := range entries {
		p, err := Clean(s)
		if err != nil {
			return nil, 0, err
		}
		if _, ok := idx.Entries[p]; !ok || p == "" {
			return nil, 0, fs.ErrNotExist
		}
		roots = append(roots, p)
	}
	sort.Strings(roots)
	keep := []string{}
	names := map[string]bool{}
	for _, p := range roots {
		covered := false
		for _, q := range keep {
			if p == q || strings.HasPrefix(p, q+"/") {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		name := path.Base(p)
		if names[name] {
			return nil, 0, errors.New("选中项有同名，请分别解压")
		}
		names[name] = true
		keep = append(keep, p)
	}
	out := map[string]string{}
	var total int64
	for _, p := range keep {
		for q, e := range idx.Entries {
			if q != p && !strings.HasPrefix(q, p+"/") {
				continue
			}
			if e.Link {
				if q == p {
					return nil, 0, errors.New("符号链接只显示，不能解压")
				}
				continue
			}
			if e.Encrypted {
				return nil, 0, ErrEncrypted
			}
			if e.Size > MaxFile {
				return nil, 0, ErrLimit
			}
			total += e.Size
			if total > MaxScan {
				return nil, 0, ErrLimit
			}
			out[q] = path.Base(p) + strings.TrimPrefix(q, p)
		}
	}
	if len(out) == 0 {
		return nil, 0, errors.New("没有选择文件")
	}
	return out, total, nil
}
func (idx *Index) Export(ctx context.Context, filename string, selected map[string]string, w io.Writer) error {
	if err := idx.unchanged(filename); err != nil {
		return err
	}
	release, err := acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	tw := tar.NewWriter(w)
	remaining := len(selected)
	// Implicit parents have no physical header; emit selected directories first.
	dirs := []string{}
	for p := range selected {
		if idx.Entries[p].IsDir {
			dirs = append(dirs, p)
		}
	}
	sort.Strings(dirs)
	for _, p := range dirs {
		e := idx.Entries[p]
		if err := tw.WriteHeader(&tar.Header{Name: selected[p] + "/", Typeflag: tar.TypeDir, Mode: 0755, ModTime: time.Unix(e.MTime, 0), Format: tar.FormatPAX}); err != nil {
			return err
		}
		remaining--
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
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: e.Size, Mode: 0644, ModTime: time.Unix(e.MTime, 0), Format: tar.FormatPAX}); err != nil {
			return err
		}
		if _, err := io.CopyN(tw, r, e.Size); err != nil {
			return err
		}
		var b [1]byte
		if _, err := r.Read(b[:]); err != io.EOF {
			return fmt.Errorf("压缩包校验错误: %v", err)
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
		return errors.New("压缩包导出不完整")
	}
	if err := idx.unchanged(filename); err != nil {
		return err
	}
	return tw.Close()
}

type guarded struct {
	ctx    context.Context
	r      io.Reader
	n, max int64
}

func (g *guarded) Read(p []byte) (int, error) {
	if err := g.ctx.Err(); err != nil {
		return 0, err
	}
	if g.n >= g.max {
		return 0, ErrLimit
	}
	p = p[:min(int64(len(p)), g.max-g.n)]
	n, err := g.r.Read(p)
	g.n += int64(n)
	return n, err
}

type contextAt struct {
	ctx context.Context
	r   io.ReaderAt
}

func (c contextAt) ReadAt(p []byte, o int64) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.r.ReadAt(p, o)
	if v, ok := c.ctx.Value(progressKey{}).(*readProgress); ok {
		v.report(o + int64(n))
	}
	return n, err
}
func budget(size int64) int64 {
	if size > MaxScan/10000 {
		return MaxScan
	}
	return min(MaxScan, max(64<<20, size*10000))
}
func encryption(err error) error {
	var e *sevenzip.ReadError
	if errors.Is(err, rardecode.ErrArchiveEncrypted) || errors.Is(err, rardecode.ErrArchivedFileEncrypted) || errors.As(err, &e) && e.Encrypted {
		return ErrEncrypted
	}
	return err
}

// walk never writes to disk. bodies controls whether random-access members
// need opening; sequential containers skip through a bounded decoder stream.
func walk(ctx context.Context, filename string, bodies bool, fn func(Entry, io.Reader) error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("压缩包格式损坏: %v", p)
		}
		err = encryption(err)
	}()
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	ext := strings.ToLower(path.Ext(filename))
	if v, ok := ctx.Value(progressKey{}).(*readProgress); ok {
		v.total = info.Size()
	}
	max := budget(info.Size())
	ordinal := 0
	var total int64
	visit := func(e Entry, r io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		e.ordinal = ordinal
		ordinal++
		if ordinal > MaxEntries || e.Size < 0 || e.Size > MaxScan-total {
			return ErrLimit
		}
		total += e.Size
		if total > max {
			return ErrLimit
		}
		if r != nil {
			r = &guarded{ctx: ctx, r: r, max: min(max, MaxFile+1)}
		}
		return fn(e, r)
	}
	switch ext {
	case ".zip":
		z, err := zip.NewReader(contextAt{ctx, f}, info.Size())
		if err != nil {
			return err
		}
		if len(z.File) > MaxEntries {
			return ErrLimit
		}
		for _, it := range z.File {
			name := it.Name
			if it.NonUTF8 {
				name, err = simplifiedchinese.GB18030.NewDecoder().String(name)
				if err != nil {
					return err
				}
			}
			e := Entry{Path: name, IsDir: it.FileInfo().IsDir(), Size: int64(it.UncompressedSize64), MTime: it.Modified.Unix(), Link: it.Mode()&fs.ModeSymlink != 0, Encrypted: it.Flags&1 != 0}
			if it.UncompressedSize64 > uint64(MaxScan) {
				return ErrLimit
			}
			var r io.ReadCloser
			if bodies && !e.IsDir && !e.Link && !e.Encrypted {
				r, err = it.Open()
				if err != nil {
					return err
				}
			}
			err = visit(e, r)
			if r != nil {
				r.Close()
			}
			if err != nil {
				return err
			}
		}
		return nil
	case ".7z":
		z, err := sevenzip.NewReader(contextAt{ctx, f}, info.Size())
		if err != nil {
			return err
		}
		if len(z.File) > MaxEntries {
			return ErrLimit
		}
		encryptedStreams := map[int]bool{}
		probedStreams := map[int]bool{}
		seenStreams := map[int]bool{}
		for _, it := range z.File {
			fi := it.FileInfo()
			e := Entry{Path: it.Name, IsDir: fi.IsDir(), Size: fi.Size(), MTime: fi.ModTime().Unix(), Link: fi.Mode()&fs.ModeSymlink != 0}
			if !bodies && !e.IsDir && !e.Link && !probedStreams[it.Stream] {
				probe, probeErr := it.Open()
				if probeErr != nil {
					if errors.Is(encryption(probeErr), ErrEncrypted) {
						e.Encrypted = true
						encryptedStreams[it.Stream] = true
					} else {
						return probeErr
					}
				}
				if probe != nil {
					probe.Close()
				}
				probedStreams[it.Stream] = true
			}
			e.Encrypted = encryptedStreams[it.Stream]
			e.solid = seenStreams[it.Stream] && it.UncompressedSize > 0
			if it.UncompressedSize > 0 {
				seenStreams[it.Stream] = true
			}
			var r io.ReadCloser
			if bodies && !e.IsDir && !e.Link {
				r, err = it.Open()
				if err != nil {
					return err
				}
			}
			err = visit(e, r)
			if r != nil {
				r.Close()
			}
			if err != nil {
				return err
			}
		}
		return nil
	case ".rar":
		z, err := rardecode.NewReader(&guarded{ctx: ctx, r: &progressReader{ctx: ctx, r: f}, max: info.Size() + 1}, rardecode.MaxDictionarySize(256<<20))
		if err != nil {
			return err
		}
		for {
			h, err := z.Next()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			e := Entry{Path: h.Name, IsDir: h.IsDir, Size: h.UnPackedSize, MTime: h.ModificationTime.Unix(), Link: h.Mode()&fs.ModeSymlink != 0 || h.LinkType != 0, Encrypted: h.Encrypted, solid: h.Solid}
			if h.UnKnownSize {
				return ErrLimit
			}
			if err := visit(e, z); err != nil {
				return err
			}
		}
	}
	var r io.Reader = &guarded{ctx: ctx, r: &progressReader{ctx: ctx, r: f}, max: info.Size() + 1}
	var closeDecoder func()
	switch ext {
	case ".gz", ".tgz":
		z, err := gzip.NewReader(r)
		if err != nil {
			return err
		}
		r = z
		closeDecoder = func() { z.Close() }
	case ".bz2", ".tbz", ".tbz2":
		r = bzip2.NewReader(r)
	case ".xz", ".txz":
		z, err := (xz.ReaderConfig{DictCap: 256 << 20}).NewReader(r)
		if err != nil {
			return err
		}
		r = z
	case ".zst", ".zstd":
		z, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(256<<20))
		if err != nil {
			return err
		}
		r = z
		closeDecoder = z.Close
	case ".tar":
	default:
		return errors.New("不支持的压缩包格式")
	}
	if closeDecoder != nil {
		defer closeDecoder()
	}
	g := &guarded{ctx: ctx, r: r, max: max}
	lower := strings.ToLower(filename)
	isTar := ext == ".tar" || ext == ".tgz" || ext == ".tbz" || ext == ".tbz2" || ext == ".txz" || strings.HasSuffix(strings.TrimSuffix(lower, ext), ".tar")
	if isTar {
		tr := tar.NewReader(g)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				// tar ends before a compressed stream footer; verify that footer too.
				_, err = io.Copy(io.Discard, g)
				return err
			}
			if err != nil {
				return err
			}
			switch h.Typeflag {
			case tar.TypeReg, tar.TypeRegA, tar.TypeDir, tar.TypeSymlink, tar.TypeLink:
			default:
				continue
			}
			if err := visit(Entry{Path: h.Name, IsDir: h.Typeflag == tar.TypeDir, Size: h.Size, MTime: h.ModTime.Unix(), Link: h.Typeflag == tar.TypeSymlink || h.Typeflag == tar.TypeLink}, tr); err != nil {
				return err
			}
		}
	}
	name := strings.TrimSuffix(path.Base(filename), path.Ext(filename))
	e := Entry{Path: name, Size: 0, MTime: info.ModTime().Unix()}
	if !bodies {
		n, err := io.Copy(io.Discard, g)
		if err != nil {
			return err
		}
		e.Size = n
		return visit(e, nil)
	}
	// Single streams have their size in the cached index; the callback can read
	// a prefix or all bytes and verifies the decoder at EOF.
	return fn(Entry{Path: name, ordinal: 0, MTime: e.MTime}, g)
}

// Progress reports compressed bytes visited, throttled to avoid UI flooding.
type progressKey struct{}
type readProgress struct {
	fn       func(int64, int64)
	total, n int64
	last     time.Time
}

func (p *readProgress) report(n int64) {
	p.n = max(p.n, min(n, p.total))
	if time.Since(p.last) > 200*time.Millisecond {
		p.last = time.Now()
		p.fn(p.n, p.total)
	}
}

type progressReader struct {
	ctx context.Context
	r   io.Reader
	n   int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if v, ok := p.ctx.Value(progressKey{}).(*readProgress); ok {
		v.report(p.n)
	}
	return n, err
}

func (idx *Index) unchanged(filename string) error {
	info, err := os.Stat(filename)
	if err != nil {
		return err
	}
	if fmt.Sprintf("%d-%d", info.Size(), info.ModTime().UnixNano()) != idx.Version {
		return errors.New("压缩包已变化，请重新进入")
	}
	return nil
}

type tailWriter struct {
	w    io.Writer
	held []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.held = append(t.held, p...)
	if len(t.held) > 64<<10 {
		n := len(t.held) - (64 << 10)
		written, err := t.w.Write(t.held[:n])
		if err == nil && written != n {
			err = io.ErrShortWrite
		}
		copy(t.held, t.held[n:])
		t.held = t.held[:len(t.held)-n]
		if err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
func (t *tailWriter) flush() error {
	n, err := t.w.Write(t.held)
	if err == nil && n != len(t.held) {
		return io.ErrShortWrite
	}
	return err
}
