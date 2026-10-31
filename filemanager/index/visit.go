package index

import (
	"context"
	"math/bits"
	"runtime"
)

// Each visits a pinned result in storage order, without sorting or allocating
// a full result page. Intended for aggregates and streaming consumers.
func (r *Results) Each(ctx context.Context, fn func(Entry) error) error {
	defer runtime.KeepAlive(r)
	if r == nil {
		return ctx.Err()
	}
	if r.remotePage != nil {
		for offset := 0; offset < r.total; offset += 1000 {
			entries, err := r.Page(ctx, offset, 1000)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if err := fn(entry); err != nil {
					return err
				}
			}
		}
		return ctx.Err()
	}
	for _, match := range r.matches {
		if err := ctx.Err(); err != nil {
			return err
		}
		root := r.snap.roots[match.base>>48]
		c, err := readChunk(ctx, root, root.chunks[uint32(match.base>>16)])
		if err != nil {
			return err
		}
		for wi, word := range match.rows {
			for word != 0 {
				i := wi*64 + bits.TrailingZeros64(word)
				if i >= len(c.start) {
					return ErrSnapshotFormat
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := fn(root.entry(c, i)); err != nil {
					return err
				}
				word &= word - 1
			}
		}
	}
	return nil
}
