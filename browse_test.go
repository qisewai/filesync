package main

import (
	"context"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"filemanager/remoteclient"
	"filemanager/remoteclient/fitest"
	"filesync/config"
	"github.com/egoist/mygo/ui"
)

func browserHarness(t *testing.T, f *fitest.Server) *harness {
	h := newHarness(t, signedIn(t, f))
	h.settle()
	// The app starts on the search: the tests start in the first root.
	sv := h.a.store.Servers[0]
	if roots := h.a.roots[sv.ID]; len(roots) > 0 {
		h.a.goPlace(rootPlace(sv.ID, roots[0].ID))
		h.settle()
		h.a.treeNode(sv.ID, roots[0].ID, "").open = true
		h.settle()
	}
	return h
}

func (h *harness) waitTransfers() {
	h.t.Helper()
	deadline := time.After(10 * time.Second)
	for h.a.activeTransfers() > 0 {
		select {
		case fn := <-h.queue:
			fn()
			h.tt.Frame()
		case <-deadline:
			h.t.Fatal("transfer never finished")
		}
	}
	h.settle()
	for _, tr := range h.a.transfers {
		if tr.state != "done" {
			h.t.Fatalf("transfer %s: %s, %s, %v", tr.title, tr.state, tr.err, tr.errors)
		}
	}
}

// waitStarted runs posted work until there are n transfers: those that
// start once a check off the main thread answers.
func (h *harness) waitStarted(n int) {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for len(h.a.transfers) < n {
		select {
		case fn := <-h.queue:
			fn()
			h.tt.Frame()
		case <-deadline:
			h.t.Fatal("the transfer did not start")
		}
	}
}

func TestNativeBrowserTreeSearchAndFileOperations(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "项目", "子目录"), 0755)
	os.WriteFile(filepath.Join(f.Root, "项目", "报告.txt"), []byte("report"), 0644)
	os.WriteFile(filepath.Join(f.Root, "项目", "其他.txt"), []byte("other"), 0644)
	h := browserHarness(t, f)
	h.click("文件夹 项目")
	h.treeArrow("文件夹 项目", 2)
	h.want("报告.txt")
	h.more("新建文件夹…")
	h.tt.Type("新目录")
	h.click("创建")
	h.want("文件夹 新目录")
	h.click("报告.txt")
	h.tt.Key(0, ui.KeyF2)
	h.settle()
	h.tt.Key(ui.Ctrl, ui.KeyA)
	h.tt.Type("计划.txt")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	h.want("计划.txt")
	if g := h.find("计划"); g.total != 1 || h.a.gsearch.scope != "folder" {
		t.Fatalf("search %+v", g)
	}
	h.tt.Key(ui.Alt, ui.KeyLeft)
	h.settle()
	h.click("网格视图")
	if !h.a.br.grid {
		t.Fatal("grid did not stay selected")
	}
	h.tt.Frame()
	h.click("计划.txt")
	h.tt.Key(0, ui.KeyDelete)
	h.click("移到回收站")
	if _, err := os.Stat(filepath.Join(f.Trash, "项目", "计划.txt")); err != nil {
		t.Fatal(err)
	}
	h.click("备份任务")
	h.click("文件夹 子目录")
	if h.a.br.path != "项目/子目录" {
		t.Fatalf("tree opened %q", h.a.br.path)
	}
	for i := 0; i < 3; i++ {
		h.tt.Frame()
	}
}

