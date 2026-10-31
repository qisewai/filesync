package archive

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"

	"github.com/bodgit/sevenzip"
	"github.com/klauspost/compress/zstd"
	"github.com/stangelandcl/ppmd"
	"github.com/ulikunitz/xz/lzma"
)

// Bound dictionaries before allocation, including encoded 7z headers. Disable
// decryption for phase one rather than trying an empty password implicitly.
func init() {
	sevenzip.RegisterDecompressor([]byte{0x06, 0xf1, 0x07, 0x01}, func([]byte, uint64, []io.ReadCloser) (io.ReadCloser, error) { return nil, ErrEncrypted })
	sevenzip.RegisterDecompressor([]byte{0x03, 0x01, 0x01}, func(p []byte, size uint64, rs []io.ReadCloser) (io.ReadCloser, error) {
		if len(rs) != 1 || len(p) != 5 {
			return nil, errors.New("7z LZMA 属性无效")
		}
		if binary.LittleEndian.Uint32(p[1:]) > 256<<20 || size > uint64(MaxScan) {
			return nil, ErrLimit
		}
		h := make([]byte, 13)
		copy(h, p)
		binary.LittleEndian.PutUint64(h[5:], size)
		return &lazyDecoder{create: func() (io.Reader, error) {
			return (lzma.ReaderConfig{DictCap: 256 << 20}).NewReader(io.MultiReader(bytes.NewReader(h), rs[0]))
		}, close: rs[0].Close}, nil
	})
	sevenzip.RegisterDecompressor([]byte{0x21}, func(p []byte, size uint64, rs []io.ReadCloser) (io.ReadCloser, error) {
		if len(rs) != 1 || len(p) != 1 || p[0] > 40 {
			return nil, errors.New("7z LZMA2 属性无效")
		}
		dict := uint64(2|int(p[0]&1)) << (p[0]/2 + 11)
		if dict > 256<<20 || size > uint64(MaxScan) {
			return nil, ErrLimit
		}
		return &lazyDecoder{create: func() (io.Reader, error) { return (lzma.Reader2Config{DictCap: int(dict)}).NewReader2(rs[0]) }, close: rs[0].Close}, nil
	})
	sevenzip.RegisterDecompressor([]byte{0x03, 0x04, 0x01}, func(p []byte, size uint64, rs []io.ReadCloser) (io.ReadCloser, error) {
		if len(rs) != 1 || len(p) != 5 {
			return nil, errors.New("7z PPMd 属性无效")
		}
		mem := binary.LittleEndian.Uint32(p[1:])
		if mem > 256<<20 || size > uint64(MaxScan) {
			return nil, ErrLimit
		}
		return &lazyDecoder{create: func() (io.Reader, error) {
			r, err := ppmd.NewH7zReader(rs[0], int(p[0]), int(mem), int(size))
			return &r, err
		}, close: rs[0].Close}, nil
	})
	sevenzip.RegisterDecompressor([]byte{0x04, 0xf7, 0x11, 0x01}, func(_ []byte, size uint64, rs []io.ReadCloser) (io.ReadCloser, error) {
		if len(rs) != 1 {
			return nil, errors.New("7z zstd 属性无效")
		}
		if size > uint64(MaxScan) {
			return nil, ErrLimit
		}
		r, err := zstd.NewReader(rs[0], zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(256<<20))
		if err != nil {
			return nil, err
		}
		return &decoderCloser{Reader: r, close: func() error { r.Close(); return rs[0].Close() }}, nil
	})
}

type decoderCloser struct {
	io.Reader
	close func() error
}

func (d *decoderCloser) Close() error { return d.close() }

// Metadata probes close before the first Read, so dictionaries are allocated
// only for streams actually decoded (or for an encoded header).
type lazyDecoder struct {
	r      io.Reader
	create func() (io.Reader, error)
	close  func() error
	err    error
}

func (d *lazyDecoder) Read(p []byte) (int, error) {
	if d.r == nil && d.err == nil {
		d.r, d.err = d.create()
	}
	if d.err != nil {
		return 0, d.err
	}
	return d.r.Read(p)
}
func (d *lazyDecoder) Close() error { return d.close() }
