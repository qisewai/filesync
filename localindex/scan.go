package localindex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	"filesync/engine"
)

var ErrScanning = errors.New("本机索引扫描正在进行")

type ScanOptions struct {
	Roots, Excludes, Skip []string
	Workers               int
}
type Progress struct {
	Scanned, Count, Errors int
	ReadDirs, ReusedDirs   int
	Current                string
	Messages               []string
}
type directoryTask struct {
	id     uint32
	path   string
	hidden bool
}
type scanBatch struct {
	task    directoryTask
	entries []Entry
	failed  []string
	err     error
	done    bool
	stamp   int64
	ready   bool
	reused  bool
}

// Scan manually enumerates metadata. Existing root versions stay searchable
// until replaced. A first scan publishes immutable chunks as they arrive.
// Canceled rescans retain old versions, and unreadable subtrees retain records.
func (x *Index) Scan(ctx context.Context, opts ScanOptions, progress func(Progress)) (Progress, error) {
	return x.scan(ctx, opts, progress, false)
}

// Update reuses stable directory name lists but still stats existing files.
// No timer or filesystem listener calls it; the desktop user requests it.
func (x *Index) Update(ctx context.Context, opts ScanOptions, progress func(Progress)) (Progress, error) {
	return x.scan(ctx, opts, progress, true)
}
func (x *Index) scan(ctx context.Context, opts ScanOptions, progress func(Progress), incremental bool) (Progress, error) {
	if !x.writer.TryLock() {
		return Progress{}, ErrScanning
	}
	defer x.writer.Unlock()
	roots, err := Validate(opts.Roots, opts.Excludes)
	if err != nil {
		return Progress{}, err
	}
	if len(roots) > 65535 {
		return Progress{}, fmt.Errorf("索引目录过多")
	}
	filter, _ := engine.NewFilter(opts.Excludes)
	previous, err := materialize(ctx, x.base.Load())
	if err != nil {
		return Progress{}, err
	}
	next := make([]*rootData, len(roots))
	updated := time.Time{}
	if previous != nil {
		updated = previous.updated
		for i, p := range roots {
			for _, r := range previous.roots {
				if r.path == p {
					next[i] = r
					break
				}
			}
		}
	}
	state := Progress{}
	count := func() int {
		n := 0
		for _, r := range next {
			if r != nil {
				n += r.count
			}
		}
		return n
	}
	pub := func() {
		live := make([]*rootData, 0, len(next))
		for _, r := range next {
			if r != nil {
				live = append(live, r)
			}
		}
		x.publish(live, updated)
	}
	report := func() {
		state.Count = count()
		if progress != nil {
			copy := state
			copy.Messages = append([]string(nil), state.Messages...)
			progress(copy)
		}
	}
	addError := func(p string, err error) {
		state.Errors++
		if len(state.Messages) < 8 {
			state.Messages = append(state.Messages, p+": "+err.Error())
		}
	}
	blocked := func(p string) bool {
		for _, skip := range opts.Skip {
			if Within(p, skip) {
				return true
			}
		}
		return false
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = min(max(1, runtime.GOMAXPROCS(0)), 4)
	}
	workers = min(workers, 8)
	for ri, p := range roots {
		if err := ctx.Err(); err != nil {
			report()
			return state, err
		}
		if blocked(p) {
			addError(p, fmt.Errorf("目录位于网络挂载或系统排除范围"))
			continue
		}
		st, err := os.Lstat(p)
		if err != nil {
			addError(p, err)
			continue
		}
		if !st.IsDir() || isLink(st) {
			addError(p, fmt.Errorf("索引根目录必须是真实的本地文件夹"))
			continue
		}
		b := newBuilder(p)
		b.data.excludes = slices.Clone(opts.Excludes)
		old := next[ri]
		var cache *directoryCache
		if incremental && old != nil && slices.Equal(old.excludes, opts.Excludes) {
			cache, err = cacheDirectories(ctx, old)
			if err != nil {
				report()
				return state, err
			}
		}
		workCtx, stop := context.WithCancel(ctx)
		jobs := make(chan directoryTask)
		batches := make(chan scanBatch, workers*2)
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-workCtx.Done():
						return
					case task, ok := <-jobs:
						if !ok {
							return
						}
						if cache == nil || !cache.read(workCtx, task, filter, p, blocked, batches) {
							readDirectory(workCtx, task, filter, p, blocked, batches)
						}
					}
				}
			}()
		}
		pending := []directoryTask{{id: 0, path: p}}
		remaining := 1
		var failed []string
		lastReport := time.Now().Add(-time.Second)
		for remaining > 0 && ctx.Err() == nil {
			var dispatch chan directoryTask
			var task directoryTask
			if len(pending) > 0 {
				dispatch = jobs
				task = pending[0]
			}
			select {
			case <-ctx.Done():
			case dispatch <- task:
				pending = pending[1:]
			case batch := <-batches:
				state.Current = batch.task.path
				for _, e := range batch.entries {
					b.add(e, batch.task.id)
					state.Scanned++
					if e.IsDir && !e.Link {
						id := b.directory(e.Path)
						pending = append(pending, directoryTask{id: id, path: e.Path, hidden: e.Hidden})
						remaining++
					}
				}
				failed = append(failed, batch.failed...)
				for _, fp := range batch.failed {
					addError(fp, fmt.Errorf("无法读取文件元数据"))
				}
				if batch.err != nil {
					failed = append(failed, batch.task.path)
					addError(batch.task.path, batch.err)
				}
				if batch.done {
					remaining--
					b.data.dirStamp[batch.task.id] = batch.stamp
					if batch.ready {
						b.data.dirReady[batch.task.id] = 1
					}
					if batch.reused {
						state.ReusedDirs++
					} else {
						state.ReadDirs++
					}
				}
				if time.Since(lastReport) >= 250*time.Millisecond {
					if old == nil {
						next[ri] = b.snapshot()
						pub()
					}
					report()
					lastReport = time.Now()
				}
			}
		}
		stop()
		close(jobs)
		wg.Wait()
		if err := ctx.Err(); err != nil {
			if old == nil {
				next[ri] = b.snapshot()
				pub()
			}
			report()
			return state, err
		}
		// A complete root failure preserves the prior snapshot. For partial
		// failures append only old records in the unreadable ranges.
		rootFailed := false
		for _, fp := range failed {
			if fp == p {
				rootFailed = true
				break
			}
		}
		if old != nil && rootFailed {
			next[ri] = old
		} else {
			if old != nil && len(failed) > 0 {
				if err := preserveFailed(ctx, b, old, failed, filter, blocked); err != nil {
					report()
					return state, err
				}
			}
			next[ri] = b.snapshot()
		}
		pub()
		report()
	}
	if err := ctx.Err(); err != nil {
		return state, err
	}
	updated = time.Now()
	pub()
	state.Current = ""
	report()
	return state, nil
}

