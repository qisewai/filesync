package fileindex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filesync/engine"
	"filesync/fileindex/fitest"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if strings.HasSuffix(rel, "/") {
			os.MkdirAll(p, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		b, _ := os.ReadFile(p)
		out[rel] = string(b)
		return nil
	})
	return out
}

func backup(t *testing.T, f *fitest.Server, src string, mode engine.Mode, excludes ...string) (*engine.Plan, *engine.Result) {
	t.Helper()
	target := NewTarget(Location{Addr: f.Addr(), Root: 1, Path: "备份/电脑"}, "tok")
	p, err := engine.Prepare(context.Background(), engine.Options{Source: src, Mode: mode, Excludes: excludes, Dest: target}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := engine.Execute(context.Background(), p, engine.Hooks{})
	if r.Fatal != nil || len(r.Errors) > 0 {
		t.Fatalf("fatal %v, errors %v", r.Fatal, r.Errors)
	}
	return p, r
}

func TestBackupToServer(t *testing.T) {
	f := fitest.New(t)
	src := t.TempDir()
	write(t, src, map[string]string{"a.txt": "a", "照片/1.jpg": "jpeg", "empty/": ""})
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(filepath.Join(src, "a.txt"), old, old)

	p, r := backup(t, f, src, engine.Incremental)
	if !p.TargetMissing || r.Created != 2 || r.Mkdirs != 2 {
		t.Errorf("missing %v, result %+v", p.TargetMissing, r)
	}
	dst := filepath.Join(f.Root, "备份", "电脑")
	got := tree(t, dst)
	if got["a.txt"] != "a" || got["照片/1.jpg"] != "jpeg" || got["empty/"] != "" || len(got) != 4 {
		t.Fatalf("server has %v", got)
	}
	if info, _ := os.Stat(filepath.Join(dst, "a.txt")); !info.ModTime().Equal(old) {
		t.Errorf("mod time %v", info.ModTime())
	}

	// Nothing changed: nothing to do.
	p, _ = backup(t, f, src, engine.Incremental)
	if p.Summary.Changes() != 0 {
		t.Errorf("second backup plans %+v", p.Summary)
	}
}

func TestMirrorTrashesOnServer(t *testing.T) {
	f := fitest.New(t)
	src := t.TempDir()
	write(t, src, map[string]string{"keep.txt": "k"})
	dst := filepath.Join(f.Root, "备份", "电脑")
	write(t, dst, map[string]string{
		"keep.txt": "k", "gone.txt": "g", "old/a": "a", "old/deep/b": "b",
		"mixed/x": "x", "mixed/cache.tmp": "excluded, stays",
	})
	p, r := backup(t, f, src, engine.Mirror, "*.tmp")
	if r.Deleted != 4 || r.Rmdirs != 2 || r.Kept != 1 {
		t.Errorf("plan %+v, result %+v", p.Summary, r)
	}
	got := tree(t, dst)
	if len(got) != 3 || got["mixed/cache.tmp"] == "" || got["keep.txt"] != "k" {
		t.Errorf("server has %v", got)
	}
	// Whole directories go to the trash in one piece.
	trashed := tree(t, f.Trash)
	if trashed["备份/电脑/old/deep/b"] != "b" || trashed["备份/电脑/gone.txt"] != "g" || trashed["备份/电脑/mixed/x"] != "x" {
		t.Errorf("trash has %v", trashed)
	}
}

func TestManySmallAndLargeFiles(t *testing.T) {
	f := fitest.New(t)
	src := t.TempDir()
	files := map[string]string{"big.bin": strings.Repeat("x", 40<<20)}
	for i := range 2500 {
		files[fmt.Sprintf("d%d/f%04d.txt", i%7, i)] = fmt.Sprint(i)
	}
	write(t, src, files)
	var last engine.Progress
	target := NewTarget(Location{Addr: f.Addr(), Root: 1, Path: "b"}, "tok")
	p, err := engine.Prepare(context.Background(), engine.Options{Source: src, Dest: target}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := engine.Execute(context.Background(), p, engine.Hooks{Progress: func(pr engine.Progress) { last = pr }})
	if len(r.Errors) > 0 || r.Created != 2501 {
		t.Fatalf("result %+v", r)
	}
	if last.Done != last.Total || last.DoneBytes != last.TotalBytes {
		t.Errorf("progress %+v", last)
	}
	// All in one stream.
	if n := f.Streams.Load(); n != 1 {
		t.Errorf("%d streams", n)
	}
	if got := tree(t, filepath.Join(f.Root, "b")); len(got) != 2501+7 || got["d3/f0010.txt"] != "10" {
		t.Errorf("server has %d entries", len(got))
	}
}

func TestExpiredSession(t *testing.T) {
	f := fitest.New(t)
	src := t.TempDir()
	write(t, src, map[string]string{"a": "a"})
	target := NewTarget(Location{Addr: f.Addr(), Root: 1, Path: "b"}, "tok")
	p, err := engine.Prepare(context.Background(), engine.Options{Source: src, Dest: target}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.Expired.Store(true)
	r := engine.Execute(context.Background(), p, engine.Hooks{})
	if r.Fatal == nil {
		t.Fatalf("result %+v", r)
	}

	// A scan says so too, for the app to ask for the password.
	if _, err := engine.Prepare(context.Background(), engine.Options{Source: src, Dest: target}, nil); err == nil || !strings.Contains(err.Error(), ErrAuth.Error()) {
		t.Errorf("scan with an expired session: %v", err)
	}
	c := New(f.Addr(), "")
	if err := c.Login(context.Background(), "wrong"); err == nil {
		t.Error("logged in with a wrong password")
	}
	if err := c.Login(context.Background(), fitest.Password); err != nil || c.Token != "tok" {
		t.Errorf("login: %v, token %q", err, c.Token)
	}
}

func TestSymlinksToServer(t *testing.T) {
	f := fitest.New(t)
	src := t.TempDir()
	write(t, src, map[string]string{"lib/libx.so.1": "elf"})
	if err := os.Symlink("libx.so.1", filepath.Join(src, "lib", "libx.so")); err != nil {
		t.Skip(err)
	}
	_, r := backup(t, f, src, engine.Mirror)
	if r.Created != 2 {
		t.Errorf("result %+v", r)
	}
	if l, err := os.Readlink(filepath.Join(f.Root, "备份", "电脑", "lib", "libx.so")); err != nil || l != "libx.so.1" {
		t.Fatalf("link on the server: %q, %v", l, err)
	}
	p, _ := backup(t, f, src, engine.Mirror)
	if p.Summary.Changes() != 0 {
		t.Errorf("second backup plans %+v", p.Summary)
	}
}

// A stream that breaks reports the files that did not land, and the next
// backup sends them.
func TestBrokenStream(t *testing.T) {
	f := fitest.New(t)
	src := t.TempDir()
	files := map[string]string{}
	for i := range 20 {
		files[fmt.Sprintf("f%02d", i)] = "x"
	}
	write(t, src, files)
	target := NewTarget(Location{Addr: f.Addr(), Root: 1, Path: "b"}, "tok")
	p, err := engine.Prepare(context.Background(), engine.Options{Source: src, Dest: target}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.StopAfter.Store(5)
	r := engine.Execute(context.Background(), p, engine.Hooks{})
	if r.Created != 5 || len(r.Errors) != 15 {
		t.Fatalf("created %d, %d errors", r.Created, len(r.Errors))
	}
	if got := len(tree(t, filepath.Join(f.Root, "b"))); got != 5 {
		t.Errorf("%d files on the server", got)
	}
	p, _ = engine.Prepare(context.Background(), engine.Options{Source: src, Dest: target}, nil)
	if r := engine.Execute(context.Background(), p, engine.Hooks{}); r.Created != 15 || len(r.Errors) != 0 {
		t.Fatalf("retry: created %d, errors %v", r.Created, r.Errors)
	}
	p2, _ := engine.Prepare(context.Background(), engine.Options{Source: src, Dest: target}, nil)
	if p2.Summary.Changes() != 0 {
		t.Errorf("left after the retry: %+v", p2.Summary)
	}
}

// The client sends without waiting for answers: a server answering only
// once the stream is all in gets it all.
func TestSendsWithoutWaitingForAnswers(t *testing.T) {
	f := fitest.New(t)
	f.HoldAnswers.Store(true)
	src := t.TempDir()
	files := map[string]string{"big.bin": strings.Repeat("x", 20<<20)}
	for i := range 3000 {
		files[fmt.Sprintf("d%d/f%04d", i%10, i)] = "x"
	}
	write(t, src, files)
	target := NewTarget(Location{Addr: f.Addr(), Root: 1, Path: "b"}, "tok")
	p, err := engine.Prepare(context.Background(), engine.Options{Source: src, Dest: target}, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *engine.Result)
	go func() { done <- engine.Execute(context.Background(), p, engine.Hooks{}) }()
	select {
	case r := <-done:
		if r.Created != 3001 || len(r.Errors) != 0 {
			t.Fatalf("created %d, errors %v", r.Created, r.Errors[:min(3, len(r.Errors))])
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the client waits for answers before sending more")
	}
}
