package fs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type CreationPolicy struct {
	FileMode, DirMode os.FileMode
	UID, GID          int
}

type RootTransferOptions struct {
	Metadata *CreationPolicy
	Move     bool
	Policy   TransferPolicy
	// Guards receive paths relative to the respective original root.
	SourceGuard, TargetGuard func(string) error
	Progress                 func(int64, string)
	Total                    func(int64)
}
type rootEntry struct {
	path, relative string
	info           os.FileInfo
}

func rootManifest(ctx context.Context, root *os.Root, p string, guard func(string) error) ([]rootEntry, error) {
	var entries []rootEntry
	var walk func(string) error
	walk = func(name string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if guard != nil {
			if err := guard(name); err != nil {
				return err
			}
		}
		fi, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if !fi.IsDir() && !fi.Mode().IsRegular() && fi.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%w: %s", ErrUnsupported, name)
		}
		rel, err := filepath.Rel(p, name)
		if err != nil {
			return err
		}
		entries = append(entries, rootEntry{name, rel, fi})
		if !fi.IsDir() {
			return nil
		}
		dir, err := root.Open(name)
		if err != nil {
			return err
		}
		defer dir.Close()
		actual, err := dir.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(fi, actual) {
			return ErrSourceChanged
		}
		for {
			ds, e := dir.ReadDir(256)
			for _, d := range ds {
				if err := walk(filepath.Join(name, d.Name())); err != nil {
					return err
				}
			}
			if e == io.EOF {
				return nil
			}
			if e != nil {
				return e
			}
		}
	}
	err := walk(p)
	return entries, err
}

