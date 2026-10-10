package fileindex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"filesync/engine"
)

// Scheme starts the URLs of directories on a fileindex server:
// fileindex://192.168.0.235:8090/3/备份/笔记本 is the directory 备份/笔记本 of
// the server's root 3.
const Scheme = "fileindex://"

// Location is a directory on a fileindex server.
type Location struct {
	Addr string
	Root int64
	Path string // relative to the root, with slashes; "" for the root
}

// IsURL reports whether s is the URL of a Location.
func IsURL(s string) bool { return strings.HasPrefix(s, Scheme) }

// Parse reads the URL of a Location.
func Parse(s string) (Location, error) {
	rest, ok := strings.CutPrefix(s, Scheme)
	if !ok {
		return Location{}, fmt.Errorf("不是 NAS 地址: %s", s)
	}
	addr, rest, _ := strings.Cut(rest, "/")
	rootID, p, _ := strings.Cut(rest, "/")
	id, err := strconv.ParseInt(rootID, 10, 64)
	if err != nil || addr == "" {
		return Location{}, fmt.Errorf("NAS 地址无效: %s", s)
	}
	return Location{Addr: addr, Root: id, Path: strings.Trim(p, "/")}, nil
}

func (l Location) String() string {
	s := Scheme + l.Addr + "/" + strconv.FormatInt(l.Root, 10)
	if l.Path != "" {
		s += "/" + l.Path
	}
	return s
}

// Tuning of the transfers. A batch carries many small files in one request,
// and several batches go at once, over connections of their own, to keep
// the link busy while each waits for the disk at its end.
const (
	Workers    = 4
	batchFiles = 1000
	batchBytes = 32 << 20
)

// Target is a directory on a fileindex server, as an engine.Target.
type Target struct {
	Client   *Client
	Location Location

	// holds are the directories of the target that hold excluded entries,
	// which a mirror must not move to the trash whole.
	holds map[string]bool
}

// NewTarget returns the target at loc, signed in with token.
func NewTarget(loc Location, token string) *Target {
	return &Target{Client: New(loc.Addr, token), Location: loc}
}

func (t *Target) String() string {
	if t.Location.Path == "" {
		return "NAS " + t.Location.Addr
	}
	return "NAS " + t.Location.Addr + " 上的 " + t.Location.Path
}

func (t *Target) CaseInsensitive() bool { return false }

func (t *Target) Scan(ctx context.Context, f *engine.Filter, onEntry func()) (*engine.Tree, bool, error) {
	tr := &engine.Tree{}
	t.holds = map[string]bool{}
	excluded := map[string]bool{} // excluded directories
	missing, err := t.Client.Manifest(ctx, t.Location.Root, t.Location.Path, func(m ManifestEntry) {
		onEntry()
		parent := path.Dir(m.Path)
		for p := parent; p != "."; p = path.Dir(p) {
			if excluded[p] {
				return
			}
		}
		if f.Excluded(m.Path, m.Dir) {
			if m.Dir {
				excluded[m.Path] = true
			}
			for p := parent; p != "."; p = path.Dir(p) {
				t.holds[p] = true
			}
			return
		}
		e := engine.Entry{Path: m.Path, IsDir: m.Dir, Size: m.Size, ModTime: time.Unix(0, m.MTime)}
		if m.Special {
			e.Special = "符号链接或特殊文件"
		}
		if m.Err != "" {
			tr.Unreadable = append(tr.Unreadable, m.Path)
			tr.Errors = append(tr.Errors, m.Path+": "+m.Err)
		}
		tr.Entries = append(tr.Entries, e)
	})
	return tr, missing, err
}