func TestNativeBrowserUploadDownloadAndTasks(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "目标"), 0755)
	h := browserHarness(t, f)
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "照片"), 0755)
	os.WriteFile(filepath.Join(src, "照片", "a.jpg"), []byte("photo"), 0644)
	os.WriteFile(filepath.Join(src, "笔记.txt"), []byte("notes"), 0644)
	h.a.upload([]string{filepath.Join(src, "照片"), filepath.Join(src, "笔记.txt")})
	h.waitTransfers()
	h.want("照片")
	h.want("笔记.txt")
	h.clickPage("照片")
	dest := t.TempDir()
	h.a.pickDir = func(string, string) (string, error) { return dest, nil }
	h.menu("照片", "下载到…")
	// The file dialog itself runs off the UI thread.
	deadline := time.After(5 * time.Second)
	for len(h.a.transfers) < 2 {
		select {
		case fn := <-h.queue:
			fn()
			h.tt.Frame()
		case <-deadline:
			t.Fatal("download did not start")
		}
	}
	h.waitTransfers()
	if b, err := os.ReadFile(filepath.Join(dest, "照片", "a.jpg")); err != nil || string(b) != "photo" {
		t.Fatalf("download %q, %v", b, err)
	}
	// Copied, and pasted in another folder of the server.
	h.click("笔记.txt")
	h.tt.Key(ui.Ctrl, ui.KeyC)
	h.click("文件夹 目标")
	h.settle()
	h.tt.Key(ui.Ctrl, ui.KeyV)
	h.waitStarted(3)
	h.waitTransfers()
	if b, err := os.ReadFile(filepath.Join(f.Root, "目标", "笔记.txt")); err != nil || string(b) != "notes" {
		t.Fatalf("copy %q, %v", b, err)
	}
	h.click("传输任务")
	h.want("上传")
	h.want("下载")
	h.want("复制")
	for _, tr := range h.a.transfers {
		if tr.kind == "下载" && (tr.localDir != dest || !tr.folderReady) {
			t.Fatalf("download directory missing: %+v", tr)
		}
	}
}

func TestTaskSnapshotRecoversMissingEvents(t *testing.T) {
	f := fitest.New(t)
	f.DropTaskEvents.Store(true)
	os.MkdirAll(filepath.Join(f.Root, "目标"), 0755)
	os.WriteFile(filepath.Join(f.Root, "a.txt"), []byte("a"), 0644)
	h := browserHarness(t, f)
	c := fileindex.New(f.Addr(), "tok")
	task, err := c.Transfer(context.Background(), false, 1, []string{"a.txt"}, 1, "目标")
	if err != nil {
		t.Fatal(err)
	}
	// The final event was emitted before this client tracked the task.
	time.Sleep(40 * time.Millisecond)
	tr := h.a.addTransfer("复制", "a.txt")
	tr.server = h.a.br.server
	tr.task = task.ID
	h.a.trackServerTask(c, tr, task)
	h.waitTransfers()
	if tr.state != "done" {
		t.Fatal("missed event left the task running")
	}
}

func TestTaskCompletionShowsServerWarnings(t *testing.T) {
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	tr := h.a.addTransfer("复制", "folder")
	note := "跳过 1 项：link（符号链接）"
	h.a.applyTask(tr, &fileindex.Task{Status: "done", Message: note})
	if tr.note != note || tr.state != "done" {
		t.Fatalf("completion %+v", tr)
	}
	h.tt.Frame()
	h.click("传输任务")
	found := false
	for _, text := range h.tt.Texts() {
		if strings.Contains(text, note) {
			found = true
		}
	}
	if !found {
		t.Fatal("server warning was hidden")
	}
}

func TestUploadPickerDoesNotFollowNavigation(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "目标"), 0755)
	h := browserHarness(t, f)
	gate := make(chan struct{})
	started := make(chan struct{})
	h.a.pickFiles = func(string) ([]string, error) {
		close(started)
		<-gate
		return []string{"/missing/should-not-upload"}, nil
	}
	h.a.pickUpload(false)
	<-started
	h.a.browse("目标")
	h.settle()
	close(gate)
	select {
	case fn := <-h.queue:
		fn()
	case <-time.After(time.Second):
		t.Fatal("picker did not return")
	}
	if len(h.a.transfers) != 0 {
		t.Fatal("uploaded into a directory picked after opening the dialog")
	}
}

func TestNativeBrowserReadOnlyAndRendering(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "项目", "资料"), 0755)
	os.WriteFile(filepath.Join(f.Root, "README.md"), []byte("readme"), 0644)
	os.WriteFile(filepath.Join(f.Root, "照片.jpg"), []byte("image"), 0644)
	h := browserHarness(t, f)
	h.click("网格视图")
	// Wait for the thumbnail request and draw additional frames.
	deadline := time.After(3 * time.Second)
	for {
		loading := false
		for _, th := range h.a.thumbs {
			loading = loading || th.loading
		}
		if !loading {
			break
		}
		select {
		case fn := <-h.queue:
			fn()
			h.tt.Frame()
		case <-deadline:
			t.Fatal("thumbnail never loaded")
		}
	}
	if out := os.Getenv("FILESYNC_UI_SNAPSHOT"); out != "" {
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
	h.click("文件夹 只读")
	h.a.upload([]string{"/missing"})
	if len(h.a.transfers) != 0 {
		t.Fatal("read-only root accepted an upload")
	}
	h.tt.Key(ui.Ctrl|ui.Shift, ui.KeyN)
	h.settle()
	if h.a.br.naming {
		t.Fatal("read-only root enabled mkdir")
	}
	h.click("备份任务")
	h.click("文件夹 nas")
	for i := 0; i < 3; i++ {
		h.tt.Frame()
	}
}

