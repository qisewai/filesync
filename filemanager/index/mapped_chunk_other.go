//go:build !linux || !amd64

package index

func mappedChunk(_ *snapshotMapping, _, _ int64) *chunk { return nil }