func (t *Target) Apply(ctx context.Context, p *engine.Plan, run *engine.Run) {
	if !t.trash(ctx, p, run) {
		return
	}
	var dirs, copies []engine.Action
	for _, a := range p.Actions {
		switch a.Kind {
		case engine.ActMkdir:
			dirs = append(dirs, a)
		case engine.ActCreate, engine.ActUpdate:
			copies = append(copies, a)
		}
	}
	batches := makeBatches(dirs, copies)
	jobs := make(chan []engine.Action)
	var wg sync.WaitGroup
	for range Workers {
		wg.Go(func() {
			for b := range jobs {
				if err := t.send(ctx, p.Source(), b, run); errors.Is(err, ErrAuth) {
					run.Fatal(errors.New("NAS 的登录已过期，请重新登录后再备份"))
				}
			}
		})
	}
	for _, b := range batches {
		select {
		case jobs <- b:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
}

// makeBatches groups the directories and copies into requests: large files
// alone and first, so that the end of the backup is many small batches
// spread over the connections rather than one large file on one.
func makeBatches(dirs, copies []engine.Action) [][]engine.Action {
	var out [][]engine.Action
	var small []engine.Action
	var large []engine.Action
	for _, a := range copies {
		if a.Size >= batchBytes {
			large = append(large, a)
		} else {
			small = append(small, a)
		}
	}
	sort.SliceStable(large, func(i, j int) bool { return large[i].Size > large[j].Size })
	for _, a := range large {
		out = append(out, []engine.Action{a})
	}
	cur := append([]engine.Action(nil), dirs...)
	var bytes int64
	for _, a := range small {
		if len(cur) >= batchFiles || bytes+a.Size > batchBytes {
			out = append(out, cur)
			cur, bytes = nil, 0
		}
		cur = append(cur, a)
		bytes += a.Size
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// sent is an item that went into a request.
type sent struct {
	i     int // its index in the batch
	a     engine.Action
	bytes int64
}

// send puts a batch in one request, and reports each item.
func (t *Target) send(ctx context.Context, source string, batch []engine.Action, run *engine.Run) error {
	pr, pw := io.Pipe()
	var items []sent
	reported := map[int]bool{} // indices of batch reported by the writer
	written := make(chan struct{})
	go func() {
		defer close(written)
		w := bufio.NewWriterSize(pw, 256<<10)
		enc := json.NewEncoder(w)
		for i, a := range batch {
			if ctx.Err() != nil {
				pw.CloseWithError(ctx.Err())
				return
			}
			run.Start(a.Path)
			if a.Kind == engine.ActMkdir {
				enc.Encode(Item{Path: a.Path, Dir: true})
				items = append(items, sent{i: i, a: a})
				continue
			}
			f, err := os.Open(filepath.Join(source, filepath.FromSlash(a.Path)))
			var info os.FileInfo
			if err == nil {
				info, err = f.Stat()
			}
			if err != nil {
				if f != nil {
					f.Close()
				}
				run.Copied(a, 0, false, err)
				reported[i] = true
				continue
			}
			if err := enc.Encode(Item{Path: a.Path, Size: info.Size(), MTime: info.ModTime().UnixNano()}); err != nil {
				f.Close()
				return // the request ended: what was sent is reported below
			}
			cw := &counter{w: w, run: run}
			_, err = io.CopyN(cw, f, info.Size())
			f.Close()
			items = append(items, sent{i: i, a: a, bytes: cw.n})
			if err != nil {
				// The file shrank, or could not be read: its frame cannot be
				// completed, and the request ends here.
				pw.CloseWithError(fmt.Errorf("读取 %s 出错: %w", a.Path, err))
				return
			}
		}
		if err := w.Flush(); err != nil {
			return
		}
		pw.Close()
	}()

	res, err := t.Client.Put(ctx, t.Location.Root, t.Location.Path, pr)
	pr.CloseWithError(errors.New("请求已结束")) // unblock the writer
	<-written
	if ctx.Err() != nil {
		return ctx.Err() // canceled: nothing more is counted
	}
	failed := map[string]string{}
	broken := ""
	if err != nil {
		broken = err.Error()
	} else {
		for _, f := range res.Failed {
			failed[f.Path] = f.Error
		}
		broken = res.Error
	}
	processed := 0
	if res != nil {
		processed = res.Written + len(res.Failed)
	}
	for k, it := range items {
		var itemErr error
		switch {
		case failed[it.a.Path] != "":
			itemErr = errors.New(failed[it.a.Path])
		case broken != "" && k >= processed:
			itemErr = errors.New(broken)
		}
		report(run, it, itemErr)
		reported[it.i] = true
	}
	// Items never sent, after the request broke.
	for i, a := range batch {
		if !reported[i] {
			msg := broken
			if msg == "" {
				msg = "没有发送"
			}
			report(run, sent{i: i, a: a}, errors.New(msg))
		}
	}
	return err
}

func report(run *engine.Run, it sent, err error) {
	if it.a.Kind == engine.ActMkdir {
		run.Done(it.a, err)
	} else {
		run.Copied(it.a, it.bytes, true, err)
	}
}

// counter counts the bytes of a file as they go.
type counter struct {
	w   io.Writer
	run *engine.Run
	n   int64
}

func (c *counter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	c.run.Bytes(int64(n))
	return n, err
}

// trash moves what a mirror deletes to the server's trash: a directory
// with everything in it in one go, unless it holds excluded files, which
// stay. It reports false when the backup cannot go on.
func (t *Target) trash(ctx context.Context, p *engine.Plan, run *engine.Run) bool {
	var dels []engine.Action
	whole := map[string]bool{} // directories trashed whole
	for _, a := range p.Actions {
		switch a.Kind {
		case engine.ActDelete, engine.ActCleanup:
			dels = append(dels, a)
		case engine.ActRmdir:
			if t.holds[a.Path] {
				continue
			}
			dels = append(dels, a)
			whole[a.Path] = true
		}
	}
	// Each deletion goes with the outermost directory around it that goes
	// whole, or alone.
	top := func(rel string) string {
		parts := strings.Split(rel, "/")
		for i := 1; i < len(parts); i++ {
			if d := strings.Join(parts[:i], "/"); whole[d] {
				return d
			}
		}
		return rel
	}
	groups := map[string][]engine.Action{}
	var order []string
	for _, a := range dels {
		k := top(a.Path)
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], a)
	}
	for i := 0; i < len(order); i += 200 {
		if ctx.Err() != nil {
			return false
		}
		chunk := order[i:min(i+200, len(order))]
		paths := make([]string, len(chunk))
		for j, rel := range chunk {
			paths[j] = path.Join(t.Location.Path, rel)
			run.Start(rel)
		}
		err := t.Client.Trash(ctx, t.Location.Root, paths)
		if errors.Is(err, ErrAuth) {
			run.Fatal(errors.New("NAS 的登录已过期，请重新登录后再备份"))
			return false
		}
		if err != nil {
			err = fmt.Errorf("移到 NAS 回收站失败: %w", err)
		}
		for _, rel := range chunk {
			for _, a := range groups[rel] {
				run.Done(a, err)
			}
		}
	}
	// Directories left because they hold excluded files.
	for _, a := range p.Actions {
		if a.Kind == engine.ActRmdir && t.holds[a.Path] {
			run.Kept(a)
		}
	}
	return ctx.Err() == nil
}

var _ engine.Target = (*Target)(nil)
