package main

import (
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"filesync/fileindex"
	"filesync/fileindex/fitest"
	"github.com/egoist/mygo/ui"
)

func TestNativePreviewTextImageNavigationAndSystemOpen(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := fitest.New(t)
	os.WriteFile(filepath.Join(f.Root, "笔记.txt"), []byte("你好，原生预览\n第二行"), 0644)
	os.WriteFile(filepath.Join(f.Root, "报告.docx"), []byte("%DOCX test"), 0644)
	img := image.NewRGBA(image.Rect(0, 0, 160, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 160; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	for _, name := range []string{"图片.png", "图片2.png"} {
		file, _ := os.Create(filepath.Join(f.Root, name))
		png.Encode(file, img)
		file.Close()
	}
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
	// The arrows step among files of a kind: texts here.
	h.a.previewStep(-1)
	h.settle()
	if h.a.preview.entry.Name != "笔记.txt" {
		t.Fatalf("stepped from a text to %q", h.a.preview.entry.Name)
	}
	h.click("关闭预览")

	// A picture shows over the window, the others of its folder below.
	h.clickPage("图片.png")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if h.a.preview == nil || h.a.preview.bitmap == nil {
		t.Fatalf("image %+v", h.a.preview)
	}
	h.want("1 / 2 · 160 × 100")
	h.tt.Key(0, ui.KeyRight)
	if p := h.a.preview; p.entry.Name != "图片2.png" || p.placeholder == nil {
		t.Fatalf("stepped to %q, placeholder %v", p.entry.Name, p.placeholder != nil)
	}
	h.settle()
	h.want("2 / 2 · 160 × 100")
	// Navigation retains the file snapshot and never depends on the live list.
	h.click("缩略图 图片.png")
	h.settle()
	if h.a.preview.entry.Name != "图片.png" {
		t.Fatalf("strip chose %q", h.a.preview.entry.Name)
	}
	h.click("原始大小")
	h.click("放大")
	if p := h.a.preview; !p.actual || p.zoom != 1.25 {
		t.Fatalf("zoomed in: %v %v", p.actual, p.zoom)
	}
	// Zoomed out to fitting, it fits again.
	h.click("缩小")
	if h.a.preview.actual {
		t.Fatal("zoomed out below fitting, but not fitting")
	}
	h.click("放大")
	h.click("适合窗口")
	if h.a.preview.actual {
		t.Fatal("not fitting")
	}
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
	h.click("报告.docx")
	h.tt.Key(0, ui.KeyEnter)
	h.waitTransfers()
	data, err := os.ReadFile(opened)
	if err != nil || string(data) != "%DOCX test" {
		t.Fatalf("opened %q %q %v", opened, data, err)
	}
	cached := opened
	if err := os.WriteFile(cached, []byte("local edit"), 0600); err != nil {
		t.Fatal(err)
	}
	opened = ""
	for _, e := range h.a.br.items {
		if e.Name == "报告.docx" {
			h.a.openEntry(e)
		}
	}
	h.waitTransfers()
	if opened == "" {
		t.Fatal("cached open failed")
	}
	if len(h.a.transfers) != 2 || h.a.transfers[0].kind != "打开" || h.a.transfers[0].localFile != cached {
		t.Fatal("cached reopening missing from history")
	}
	flushHistory(t, h.a)
	if saved := readHistory(t, h.a.store.Dir); len(saved.Items) != 2 || saved.Items[0].File != cached {
		t.Fatal("cached open path not persisted")
	}
	data, err = os.ReadFile(opened)
	if err != nil || string(data) != "local edit" {
		t.Fatal("reopening cache overwrote local application edits")
	}
}

// waitOpened runs the work posted until *opened names what was opened.
func (h *harness) waitOpened(opened *string) {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for *opened == "" {
		select {
		case fn := <-h.queue:
			fn()
			h.tt.Frame()
		case <-deadline:
			h.t.Fatal("nothing opened")
		}
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
	// The player opens at once; nothing transfers but what it reads.
	h.waitOpened(&opened)
	if len(h.a.transfers) != 0 {
		t.Fatalf("streaming shows as a transfer: %q", h.a.transfers[0].title)
	}
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
	// Downloaded first, it is a transfer.
	if len(h.a.transfers) != 1 || h.a.transfers[0].kind != "下载并打开" {
		t.Fatalf("transfers %d", len(h.a.transfers))
	}
	if tr := h.a.transfers[0]; tr.localDir != filepath.Dir(opened) || !tr.folderReady {
		t.Fatalf("download cache directory missing: %+v", tr)
	}
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
	h.click("恢复")
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
	h.waitOpened(&opened)
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

// The wheel zooms about the pointer, the point under it staying put,
// within the picture's edges; a double click toggles fitting.
func TestViewerZoomAndPan(t *testing.T) {
	bmp := ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 2000, 1000)))
	p := &preview{bitmap: bmp, zoom: 1, stageW: 1000, stageH: 800}
	if f := p.fitScale(); f != 0.5 {
		t.Fatalf("fits at %v", f)
	}
	// The wheel up, 100 DIPs right of the center: from fitting, zoomed.
	p.stageInput(ui.InputEvent{Kind: ui.InputScroll, DY: -1, X: 600, Y: 400})
	if !p.actual || math.Abs(float64(p.zoom-0.5*wheelZoom)) > 1e-6 {
		t.Fatalf("zoom %v", p.zoom)
	}
	// The point that was under the pointer (200 picture pixels right of
	// its center) still is.
	if got := p.pan.X + 200*p.zoom; math.Abs(float64(got-100)) > 0.01 {
		t.Fatalf("point moved to %v", got)
	}
	// Narrower than the stage when its height is, it stays centered
	// up and down.
	if p.pan.Y != 0 {
		t.Fatalf("pan y %v", p.pan.Y)
	}
	// Zoomed about a point far right, it stops at its left edge.
	for range 8 {
		p.stageInput(ui.InputEvent{Kind: ui.InputScroll, DY: -1, X: 990, Y: 400})
	}
	if want := (2000*p.zoom - 1000) / 2; p.pan.X > want+0.01 || p.pan.X < -want-0.01 {
		t.Fatalf("pan %v, edge %v", p.pan.X, want)
	}
	// The wheel down past fitting fits it, centered.
	for range 30 {
		p.stageInput(ui.InputEvent{Kind: ui.InputScroll, DY: 1, X: 500, Y: 400})
	}
	if p.actual || p.pan != (ui.Point{}) {
		t.Fatalf("not fitting: %v %v", p.actual, p.pan)
	}
	// A double click shows its own size about the pointer, another fits.
	p.stageInput(ui.InputEvent{Kind: ui.InputPointerDown, X: 500, Y: 400, Clicks: 2})
	if !p.actual || math.Abs(float64(p.zoom-1)) > 1e-4 {
		t.Fatalf("double click: %v %v", p.actual, p.zoom)
	}
	p.stageInput(ui.InputEvent{Kind: ui.InputPointerDown, X: 500, Y: 400, Clicks: 2})
	if p.actual {
		t.Fatal("double click again did not fit")
	}
}

// The wheel over the strip steps through the pictures.
func TestStripWheelSteps(t *testing.T) {
	f := fitest.New(t)
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	for _, name := range []string{"a.png", "b.png", "c.png"} {
		file, _ := os.Create(filepath.Join(f.Root, name))
		png.Encode(file, img)
		file.Close()
	}
	h := browserHarness(t, f)
	h.clickPage("a.png")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	r, ok := h.tt.Find("缩略图 b.png")
	if !ok {
		t.Fatal("no strip")
	}
	h.tt.Scroll(r.X+r.W/2, r.Y+r.H/2, 0, 40)
	h.settle()
	if h.a.preview.entry.Name != "b.png" {
		t.Fatalf("wheel down showed %q", h.a.preview.entry.Name)
	}
	h.tt.Scroll(r.X+r.W/2, r.Y+r.H/2, 0, -40)
	h.settle()
	if h.a.preview.entry.Name != "a.png" {
		t.Fatalf("wheel up showed %q", h.a.preview.entry.Name)
	}
}

// A zoom moves smoothly to its scale.
func TestViewerZoomAnimates(t *testing.T) {
	// Slow enough to catch on its way, however slow the test runs.
	defer func(d time.Duration) { zoomMotion = d }(zoomMotion)
	zoomMotion = 30 * time.Second
	f := fitest.New(t)
	img := image.NewRGBA(image.Rect(0, 0, 300, 200))
	file, _ := os.Create(filepath.Join(f.Root, "a.png"))
	png.Encode(file, img)
	file.Close()
	h := browserHarness(t, f)
	h.clickPage("a.png")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	h.tt.Frame() // the stage has its size
	width := func() float32 {
		r, ok := h.tt.Find("图片 a.png")
		if !ok {
			t.Fatal("no picture")
		}
		return r.W
	}
	if w := width(); w != 300 {
		t.Fatalf("fits at %v", w)
	}
	h.a.preview.zoomBy(2)
	h.tt.Frame()
	h.tt.Advance(400 * time.Millisecond)
	if w := width(); w <= 300.5 || w >= 590 {
		t.Fatalf("not on its way: %v", w)
	}
	// Quick, it ends at the scale asked.
	zoomMotion = 10 * time.Millisecond
	h.a.preview.zoomBy(1.25) // 750, within the stage
	for deadline := time.Now().Add(2 * time.Second); width() != 750; {
		if time.Now().After(deadline) {
			t.Fatalf("zoomed to %v", width())
		}
		h.tt.Advance(20 * time.Millisecond)
	}
}

// Zoomed, a drag moves the picture, held within its edges once let go;
// fitting, a swipe shows the next picture, and a short drag springs back.
func TestViewerDragAndSwipe(t *testing.T) {
	defer func(d time.Duration) { zoomMotion = d }(zoomMotion)
	zoomMotion = 10 * time.Millisecond
	f := fitest.New(t)
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for _, name := range []string{"a.png", "b.png"} {
		file, _ := os.Create(filepath.Join(f.Root, name))
		png.Encode(file, img)
		file.Close()
	}
	h := browserHarness(t, f)
	h.clickPage("a.png")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	h.tt.Frame()
	p := h.a.preview
	// rest lets the picture come to rest: it shows where it did a few
	// frames ago.
	rest := func() {
		t.Helper()
		var last ui.Rect
		still := 0
		for deadline := time.Now().Add(3 * time.Second); still < 5 && time.Now().Before(deadline); {
			time.Sleep(20 * time.Millisecond)
			h.tt.Frame()
			h.settle()
			r, _ := h.tt.Find("图片 " + h.a.preview.entry.Name)
			if r == last {
				still++
			} else {
				still, last = 0, r
			}
		}
	}
	center := func() (float32, float32) {
		r, ok := h.tt.Find("图片 " + p.entry.Name)
		if !ok {
			t.Fatalf("no picture %q in %q", p.entry.Name, h.tt.Texts())
		}
		return r.X + r.W/2, r.Y + r.H/2
	}
	// A drag, held still before it lets go (a short one), or let go on
	// its way (a throw).
	drag := func(dx float32, hold bool) {
		x, y := center()
		h.tt.Press(x, y)
		for i := float32(1); i <= 10; i++ {
			h.tt.Move(x+dx*i/10, y)
			time.Sleep(5 * time.Millisecond)
		}
		if hold {
			time.Sleep(200 * time.Millisecond)
			h.tt.Move(x+dx, y)
		}
		h.tt.Release(x+dx, y)
		h.settle()
	}

	// Fitting, a short drag springs back; a swipe steps.
	drag(-30, true)
	rest()
	if p.entry.Name != "a.png" || p.pan != (ui.Point{}) {
		t.Fatalf("short drag: %q at %v", p.entry.Name, p.pan)
	}
	drag(-300, false)
	rest()
	h.settle()
	if p = h.a.preview; p.entry.Name != "b.png" {
		t.Fatalf("swipe showed %q", p.entry.Name)
	}

	// Zoomed, dragged far, it rests at its edge.
	p.zoomBy(4)
	rest()
	drag(3000, true)
	rest()
	w, _ := p.bitmap.Size()
	if edge := (float32(w)*p.zoom - p.stageW) / 2; math.Abs(float64(p.pan.X-edge)) > 1 {
		t.Fatalf("pan %v, edge %v", p.pan.X, edge)
	}
}
