package fileindex

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filesync/engine"
	"filesync/fileindex/fitest"
)

func TestBrowse(t *testing.T) {
	f := fitest.New(t)
	write(t, f.Root, map[string]string{"照片/a.jpg": "jpeg", "照片/b.txt": "b", "文档/报告.docx": "doc"})
	c := New(f.Addr(), "tok")
	ctx := context.Background()

	list, err := c.List(ctx, 1, "照片", false)
	if err != nil || len(list) != 2 || list[0].Path != "照片/a.jpg" || list[0].Size != 4 || list[0].Ext != "jpg" {
		t.Fatalf("list %+v, %v", list, err)
	}
	found, total, err := c.Search(ctx, "报告", SearchOptions{})
	if err != nil || total != 1 || found[0].Path != "文档/报告.docx" {
		t.Fatalf("search %+v %d %v", found, total, err)
	}
	// A page of the matches, in the order asked, and how many there are.
	found, total, err = c.Search(ctx, ".", SearchOptions{Root: 1, Sort: "size", Desc: true, Page: 2, Size: 1})
	if err != nil || total != 3 || len(found) != 1 || found[0].Path != "文档/报告.docx" {
		t.Fatalf("page 2 by size %+v %d %v", found, total, err)
	}
	info, err := c.Info(ctx)
	if err != nil || len(info.ThumbExts) == 0 {
		t.Fatalf("info %+v %v", info, err)
	}
	if b, err := c.Thumb(ctx, 1, "照片/a.jpg"); err != nil || len(b) == 0 {
		t.Fatalf("thumb %d bytes, %v", len(b), err)
	}
	if err := c.Rename(ctx, 1, "照片/b.txt", "c.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.Root, "照片", "c.txt")); err != nil {
		t.Fatal("not renamed")
	}

	// A copy is a task the events tell the end of.
	events := make(chan Event, 8)
	ectx, stop := context.WithCancel(ctx)
	defer stop()
	go c.Events(ectx, func(e Event) { events <- e })
	time.Sleep(50 * time.Millisecond)
	task, err := c.Transfer(ctx, false, 1, []string{"照片"}, 1, "文档")
	if err != nil || task == nil {
		t.Fatalf("transfer %v %v", task, err)
	}
	deadline := time.After(5 * time.Second)
	for done := false; !done; {
		select {
		case e := <-events:
			done = e.Type == "task" && e.Task != nil && e.Task.ID == task.ID && e.Task.Finished()
		case <-deadline:
			t.Fatal("no event for the task")
		}
	}
	if _, err := os.Stat(filepath.Join(f.Root, "文档", "照片", "a.jpg")); err != nil {
		t.Error("not copied")
	}
}

func TestDownloadDetectsTruncatedArchiveAndKeepsOriginal(t *testing.T) {
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	tw.WriteHeader(&tar.Header{Name: "a.txt", Typeflag: tar.TypeReg, Size: 1024, Mode: 0644})
	tw.Write(bytes.Repeat([]byte("a"), 1024))
	tw.Close()
	for _, cut := range []int{600, 1536} {
		t.Run(fmt.Sprint(cut), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(archive.Bytes()[:cut]) }))
			defer srv.Close()
			c := New(strings.TrimPrefix(srv.URL, "http://"), "")
			dest := t.TempDir()
			os.WriteFile(filepath.Join(dest, "a.txt"), []byte("original"), 0644)
			_, err := c.Download(context.Background(), 1, []string{"a.txt"}, dest, nil)
			if err == nil {
				t.Fatal("truncated archive reported success")
			}
			if cut == 600 {
				if b, _ := os.ReadFile(filepath.Join(dest, "a.txt")); string(b) != "original" {
					t.Fatalf("replaced original with %q", b)
				}
			}
			if files, _ := filepath.Glob(filepath.Join(dest, ".filesync-*")); len(files) > 0 {
				t.Fatalf("temporary files left: %v", files)
			}
		})
	}
}

func TestDownloadRequiresAnnouncedCompletion(t *testing.T) {
	var archive bytes.Buffer
	tar.NewWriter(&archive).Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", "X-Export-Complete")
		w.Write(archive.Bytes())
	}))
	defer srv.Close()
	c := New(strings.TrimPrefix(srv.URL, "http://"), "")
	if _, err := c.Download(context.Background(), 1, []string{"folder"}, t.TempDir(), nil); err == nil {
		t.Fatal("accepted an export without completion trailer")
	}
}

func TestUploadReportsMissingFilesAndDuplicateNames(t *testing.T) {
	f := fitest.New(t)
	c := New(f.Addr(), "tok")
	loc := Location{Addr: f.Addr(), Root: 1}
	r := c.Upload(context.Background(), loc, []string{filepath.Join(t.TempDir(), "missing")}, engine.Hooks{})
	if r.Fatal == nil && len(r.Errors) == 0 {
		t.Fatal("missing source reported success")
	}
	left, right := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(left, "same.txt"), []byte("left"), 0644)
	os.WriteFile(filepath.Join(right, "same.txt"), []byte("right"), 0644)
	r = c.Upload(context.Background(), loc, []string{filepath.Join(left, "same.txt"), filepath.Join(right, "same.txt")}, engine.Hooks{})
	if r.Fatal == nil {
		t.Fatal("duplicate names accepted")
	}
	if _, err := os.Stat(filepath.Join(f.Root, "same.txt")); !os.IsNotExist(err) {
		t.Fatal("sent duplicate files before validation")
	}
}

