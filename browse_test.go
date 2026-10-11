package main

import (
	"context"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filesync/config"
	"filesync/fileindex"
	"filesync/fileindex/fitest"
	"github.com/egoist/mygo/ui"
)

func browserHarness(t *testing.T, f *fitest.Server) *harness {
	h := newHarness(t, signedIn(t, f))
	h.settle()
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

func TestNativeBrowserTreeSearchAndFileOperations(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "项目", "子目录"), 0755)
	os.WriteFile(filepath.Join(f.Root, "项目", "报告.txt"), []byte("report"), 0644)
	os.WriteFile(filepath.Join(f.Root, "项目", "其他.txt"), []byte("other"), 0644)
	h := browserHarness(t, f)
	h.click("文件夹 项目")
	h.want("报告.txt")
	h.click("新建文件夹")
	h.tt.Type("新目录")
	h.click("创建")
	h.want("文件夹 新目录")
	h.click("报告.txt")
	h.click("重命名")
	h.tt.Key(ui.Ctrl, ui.KeyA)
	h.tt.Type("计划.txt")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	h.want("计划.txt")
	h.typeInto("搜索", "计划")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if !h.a.br.results || len(h.a.br.items) != 1 {
		t.Fatalf("search %+v", h.a.br.items)
	}
	h.a.reloadKeeping()
	h.settle()
	if !h.a.br.results || len(h.a.br.items) != 1 {
		t.Fatal("refresh replaced search with directory contents")
	}
	h.click("网格")
	if !h.a.br.grid {
		t.Fatal("grid did not stay selected")
	}
	h.tt.Frame()
	h.click("计划.txt")
	h.click("删除")
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
	h.click("下载…")
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
	h.click("笔记.txt")
	h.click("复制到…")
	h.click("打开")
	for i, name := range h.a.nas.dirs {
		if name == "目标" {
			h.a.nas.sel = i
		}
	}
	h.tt.Frame()
	h.click("选择“目标”")
	h.waitTransfers()
	if b, err := os.ReadFile(filepath.Join(f.Root, "目标", "笔记.txt")); err != nil || string(b) != "notes" {
		t.Fatalf("copy %q, %v", b, err)
	}
	h.click("传输任务")
	h.want("上传")
	h.want("下载")
	h.want("复制")
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
	h.click("网格")
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
	h.click("新建文件夹")
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
