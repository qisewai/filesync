package index

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"runtime"
)

// readChunk reads one immutable snapshot block. In-memory scan results use the
// same interface; a saved snapshot keeps only offsets and an open file handle.
func readChunk(ctx context.Context, root *rootData, stored *chunk) (*chunk, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if stored.file == nil {
		return stored, nil
	}
	var source io.Reader
	if stored.mapping != nil {
		// Detect truncated snapshots before touching mapped pages. Application
		// writers never modify a published inode in place.
		st, err := stored.file.Stat()
		if err != nil {
			return nil, err
		}
		end := stored.offset + stored.length
		if stored.offset < 0 || end < stored.offset || end > st.Size() || end > int64(len(stored.mapping.data)) {
			return nil, ErrSnapshotFormat
		}
		if len(stored.start) != 0 {
			return stored, nil
		}
		source = bytes.NewReader(stored.mapping.data[stored.offset:end])
		defer runtime.KeepAlive(stored.mapping)
	} else {
		source = bufio.NewReaderSize(io.NewSectionReader(stored.file, stored.offset, stored.length), 64*1024)
	}
	r := snapshotReader{ctx: ctx, r: &io.LimitedReader{R: source, N: stored.length}}
	rows := int(r.u32())
	if rows < 1 || rows > chunkSize {
		return nil, ErrSnapshotFormat
	}
	c := &chunk{names: r.blob(), folded: r.blob()}
	if r.err != nil || int64(rows)*32 > r.r.N {
		return nil, ErrSnapshotFormat
	}
	c.start = make([]uint32, rows)
	c.foldStart = make([]uint32, rows)
	c.parent = make([]uint32, rows)
	c.size = make([]int64, rows)
	c.mtime = make([]int64, rows)
	c.ext = make([]uint16, rows)
	readSnapshotArray(&r, c.start)
	readSnapshotArray(&r, c.foldStart)
	readSnapshotArray(&r, c.parent)
	readSnapshotArray(&r, c.size)
	readSnapshotArray(&r, c.mtime)
	readSnapshotArray(&r, c.ext)
	c.flags = r.raw(rows)
	c.kind = r.raw(rows)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.err != nil || r.r.N != 0 || !validChunk(c, root) {
		return nil, ErrSnapshotFormat
	}
	c.compact()
	return c, nil
}

// A manual rescan still uses the existing in-memory incremental algorithm.
// Materialize a private working copy, leaving searches on the saved version.
func materialize(ctx context.Context, s *snapshot) (*snapshot, error) {
	if s == nil {
		return nil, nil
	}
	copy := &snapshot{version: s.version, updated: s.updated, roots: make([]*rootData, len(s.roots))}
	for i, root := range s.roots {
		r := *root
		r.chunks = make([]*chunk, len(root.chunks))
		for j, stored := range root.chunks {
			c, err := readChunk(ctx, root, stored)
			if err != nil {
				return nil, err
			}
			r.chunks[j] = c
		}
		copy.roots[i] = &r
	}
	return copy, nil
}