func TestExtractionRejectsExistingDirectorySymlink(t *testing.T) {
	dest, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dest, "folder")); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	tw.WriteHeader(&tar.Header{Name: "folder/", Typeflag: tar.TypeDir, ModTime: time.Now()})
	tw.Close()
	x := &extractor{dest: dest}
	if err := x.extract(tar.NewReader(&archive), func(int64, string) {}); err != nil {
		t.Fatal(err)
	}
	if len(x.errs) != 1 {
		t.Fatalf("errors: %v", x.errs)
	}
	// An interrupted single-file download also preserves the old destination.
	os.WriteFile(filepath.Join(dest, "a.txt"), []byte("old"), 0644)
	if err := writeFile(filepath.Join(dest, "a.txt"), &failingReader{}, 0644, time.Time{}, nil); err == nil {
		t.Fatal("expected interrupted write")
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "a.txt")); string(b) != "old" {
		t.Fatalf("original %q", b)
	}
}

type failingReader struct{}

func (*failingReader) Read(p []byte) (int, error) { return copy(p, "partial"), io.ErrUnexpectedEOF }

// What goes up comes down the same.
func TestUploadAndDownload(t *testing.T) {
	f := fitest.New(t)
	src := t.TempDir()
	write(t, src, map[string]string{"项目/main.go": "package main", "项目/lib/x.so.1": "elf", "单个.txt": "one"})
	os.Symlink("x.so.1", filepath.Join(src, "项目", "lib", "x.so"))
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(filepath.Join(src, "单个.txt"), old, old)
	c := New(f.Addr(), "tok")
	ctx := context.Background()

	var last engine.Progress
	r := c.Upload(ctx, Location{Addr: f.Addr(), Root: 1, Path: "上传"},
		[]string{filepath.Join(src, "项目"), filepath.Join(src, "单个.txt")}, engine.Hooks{Progress: func(p engine.Progress) { last = p }})
	if r.Fatal != nil || len(r.Errors) > 0 || r.Created != 4 {
		t.Fatalf("upload %+v", r)
	}
	if last.Done != last.Total {
		t.Errorf("progress %+v", last)
	}
	if got := tree(t, filepath.Join(f.Root, "上传")); got["项目/main.go"] != "package main" || got["单个.txt"] != "one" {
		t.Fatalf("server has %v", got)
	}

	dest := t.TempDir()
	errs, err := c.Download(ctx, 1, []string{"上传/项目", "上传/单个.txt"}, dest, func(Progress) {})
	if err != nil || len(errs) > 0 {
		t.Fatalf("download: %v %v", errs, err)
	}
	if got := tree(t, dest); got["项目/main.go"] != "package main" || got["项目/lib/x.so.1"] != "elf" || got["单个.txt"] != "one" {
		t.Fatalf("downloaded %v", got)
	}
	if l, err := os.Readlink(filepath.Join(dest, "项目", "lib", "x.so")); err != nil || l != "x.so.1" {
		t.Errorf("link %q %v", l, err)
	}
	if fi, _ := os.Stat(filepath.Join(dest, "单个.txt")); !fi.ModTime().Equal(old) {
		t.Errorf("mtime %v", fi.ModTime())
	}

	// One file, as itself.
	one := filepath.Join(t.TempDir(), "x.txt")
	if err := c.DownloadFile(ctx, 1, "上传/单个.txt", one, func(Progress) {}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(one); string(b) != "one" {
		t.Errorf("file %q", b)
	}
}

// An archive cannot write out of the directory it is unpacked into.
func TestExtractStaysInside(t *testing.T) {
	dest, outside := t.TempDir(), t.TempDir()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, h := range []*tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg, Size: 1},
		{Name: "/abs", Typeflag: tar.TypeReg, Size: 1},
		{Name: "out", Typeflag: tar.TypeSymlink, Linkname: outside},
		{Name: "out/through", Typeflag: tar.TypeReg, Size: 1},
		{Name: "ok/fine", Typeflag: tar.TypeReg, Size: 1},
	} {
		tw.WriteHeader(h)
		if h.Size > 0 {
			tw.Write([]byte("x"))
		}
	}
	tw.Close()
	x := &extractor{dest: dest}
	if err := x.extract(tar.NewReader(&b), func(int64, string) {}); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("wrote through a link: %v", ents)
	}
	got := tree(t, dest)
	// Invalid names are rejected; a file through a link is rejected too.
	if got["ok/fine"] != "x" || got["escape"] != "" || got["abs"] != "" || len(x.errs) != 3 || !strings.Contains(x.errs[2].Error(), "out") {
		t.Errorf("tree %v, errors %v", got, x.errs)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "escape")); err == nil {
		t.Error("escaped")
	}
}
