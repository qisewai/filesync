package engine

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// Target is where a backup writes: a local directory, or a server.
type Target interface {
	// String names it for messages.
	String() string
	// Scan lists what the target has, leaving out what f excludes, and
	// calls onEntry for every entry listed, for progress. A target that
	// does not exist yet has nothing, and missing is true.
	Scan(ctx context.Context, f *Filter, onEntry func()) (t *Tree, missing bool, err error)
	// CaseInsensitive reports whether the target folds the case of names.
	CaseInsensitive() bool
	// Apply carries out the changes of a plan this target scanned for,
	// reporting each to run. It returns when they are all done or ctx is
	// canceled.
	Apply(ctx context.Context, p *Plan, run *Run)
}

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

// Hooks receive what Execute does. Both may be nil. They are called one at
// a time, from the goroutines doing the work.
type Hooks struct {
	// Progress is called at most ten times a second, and once at the end.
	Progress func(Progress)
	// Done is called after each action that changes the target, with its
	// error or nil.
	Done func(a Action, err error)
}

// Run is a backup being executed: targets report to it what they do, from
// as many goroutines as they like.
type Run struct {
	mu    sync.Mutex
	res   Result
	prog  Progress
	hooks Hooks
	last  time.Time
}

func (r *Run) report(force bool) {
	if r.hooks.Progress != nil && (force || time.Since(r.last) >= 100*time.Millisecond) {
		r.last = time.Now()
		r.hooks.Progress(r.prog)
	}
}

// Start notes the path being worked on.
func (r *Run) Start(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prog.Current = path
	r.report(false)
}

// Bytes counts bytes of a copy as they go.
func (r *Run) Bytes(n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prog.DoneBytes += n
	r.report(false)
}

// Done records an action other than a copy.
func (r *Run) Done(a Action, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		switch a.Kind {
		case ActDelete:
			r.res.Deleted++
		case ActCleanup:
			r.res.Cleaned++
		case ActRmdir:
			r.res.Rmdirs++
		case ActMkdir:
			r.res.Mkdirs++
		}
	}
	r.finish(a, err)
}

// Kept records a directory a mirror leaves because it holds excluded files.
func (r *Run) Kept(a Action) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.res.Kept++
	r.finish(a, nil)
}

// Copied records a copy, of which sent bytes went through Bytes. timeKept
// tells whether the target kept the source's modification time.
func (r *Run) Copied(a Action, sent int64, timeKept bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// The file may have changed size since the plan was made.
	r.prog.DoneBytes += a.Size - sent
	if err == nil {
		r.res.Bytes += a.Size
		if a.Kind == ActCreate {
			r.res.Created++
		} else {
			r.res.Updated++
		}
		if !timeKept {
			r.res.TimesLost++
		}
	}
	r.finish(a, err)
}

func (r *Run) finish(a Action, err error) {
	r.prog.Done++
	if err != nil {
		r.res.Errors = append(r.res.Errors, FileError{Action: a, Err: err})
	}
	if r.hooks.Done != nil {
		r.hooks.Done(a, err)
	}
	r.report(false)
}

// Fatal stops the backup for a reason that concerns it all.
func (r *Run) Fatal(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.res.Fatal == nil {
		r.res.Fatal = err
	}
}

// NewRun starts reporting work of total actions and totalBytes bytes that
// is not a plan, as an upload: report to it, then call Finish.
func NewRun(total int, totalBytes int64, h Hooks) *Run {
	r := &Run{hooks: h}
	r.res.Started = time.Now()
	r.prog = Progress{Total: total, TotalBytes: totalBytes}
	return r
}

// Finish ends the run, canceled if ctx is, and returns its result.
func (r *Run) Finish(ctx context.Context) *Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		r.res.Canceled = true
	}
	r.prog.Current = ""
	r.report(true)
	res := r.res
	res.Finished = time.Now()
	return &res
}

// Execute carries out the plan. It stops when ctx is canceled; files being
// copied then are left as they were.
func Execute(ctx context.Context, p *Plan, h Hooks) *Result {
	r := NewRun(p.Summary.Changes(), p.Summary.CopyBytes, h)
	// The source may have gone since the plan was made, as a share unmounted.
	if info, err := os.Stat(p.source); err != nil || !info.IsDir() {
		r.Fatal(fmt.Errorf("源目录已无法访问: %v", err))
	} else {
		p.dest.Apply(ctx, p, r)
	}
	return r.Finish(ctx)
}
