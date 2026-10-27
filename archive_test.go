package main

import (
	"archive/zip"
	"context"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filesync/config"
	"filesync/fileindex"
	"filesync/fileindex/fitest"
	"github.com/egoist/mygo/ui"
)

func archiveFixture(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "备份.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, body := range map[string]string{"照片/说明.txt": "压缩包内的文本", "readme.txt": "root text", "empty/": ""} {
		out, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		out.Write([]byte(body))
	}
	w.Close()
	f.Close()
	return p
}
func TestArchiveBrowserNASHistoryPreviewAndReadOnly(t *testing.T) {
	f := fitest.New(t)
	archiveFixture(t, f.Root)
	h := browserHarness(t, f)
	server := h.a.store.Servers[0].ID
	h.a.openEntry(fileindex.Entry{Root: 1, Path: "备份.zip", Name: "备份.zip"})
	h.settle()
	if !h.a.onArchive() || len(h.a.arc.items) != 3 {
		t.Fatalf("archive %+v", h.a.arc)
	}
	h.want("压缩包内 3 项")
	h.want("压缩包 · 只读")
	for _, label := range []string{"新建文件夹", "上传", "重命名", "删除", "移动到…"} {
		if h.tt.HasText(label) {
			t.Fatalf("write command present %s", label)
		}
	}
	rootLocation := h.a.location()
	if !strings.Contains(rootLocation, "%21") {
		t.Fatal(rootLocation)
	}
	h.a.archiveOpen(h.a.arc.items[1])
	h.settle() // directories sorted first: 照片
	if h.a.arc.path != "照片" {
		h.a.archiveBrowse("照片")
		h.settle()
	}
	inner := h.a.location()
	h.a.archiveOpen(h.a.arc.items[0])
	h.settle()
	if h.a.preview == nil || h.a.preview.text != "压缩包内的文本" {
		t.Fatalf("preview %+v", h.a.preview)
	}
	h.a.closePreview()
	h.tt.Frame()
	h.tt.Key(ui.Alt, ui.KeyUp)
	h.settle()
	if h.a.arc.path != "" {
		t.Fatal(h.a.arc.path)
	}
	h.tt.Key(ui.Alt, ui.KeyLeft)
	h.settle()
	if h.a.location() != inner {
		t.Fatalf("back %s != %s", h.a.location(), inner)
	}
	h.tt.Key(ui.Alt, ui.KeyRight)
	h.settle()
	if h.a.location() != rootLocation {
		t.Fatal(h.a.location())
	}
	// Grids rebuild across directory transitions without stored build values.
	h.a.arc.grid = true
	h.tt.Frame()
	h.tt.Frame()
	if out := os.Getenv("FILESYNC_ARCHIVE_SNAPSHOT"); out != "" {
		file, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(file, h.tt.Image())
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	h.a.archiveBrowse("照片")
	h.settle()
	h.a.archiveUp()
	h.settle()
	h.a.archiveUp()
	h.settle()
	if !h.a.onRoot() || h.a.br.server != server || h.a.br.path != "" {
		t.Fatalf("exit %s", h.a.place)
	}
}
func TestArchiveBrowserLocalExportAndPicker(t *testing.T) {
	dir := t.TempDir()
	p := archiveFixture(t, dir)
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.openLocal(dir)
	h.settle()
	h.a.openLocalEntry(fileindex.Entry{Path: p, Name: filepath.Base(p)})
	h.settle()
	if !h.a.onArchive() || !h.a.arc.local {
		t.Fatal("not local archive")
	}
	h.a.archiveBrowse("照片")
	h.settle()
	e := h.a.arc.items[0]
	h.a.archiveOpen(e)
	h.settle()
	if h.a.preview == nil || h.a.preview.text != "压缩包内的文本" {
		t.Fatalf("preview %+v", h.a.preview)
	}
	h.a.closePreview()
	h.tt.Frame()
	dest := t.TempDir()
	h.a.pickDir = func(string, string) (string, error) { return dest, nil }
	h.a.downloadTo(h.a.archiveFiles(), false, []fileindex.Entry{e}, "解压到本机")
	h.waitStarted(1)
	h.waitTransfers()
	b, err := os.ReadFile(filepath.Join(dest, e.Name))
	if err != nil || string(b) != "压缩包内的文本" {
		t.Fatalf("download %q %v", b, err)
	}
	loc := h.a.location()
	h.a.archiveUp()
	h.settle()
	h.a.archiveUp()
	h.settle()
	if !h.a.onLocal() || h.a.local.dir != dir {
		t.Fatal("didn't exit archive")
	}
	h.a.goLocation(loc)
	h.settle()
	if h.a.arc.path != "照片" {
		t.Fatal("lost member directory")
	}
	h.a.archiveExtract(h.a.arc.items)
	h.tt.Frame()
	if !h.a.nas.open || h.a.nas.local == nil || h.a.nas.title != "解压到" {
		t.Fatal("local archive picker missing")
	}
}
func TestArchiveCacheIncludesMemberAndContainerVersion(t *testing.T) {
	c := fileindex.New("nas:8090", "token")
	e := fileindex.Entry{Root: 1, Name: "a.txt", Path: "a.txt", Size: 3, MTime: 100}
	a, _ := openCachePath(c.Archive(1, "a.zip", "v1"), e)
	b, _ := openCachePath(c.Archive(1, "b.zip", "v1"), e)
	d, _ := openCachePath(c.Archive(1, "a.zip", "v2"), e)
	if a == b || a == d {
		t.Fatal("archive cache collision")
	}
}
func TestArchiveNASExtractSameAndOtherServer(t *testing.T) {
	src, dst := fitest.New(t), fitest.New(t)
	archiveFixture(t, src.Root)
	c := fileindex.New(src.Addr(), "tok")
	items, version, _, err := c.ArchiveList(context.Background(), 1, "备份.zip", "")
	if err != nil {
		t.Fatal(err)
	}
	ac := c.Archive(1, "备份.zip", version)
	for _, target := range []*fitest.Server{src, dst} {
		dc := fileindex.New(target.Addr(), "tok")
		whole, errs, err := fileindex.Relay(context.Background(), ac, 1, paths(items), dc, 1, "解压目录", nil)
		if err != nil || len(errs) > 0 || len(whole) != 3 {
			t.Fatalf("relay %v %v %v", whole, errs, err)
		}
		b, err := os.ReadFile(filepath.Join(target.Root, "解压目录", "照片", "说明.txt"))
		if err != nil || string(b) != "压缩包内的文本" {
			t.Fatalf("relay body %q %v", b, err)
		}
	}
}

func TestArchiveSelectionQueuesServerExtractionWithoutBrowsing(t *testing.T) {
	f := fitest.New(t)
	archiveFixture(t, f.Root)
	os.Mkdir(filepath.Join(f.Root, "目标"), 0755)
	h := browserHarness(t, f)
	server := h.a.br.server
	c, _ := h.a.serverClient(server)
	f.HoldArchiveLists.Store(true)
	h.a.extractArchive(server, c, fileindex.Entry{Root: 1, Path: "备份.zip", Name: "备份.zip"})
	h.settle()
	if !h.a.nas.open || h.a.onArchive() {
		t.Fatal("extraction unnecessarily browsed archive")
	}
	h.a.nas.pick(server, 1, "nas", "目标")
	h.settle()
	h.waitTransfers()
	if f.ArchiveExtracts.Load() != 1 {
		t.Fatal("server extraction endpoint not used")
	}
	tr := h.a.transfers[len(h.a.transfers)-1]
	if tr.task == 0 || tr.kind != "解压" || tr.state != "done" {
		t.Fatalf("task %+v", tr)
	}
	body, err := os.ReadFile(filepath.Join(f.Root, "目标", "照片", "说明.txt"))
	if err != nil || string(body) != "压缩包内的文本" {
		t.Fatal(string(body), err)
	}
	h.tt.Frame()
	h.tt.Frame()
}

func TestArchiveMemberExtractionUsesServerTask(t *testing.T) {
	f := fitest.New(t)
	archiveFixture(t, f.Root)
	os.Mkdir(filepath.Join(f.Root, "目标"), 0755)
	h := browserHarness(t, f)
	h.a.openEntry(fileindex.Entry{Root: 1, Path: "备份.zip", Name: "备份.zip"})
	h.settle()
	b := h.a.arc
	h.a.archiveBrowse("照片")
	h.settle()
	h.a.archiveExtract(h.a.arc.items)
	h.settle()
	h.a.nas.pick(b.server, 1, "nas", "目标")
	h.settle()
	h.waitTransfers()
	if f.ArchiveExtracts.Load() != 1 {
		t.Fatal("selection relayed through client")
	}
	body, err := os.ReadFile(filepath.Join(f.Root, "目标", "说明.txt"))
	if err != nil || string(body) != "压缩包内的文本" {
		t.Fatal(string(body), err)
	}
}

func TestArchiveCredentialsChangeCancelsAndLeavesPage(t *testing.T) {
	f := fitest.New(t)
	archiveFixture(t, f.Root)
	h := browserHarness(t, f)
	h.a.openEntry(fileindex.Entry{Root: 1, Path: "备份.zip", Name: "备份.zip"})
	h.settle()
	id := h.a.arc.server
	h.a.store.Server(id).Token = ""
	h.a.serverChanged(id)
	h.settle()
	if h.a.arc != nil || h.a.place == "archive" {
		t.Fatal("stale archive survived logout")
	}
}

func TestArchiveNASDownloadPickerUsesArchiveSource(t *testing.T) {
	f := fitest.New(t)
	archiveFixture(t, f.Root)
	h := browserHarness(t, f)
	h.a.openEntry(fileindex.Entry{Root: 1, Path: "备份.zip", Name: "备份.zip"})
	h.settle()
	dest := t.TempDir()
	h.a.pickDir = func(string, string) (string, error) { return dest, nil }
	h.a.downloadTo(h.a.archiveFiles(), false, h.a.arc.items, "解压到本机")
	h.settle()
	// The directory picker posts task creation from a goroutine. Wait for
	// that post before checking whether the transfer has finished.
	h.waitStarted(1)
	h.waitTransfers()
	body, err := os.ReadFile(filepath.Join(dest, "照片", "说明.txt"))
	if err != nil || string(body) != "压缩包内的文本" {
		t.Fatalf("%q %v", body, err)
	}
}

func TestArchiveCancelLoadingRejectsLateResults(t *testing.T) {
	f := fitest.New(t)
	archiveFixture(t, f.Root)
	h := browserHarness(t, f)
	f.HoldArchiveLists.Store(true)
	h.a.openEntry(fileindex.Entry{Root: 1, Path: "备份.zip", Name: "备份.zip"})
	h.tt.Frame()
	b := h.a.arc
	if b == nil || !b.loading {
		t.Fatal("not loading")
	}
	if err := h.tt.Click("取消读取"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if b.loading || b.err != "读取已取消，可刷新重试" || len(b.items) != 0 {
		t.Fatalf("cancel %+v", b)
	}
	f.HoldArchiveLists.Store(false)
	h.a.archiveBrowse("")
	h.settle()
	if b.err != "" || len(b.items) != 3 {
		t.Fatalf("reload %+v", b)
	}
}