func TestCloseWaitsForLocalTransfers(t *testing.T) {
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	closed := false
	h.a.closeWindow = func() { closed = true }
	tr := h.a.addTransfer("上传", "file")
	tr.cancel = func() { h.a.post(func() { h.a.finishTransfer(tr, context.Canceled, nil) }) }
	if h.a.requestClose() {
		t.Fatal("closed during transfer")
	}
	h.a.quit()
	if closed {
		t.Fatal("closed before transfer cleaned up")
	}
	(<-h.queue)()
	if !closed || !h.a.closed {
		t.Fatal("did not close after cancellation")
	}
}

func TestCloseDoesNotWaitForeverWhenServerCannotCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	closed := false
	h.a.closeWindow = func() { closed = true }
	tr := h.a.addTransfer("复制", "folder")
	tr.task = 1
	h.a.trackServerTask(fileindex.New(strings.TrimPrefix(srv.URL, "http://"), ""), tr, &fileindex.Task{ID: 1, Status: "running"})
	h.a.quit()
	deadline := time.After(3 * time.Second)
	for !closed {
		select {
		case fn := <-h.queue:
			fn()
		case <-deadline:
			t.Fatal("quit got stuck waiting for an unavailable server")
		}
	}
	if tr.state != "failed" || !strings.Contains(tr.err, "可能继续运行") {
		t.Fatalf("server cancellation %+v", tr)
	}
}

// Copied on one server, pasted on another: its export passes through this
// computer; cut, the source then goes to its trash.
func TestCopyAndMoveToAnotherServer(t *testing.T) {
	f, f2 := fitest.New(t), fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "照片"), 0755)
	os.WriteFile(filepath.Join(f.Root, "照片", "a.jpg"), []byte("photo"), 0644)
	os.WriteFile(filepath.Join(f.Root, "笔记.txt"), []byte("notes"), 0644)
	os.MkdirAll(filepath.Join(f2.Root, "收"), 0755)
	os.WriteFile(filepath.Join(f2.Root, "收", "笔记.txt"), []byte("old"), 0644)
	store := signedIn(t, f)
	store.AddServer(&config.Server{Name: "公司", Addr: f2.Addr(), Token: "tok"})
	h := newHarness(t, store)
	h.settle()
	home := rootPlace(store.Servers[0].ID, 1)
	h.a.goPlace(home)
	h.settle()

	h.click("笔记.txt")
	h.tt.Key(ui.Ctrl, ui.KeyC)
	h.want("已复制 1 项")
	h.a.goLocation(nasLocation(store.Servers[1].ID, 1, "收"))
	h.settle()
	h.tt.Key(ui.Ctrl, ui.KeyV)
	h.settle()
	h.want("合并同名目录并覆盖文件？")
	h.click("合并并覆盖")
	h.waitTransfers()
	if b, _ := os.ReadFile(filepath.Join(f2.Root, "收", "笔记.txt")); string(b) != "notes" {
		t.Fatalf("copy %q", b)
	}
	if _, err := os.Stat(filepath.Join(f.Root, "笔记.txt")); err != nil {
		t.Fatal("copy removed the source")
	}
	if h.a.clip == nil {
		t.Fatal("a copy pasted leaves the clipboard")
	}

	h.a.goPlace(home)
	h.settle()
	h.clickPage("照片")
	h.tt.Key(ui.Ctrl, ui.KeyX)
	h.want("已剪切 1 项")
	h.a.goLocation(nasLocation(store.Servers[1].ID, 1, ""))
	h.settle()
	h.tt.Key(ui.Ctrl, ui.KeyV)
	h.waitStarted(2)
	h.waitTransfers()
	if b, _ := os.ReadFile(filepath.Join(f2.Root, "照片", "a.jpg")); string(b) != "photo" {
		t.Fatalf("move %q", b)
	}
	if _, err := os.Stat(filepath.Join(f.Trash, "照片", "a.jpg")); err != nil {
		t.Fatal("moved source not in the trash:", err)
	}
	if h.a.clip != nil {
		t.Error("a cut pasted stays on the clipboard")
	}
}

