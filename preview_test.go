package main

import (
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"filesync/fileindex"
	"filesync/fileindex/fitest"
	"github.com/egoist/mygo/ui"
)

func TestNativePreviewTextImageNavigationAndSystemOpen(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := fitest.New(t)
	os.WriteFile(filepath.Join(f.Root, "笔记.txt"), []byte("你好，原生预览\n第二行"), 0644)
	os.WriteFile(filepath.Join(f.Root, "报告.pdf"), []byte("%PDF-1.4 test"), 0644)
	img := image.NewRGBA(image.Rect(0, 0, 160, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 160; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	file, _ := os.Create(filepath.Join(f.Root, "图片.png"))
	png.Encode(file, img)
	file.Close()
	h := browserHarness(t, f)
	opened := ""
	h.a.openPath = func(p string) { opened = p }
	h.click("笔记.txt")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if h.a.preview == nil || h.a.preview.text != "你好，原生预览\n第二行" {
		t.Fatalf("preview %+v", h.a.preview)
	}
	h.click("文本预览")
	h.tt.Type("不应修改")
	h.tt.Frame()
	if h.a.preview.text != "你好，原生预览\n第二行" {
		t.Fatal("preview editable")
	}
	// Navigation retains the file snapshot and never depends on the live list.
	h.a.previewStep(-1)
	h.settle()
	if h.a.preview == nil || h.a.preview.bitmap == nil {
		t.Fatalf("image %+v", h.a.preview)
	}
	h.click("原始大小")
	h.click("放大")
	h.click("缩小")
	h.click("适合窗口")
	for i := 0; i < 3; i++ {
		h.tt.Frame()
	}
	if out := os.Getenv("FILESYNC_PREVIEW_SCREENSHOT"); out != "" {
		f, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, h.tt.Image())
		f.Close()
	}
	h.click("关闭预览")
	h.click("报告.pdf")
	h.tt.Key(0, ui.KeyEnter)
	h.waitTransfers()
	data, err := os.ReadFile(opened)
	if err != nil || string(data) != "%PDF-1.4 test" {
		t.Fatalf("opened %q %q %v", opened, data, err)
	}
	cached := opened
	if err := os.WriteFile(cached, []byte("local edit"), 0600); err != nil {
		t.Fatal(err)
	}
	opened = ""
	h.a.openEntry(h.a.br.items[1])
	h.waitTransfers()
	if opened == "" {
		t.Fatal("cached open failed")
	}
	data, err = os.ReadFile(opened)
	if err != nil || string(data) != "local edit" {
		t.Fatal("reopening cache overwrote local application edits")
	}
}
func TestMediaOpenUsesPlaylistProxyAndFallback(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := fitest.New(t)
	os.WriteFile(filepath.Join(f.Root, "电影.mp4"), []byte("0123456789"), 0644)
	h := browserHarness(t, f)
	opened := ""
	h.a.openPath = func(p string) { opened = p }
	h.click("电影.mp4")
	h.tt.Key(0, ui.KeyEnter)
	h.waitTransfers()
	data, err := os.ReadFile(opened)
	if err != nil || !strings.HasSuffix(opened, ".m3u") {
		t.Fatalf("playlist %q %v", opened, err)
	}
	address := strings.TrimSpace(strings.TrimPrefix(string(data), "#EXTM3U\n"))
	req, _ := http.NewRequest("GET", address, nil)
	req.Header.Set("Range", "bytes=7-9")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 206 || string(body) != "789" {
		t.Fatalf("stream %d %q", res.StatusCode, body)
	}
	c, sv := h.a.brClient()
	h.a.systemOpenSource(sv.ID, c, h.a.br.items[0], true)
	h.waitTransfers()
	data, err = os.ReadFile(opened)
	if err != nil || string(data) != "0123456789" || strings.HasSuffix(opened, ".m3u") {
		t.Fatalf("fallback %q %q %v", opened, data, err)
	}
	h.a.serverChanged(sv.ID)
	res, err = http.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatal("source not revoked")
	}
}
func TestDecodePreviewText(t *testing.T) {
	units := utf16.Encode([]rune("你好😀"))
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		data := make([]byte, 2+2*len(units))
		order.PutUint16(data, 0xfeff)
		for i, u := range units {
			order.PutUint16(data[2+2*i:], u)
		}
		text, err := decodePreviewText(data, false)
		if err != nil || text != "你好😀" {
			t.Fatalf("%q %v", text, err)
		}
	}
	text, err := decodePreviewText([]byte("\xef\xbb\xbf你好\xe4\xbd"), true)
	if err != nil || text != "你好" {
		t.Fatalf("partial %q %v", text, err)
	}
	for _, data := range [][]byte{{0, 1, 2}, {0xff, 0xab}, {0xff, 0xfe, 0}} {
		if _, err := decodePreviewText(data, false); err == nil {
			t.Fatal("accepted binary/incomplete text")
		}
	}
}
func TestNativePreviewRejectsLateResultsAndAuthExpiry(t *testing.T) {
	f := fitest.New(t)
	os.WriteFile(filepath.Join(f.Root, "a.txt"), []byte("a"), 0644)
	h := browserHarness(t, f)
	c, sv := h.a.brClient()
	e := h.a.br.items[0]
	h.a.showPreview(sv.ID, c, e, []fileindex.Entry{e})
	h.a.closePreview()
	h.settle()
	if h.a.preview != nil {
		t.Fatal("late result revived preview")
	}
	f.Expired.Store(true)
	h.a.showPreview(sv.ID, c, e, []fileindex.Entry{e})
	h.settle()
	if sv.Token != "" || h.a.preview != nil {
		t.Fatal("expired session not cleared")
	}
}
func TestNativeRecycleRestoreCollisionPurgeAndFilter(t *testing.T) {
	f := fitest.New(t)
	os.WriteFile(filepath.Join(f.Root, "a.txt"), []byte("original"), 0644)
	os.MkdirAll(filepath.Join(f.Root, "目录"), 0755)
	os.WriteFile(filepath.Join(f.Root, "目录", "nested.txt"), []byte("nested"), 0644)
	h := browserHarness(t, f)
	c, sv := h.a.brClient()
	if err := c.Trash(context.Background(), 1, []string{"a.txt", "目录"}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(f.Root, "a.txt"), []byte("replacement"), 0644)
	h.click("回收站")
	h.want("回收站 · 家里")
	if out := os.Getenv("FILESYNC_RECYCLE_SCREENSHOT"); out != "" {
		f, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, h.tt.Image())
		f.Close()
	}
	if len(h.a.recycle.items) != 2 {
		t.Fatal(h.a.recycle.items)
	}
	h.typeInto("筛选回收站", "a.txt")
	h.tt.Frame()
	if len(h.a.recycle.shown) != 1 {
		t.Fatal("filter failed")
	}
	h.click("a.txt")
	h.click("恢复所选")
	h.waitTransfers()
	data, _ := os.ReadFile(filepath.Join(f.Root, "a.txt"))
	if string(data) != "replacement" {
		t.Fatal("restore overwrote existing")
	}
	data, _ = os.ReadFile(filepath.Join(f.Root, "a.txt (1)"))
	if string(data) != "original" {
		t.Fatal("numbered restore missing")
	}
	h.click("筛选回收站")
	h.tt.Key(ui.Ctrl, ui.KeyA)
	h.tt.Key(0, ui.KeyBackspace)
	h.tt.Frame()
	h.settle()
	h.click("目录")
	h.click("彻底删除…")
	h.click("取消")
	if len(h.a.recycle.items) != 1 {
		t.Fatal("cancel deleted")
	}
	h.click("彻底删除…")
	h.click("确认永久删除")
	h.waitTransfers()
	if len(h.a.recycle.items) != 0 {
		t.Fatal("purged item retained")
	}
	if _, err := os.Stat(filepath.Join(f.Trash, "目录")); !os.IsNotExist(err) {
		t.Fatal("purge left contents")
	}
	h.a.serverChanged(sv.ID)
	h.tt.Frame()
	if h.a.recycle != nil {
		t.Fatal("stale recycle session")
	}
}
func TestNativeRecycleReadOnlyAndEvents(t *testing.T) {
	f := fitest.New(t)
	f.Writable.Store(true)
	os.WriteFile(filepath.Join(f.ReadOnly, "locked.txt"), []byte("data"), 0644)
	c := fileindex.New(f.Addr(), "tok")
	if err := c.Trash(context.Background(), 2, []string{"locked.txt"}); err != nil {
		t.Fatal(err)
	}
	f.Writable.Store(false)
	h := browserHarness(t, f)
	h.click("回收站")
	h.click("locked.txt")
	if a := h.a; a.trashWritable(a.recycle) {
		t.Fatal("read-only trash enabled")
	}
	f.Writable.Store(true)
	if _, err := c.RestoreTrash(context.Background(), h.a.recycle.chosen()); err != nil {
		t.Fatal(err)
	}
	h.a.onEvent(h.a.recycle.server, fileindex.Event{Type: "trash"})
	// Debounce is asynchronous; wait for the queued reload.
	fn := <-h.queue
	fn()
	h.settle()
	if len(h.a.recycle.items) != 0 {
		t.Fatal("trash event ignored")
	}
}

