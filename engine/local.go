package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
)

// LocalWorkers is how many files a local target copies at once: on a
// network mount, each file waits for round trips, which overlap.
const LocalWorkers = 8

// localTarget is a directory of this computer, or one the system mounts.
type localTarget struct {
	dir string // resolved, absolute
	// The first copies check that the target keeps modification times,
	// which does not change from file to file.
	checked   atomic.Int32
	timesLost atomic.Bool
}

func newLocalTarget(source, target string) (*localTarget, error) {
	if target == "" {
		return nil, errors.New("请选择目标目录")
	}
	dst, err := filepath.Abs(target)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(dst); err == nil {
		dst = resolved
		info, err := os.Stat(dst)
		if err != nil {
			return nil, fmt.Errorf("无法访问目标目录 %s: %w", target, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("目标 %s 不是目录", target)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("无法访问目标目录 %s: %w", target, err)
	}
	if within(source, dst) || within(dst, source) {
		return nil, errors.New("源目录和目标目录不能相同，也不能一个在另一个里面")
	}
	return &localTarget{dir: dst}, nil
}

func (t *localTarget) String() string { return t.dir }

func (t *localTarget) Scan(ctx context.Context, f *Filter, onEntry func()) (*Tree, bool, error) {
	if _, err := os.Stat(t.dir); errors.Is(err, os.ErrNotExist) {
		return &Tree{}, true, nil
	}
	tr, err := scan(ctx, t.dir, f, onEntry)
	return tr, false, err
}

func (t *localTarget) CaseInsensitive() bool { return caseInsensitive(t.dir) }

func (t *localTarget) Apply(ctx context.Context, p *Plan, run *Run) {
	if p.TargetMissing {
		if err := os.MkdirAll(t.dir, 0o755); err != nil {
			run.Fatal(fmt.Errorf("无法创建目标目录: %w", err))
			return
		}
	} else if info, err := os.Stat(t.dir); err != nil || !info.IsDir() {
		run.Fatal(fmt.Errorf("目标目录已无法访问: %v", err))
		return
	}

	// Deletions and directories, in order, then the copies, at once.
	var copies []Action
	for _, a := range p.Actions {
		if ctx.Err() != nil {
			return
		}
		switch a.Kind {
		case ActSkip:
		case ActCreate, ActUpdate:
			copies = append(copies, a)
		default:
			run.Start(a.Path)
			t.apply(a, run)
		}
	}

	jobs := make(chan Action)
	var wg sync.WaitGroup
	for range LocalWorkers {
		wg.Go(func() {
			buf := make([]byte, 1<<20)
			for a := range jobs {
				run.Start(a.Path)
				var sent int64
				kept, err := t.copyFile(ctx, p.source, a, buf, func(n int64) {
					sent += n
					run.Bytes(n)
				})
				if ctx.Err() != nil {
					return // canceled: the file is not counted
				}
				run.Copied(a, sent, kept, err)
			}
		})
	}
	for _, a := range copies {
		select {
		case jobs <- a:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
}

func (t *localTarget) path(rel string) string { return filepath.Join(t.dir, filepath.FromSlash(rel)) }

func (t *localTarget) apply(a Action, run *Run) {
	dst := t.path(a.Path)
	switch a.Kind {
	case ActDelete, ActCleanup:
		err := os.Remove(dst)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		run.Done(a, err)
	case ActRmdir:
		kept, err := removeDir(dst)
		if kept {
			run.Kept(a)
		} else {
			run.Done(a, err)
		}
	case ActMkdir:
		run.Done(a, os.MkdirAll(dst, 0o755))
	}
}

// removeDir removes an empty directory. One still holding entries, as files
// excluded from the backup, stays, and kept is true.
func removeDir(dir string) (kept bool, err error) {
	f, err := os.Open(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	names, _ := f.Readdirnames(1)
	f.Close()
	if len(names) > 0 {
		return true, nil
	}
	return false, os.Remove(dir)
}

// copyFile copies a file of the source through a temporary file beside its
// destination, renamed over it once complete, so that the destination is
// never half written. Nothing is flushed to the disk before the rename: a
// file cut short by a crash has the wrong size, and is copied again by the
// next backup. timeKept reports whether the destination has the source's
// modification time.
func (t *localTarget) copyFile(ctx context.Context, source string, a Action, buf []byte, onBytes func(int64)) (timeKept bool, err error) {
	in, err := os.Open(filepath.Join(source, filepath.FromSlash(a.Path)))
	if err != nil {
		return false, err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return false, err
	}
	modTime := info.ModTime()

	dst := t.path(a.Path)
	tmp := filepath.Join(filepath.Dir(dst), tempFileName())
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, a.perm|0o600)
	if errors.Is(err, os.ErrNotExist) {
		// Its directory went, or was never planned, as for a plan made
		// before the directory was excluded.
		if err = os.MkdirAll(filepath.Dir(dst), 0o755); err == nil {
			out, err = os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, a.perm|0o600)
		}
	}
	if err != nil {
		return false, err
	}
	done := false
	defer func() {
		if !done {
			out.Close()
			os.Remove(tmp)
		}
	}()

	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return false, werr
			}
			onBytes(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return false, rerr
		}
	}
	if err := out.Close(); err != nil {
		return false, err
	}
	done = true
	timeErr := os.Chtimes(tmp, modTime, modTime)
	if err := rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return false, err
	}
	if timeErr != nil {
		return false, nil
	}
	if t.checked.Add(1) > 16 {
		return !t.timesLost.Load(), nil
	}
	got, err := os.Stat(dst)
	kept := err == nil && absDuration(got.ModTime().Sub(modTime)) <= DefaultTimeTolerance
	if !kept {
		t.timesLost.Store(true)
	}
	return kept, nil
}

// rename moves tmp over dst, which Windows refuses while dst is read-only.
func rename(tmp, dst string) error {
	err := os.Rename(tmp, dst)
	if err != nil && runtime.GOOS == "windows" {
		if info, serr := os.Stat(dst); serr == nil && info.Mode().Perm()&0o200 == 0 {
			if os.Chmod(dst, 0o666) == nil {
				err = os.Rename(tmp, dst)
			}
		}
	}
	return err
}

func tempFileName() string {
	var b [4]byte
	rand.Read(b[:])
	return ".filesync-" + hex.EncodeToString(b[:]) + ".tmp"
}
