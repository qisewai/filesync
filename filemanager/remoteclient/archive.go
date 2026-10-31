package fileindex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sync"

	"archive/tar"
	"bufio"
	"runtime"

	"github.com/klauspost/compress/zstd"
	"github.com/klauspost/pgzip"

	"filemanager/engine"
)

// Archive is a target that packs the whole source into one tar file, a new
// one each time, in a directory of a fileindex server.
type Archive struct {
	Client *Client
	// Location is the directory the archive goes to.
	Location Location
	// Name is the archive's file name, as xiaomai-20261010-160500.tar.
	Name string
	// Prefix starts the paths in the archive, as xiaomai, when the source
	// directory itself is archived.
	Prefix string
	// Compress is how the archive is compressed: "", "gzip" or "zstd".
	Compress string

	saved string // the path the archive got
}

// NewArchive returns the archive name in the directory loc, compressed
// with compress: "", "gzip" or "zstd".
func NewArchive(loc Location, token, name, prefix, compress string) *Archive {
	return &Archive{Client: New(loc.Addr, token), Location: loc, Name: name, Prefix: prefix, Compress: compress}
}

// compressor wraps w in the archive's compression, which uses every core:
// one would not keep up with a network.
func (t *Archive) compressor(w io.Writer) (io.WriteCloser, error) {
	switch t.Compress {
	case "gzip":
		z, err := pgzip.NewWriterLevel(w, pgzip.BestSpeed)
		if err != nil {
			return nil, err
		}
		if err := z.SetConcurrency(1<<20, runtime.GOMAXPROCS(0)); err != nil {
			return nil, err
		}
		return z, nil
	case "zstd":
		return zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedFastest))
	case "":
		return nopCloser{w}, nil
	}
	return nil, fmt.Errorf("不支持的压缩方式 %q", t.Compress)
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// Saved is the path the archive got in its root, once Apply saved it.
func (t *Archive) Saved() string { return t.saved }

func (t *Archive) String() string {
	return "NAS " + t.Location.Addr + " 上的 " + path.Join(t.Location.Path, t.Name)
}

func (t *Archive) CaseInsensitive() bool { return false }

// Scan lists nothing: an archive holds the whole source, whatever is there.
func (t *Archive) Scan(ctx context.Context, f *engine.Filter, onEntry func()) (*engine.Tree, bool, error) {
	ok, err := t.Client.Authed(ctx)
	if err == nil && !ok {
		err = ErrAuth
	}
	if err != nil {
		return nil, false, err
	}
	return &engine.Tree{}, false, nil
}

// Apply writes the directories, files and links of the plan as one tar
// stream into the archive file, as it goes. They are all done once the
// server has the file whole.
func (t *Archive) Apply(ctx context.Context, p *engine.Plan, run *engine.Run) {
	var items []engine.Action
	for _, a := range p.Actions {
		switch a.Kind {
		case engine.ActMkdir, engine.ActCreate, engine.ActUpdate:
			items = append(items, a)
		}
	}
	type packed struct {
		a    engine.Action
		sent int64
		bad  error
	}
	var (
		mu   sync.Mutex
		done []packed
	)
	pr, pw := io.Pipe()
	written := make(chan error, 1)
	go func() {
		err := func() error {
			bw := bufio.NewWriterSize(pw, 1<<20)
			zw, err := t.compressor(bw)
			if err != nil {
				return err
			}
			tw := tar.NewWriter(zw)
			if t.Prefix != "" {
				hdr := &tar.Header{Name: t.Prefix + "/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: p.Created, Format: tar.FormatPAX}
				if err := tw.WriteHeader(hdr); err != nil {
					return err
				}
			}
			for _, a := range items {
				if err := ctx.Err(); err != nil {
					return err
				}
				run.Start(a.Path)
				var sent int64
				e := entryWriter{tw: tw, run: run, name: path.Join(t.Prefix, a.Path), onSent: func(n int64) { sent = n }}
				err := e.write(filepath.Join(p.Source(), filepath.FromSlash(a.Path)), a, func() {})
				var bad *badFile
				switch {
				case errors.As(err, &bad):
					// Not in the archive, or in it filled out with zeros.
					mu.Lock()
					done = append(done, packed{a, sent, bad.err})
					mu.Unlock()
				case err != nil:
					return err
				default:
					mu.Lock()
					done = append(done, packed{a, sent, nil})
					mu.Unlock()
				}
			}
			if err := tw.Close(); err != nil {
				return err
			}
			if err := zw.Close(); err != nil {
				return err
			}
			return bw.Flush()
		}()
		pw.CloseWithError(err)
		written <- err
	}()
	saved, err := t.Client.SaveFile(ctx, t.Location.Root, t.Location.Path, t.Name, pr)
	pr.CloseWithError(errors.New("传输已结束"))
	if werr := <-written; err == nil && werr != nil {
		err = werr
	}
	if ctx.Err() != nil {
		return // stopped: the server drops what it got
	}
	if errors.Is(err, ErrAuth) {
		run.Fatal(errors.New("NAS 的登录已过期，请重新登录后再备份"))
		return
	}
	if err != nil {
		run.Fatal(fmt.Errorf("没有保存归档 %s: %w", t.Name, err))
		return
	}
	t.saved = saved
	for _, d := range done {
		if d.a.Kind == engine.ActMkdir {
			run.Done(d.a, d.bad)
		} else {
			run.Copied(d.a, d.sent, true, d.bad)
		}
	}
}

var _ engine.Target = (*Archive)(nil)