func TestProxyExpiryClearsNativeSession(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := fitest.New(t)
	os.WriteFile(filepath.Join(f.Root, "media.mp4"), []byte("test"), 0644)
	h := browserHarness(t, f)
	opened := ""
	h.a.openPath = func(p string) { opened = p }
	h.a.openEntry(h.a.br.items[0])
	h.waitTransfers()
	data, _ := os.ReadFile(opened)
	address := strings.TrimSpace(strings.TrimPrefix(string(data), "#EXTM3U\n"))
	sv := h.a.store.Servers[0]
	f.Expired.Store(true)
	res, err := http.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	fn := <-h.queue
	fn()
	h.settle()
	if sv.Token != "" {
		t.Fatal("proxy failed to expire the native session")
	}
	h.want("登录会话已过期，请重新登录 NAS")
}
func TestPreviewFitsMinimumWindowAndOversizeImage(t *testing.T) {
	f := fitest.New(t)
	os.WriteFile(filepath.Join(f.Root, "a.txt"), []byte("text"), 0644)
	h := browserHarness(t, f)
	h.tt = ui.NewTester(h.a.view, 900, 480)
	h.a.openEntry(h.a.br.items[0])
	h.settle()
	h.click("关闭预览")
	c, sv := h.a.brClient()
	h.a.showPreview(sv.ID, c, fileindex.Entry{Root: 1, Path: "large.png", Name: "large.png", Size: imagePreviewLimit + 1}, nil)
	h.settle()
	if h.a.preview == nil || !strings.Contains(h.a.preview.err, "32 MB") {
		t.Fatal("oversized image was fetched/decoded")
	}
	h.click("关闭预览")
}
func TestOpenCachePathSeparatesSourcesAndConfinesNames(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	c := fileindex.New("nas:8090", "tok")
	entry := fileindex.Entry{Root: 1, Path: "../../private", Name: "../CON.txt", Size: 1, MTime: 1}
	p, err := openCachePath(c, entry)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != "file-CON.txt" || strings.Contains(p, "../") {
		t.Fatal(p)
	}
	c.Addr = "other:8090"
	other, err := openCachePath(c, entry)
	if err != nil || p == other {
		t.Fatal("server identities shared cache")
	}
	entry.Name = strings.Repeat("图", 100) + ".pdf"
	p, err = openCachePath(c, entry)
	if err != nil || len(filepath.Base(p)) > 240 || !strings.HasSuffix(p, ".pdf") {
		t.Fatalf("long filename %q %v", p, err)
	}
}
