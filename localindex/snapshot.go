package localindex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

const snapshotHeaderSize = 104
const snapshotFormat = 2

var snapshotMagic = [8]byte{'F', 'S', 'I', 'D', 'X', 'B', 'I', 'N'}
var ErrSnapshotFormat = errors.New("索引快照损坏或格式不兼容")
var ErrSnapshotScope = errors.New("快照的索引范围或排除规则已改变")

type SnapshotMeta struct {
	Partial  bool
	Errors   int
	Messages []string
}
type LoadProgress struct{ Read, Total int64 }

func DefaultSnapshotPath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "filesync", "local-index.bin"), nil
}
func scopeDigest(opts ScanOptions) ([32]byte, error) {
	roots, err := Validate(opts.Roots, opts.Excludes)
	if err != nil {
		return [32]byte{}, err
	}
	data, err := json.Marshal(struct {
		OS              string
		Roots, Excludes []string
	}{runtime.GOOS, roots, opts.Excludes})
	return sha256.Sum256(data), err
}

// SaveSnapshot writes a private, checksummed binary snapshot through a synced
// temporary file. Cancellation and any write failure leave the previous file.
func (x *Index) SaveSnapshot(ctx context.Context, filename string, opts ScanOptions, meta SnapshotMeta) error {
	x.disk.Lock()
	defer x.disk.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	scope, err := scopeDigest(opts)
	if err != nil {
		return err
	}
	s := x.snap.Load()
	if s == nil {
		return fmt.Errorf("尚未建立索引")
	}
	roots, _ := NormalizeRoots(opts.Roots)
	for _, r := range s.roots {
		if !slices.Contains(roots, r.path) || !slices.Equal(r.excludes, opts.Excludes) {
			return ErrSnapshotScope
		}
	}
	dir := filepath.Dir(filename)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".local-index-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { f.Close(); os.Remove(tmp) }()
	if _, err = f.Write(make([]byte, snapshotHeaderSize)); err != nil {
		return err
	}
	digest := sha256.New()
	buffer := bufio.NewWriterSize(io.MultiWriter(f, digest), 256*1024)
	w := snapshotWriter{ctx: ctx, w: buffer}
	for _, r := range s.roots {
		w.str(r.path)
		w.strings(r.dirs)
		w.strings(r.excludes)
		w.strings(r.exts)
		w.array(r.dirStamp)
		w.raw(r.dirReady)
		w.u32(uint32(len(r.chunks)))
		for _, c := range r.chunks {
			w.u32(uint32(len(c.start)))
			w.blob(c.names)
			w.blob(c.folded)
			w.array(c.start)
			w.array(c.foldStart)
			w.array(c.parent)
			w.array(c.size)
			w.array(c.mtime)
			w.array(c.ext)
			w.raw(c.flags)
			w.raw(c.kind)
		}
	}
	w.strings(meta.Messages[:min(len(meta.Messages), 8)])
	if w.err != nil {
		return w.err
	}
	if err = buffer.Flush(); err != nil {
		return err
	}
	end, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	header := make([]byte, snapshotHeaderSize)
	copy(header, snapshotMagic[:])
	binary.LittleEndian.PutUint32(header[8:], snapshotFormat)
	if meta.Partial {
		binary.LittleEndian.PutUint32(header[12:], 1)
	}
	binary.LittleEndian.PutUint64(header[16:], uint64(end-snapshotHeaderSize))
	if !s.updated.IsZero() {
		binary.LittleEndian.PutUint64(header[24:], uint64(s.updated.UnixNano()))
	}
	binary.LittleEndian.PutUint32(header[32:], uint32(max(meta.Errors, 0)))
	binary.LittleEndian.PutUint32(header[36:], uint32(len(s.roots)))
	copy(header[40:72], scope[:])
	digest.Write(header[:72])
	copy(header[72:], digest.Sum(nil))
	if _, err = f.WriteAt(header, 0); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(tmp, filename); err != nil {
		return err
	}
	// Best effort directory sync: Windows does not support syncing directories.
	if parent, e := os.Open(dir); e == nil {
		parent.Sync()
		parent.Close()
	}
	return nil
}

