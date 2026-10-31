package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// TreeSize is the size of the files below p, links not followed.
func TreeSize(p string) int64 {
	var n int64
	filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// MoveTree moves from to to: a rename where it can, else a copy, the
// source removed once all of it was copied.
func MoveTree(ctx context.Context, from, to string, progress func(int64, string)) error {
	return MoveTreeChecked(ctx, from, to, progress, nil)
}
func MoveTreeChecked(ctx context.Context, from, to string, progress func(int64, string), guard func(string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if progress == nil {
		progress = func(int64, string) {}
	}
	if filepath.Clean(from) == filepath.Clean(to) {
		return nil
	}
	if guard != nil {
		if err := guard(to); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(to); err != nil {
		if err := os.Rename(from, to); err == nil {
			progress(TreeSize(to), filepath.Base(to))
			return nil
		} else if !isCrossDevice(err) {
			return err
		}
	} else if fi, err := os.Lstat(from); err == nil && !fi.IsDir() {
		// A file over one: renamed over it, as replacing it.
		if err := os.Rename(from, to); err == nil {
			progress(fi.Size(), filepath.Base(to))
			return nil
		}
	}
	receipt, err := Capture(ctx, from)
	if err != nil {
		return err
	}
	if err := CopyTreeChecked(ctx, from, to, progress, guard); err != nil {
		return err
	}
	return receipt.RemoveCopied(ctx)
}

func isCrossDevice(err error) bool {
	var le *os.LinkError
	return errors.As(err, &le) && errors.Is(le.Err, syscall.EXDEV)
}

// CopyTree copies from to to, folders merged, files replaced, links as
// links, keeping times and permissions.
func CopyTree(ctx context.Context, from, to string, progress func(int64, string)) error {
	return CopyTreeChecked(ctx, from, to, progress, nil)
}
func CopyTreeChecked(ctx context.Context, from, to string, progress func(int64, string), guard func(string) error) error {
	return copyTreePolicy(ctx, from, to, progress, guard, TransferPolicy{ReplaceFiles: true, MergeDirectories: true})
}

func copyTreePolicy(ctx context.Context, from, to string, progress func(int64, string), guard func(string) error, policy TransferPolicy) error {
	if err := checkCopyDestination(from, to); err != nil {
		return err
	}
	if progress == nil {
		progress = func(int64, string) {}
	}
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if guard != nil {
			if err := guard(dst); err != nil {
				return err
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if existing, e := os.Lstat(dst); e == nil {
				if !policy.MergeDirectories || !existing.IsDir() {
					return &os.PathError{Op: "copy", Path: dst, Err: os.ErrExist}
				}
			} else if !errors.Is(e, os.ErrNotExist) {
				return e
			}
			if err := os.Mkdir(dst, info.Mode().Perm()|0o700); err != nil && !(policy.MergeDirectories && errors.Is(err, os.ErrExist)) {
				return err
			}
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if policy.ReplaceFiles {
				if existing, e := os.Lstat(dst); e == nil && existing.IsDir() {
					return &os.PathError{Op: "copy", Path: dst, Err: os.ErrExist}
				}
				if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			return os.Symlink(target, dst)
		case !d.Type().IsRegular():
			return fmt.Errorf("%w: %s", ErrUnsupported, p)
		}
		if err := copyFilePolicy(ctx, p, dst, info, func(n int64) { progress(n, d.Name()) }, policy.ReplaceFiles); err != nil {
			return err
		}
		return nil
	})
}

func checkCopyDestination(from, to string) error {
	source, err := filepath.Abs(from)
	if err != nil {
		return err
	}
	target, err := filepath.Abs(to)
	if err != nil {
		return err
	}
	if parent, err := filepath.EvalSymlinks(filepath.Dir(source)); err == nil {
		source = filepath.Join(parent, filepath.Base(source))
	}
	if parent, err := filepath.EvalSymlinks(filepath.Dir(target)); err == nil {
		target = filepath.Join(parent, filepath.Base(target))
	}
	rel, err := filepath.Rel(source, target)
	if err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return fmt.Errorf("cannot copy into the source: %s", to)
	}
	return nil
}

// CopyFile copies a file, through a temporary file beside the copy, which
// replaces what was there once whole.
func CopyFile(ctx context.Context, from, to string, info fs.FileInfo, progress func(int64)) error {
	return copyFilePolicy(ctx, from, to, info, progress, true)
}
func copyFilePolicy(ctx context.Context, from, to string, info fs.FileInfo, progress func(int64), replace bool) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(to), ".filemanager-copy-*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer os.Remove(tmp)
	if err := out.Chmod(info.Mode().Perm()); err != nil {
		out.Close()
		return err
	}
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			out.Close()
			os.Remove(tmp)
			return err
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(tmp)
				return werr
			}
			progress(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(tmp)
			return rerr
		}
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chtimes(tmp, info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !replace {
		return renameNoReplace(tmp, to)
	}
	return os.Rename(tmp, to)
}