// Between this computer and a server, and among the folders of this
// computer, files go as they are copied, or cut, and pasted.
func TestCopyAndMoveBetweenComputerAndServer(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "照片"), 0755)
	os.WriteFile(filepath.Join(f.Root, "照片", "a.jpg"), []byte("photo"), 0644)
	os.WriteFile(filepath.Join(f.Root, "笔记.txt"), []byte("notes"), 0644)
	h := browserHarness(t, f)
	server := h.a.store.Servers[0].ID
	dest := t.TempDir()
	os.WriteFile(filepath.Join(dest, "笔记.txt"), []byte("old"), 0644)
	paste := func(loc string) {
		h.t.Helper()
		h.a.goLocation(loc)
		h.settle()
		h.tt.Key(ui.Ctrl, ui.KeyV)
		h.settle()
	}

	// Server → computer.
	h.clickPage("笔记.txt")
	h.tt.Key(ui.Ctrl, ui.KeyC)
	paste(localLocation(dest))
	h.want("合并同名目录并覆盖文件？")
	h.click("合并并覆盖")
	h.waitTransfers()
	if b, _ := os.ReadFile(filepath.Join(dest, "笔记.txt")); string(b) != "notes" {
		t.Fatalf("copy to computer %q", b)
	}
	h.a.goLocation(nasLocation(server, 1, ""))
	h.settle()
	h.clickPage("照片")
	h.tt.Key(ui.Ctrl, ui.KeyX)
	paste(localLocation(dest))
	h.waitTransfers()
	if b, _ := os.ReadFile(filepath.Join(dest, "照片", "a.jpg")); string(b) != "photo" {
		t.Fatalf("move to computer %q", b)
	}
	if _, err := os.Stat(filepath.Join(f.Trash, "照片", "a.jpg")); err != nil {
		t.Fatal("moved folder not in the server's trash:", err)
	}

	// Computer → server.
	os.WriteFile(filepath.Join(dest, "新.txt"), []byte("new"), 0644)
	h.a.openLocal(dest)
	h.settle()
	h.clickPage("新.txt")
	h.tt.Key(ui.Ctrl, ui.KeyC)
	paste(nasLocation(server, 1, ""))
	// Target checks run asynchronously before the transfer is registered.
	h.waitStarted(3)
	h.waitTransfers()
	if b, _ := os.ReadFile(filepath.Join(f.Root, "新.txt")); string(b) != "new" {
		t.Fatalf("copy to server %q", b)
	}
	if _, err := os.Stat(filepath.Join(dest, "新.txt")); err != nil {
		t.Fatal("copy removed the local file")
	}
	h.a.goLocation(localLocation(dest))
	h.settle()
	h.clickPage("照片")
	h.tt.Key(ui.Ctrl, ui.KeyX)
	paste(nasLocation(server, 1, ""))
	h.waitStarted(4)
	h.waitTransfers()
	if b, _ := os.ReadFile(filepath.Join(f.Root, "照片", "a.jpg")); string(b) != "photo" {
		t.Fatalf("move to server %q", b)
	}
	if _, err := os.Lstat(filepath.Join(dest, "照片")); !os.IsNotExist(err) {
		t.Fatal("moved folder still on the computer")
	}
	if b, _ := os.ReadFile(filepath.Join(os.Getenv("XDG_DATA_HOME"), "Trash", "files", "照片", "a.jpg")); string(b) != "photo" {
		t.Fatalf("local trash has %q", b)
	}
}

