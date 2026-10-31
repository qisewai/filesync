//go:build linux && amd64

package index

import (
	"encoding/binary"
	"unsafe"
)

// Only amd64 uses unaligned little-endian array views. The mapping is read-only
// and owned by the chunk; portable platforms use the ordinary decoder.
func mappedChunk(m *snapshotMapping, start, end int64) *chunk {
	if start < 0 || end < start || end > int64(len(m.data)) {
		return nil
	}
	b := m.data[start:end]
	if len(b) < 4 {
		return nil
	}
	rows := int(binary.LittleEndian.Uint32(b))
	b = b[4:]
	if rows < 1 || rows > chunkSize {
		return nil
	}
	c := &chunk{mapping: m}
	for _, dst := range []*[]byte{&c.names, &c.folded} {
		if len(b) < 4 {
			return nil
		}
		n := uint64(binary.LittleEndian.Uint32(b))
		b = b[4:]
		if n > uint64(len(b)) {
			return nil
		}
		*dst = b[:n:n]
		b = b[n:]
	}
	if len(b) != rows*32 {
		return nil
	}
	c.start = mappedArray[uint32](&b, rows)
	c.foldStart = mappedArray[uint32](&b, rows)
	c.parent = mappedArray[uint32](&b, rows)
	c.size = mappedArray[int64](&b, rows)
	c.mtime = mappedArray[int64](&b, rows)
	c.ext = mappedArray[uint16](&b, rows)
	c.flags = b[:rows:rows]
	b = b[rows:]
	c.kind = b[:rows:rows]
	return c
}
func mappedArray[T uint16 | uint32 | int64](b *[]byte, n int) []T {
	var t T
	size := n * int(unsafe.Sizeof(t))
	v := unsafe.Slice((*T)(unsafe.Pointer(unsafe.SliceData(*b))), n)
	*b = (*b)[size:]
	return v
}
