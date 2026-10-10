package fileindex

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"filesync/engine"
	"filesync/fileindex/fitest"
)

// entries reads a tar: name → content, "->target" for a link, "/" for a
// directory.
func entries(t *testing.T, file string) (map[string]string, map[string]int64) {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var r io.Reader = f
	switch {
	case strings.HasSuffix(file, ".gz"):
		z, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		r = z
	case strings.HasSuffix(file, ".zst"):
		z, err := zstd.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		defer z.Close()
		r = z
	}
	out, modes := map[string]string{}, map[string]int64{}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out, modes
		}
		if err != nil {
			t.Fatal(err)
		}
		modes[h.Name] = h.Mode
		switch h.Typeflag {
		case tar.TypeDir:
			out[h.Name] = "/"
		case tar.TypeSymlink:
			out[h.Name] = "->" + h.Linkname
		default:
			b, _ := io.ReadAll(tr)
			out[h.Name] = string(b)
		}
	}
}

func archive(t *testing.T, f *fitest.Server, src, name, prefix, compress string) (*Archive, *engine.Result) {
	t.Helper()
	target := NewArchive(Location{Addr: f.Addr(), Root: 1, Path: "归档"}, "tok", name, prefix, compress)
	p, err := engine.Prepare(context.Background(), engine.Options{Source: src, Mode: engine.Archive, Dest: target}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return target, engine.Execute(context.Background(), p, engine.Hooks{})
}

func TestArchive(t *testing.T) {
	f := fitest.New(t)
	src := t.TempDir()
	write(t, src, map[string]string{"a.txt": "a", "sub/b.txt": "b", "empty/": "", "skip.tmp": "x"})
	os.Chmod(filepath.Join(src, "a.txt"), 0o755)
	os.Symlink("a.txt", filepath.Join(src, "link"))
	// Something already there is no matter: an archive holds all.
	write(t, filepath.Join(f.Root, "归档"), map[string]string{"a.txt": "old"})

	target := NewArchive(Location{Addr: f.Addr(), Root: 1, Path: "归档"}, "tok", "xiaomai-1.tar", "xiaomai", "")
	p, err := engine.Prepare(context.Background(), engine.Options{Source: src, Mode: engine.Archive, Excludes: []string{"*.tmp"}, Dest: target}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := engine.Execute(context.Background(), p, engine.Hooks{})
	if r.Fatal != nil || len(r.Errors) > 0 || r.Created != 3 || r.Mkdirs != 2 {
		t.Fatalf("result %+v", r)
	}
	if target.Saved() != "归档/xiaomai-1.tar" {
		t.Errorf("saved %q", target.Saved())
	}
	got, modes := entries(t, filepath.Join(f.Root, "归档", "xiaomai-1.tar"))
	want := map[string]string{"xiaomai/": "/", "xiaomai/a.txt": "a", "xiaomai/sub/": "/", "xiaomai/sub/b.txt": "b",
		"xiaomai/empty/": "/", "xiaomai/link": "->a.txt"}
	if len(got) != len(want) {
		t.Fatalf("archive has %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
	if modes["xiaomai/a.txt"]&0o111 == 0 {
		t.Errorf("a.txt mode %o", modes["xiaomai/a.txt"])
	}
	if b, _ := os.ReadFile(filepath.Join(f.Root, "归档", "a.txt")); string(b) != "old" {
		t.Error("an archive changed the files of the target")
	}

	// Without the prefix, the contents are at the top; each backup makes a
	// new archive.
	if _, r := archive(t, f, src, "xiaomai-2.tar", "", ""); r.Fatal != nil {
		t.Fatal(r.Fatal)
	}
	got, _ = entries(t, filepath.Join(f.Root, "归档", "xiaomai-2.tar"))
	if got["a.txt"] != "a" || got["sub/b.txt"] != "b" {
		t.Errorf("archive has %v", got)
	}
	if _, err := os.Stat(filepath.Join(f.Root, "归档", "xiaomai-1.tar")); err != nil {
		t.Error("the first archive went")
	}
}

func TestArchiveNeedsLogin(t *testing.T) {
	f := fitest.New(t)
	f.Expired.Store(true)
	src := t.TempDir()
	write(t, src, map[string]string{"a": "a"})
	target := NewArchive(Location{Addr: f.Addr(), Root: 1}, "tok", "x.tar", "", "")
	_, err := engine.Prepare(context.Background(), engine.Options{Source: src, Mode: engine.Archive, Dest: target}, nil)
	if err == nil || !strings.Contains(err.Error(), ErrAuth.Error()) {
		t.Errorf("scan with an expired session: %v", err)
	}
}

// Compressed archives hold the same, in less room for what compresses.
func TestCompressedArchives(t *testing.T) {
	f := fitest.New(t)
	src := t.TempDir()
	text := strings.Repeat("一行源代码 source code line\n", 20000)
	write(t, src, map[string]string{"main.go": text, "sub/b.txt": "b"})
	os.Symlink("main.go", filepath.Join(src, "link"))
	for _, c := range []struct{ compress, name string }{{"gzip", "x.tar.gz"}, {"zstd", "x.tar.zst"}} {
		_, r := archive(t, f, src, c.name, "x", c.compress)
		if r.Fatal != nil || len(r.Errors) > 0 || r.Created != 3 {
			t.Fatalf("%s: %+v", c.compress, r)
		}
		file := filepath.Join(f.Root, "归档", c.name)
		got, _ := entries(t, file)
		if got["x/main.go"] != text || got["x/sub/b.txt"] != "b" || got["x/link"] != "->main.go" {
			t.Errorf("%s: archive has %d entries, main.go %d bytes", c.compress, len(got), len(got["x/main.go"]))
		}
		if info, _ := os.Stat(file); info.Size() > int64(len(text))/4 {
			t.Errorf("%s: %d bytes for %d of text", c.compress, info.Size(), len(text))
		}
	}
}
