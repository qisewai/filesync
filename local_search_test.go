package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"filesync/config"
	"filesync/localindex"
	"github.com/egoist/mygo/ui"
)

func localSearchHarness(t *testing.T, root string) *harness {
	t.Helper()
	h := newHarness(t, &config.Store{Dir: t.TempDir(), LocalIndex: &config.LocalIndexSettings{Roots: []string{root}, Excludes: []string{".cache/"}}})
	h.a.localRoots = []localRoot{{"测试文件夹", root}}
	h.a.openLocal(root)
	h.settle()
	return h
}

// indexMenu chooses an item of the index's menu, in the status bar.
func (h *harness) indexMenu(item string) {
	h.t.Helper()
	if err := h.tt.Click("本机索引"); err != nil {
		h.t.Fatalf("%v; texts %q", err, h.tt.Texts())
	}
	if err := h.tt.ChooseMenuItem(item); err != nil {
		h.t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.settle()
}

// clearLocalSearch clears the search, with Escape, back to the directory.
func (h *harness) clearLocalSearch() {
	h.t.Helper()
	h.click("本机搜索")
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	h.settle()
}

func localWrite(t *testing.T, root, name string, n int) string {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
func (h *harness) captureLocal(name string) {
	h.t.Helper()
	dir := os.Getenv("FILESYNC_LOCAL_SCREENSHOT_DIR")
	if dir == "" {
		return
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, h.tt.Image()); err != nil {
		h.t.Fatal(err)
	}
}

func TestLocalSearchManualWorkflow(t *testing.T) {
	root := t.TempDir()
	localWrite(t, root, "文档/项目 报告2.pdf", 2)
	localWrite(t, root, "文档/项目 报告10.pdf", 10)
	localWrite(t, root, "图片/项目.jpg", 4)
	localWrite(t, root, ".cache/项目.txt", 3)
	h := localSearchHarness(t, root)
	b, m := h.a.local, h.a.localIdx
	if m.running || m.index.Status().Count != 0 {
		t.Fatal("opening a local page started a scan")
	}
	h.replaceField("本机搜索", "项目")
	if !b.results || b.total != 0 {
		t.Fatal("unbuilt index should be empty")
	}
	h.indexMenu("更新索引")
	h.want("本机索引已更新")
	h.want("位置")
	if b.total != 3 || len(b.items) != 3 || m.status.Count != 5 {
		t.Fatalf("total %d, index %+v", b.total, m.status)
	}
	h.captureLocal("search")
	h.replaceField("扩展名", "PDF")
	if b.total != 2 || b.items[0].Name != "项目 报告2.pdf" {
		t.Fatalf("filter %+v", b.items)
	}
	h.replaceField("本机搜索", "项目 10")
	if b.total != 1 {
		t.Fatalf("AND: %d", b.total)
	}
	localWrite(t, root, "新项目10.pdf", 8)
	h.more("刷新")
	if b.total != 1 {
		t.Fatal("refresh changed index without manual scan")
	}
	h.indexMenu("更新索引")
	if b.total != 2 {
		t.Fatalf("manual scan total %d", b.total)
	}
	h.clearLocalSearch()
	if b.results || b.query != "" {
		t.Fatal("return did not exit search")
	}
	h.want("新项目10.pdf")
}

func TestLocalSearchDebounceNavigationAndOpen(t *testing.T) {
	root := t.TempDir()
	path := localWrite(t, root, "文档/hello.txt", 1)
	localWrite(t, root, "另一处/other.txt", 2)
	h := localSearchHarness(t, root)
	h.indexMenu("更新索引")
	b := h.a.local
	h.typeInto("本机搜索", "hello")
	if !b.queryPending {
		t.Fatal("typing did not debounce search")
	}
	h.settle()
	if b.total != 1 {
		t.Fatalf("typing result %d", b.total)
	}
	// A pending query must never replace a directory opened in the meantime.
	h.typeInto("本机搜索", "obsolete")
	h.a.openLocal(filepath.Join(root, "文档"))
	h.settle()
	time.Sleep(110 * time.Millisecond)
	h.settle()
	if b.results || b.dir != filepath.Join(root, "文档") || len(b.items) != 1 {
		t.Fatal("stale search replaced directory")
	}
	h.replaceField("本机搜索", "hello")
	opened := ""
	h.a.openPath = func(p string) { opened = p }
	h.click("hello.txt")
	h.localMenu("在文件管理器中显示")
	if opened != filepath.Dir(path) {
		t.Fatalf("location %q", opened)
	}
	h.a.openIndexedLocal(b.items[0], false, false)
	h.settle()
	if opened != path {
		t.Fatalf("open %q", opened)
	}
	h.a.openIndexedLocal(b.items[0], false, true)
	h.settle()
	if b.results || !b.sel.Has(path) {
		t.Fatal("open containing folder lost selection")
	}
	h.replaceField("本机搜索", "hello")
	os.Remove(path)
	opened = ""
	h.a.openIndexedLocal(b.items[0], false, false)
	h.settle()
	if opened != "" || !strings.Contains(b.err, "已移动、删除") {
		t.Fatalf("stale entry: %q, %q", opened, b.err)
	}
}

func TestLocalSearchPagesAndGlobalSort(t *testing.T) {
	root := t.TempDir()
	const n = 1503
	for i := range n {
		localWrite(t, root, fmt.Sprintf("match-%04d.txt", i), n-i)
	}
	h := localSearchHarness(t, root)
	h.indexMenu("更新索引")
	h.replaceField("本机搜索", "match")
	b := h.a.local
	if b.total != n || len(b.items) != localPageSize || b.items[0].Name != "match-0000.txt" {
		t.Fatalf("first %d/%d", len(b.items), b.total)
	}
	b.list.ScrollTo(localPageSize-1, ui.End)
	h.tt.Frame()
	h.settle()
	if len(b.items) != n || b.items[n-1].Name != "match-1502.txt" {
		t.Fatal("scroll did not append next page")
	}
	h.click(colSize)
	if len(b.items) != localPageSize || b.items[0].Size != 1 || b.items[0].Name != "match-1502.txt" {
		t.Fatalf("global size order: first %+v, shown %d", b.items[0], len(b.items))
	}
	h.click(colSize)
	if b.items[0].Size != n || b.items[0].Name != "match-0000.txt" {
		t.Fatal("descending global order")
	}
	b.here = true
	b.filter.kind = "文档"
	h.a.queueLocalSearch(0)
	h.settle()
	if b.total != n {
		t.Fatalf("scoped type result %d", b.total)
	}
}

func TestLocalIndexSettingsAndRestart(t *testing.T) {
	root := t.TempDir()
	localWrite(t, root, "hello.txt", 1)
	h := localSearchHarness(t, root)
	h.indexMenu("更新索引")
	x := h.a.localIdx.index
	h.indexMenu("索引范围…")
	h.captureLocal("settings")
	h.tt.SetSize(780, 520)
	h.captureLocal("settings-small")
	h.tt.SetSize(980, 680)
	h.click("保存索引设置")
	if h.a.localIdx.index != x {
		t.Fatal("unchanged settings discarded the index")
	}
	h.indexMenu("索引范围…")
	picked := "relative"
	h.a.pickDir = func(string, string) (string, error) { return picked, nil }
	h.click("添加文件夹…")
	h.click("保存索引设置")
	if !strings.Contains(h.a.localIdx.draft.err, "绝对路径") {
		t.Fatal("relative root saved")
	}
	h.click("移除 relative")
	picked = filepath.Join(root, "nested")
	h.click("添加文件夹…")
	h.replaceField("本机索引排除规则", "*.txt")
	h.click("保存索引设置")
	if h.a.localIdx.status.Count != 0 || !slices.Equal(h.a.store.LocalIndex.Roots, []string{root}) {
		t.Fatal("changed scope did not invalidate old index")
	}
	h.indexMenu("更新索引")
	if h.a.localIdx.status.Count != 0 {
		t.Fatal("excludes not used")
	}
	loaded, err := config.Load(h.a.store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	a := newApp(loaded)
	a.ensureLocalIndex()
	if a.localIdx.running || a.localIdx.index.Status().Count != 0 || !slices.Equal(a.localIdx.settings.Excludes, []string{"*.txt"}) {
		t.Fatal("restart must retain settings without scanning")
	}
}

func TestLocalSearchBackupsAcrossParents(t *testing.T) {
	root := t.TempDir()
	p1 := localWrite(t, root, "a/same.txt", 1)
	p2 := localWrite(t, root, "b/same.txt", 2)
	h := localSearchHarness(t, root)
	h.a.store.Servers = []*config.Server{{ID: "server", Name: "NAS", Addr: "unused"}}
	h.indexMenu("更新索引")
	h.replaceField("本机搜索", "same.txt")
	b := h.a.local
	b.sel.Add(p1)
	b.sel.Add(p2)
	h.tt.Frame()
	h.localMenu("创建备份任务…")
	if h.a.draft.source != filepath.Dir(p1) || !slices.Equal(h.a.draft.items, []string{"same.txt"}) || len(h.a.localBackups) != 1 {
		t.Fatalf("first %+v", h.a.draft)
	}
	h.a.draft.server, h.a.draft.root, h.a.draft.rootName = "server", 1, "target"
	h.click("保存")
	if !h.a.editing || h.a.draft.source != filepath.Dir(p2) || len(h.a.localBackups) != 0 {
		t.Fatalf("next %+v", h.a.draft)
	}
	h.click("保存")
	if h.a.editing || len(h.a.store.Tasks) != 2 || h.a.store.Tasks[0].Source == h.a.store.Tasks[1].Source {
		t.Fatalf("tasks %+v", h.a.store.Tasks)
	}
	for _, task := range h.a.store.Tasks {
		if !slices.Equal(task.Items, []string{"same.txt"}) || task.WithDir {
			t.Fatalf("task %+v", task)
		}
	}
	h.a.openLocal(root)
	h.settle()
	h.replaceField("本机搜索", "same.txt")
	b.sel.Add(p1)
	b.sel.Add(p2)
	h.tt.Frame()
	h.localMenu("创建备份任务…")
	h.click("取消")
	if len(h.a.localBackups) != 0 {
		t.Fatal("canceled group still queued")
	}
}

func TestLocalIndexCanceledProgress(t *testing.T) {
	root := t.TempDir()
	for i := range 600 {
		localWrite(t, root, fmt.Sprintf("file%d.txt", i), 1)
	}
	h := localSearchHarness(t, root)
	// Render the running controls, then cancel an actual background request
	// and verify that the next manual request can complete normally.
	m := h.a.localIdx
	m.running = true
	m.note = ""
	canceled := false
	m.cancel = func() { canceled = true; m.running = false }
	h.tt.Frame()
	h.want("取消扫描")
	h.click("取消扫描")
	if !canceled {
		t.Fatal("cancel button did not request cancellation")
	}
	h.a.startLocalScan()
	m.cancel()
	h.settle()
	h.want("扫描已取消，已建立的索引仍可搜索")
	if m.running {
		t.Fatal("scan remained running")
	}
	h.indexMenu("更新索引")
	if m.status.Count != 600 {
		t.Fatalf("rescan %+v", m.status)
	}
	// A changed scope no longer exposes results from the former index.
	m.index = localindex.New()
	h.replaceField("本机搜索", "file")
	if h.a.local.total != 0 {
		t.Fatal("old snapshot survived scope change")
	}
}

func TestExplicitLocalMountScope(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux mount paths")
	}
	if skips := indexSkipPaths([]string{"/"}); !slices.Contains(skips, "/run") || !slices.Contains(skips, "/tmp") {
		t.Fatalf("broad scope exclusions %q", skips)
	}
	if skips := indexSkipPaths([]string{"/run/media/me/disk", t.TempDir()}); slices.Contains(skips, "/run") || slices.Contains(skips, "/tmp") || !slices.Contains(skips, "/proc") {
		t.Fatalf("explicit disk or temporary scope rejected: %q", skips)
	}
}
