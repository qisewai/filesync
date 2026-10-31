//go:build !linux

package index

import "os"

type snapshotMapping struct{ data []byte }

func mapSnapshot(_ *os.File, _ int64) *snapshotMapping { return nil }
