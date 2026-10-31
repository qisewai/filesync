package fileindex

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filemanager/engine"
)

// Location is a directory on a fileindex server.
type Location struct {
	Addr string
	Root int64
	Path string // relative to the root, with slashes; "" for the root
}

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
		e := engine.Entry{Path: m.Path, IsDir: m.Dir, Size: m.Size, ModTime: time.Unix(0, m.MTime), Perm: os.FileMode(m.Mode), Link: m.Link}
		if m.Special {
			e.Special = "设备、管道或套接字等特殊文件"
		}
		if m.Err != "" {
			tr.Unreadable = append(tr.Unreadable, m.Path)
			tr.Errors = append(tr.Errors, m.Path+": "+m.Err)
		}
		tr.Entries = append(tr.Entries, e)
	})
	return tr, missing, err
}

// Apply moves what a mirror deletes to the trash, then sends the
// directories, files and links to make as one tar stream, in one request:
// one connection writing in order keeps a link busy, and the disks of the
// NAS writing one file after another. The server answers each entry as it
// lands, which reports it done.
func (t *Target) Apply(ctx context.Context, p *engine.Plan, run *engine.Run) {
	if !t.trash(ctx, p, run) {
		return
	}
	var items []engine.Action
	for _, a := range p.Actions {
		switch a.Kind {
		case engine.ActMkdir, engine.ActCreate, engine.ActUpdate:
			items = append(items, a)
		}
	}
	source := p.Source()
	t.Client.sendTree(ctx, t.Location, items, func(a engine.Action) string {
		return filepath.Join(source, filepath.FromSlash(a.Path))
	}, run)
}

// sendTree sends the directories, files and links of items into the
// directory loc as one tar stream, in one request: one connection writing
// in order keeps a link busy, and the disks of the NAS writing one file
// after another. local gives the file of this computer an item copies.
// The server answers each entry as it lands, which reports it to run.
func (c *Client) sendTree(ctx context.Context, loc Location, items []engine.Action, local func(engine.Action) string, run *engine.Run) {
	if len(items) == 0 {
		return
	}
	s := &stream{run: run, pending: map[string]*pending{}, local: local}
	stopped := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	written := make(chan struct{})
	go func() {
		defer close(written)
		pw.CloseWithError(s.write(ctx, items, pw))
	}()
	answers, err := c.Tar(ctx, loc.Root, loc.Path, pr)
	if err == nil {
		err = s.read(answers)
		answers.Close()
	}
	cancel()
	pr.CloseWithError(errors.New("传输已结束"))
	<-written
	if stopped.Err() != nil {
		return // stopped: what did not land is not counted
	}
	if errors.Is(err, ErrAuth) {
		run.Fatal(errors.New("NAS 的登录已过期，请重新登录后再备份"))
	}
	// What was sent but not acknowledged, or never sent, did not land.
	msg := "传输中断，没有写入 NAS"
	if err != nil {
		msg += ": " + err.Error()
	}
	for _, a := range items[s.next:] {
		s.add(a)
	}
	for _, pd := range s.pending {
		s.report(pd, errors.New(msg))
	}
}

// stream is a backup's tar stream: the entries sent and not yet answered.
type stream struct {
	run     *engine.Run
	local   func(engine.Action) string
	mu      sync.Mutex
	pending map[string]*pending // by path
	next    int                 // the index of the first item neither sent nor reported
}

type pending struct {
	a      engine.Action
	sent   int64
	broken error // the file could not be read whole: it does not count
}

func (s *stream) add(a engine.Action) *pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	pd := &pending{a: a}
	s.pending[a.Path] = pd
	return pd
}

// report reports an entry, once.
func (s *stream) report(pd *pending, err error) {
	s.mu.Lock()
	if s.pending[pd.a.Path] != pd {
		s.mu.Unlock()
		return
	}
	delete(s.pending, pd.a.Path)
	if pd.broken != nil {
		err = pd.broken
	}
	sent := pd.sent
	s.mu.Unlock()
	if pd.a.Kind == engine.ActMkdir {
		s.run.Done(pd.a, err)
	} else {
		s.run.Copied(pd.a, sent, true, err)
	}
}

