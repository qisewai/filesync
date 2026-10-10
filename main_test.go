package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"filesync/config"
	"filesync/engine"

	"github.com/egoist/mygo/ui"
)

// harness runs the app's view without a window. Work that background
// goroutines post waits in a queue until the test runs it, on its own
// goroutine as the main thread would.
type harness struct {
	t     *testing.T
	a     *app
	tt    *ui.Tester
	queue chan func()
}

func newHarness(t *testing.T, store *config.Store) *harness {
	h := &harness{t: t, a: newApp(store), queue: make(chan func(), 1024)}
	h.a.post = func(fn func()) { h.queue <- fn }
	h.tt = ui.NewTester(h.a.view, 980, 680)
	return h
}

// settle runs posted work until no scan or backup is running.
func (h *harness) settle() {
	h.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case fn := <-h.queue:
			fn()
			h.tt.Frame()
		case <-deadline:
			h.t.Fatal("the job did not finish")
		default:
			if !h.a.busy() {
				h.tt.Frame()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func (h *harness) click(label string) {
	h.t.Helper()
	if err := h.tt.Click(label); err != nil {
		h.t.Fatalf("%v; texts %q", err, h.tt.Texts())
	}
}

func (h *harness) want(text string) {
	h.t.Helper()
	if !h.tt.HasText(text) {
		h.t.Fatalf("no %q in %q", text, h.tt.Texts())
	}
}

func TestCreateTaskPreviewAndBackUp(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(src, "photo.jpg"), []byte("jpeg"), 0o644)
	os.MkdirAll(filepath.Join(src, "docs"), 0o755)
	os.WriteFile(filepath.Join(src, "docs", "report.txt"), []byte("report"), 0o644)
	os.WriteFile(filepath.Join(src, "Thumbs.db"), []byte("excluded by default"), 0o644)

	store := &config.Store{Dir: t.TempDir()}
	h := newHarness(t, store)
	h.want("还没有备份任务")
	h.click("新建任务")
	h.want("新建任务")

	h.click("源目录")
	h.tt.Type(src)
	h.click("目标目录")
	h.tt.Type(dst)
	h.click("保存")
	if len(store.Tasks) != 1 || store.Tasks[0].Source != src || store.Tasks[0].Target != dst {
		t.Fatalf("tasks %+v", store.Tasks)
	}
	if _, err := os.Stat(filepath.Join(store.Dir, "tasks.json")); err != nil {
		t.Fatal("tasks not saved:", err)
	}
	h.want("备份任务 1")

	h.click("预览并备份…")
	h.settle()
	h.want("预览：备份任务 1")
	h.want("photo.jpg")
	h.want("docs/report.txt")
	if h.tt.HasText("Thumbs.db") {
		t.Error("an excluded file is planned")
	}

	h.click("开始备份")
	h.settle()
	h.want("备份完成")
	if b, err := os.ReadFile(filepath.Join(dst, "docs", "report.txt")); err != nil || string(b) != "report" {
		t.Fatalf("report.txt: %q, %v", b, err)
	}
	run := store.Tasks[0].History
	if len(run) != 1 || run[0].Status != config.StatusOK || run[0].Copied != 2 || run[0].Log == "" {
		t.Fatalf("history %+v", run)
	}
	if _, err := os.Stat(run[0].Log); err != nil {
		t.Fatal("no log:", err)
	}

	h.click("完成")
	h.want("成功")

	// A second preview finds nothing to do.
	h.click("预览并备份…")
	h.settle()
	h.want("目标已是最新")
}

func TestEditorValidates(t *testing.T) {
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.click("新建任务")
	h.click("保存")
	h.want("请选择源目录。")
	h.want("请选择目标目录。")
	if len(h.a.store.Tasks) != 0 {
		t.Error("an invalid task was saved")
	}
	h.click("取消")
	h.want("还没有备份任务")
}

func TestMirrorAsksBeforeMassDelete(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(src, "only"), []byte("1"), 0o644)
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} {
		os.WriteFile(filepath.Join(dst, name), []byte("x"), 0o644)
	}
	store := &config.Store{Dir: t.TempDir()}
	store.Add(&config.Task{Name: "镜像", Source: src, Target: dst, Mode: engine.Mirror})
	h := newHarness(t, store)

	h.click("预览并备份…")
	h.settle()
	h.click("开始备份")
	h.want("确定要删除这么多文件吗？")
	h.click("取消")
	if _, err := os.Stat(filepath.Join(dst, "a")); err != nil {
		t.Fatal("deleted although canceled")
	}

	h.click("开始备份")
	h.click("仍然备份")
	h.settle()
	h.want("备份完成")
	entries, _ := os.ReadDir(dst)
	if len(entries) != 1 || entries[0].Name() != "only" {
		t.Errorf("target has %v", entries)
	}
}

func TestRemoveTaskKeepsFiles(t *testing.T) {
	src := t.TempDir()
	store := &config.Store{Dir: t.TempDir()}
	store.Add(&config.Task{Name: "要删除的任务", Source: src, Target: t.TempDir()})
	h := newHarness(t, store)
	h.click("删除任务")
	h.want("删除任务“要删除的任务”？")
	h.click("删除")
	if len(store.Tasks) != 0 {
		t.Fatal("task kept")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("source gone")
	}
	h.want("还没有备份任务")
}