// Among the folders of this computer: a copy into its own folder takes a
// numbered name, a cut moves, a folder pasted into itself is refused.
func TestLocalCopyAndMove(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "子", "孙"), 0o755)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.openLocal(dir)
	h.settle()

	h.clickPage("a.txt")
	h.tt.Key(ui.Ctrl, ui.KeyC)
	h.tt.Key(ui.Ctrl, ui.KeyV)
	h.waitTransfers()
	h.settle()
	if b, _ := os.ReadFile(filepath.Join(dir, "a (2).txt")); string(b) != "a" {
		t.Fatalf("copy in place %q", b)
	}
	h.want("a (2).txt")

	// Cut, then open the destination and paste into the displayed folder.
	h.clickPage("a (2).txt")
	h.tt.Key(ui.Ctrl, ui.KeyX)
	h.a.openLocal(filepath.Join(dir, "子"))
	h.settle()
	h.menu("孙", "粘贴")
	h.waitTransfers()
	h.settle()
	if _, err := os.Stat(filepath.Join(dir, "子", "a (2).txt")); err != nil {
		t.Fatal("not moved:", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a (2).txt")); !os.IsNotExist(err) {
		t.Fatal("still where it was")
	}

	// A folder into itself.
	h.a.openLocal(dir)
	h.settle()
	h.clickPage("子")
	h.tt.Key(ui.Ctrl, ui.KeyC)
	h.a.openLocal(filepath.Join(dir, "子", "孙"))
	h.settle()
	h.tt.Key(ui.Ctrl, ui.KeyV)
	h.settle()
	if !strings.Contains(h.a.errorMsg, "到它自己里面") {
		t.Fatalf("pasted into itself: %q", h.a.errorMsg)
	}
}

// New folders, renaming and deleting, on this computer as on a server.
func TestLocalFileOperations(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.openLocal(dir)
	h.settle()
	h.tt.Key(ui.Ctrl|ui.Shift, ui.KeyN)
	h.settle()
	h.tt.Key(ui.Ctrl, ui.KeyA)
	h.tt.Type("新目录")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if fi, err := os.Stat(filepath.Join(dir, "新目录")); err != nil || !fi.IsDir() {
		t.Fatal("no folder:", err)
	}
	h.clickPage("a.txt")
	h.tt.Key(0, ui.KeyF2)
	h.settle()
	h.tt.Key(ui.Ctrl, ui.KeyA)
	h.tt.Type("b.txt")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal("not renamed:", err)
	}
	h.clickPage("b.txt")
	h.tt.Key(0, ui.KeyDelete)
	h.click("移到回收站")
	h.settle()
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); !os.IsNotExist(err) {
		t.Fatal("not deleted")
	}
	if b, _ := os.ReadFile(filepath.Join(os.Getenv("XDG_DATA_HOME"), "Trash", "files", "b.txt")); string(b) != "a" {
		t.Fatalf("trash has %q", b)
	}
}

// Files another app copied, or cut, paste here; files of this computer
// copied here paste there.
func TestSystemClipboard(t *testing.T) {
	f := fitest.New(t)
	h := browserHarness(t, f)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "外部.txt"), []byte("ext"), 0o644)
	data, _ := systemFiles([]string{filepath.Join(src, "外部.txt")}, false)
	h.tt.SetClipboardData(data)
	h.tt.Key(ui.Ctrl, ui.KeyV)
	h.waitStarted(1)
	h.waitTransfers()
	if b, _ := os.ReadFile(filepath.Join(f.Root, "外部.txt")); string(b) != "ext" {
		t.Fatalf("pasted %q", b)
	}

	// Copied here: on the system's clipboard as a file manager reads it.
	h.a.openLocal(src)
	h.settle()
	h.clickPage("外部.txt")
	h.tt.Key(ui.Ctrl, ui.KeyC)
	files, err := h.tt.ClipboardData().Files()
	if err != nil || len(files) != 1 || files[0] != filepath.Join(src, "外部.txt") {
		t.Fatalf("system clipboard %v, %v", files, err)
	}
}