// LoadSnapshot never stats indexed paths. Decode, bounds and checksum checks
// finish before publishing, so malformed files cannot replace a live version.
func (x *Index) LoadSnapshot(ctx context.Context, filename string, opts ScanOptions, progress func(LoadProgress)) (SnapshotMeta, error) {
	if !x.writer.TryLock() {
		return SnapshotMeta{}, ErrScanning
	}
	defer x.writer.Unlock()
	scope, err := scopeDigest(opts)
	if err != nil {
		return SnapshotMeta{}, err
	}
	f, err := os.Open(filename)
	if err != nil {
		return SnapshotMeta{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return SnapshotMeta{}, err
	}
	header := make([]byte, snapshotHeaderSize)
	if _, err := io.ReadFull(f, header); err != nil {
		return SnapshotMeta{}, ErrSnapshotFormat
	}
	if !bytes.Equal(header[:8], snapshotMagic[:]) || binary.LittleEndian.Uint32(header[8:]) != snapshotFormat || binary.LittleEndian.Uint32(header[12:]) > 1 {
		return SnapshotMeta{}, ErrSnapshotFormat
	}
	n := binary.LittleEndian.Uint64(header[16:])
	if st.Size() < snapshotHeaderSize || n != uint64(st.Size()-snapshotHeaderSize) {
		return SnapshotMeta{}, ErrSnapshotFormat
	}
	if !bytes.Equal(header[40:72], scope[:]) {
		return SnapshotMeta{}, ErrSnapshotScope
	}
	roots := int(binary.LittleEndian.Uint32(header[36:]))
	if roots > 65535 || uint64(roots)*20 > n {
		return SnapshotMeta{}, ErrSnapshotFormat
	}
	digest := sha256.New()
	raw := &snapshotInput{ctx: ctx, r: f, h: digest, total: int64(n), progress: progress}
	r := snapshotReader{ctx: ctx, r: &io.LimitedReader{R: bufio.NewReaderSize(raw, 256*1024), N: int64(n)}}
	data := make([]*rootData, 0, roots)
	want, _ := NormalizeRoots(opts.Roots)
	seenRoots := map[string]bool{}
	for range roots {
		rd := &rootData{path: r.str(), dirs: r.strings(), excludes: r.strings(), exts: r.strings()}
		if r.err != nil {
			break
		}
		if !slices.Contains(want, rd.path) || seenRoots[rd.path] || len(rd.dirs) == 0 || rd.dirs[0] != rd.path || len(rd.exts) == 0 || len(rd.exts) > 65536 || !slices.Equal(rd.excludes, opts.Excludes) {
			r.err = ErrSnapshotFormat
			break
		}
		seenRoots[rd.path] = true
		rd.dirStamp = make([]int64, len(rd.dirs))
		r.array(rd.dirStamp)
		rd.dirReady = r.raw(len(rd.dirs))
		for _, ready := range rd.dirReady {
			if ready > 1 {
				r.err = ErrSnapshotFormat
				break
			}
		}
		rd.foldDirs = make([][]byte, len(rd.dirs))
		for i, p := range rd.dirs {
			if i%256 == 0 && ctx.Err() != nil {
				return SnapshotMeta{}, ctx.Err()
			}
			if !filepath.IsAbs(p) || filepath.Clean(p) != p || !Within(p, rd.path) {
				r.err = ErrSnapshotFormat
				break
			}
			rd.foldDirs[i] = []byte(strings.ToLower(p))
		}
		parts := r.u32()
		if uint64(parts)*40 > uint64(max(r.r.N, 0)) {
			r.err = ErrSnapshotFormat
			break
		}
		for range parts {
			rows := int(r.u32())
			if rows < 1 || rows > chunkSize {
				r.err = ErrSnapshotFormat
				break
			}
			c := &chunk{names: r.blob(), folded: r.blob()}
			if r.err != nil || int64(rows)*32 > r.r.N {
				r.err = ErrSnapshotFormat
				break
			}
			c.start = make([]uint32, rows)
			c.foldStart = make([]uint32, rows)
			c.parent = make([]uint32, rows)
			c.size = make([]int64, rows)
			c.mtime = make([]int64, rows)
			c.ext = make([]uint16, rows)
			r.array(c.start)
			r.array(c.foldStart)
			r.array(c.parent)
			r.array(c.size)
			r.array(c.mtime)
			r.array(c.ext)
			c.flags = r.raw(rows)
			c.kind = r.raw(rows)
			if r.err != nil {
				break
			}
			if !validChunk(c, rd) {
				r.err = ErrSnapshotFormat
				break
			}
			rd.chunks = append(rd.chunks, c)
			rd.count += rows
		}
		if r.err != nil {
			break
		}
		data = append(data, rd)
	}
	var messages []string
	// Early format-2 snapshots had no warning footer. Keep reading those;
	// newer snapshots retain the bounded list of unreadable paths as well.
	if r.err == nil && r.r.N > 0 {
		count := r.u32()
		if count > 8 {
			r.err = ErrSnapshotFormat
		} else {
			for range count {
				messages = append(messages, r.str())
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return SnapshotMeta{}, err
	}
	digest.Write(header[:72])
	if r.err != nil || r.r.N != 0 || !bytes.Equal(header[72:], digest.Sum(nil)) {
		return SnapshotMeta{}, ErrSnapshotFormat
	}
	updated := time.Time{}
	if stamp := int64(binary.LittleEndian.Uint64(header[24:])); stamp != 0 {
		updated = time.Unix(0, stamp)
	}
	x.publish(data, updated)
	if progress != nil {
		progress(LoadProgress{Read: int64(n), Total: int64(n)})
	}
	return SnapshotMeta{Partial: header[12] == 1, Errors: int(binary.LittleEndian.Uint32(header[32:])), Messages: messages}, nil
}

type snapshotWriter struct {
	ctx context.Context
	w   io.Writer
	err error
}

func (w *snapshotWriter) array(v any) {
	if w.err == nil {
		w.err = w.ctx.Err()
		if w.err == nil {
			w.err = binary.Write(w.w, binary.LittleEndian, v)
		}
	}
}
func (w *snapshotWriter) u32(v uint32) { w.array(v) }
func (w *snapshotWriter) raw(v []byte) {
	if w.err == nil {
		w.err = w.ctx.Err()
		if w.err == nil {
			_, w.err = w.w.Write(v)
		}
	}
}
func (w *snapshotWriter) blob(v []byte) { w.u32(uint32(len(v))); w.raw(v) }
func (w *snapshotWriter) str(v string)  { w.blob([]byte(v)) }
func (w *snapshotWriter) strings(v []string) {
	w.u32(uint32(len(v)))
	for _, s := range v {
		w.str(s)
	}
}

type snapshotReader struct {
	ctx context.Context
	r   *io.LimitedReader
	err error
}

func (r *snapshotReader) array(v any) {
	if r.err == nil {
		r.err = r.ctx.Err()
		if r.err == nil {
			r.err = binary.Read(r.r, binary.LittleEndian, v)
		}
	}
}
func (r *snapshotReader) u32() uint32 { var n uint32; r.array(&n); return n }
func (r *snapshotReader) raw(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || int64(n) > r.r.N {
		r.err = ErrSnapshotFormat
		return nil
	}
	b := make([]byte, n)
	_, r.err = io.ReadFull(r.r, b)
	return b
}
func (r *snapshotReader) blob() []byte {
	n := r.u32()
	if n > 64*1024*1024 {
		r.err = ErrSnapshotFormat
		return nil
	}
	return r.raw(int(n))
}
func (r *snapshotReader) str() string {
	n := r.u32()
	if n > 1024*1024 {
		r.err = ErrSnapshotFormat
		return ""
	}
	return string(r.raw(int(n)))
}
func (r *snapshotReader) strings() []string {
	n := r.u32()
	if uint64(n)*4 > uint64(max(r.r.N, 0)) || r.err != nil {
		r.err = ErrSnapshotFormat
		return nil
	}
	out := make([]string, n)
	for i := range out {
		out[i] = r.str()
		if r.err != nil {
			return nil
		}
	}
	return out
}

type snapshotInput struct {
	ctx               context.Context
	r                 io.Reader
	h                 hash.Hash
	total, read, next int64
	progress          func(LoadProgress)
}

func (r *snapshotInput) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	r.h.Write(p[:n])
	r.read += int64(n)
	if r.progress != nil && r.read >= r.next {
		r.next = r.read + 1024*1024
		r.progress(LoadProgress{Read: r.read, Total: r.total})
	}
	return n, err
}
func validChunk(c *chunk, r *rootData) bool {
	validNames := func(buf []byte, offsets []uint32) bool {
		if len(buf) == 0 || offsets[0] != 0 || buf[len(buf)-1] != 0 {
			return false
		}
		for i, start := range offsets {
			end := uint32(len(buf))
			if i+1 < len(offsets) {
				end = offsets[i+1]
			}
			if start >= end || int(end) > len(buf) || buf[end-1] != 0 || bytes.IndexByte(buf[start:end-1], 0) >= 0 {
				return false
			}
		}
		return true
	}
	if !validNames(c.names, c.start) || !validNames(c.folded, c.foldStart) {
		return false
	}
	for i := range c.start {
		name := c.name(i)
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name || filepath.VolumeName(name) != "" || int(c.parent[i]) >= len(r.dirs) || int(c.ext[i]) >= len(r.exts) || c.flags[i]&^(flagDir|flagHidden|flagLink) != 0 || c.kind[i] != category(r.exts[c.ext[i]], c.flags[i]&flagDir != 0) {
			return false
		}
		// Case-folded buffers are checked without allocating one string per
		// entry. The checksum protects their contents and their correspondence.
	}
	return true
}
