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
	"time"
)

// Progress is reported while Execute runs.
type Progress struct {
	// Done and Total count the plan's actions that change the target.
	Done, Total int
	// DoneBytes and TotalBytes measure the copies.
	DoneBytes, TotalBytes int64
	// Current is the path being worked on.
	Current string
}

// FileError is an action that failed.
type FileError struct {
	Action Action
	Err    error
}

// Result is what Execute did.
type Result struct {
	Created, Updated, Mkdirs, Deleted, Rmdirs, Cleaned int
	Bytes                                              int64
	// Kept counts the target directories a mirror could not delete because
	// they hold excluded files.
	Kept     int
	Errors   []FileError
	Canceled bool
	// Fatal is set when the backup stopped before its end, as when a root
	// went away.
	Fatal error
	// TimesLost counts the copies whose modification time the target did not
	// keep: they will be copied again by the next backup.
	TimesLost int
	Started   time.Time
	Finished  time.Time
}

// Hooks receive what Execute does. Both may be nil, and are called from the
// goroutine running Execute.
type Hooks struct {
	// Progress is called at most ten times a second, and once at the end.
	Progress func(Progress)
	// Done is called after each action that changes the target, with its
	// error or nil.
	Done func(a Action, err error)
}

// Execute carries out the plan. It stops at the next file when ctx is
// canceled; a file being copied when it stops is left as it was.
func Execute(ctx context.Context, p *Plan, h Hooks) *Result {
	r := &Result{Started: time.Now()}
	defer func() { r.Finished = time.Now() }()

	prog := Progress{Total: p.Summary.Changes(), TotalBytes: p.Summary.CopyBytes}
	var last time.Time
	report := func(force bool) {
		if h.Progress != nil && (force || time.Since(last) >= 100*time.Millisecond) {
			last = time.Now()
			h.Progress(prog)
		}
	}
	defer report(true)

	// The roots may have gone since the plan was made, as a share unmounted.
	if info, err := os.Stat(p.source); err != nil || !info.IsDir() {
		r.Fatal = fmt.Errorf("源目录已无法访问: %v", err)
		return r
	}
	if p.TargetMissing {
		if err := os.MkdirAll(p.target, 0o755); err != nil {
			r.Fatal = fmt.Errorf("无法创建目标目录: %w", err)
			return r
		}
	} else if info, err := os.Stat(p.target); err != nil || !info.IsDir() {
		r.Fatal = fmt.Errorf("目标目录已无法访问: %v", err)
		return r
	}

	buf := make([]byte, 1<<20)
	for _, a := range p.Actions {
		if a.Kind == ActSkip {
			continue
		}
		if ctx.Err() != nil {
			r.Canceled = true
			return r
		}
		prog.Current = a.Path
		report(false)
		dst := filepath.Join(p.target, filepath.FromSlash(a.Path))
		var err error
		switch a.Kind {
		case ActDelete, ActCleanup:
			err = os.Remove(dst)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
			if err == nil {
				if a.Kind == ActDelete {
					r.Deleted++
				} else {
					r.Cleaned++
				}
			}
		case ActRmdir:
			var kept bool
			if kept, err = removeDir(dst); err == nil {
				if kept {
					r.Kept++
				} else {
					r.Rmdirs++
				}
			}
		case ActMkdir:
			if err = os.MkdirAll(dst, 0o755); err == nil {
				r.Mkdirs++
			}
		case ActCreate, ActUpdate:
			src := filepath.Join(p.source, filepath.FromSlash(a.Path))
			start := prog.DoneBytes
			var kept bool
			kept, err = copyFile(ctx, src, dst, a, buf, func(n int64) {
				prog.DoneBytes += n
				report(false)
			})
			// The file may have changed size since the plan was made.
			prog.DoneBytes = start + a.Size
			if err != nil {
				if ctx.Err() != nil {
					r.Canceled = true
					return r
				}
			} else {
				r.Bytes += a.Size
				if a.Kind == ActCreate {
					r.Created++
				} else {
					r.Updated++
				}
				if !kept {
					r.TimesLost++
				}
			}
		}
		prog.Done++
		if err != nil {
			r.Errors = append(r.Errors, FileError{Action: a, Err: err})
		}
		if h.Done != nil {
			h.Done(a, err)
		}
	}
	prog.Current = ""
	return r
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

// copyFile copies src to dst through a temporary file beside dst, renamed
// over it once complete, so that dst is never half written. timeKept reports
// whether dst has the source's modification time.
func copyFile(ctx context.Context, src, dst string, a Action, buf []byte, onBytes func(int64)) (timeKept bool, err error) {
	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return false, err
	}
	modTime := info.ModTime()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	tmp := filepath.Join(filepath.Dir(dst), tempFileName())
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, a.perm|0o600)
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
	if err := out.Sync(); err != nil {
		return false, err
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
	got, err := os.Stat(dst)
	return err == nil && absDuration(got.ModTime().Sub(modTime)) <= DefaultTimeTolerance, nil
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