func readDirectory(ctx context.Context, t directoryTask, filter *engine.Filter, root string, blocked func(string) bool, out chan<- scanBatch) {
	send := func(b scanBatch) bool {
		select {
		case out <- b:
			return true
		case <-ctx.Done():
			return false
		}
	}
	st, err := os.Lstat(t.path)
	if err == nil && (!st.IsDir() || isLink(st)) {
		err = fmt.Errorf("目录已被替换，符号链接不会递归扫描")
	}
	if err != nil {
		send(scanBatch{task: t, done: true, err: err})
		return
	}
	f, err := os.Open(t.path)
	if err != nil {
		send(scanBatch{task: t, done: true, err: err})
		return
	}
	defer f.Close()
	before, statErr := f.Stat()
	if statErr != nil || !os.SameFile(st, before) {
		send(scanBatch{task: t, done: true, err: fmt.Errorf("目录在扫描时已改变")})
		return
	}
	for ctx.Err() == nil {
		dirs, err := f.ReadDir(256)
		batch := scanBatch{task: t}
		for _, d := range dirs {
			if ctx.Err() != nil {
				return
			}
			p := filepath.Join(t.path, d.Name())
			rel, _ := filepath.Rel(root, p)
			if blocked(p) || filter.Excluded(filepath.ToSlash(rel), d.IsDir()) {
				continue
			}
			st, e := d.Info()
			if e != nil {
				batch.failed = append(batch.failed, p)
				continue
			}
			if !st.Mode().IsRegular() && !st.IsDir() && !isLink(st) {
				continue
			}
			entry := metadataEntry(d.Name(), p, t.path, t.hidden, st)
			batch.entries = append(batch.entries, entry)
		}
		batch.done = err != nil
		if batch.done && statErr == nil && err == io.EOF && len(batch.failed) == 0 {
			after, e := f.Stat()
			if e == nil && before.ModTime() == after.ModTime() {
				batch.stamp, batch.ready = after.ModTime().UnixNano(), true
			}
		}
		if err != nil && err != io.EOF {
			batch.err = err
		}
		if !send(batch) || batch.done {
			return
		}
	}
}
func preserveFailed(ctx context.Context, b *builder, old *rootData, failed []string, filter *engine.Filter, blocked func(string) bool) error {
	dirs := map[string]uint32{}
	for p, id := range b.dirIDs {
		dirs[p] = id
	}
	// Enumerations may return a partial directory before failing; never add
	// duplicate records from the old snapshot for those already observed.
	seen := map[string]bool{}
	for _, c := range b.data.chunks {
		for i := range c.start {
			if i%256 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			e := b.data.entry(c, i)
			for _, fp := range failed {
				if Within(e.Path, fp) {
					seen[e.Path] = true
					break
				}
			}
		}
	}
	if b.current != nil {
		for i := range b.current.start {
			if i%256 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			e := b.data.entry(b.current, i)
			for _, fp := range failed {
				if Within(e.Path, fp) {
					seen[e.Path] = true
					break
				}
			}
		}
	}
	for _, c := range old.chunks {
		for i := range c.start {
			if i%256 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			e := old.entry(c, i)
			keep := false
			for _, fp := range failed {
				if Within(e.Path, fp) {
					keep = true
					break
				}
			}
			if !keep || seen[e.Path] || blocked(e.Path) {
				continue
			}
			rel, _ := filepath.Rel(b.data.path, e.Path)
			rel = filepath.ToSlash(rel)
			excluded := filter.Excluded(rel, e.IsDir)
			for parent := path.Dir(rel); !excluded && parent != "."; parent = path.Dir(parent) {
				excluded = filter.Excluded(parent, true)
			}
			if excluded {
				continue
			}
			parent, ok := dirs[e.Parent]
			if !ok {
				parent = b.directory(e.Parent)
				dirs[e.Parent] = parent
			}
			b.add(e, parent)
			seen[e.Path] = true
		}
	}
	return ctx.Err()
}