// RootTransfer never opens or mutates absolute paths. Both roots remain owned
// by the caller until this operation finishes. Leaf symlinks are copied as
// links, and directory symlinks are never merged into the destination tree.
func RootTransfer(ctx context.Context, src *os.Root, from string, dst *os.Root, to string, o RootTransferOptions) error {
	if from == "." || to == "." || !filepath.IsLocal(from) || !filepath.IsLocal(to) {
		return errors.New("transfer requires non-root relative paths")
	}
	if o.Progress == nil {
		o.Progress = func(int64, string) {}
	}
	entries, err := rootManifest(ctx, src, from, o.SourceGuard)
	if err != nil {
		return err
	}
	if target, err := dst.Lstat(to); err == nil && os.SameFile(entries[0].info, target) {
		return errors.New("source and destination are the same file")
	}
	// Reject overlapping trees even when separate configured roots or bind
	// mounts give the same directory a different pathname.
	for parent := to; ; parent = filepath.Dir(parent) {
		fi, e := dst.Stat(parent)
		if e == nil && fi.IsDir() {
			for _, entry := range entries {
				if entry.info.IsDir() && os.SameFile(entry.info, fi) {
					return errors.New("destination overlaps source tree")
				}
			}
		}
		if parent == "." {
			break
		}
	}
	var total int64
	for _, e := range entries {
		if e.info.Mode().IsRegular() {
			total += e.info.Size()
		}
	}
	if o.Total != nil {
		o.Total(total)
	}
	if o.TargetGuard != nil {
		if err := o.TargetGuard(to); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.Move {
		// Atomic no-replace fast path; cross-device or existing targets use the
		// exact same conflict policies as a copy followed by receipt cleanup.
		if _, e := dst.Lstat(to); errors.Is(e, os.ErrNotExist) {
			e = rootRename(src, from, dst, to, false)
			if e == nil {
				o.Progress(total, filepath.Base(to))
				return nil
			}
			if !isCrossDevice(e) && !errors.Is(e, os.ErrExist) {
				return e
			}
		}
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		actual, err := src.Lstat(e.path)
		if err != nil {
			return err
		}
		if !sameRootEntry(e.info, actual) {
			return ErrSourceChanged
		}
		target := filepath.Join(to, e.relative)
		if o.TargetGuard != nil {
			if err := o.TargetGuard(target); err != nil {
				return err
			}
		}
		switch {
		case e.info.IsDir():
			fi, err := dst.Lstat(target)
			if err == nil {
				if !fi.IsDir() || !o.Policy.MergeDirectories {
					return &os.PathError{Op: "copy", Path: target, Err: os.ErrExist}
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			} else {
				if err := dst.Mkdir(target, e.info.Mode().Perm()|0700); err != nil {
					return err
				}
				if o.Metadata != nil {
					d, err := dst.Open(target)
					if err != nil {
						return err
					}
					err = applyCreation(d, o.Metadata, true)
					d.Close()
					if err != nil {
						return err
					}
				}
			}
		case e.info.Mode()&os.ModeSymlink != 0:
			link, err := src.Readlink(e.path)
			if err != nil {
				return err
			}
			if err := rootWriteLink(ctx, dst, target, link, o.Policy.ReplaceFiles, o.Metadata); err != nil {
				return err
			}
		default:
			if err := rootCopyFile(ctx, src, e.path, dst, target, e.info, o.Policy.ReplaceFiles, o.Progress, o.Metadata); err != nil {
				return err
			}
		}
	}
	if !o.Move {
		return nil
	}
	// Validate the entire receipt before deleting any source file. A new source
	// child survives because removal of its parent fails with ENOTEMPTY.
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		fi, err := src.Lstat(e.path)
		if err != nil {
			return err
		}
		if !sameRootEntry(e.info, fi) {
			return ErrSourceChanged
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		e := entries[i]
		if o.SourceGuard != nil {
			if err := o.SourceGuard(e.path); err != nil {
				return err
			}
		}
		fi, err := src.Lstat(e.path)
		if err != nil {
			return err
		}
		if !sameRootEntry(e.info, fi) {
			return ErrSourceChanged
		}
		if err := src.Remove(e.path); err != nil {
			return err
		}
	}
	return nil
}
func sameRootEntry(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Mode().Type() == b.Mode().Type() && (a.IsDir() || a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()))
}
func rootTemp(root *os.Root, parent string) (string, *os.File, error) {
	for i := 0; i < 10; i++ {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return "", nil, err
		}
		name := filepath.Join(parent, ".filemanager-copy-"+hex.EncodeToString(id[:]))
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return name, f, err
	}
	return "", nil, os.ErrExist
}
func rootCopyFile(ctx context.Context, src *os.Root, from string, dst *os.Root, to string, fi os.FileInfo, replace bool, progress func(int64, string), metadata *CreationPolicy) error {
	in, err := src.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	actual, err := in.Stat()
	if err != nil {
		return err
	}
	if !sameRootEntry(fi, actual) {
		return ErrSourceChanged
	}
	// Pin the destination parent so temporary creation and publication use the
	// same directory even if another process renames an ancestor.
	parent, err := dst.OpenRoot(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer parent.Close()
	name, out, err := rootTemp(parent, ".")
	if err != nil {
		return err
	}
	defer parent.Remove(name)
	defer out.Close()
	if metadata != nil {
		if err := applyCreation(out, metadata, false); err != nil {
			return err
		}
	}
	if metadata == nil {
		if err := out.Chmod(fi.Mode().Perm()); err != nil {
			return err
		}
	}
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, e := in.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				return err
			}
			progress(int64(n), filepath.Base(from))
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := parent.Chtimes(name, fi.ModTime(), fi.ModTime()); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return rootRename(parent, name, parent, filepath.Base(to), replace)
}
func rootWriteLink(ctx context.Context, dst *os.Root, to, link string, replace bool, metadata *CreationPolicy) error {
	parent, err := dst.OpenRoot(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer parent.Close()
	// A private directory lets symlink creation fail without touching the old target.
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	dir := ".filemanager-copy-" + hex.EncodeToString(id[:])
	if err := parent.Mkdir(dir, 0700); err != nil {
		return err
	}
	defer parent.Remove(dir)
	temp := filepath.Join(dir, "link")
	if err := parent.Symlink(link, temp); err != nil {
		return err
	}
	defer parent.Remove(temp)
	if metadata != nil && metadata.UID >= 0 {
		if err := parent.Lchown(temp, metadata.UID, metadata.GID); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return rootRename(parent, temp, parent, filepath.Base(to), replace)
}

func applyCreation(f *os.File, p *CreationPolicy, dir bool) error {
	mode := p.FileMode
	if dir {
		mode = p.DirMode
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if p.UID >= 0 {
		return f.Chown(p.UID, p.GID)
	}
	return nil
}