// write writes the items as a tar stream to w.
func (s *stream) write(ctx context.Context, items []engine.Action, w io.Writer) error {
	bw := bufio.NewWriterSize(w, 1<<20)
	tw := tar.NewWriter(bw)
	for i, a := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.run.Start(a.Path)
		var pd *pending
		e := entryWriter{tw: tw, run: s.run, name: a.Path, onSent: func(n int64) {
			s.mu.Lock()
			pd.sent = n
			s.mu.Unlock()
		}}
		err := e.write(s.local(a), a, func() { pd = s.add(a) })
		s.next = i + 1
		var bad *badFile
		switch {
		case errors.As(err, &bad) && pd == nil:
			s.run.Copied(a, 0, false, bad.err) // not sent
		case errors.As(err, &bad):
			// Its entry was filled out: the server drops it.
			s.mu.Lock()
			pd.broken = bad.err
			s.mu.Unlock()
			if err := tw.WriteHeader(&tar.Header{Name: DiscardName, Typeflag: tar.TypeReg, Format: tar.FormatPAX,
				PAXRecords: map[string]string{DiscardKey: a.Path}}); err != nil {
				return err
			}
		case err != nil:
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return bw.Flush()
}

// badFile is a file that could not be sent: not opened, or not read whole,
// as when it shrank, its entry then filled out with zeros.
type badFile struct{ err error }

func (b *badFile) Error() string { return b.err.Error() }

// entryWriter writes the entries of a tar stream.
type entryWriter struct {
	tw   *tar.Writer
	run  *engine.Run
	name string // of the entry
	// onSent receives the bytes of a file written.
	onSent func(int64)
}

// write writes the entry of an action, the file of this computer it
// copies being file, calling begin before its header. It returns a
// *badFile for a file that could not be sent, and other errors for the
// stream.
func (e *entryWriter) write(file string, a engine.Action, begin func()) error {
	hdr := &tar.Header{Name: e.name, ModTime: a.ModTime, Mode: int64(a.Mode.Perm()), Format: tar.FormatPAX}
	var f *os.File
	switch {
	case a.Kind == engine.ActMkdir:
		hdr.Typeflag, hdr.Name = tar.TypeDir, e.name+"/"
	case a.Link != "":
		hdr.Typeflag, hdr.Linkname = tar.TypeSymlink, a.Link
	default:
		var err error
		f, err = os.Open(file)
		var info os.FileInfo
		if err == nil {
			info, err = f.Stat()
		}
		if err != nil {
			if f != nil {
				f.Close()
			}
			return &badFile{err}
		}
		defer f.Close()
		hdr.Typeflag, hdr.Size, hdr.ModTime = tar.TypeReg, info.Size(), info.ModTime()
	}
	begin()
	if err := e.tw.WriteHeader(hdr); err != nil {
		return err
	}
	if f == nil {
		return nil
	}
	cw := &counter{w: e.tw, run: e.run}
	_, err := io.CopyN(cw, f, hdr.Size)
	e.onSent(cw.n)
	if err == nil {
		return nil
	}
	if cw.err != nil {
		return cw.err // the stream broke
	}
	if _, err := io.CopyN(e.tw, zeros{}, hdr.Size-cw.n); err != nil {
		return err
	}
	return &badFile{fmt.Errorf("读取出错，文件在备份时被修改了吗？%w", err)}
}

// read reports the entries the server answers.
func (s *stream) read(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var a Answer
		if err := json.Unmarshal(sc.Bytes(), &a); err != nil {
			return fmt.Errorf("NAS 的回答无法解析: %w", err)
		}
		if a.End {
			if a.Fatal != "" {
				return errors.New("NAS: " + a.Fatal)
			}
			return nil
		}
		s.mu.Lock()
		pd := s.pending[a.Path]
		s.mu.Unlock()
		if pd == nil {
			continue // a file discarded, already reported
		}
		var err error
		if a.Error != "" {
			err = errors.New(a.Error)
		}
		s.report(pd, err)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("NAS 的回答意外结束")
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// counter counts the bytes of a file as they go, and keeps the error of
// the stream, to tell it from one reading the file.
type counter struct {
	w   io.Writer
	run *engine.Run
	n   int64
	err error
}

func (c *counter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	c.run.Bytes(int64(n))
	if err != nil {
		c.err = err
	}
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
