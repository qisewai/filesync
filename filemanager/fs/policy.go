package fs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// TransferPolicy keeps file replacement separate from directory merging.
type TransferPolicy struct{ ReplaceFiles, MergeDirectories bool }

// Transfer is for trusted local paths. Restricted roots must not expose these
// absolute-path primitives without a confined implementation.
func Transfer(ctx context.Context, from, to string, move bool, policy TransferPolicy, progress func(int64, string), guard func(string) error) error {
	if progress == nil {
		progress = func(int64, string) {}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkCopyDestination(from, to); err != nil {
		return err
	}
	if guard != nil {
		if err := guard(to); err != nil {
			return err
		}
		if move {
			if err := guard(from); err != nil {
				return err
			}
		}
	}
	if move {
		if _, err := os.Lstat(to); errors.Is(err, os.ErrNotExist) {
			err = renameNoReplace(from, to)
			if err == nil {
				n, e := TreeSizeContext(ctx, to)
				progress(n, filepath.Base(to))
				return e
			}
			if !isCrossDevice(err) && !errors.Is(err, os.ErrExist) {
				return err
			}
		}
	}
	var receipt *Receipt
	var err error
	if move {
		receipt, err = Capture(ctx, from)
		if err != nil {
			return err
		}
	}
	err = copyTreePolicy(ctx, from, to, progress, guard, policy)
	if err != nil {
		return err
	}
	if receipt != nil {
		return receipt.RemoveCopied(ctx)
	}
	return nil
}

func TreeSizeContext(ctx context.Context, p string) (int64, error) {
	var n int64
	err := filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			n += info.Size()
		}
		return nil
	})
	return n, err
}