// The properties of an entry of a server come from it; of a folder, with
// what its index holds.
func TestServerProps(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "项目", "子目录"), 0755)
	os.WriteFile(filepath.Join(f.Root, "项目", "报告.txt"), []byte("report"), 0644)
	os.WriteFile(filepath.Join(f.Root, "项目", "子目录", "b.bin"), make([]byte, 2048), 0644)
	h := browserHarness(t, f)
	h.menu("文件夹 项目", "属性")
	h.settle()
	h.want("项目 的属性")
	h.want("2,054 字节")
	h.want("2 个文件，1 个文件夹")
	h.want("nas:users（1000:1000）")
	h.click("关闭")
	if h.a.props != nil {
		t.Fatal("props stayed open")
	}
	h.click("文件夹 项目")
	h.click("报告.txt")
	h.menu("报告.txt", "属性")
	h.settle()
	h.want("TXT 文件")
	h.want("6 字节")
	h.click("关闭")

	// the folder shown
	h.more("文件夹属性")
	h.settle()
	h.want("项目 的属性")
	h.want("2 个文件，1 个文件夹")
	h.click("关闭")

	// an older server: what the list told
	f.Props404.Store(true)
	h.menu("报告.txt", "属性")
	h.settle()
	h.want("服务器版本较旧，只显示基本信息")
	h.want("6 字节")
}

// The file-page menu always targets the open directory, regardless of
// whether the right-clicked row is a directory, a file, or empty space.
func TestPasteMenuUsesShownDirectory(t *testing.T) {
	for _, remote := range []bool{false, true} {
		for _, target := range []string{"child", "file.txt"} {
			t.Run(fmt.Sprintf("remote=%v/%s", remote, target), func(t *testing.T) {
				var h *harness
				var dir string
				if remote {
					f := fitest.New(t)
					dir = f.Root
					os.MkdirAll(filepath.Join(dir, "source"), 0o755)
					os.MkdirAll(filepath.Join(dir, "dest", "child"), 0o755)
					os.WriteFile(filepath.Join(dir, "dest", "file.txt"), nil, 0o644)
					h = browserHarness(t, f)
				} else {
					dir = t.TempDir()
					os.MkdirAll(filepath.Join(dir, "source"), 0o755)
					os.MkdirAll(filepath.Join(dir, "dest", "child"), 0o755)
					os.WriteFile(filepath.Join(dir, "dest", "file.txt"), nil, 0o644)
					h = newHarness(t, &config.Store{Dir: t.TempDir()})
					h.a.openLocal(dir)
					h.settle()
				}
				h.clickPage("source")
				h.tt.Key(ui.Ctrl, ui.KeyX)
				if remote {
					h.a.browse("dest")
				} else {
					h.a.openLocal(filepath.Join(dir, "dest"))
				}
				h.settle()
				for _, sv := range h.a.store.Servers {
					*h.a.sectionOpen("server/" + sv.ID) = false
				}
				h.tt.Frame()
				if err := h.tt.RightClick(target); err != nil {
					t.Fatal(err)
				}
				if menu := h.tt.Menu(); slices.Contains(menu, "粘贴到此文件夹") || !slices.Contains(menu, "粘贴") {
					t.Fatalf("unexpected paste menu: %v", menu)
				}
				if err := h.tt.ChooseMenuItem("粘贴"); err != nil {
					t.Fatal(err)
				}
				h.settle()
				h.waitTransfers()
				if _, err := os.Stat(filepath.Join(dir, "dest", "source")); err != nil {
					t.Fatalf("paste did not target displayed directory: %v", err)
				}
				if _, err := os.Stat(filepath.Join(dir, "source")); !os.IsNotExist(err) {
					t.Fatal("source was not moved")
				}
			})
		}
	}
}

func TestRejectedLocalPasteKeepsCut(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "source", "child"), 0o755)
	os.MkdirAll(filepath.Join(dir, "dest"), 0o755)
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.openLocal(dir)
	h.settle()
	h.clickPage("source")
	h.tt.Key(ui.Ctrl, ui.KeyX)
	cl := h.a.clip
	for _, dest := range []string{dir, filepath.Join(dir, "source", "child")} {
		h.a.openLocal(dest)
		h.settle()
		h.tt.Key(ui.Ctrl, ui.KeyV)
		h.settle()
		if h.a.clip != cl || h.a.errorMsg == "" {
			t.Fatalf("rejected paste lost cut state or feedback: %q", h.a.errorMsg)
		}
	}
	h.a.openLocal(filepath.Join(dir, "dest"))
	h.settle()
	h.tt.Key(ui.Ctrl, ui.KeyV)
	h.waitTransfers()
	if _, err := os.Stat(filepath.Join(dir, "dest", "source", "child")); err != nil {
		t.Fatal(err)
	}
}
