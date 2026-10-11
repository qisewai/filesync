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

// find searches q from the field of the toolbar of the page shown, on the
// page of the search, and returns the results of the place shown.
func (h *harness) find(q string) *searchGroup {
	h.t.Helper()
	label := "全局搜索"
	switch {
	case h.a.onLocal():
		label = "本机搜索"
	case h.a.onRoot():
		label = "搜索文件"
	}
	if label == "全局搜索" {
		h.replaceField(label, q)
	} else {
		h.typeInto(label, q)
	}
	h.settle()
	s := h.a.gsearch
	if s == nil || !h.a.onSearch() {
		h.t.Fatalf("no search: %q", h.a.location())
	}
	g := s.group(s.scope)
	if g == nil {
		h.t.Fatalf("no results of %q in %+v", s.scope, s.groups)
	}
	return g
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
	m := h.a.localIdx
	if m.running || m.index.Status().Count != 0 {
		t.Fatal("opening a local page started a scan")
	}
	// The search of this computer, from its page, before the index.
	g := h.find("项目")
	if h.a.gsearch.scope != localPlace || g.total != 0 {
		t.Fatalf("unbuilt index: scope %q, %d", h.a.gsearch.scope, g.total)
	}
	h.want("本机还没有建立索引，搜不到本机的文件。建立索引只读取文件名、大小和修改时间。")
	h.click("建立索引")
	s := h.a.gsearch
	g = s.group(localPlace)
	if g.total != 3 || len(s.items) != 3 || m.status.Count != 5 {
		t.Fatalf("total %d, index %+v", g.total, m.status)
	}
	h.want("位置")
	h.captureLocal("search")
	h.replaceField("扩展名", "PDF")
	if g = s.group(localPlace); g.total != 2 || s.items[0].Name != "项目 报告2.pdf" {
		t.Fatalf("filter %+v", s.items)
	}
	h.replaceField("全局搜索", "项目 10")
	if g = s.group(localPlace); g.total != 1 {
		t.Fatalf("AND: %d", g.total)
	}
	// The index changes only as it is updated, and the search follows.
	localWrite(t, root, "新项目10.pdf", 8)
	h.a.runSearch(0)
	h.settle()
	if g = s.group(localPlace); g.total != 1 {
		t.Fatal("searched again without an update of the index")
	}
	h.a.startLocalScan()
	h.settle()
	if g = s.group(localPlace); g.total != 2 {
		t.Fatalf("after the update: %d", g.total)
	}
	// Back is the folder the search began in.
	h.tt.Key(ui.Alt, ui.KeyLeft)
	h.settle()
	if !h.a.onLocal() {
		t.Fatalf("back: %q", h.a.location())
	}
	h.want("新项目10.pdf")
}

func TestLocalSearchOpen(t *testing.T) {
	root := t.TempDir()
	path := localWrite(t, root, "文档/hello.txt", 1)
	localWrite(t, root, "另一处/other.txt", 2)
	h := localSearchHarness(t, root)
	h.indexMenu("更新索引")
	if g := h.find("hello"); g.total != 1 {
		t.Fatalf("typing result %d", g.total)
	}
	s := h.a.gsearch
	opened := ""
	h.a.openPath = func(p string) { opened = p }
	h.a.openHit(s.hits[0], false)
	h.settle()
	if opened != path {
		t.Fatalf("open %q", opened)
	}
	// Its folder, with it chosen; and the folder in the file manager.
	h.a.openHit(s.hits[0], true)
	h.settle()
	b := h.a.local
	if !h.a.onLocal() || b.dir != filepath.Dir(path) || !b.sel.Has(path) {
		t.Fatal("open containing folder lost selection")
	}
	h.localMenu("在文件管理器中显示")
	if opened != filepath.Dir(path) {
		t.Fatalf("location %q", opened)
	}
}

func TestLocalSearchPagesAndGlobalSort(t *testing.T) {
	root := t.TempDir()
	const n = 503
	for i := range n {
		localWrite(t, root, fmt.Sprintf("match-%04d.txt", i), n-i)
	}
	h := localSearchHarness(t, root)
	h.indexMenu("更新索引")
	g := h.find("match")
	s := h.a.gsearch
	if g.total != n || len(s.items) != searchPageSize || s.items[0].Name != "match-0000.txt" {
		t.Fatalf("first %d/%d", len(s.items), g.total)
	}
	for len(s.items) < n {
		shown := len(s.items)
		s.list.ScrollTo(shown-1, ui.End)
		h.tt.Frame()
		h.settle()
		if len(s.items) == shown {
			t.Fatalf("scroll did not append the next page: %d", shown)
		}
	}
	if s.items[n-1].Name != "match-0502.txt" {
		t.Fatal("pages out of order")
	}
	h.click(colSize)
	if len(s.items) != searchPageSize || s.items[0].Size != 1 || s.items[0].Name != "match-0502.txt" {
		t.Fatalf("global size order: first %+v, shown %d", s.items[0], len(s.items))
	}
	h.click(colSize)
	if s.items[0].Size != n || s.items[0].Name != "match-0000.txt" {
		t.Fatal("descending global order")
	}
	// The folder the search began in, of documents.
	s.filter.kind = "文档"
	h.a.runSearch(0)
	h.settle()
	h.click("搜索范围 此文件夹 · " + filepath.Base(root))
	if g := s.group("folder"); s.scope != "folder" || g.total != n {
		t.Fatalf("scoped type result %+v", g)
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
	h.find("same.txt")
	choose := func() {
		s := h.a.gsearch
		for _, hit := range s.hits {
			s.sel.Add(hit.key())
		}
		h.tt.Frame()
	}
	choose()
	h.searchMenu("创建备份任务…")
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
	// Saved, the tasks show; the search is still there to go back to.
	h.a.nav.Back()
	h.settle()
	choose()
	h.searchMenu("创建备份任务…")
	h.click("取消")
	if len(h.a.localBackups) != 0 {
		t.Fatal("canceled group still queued")
	}
}

// searchMenu chooses an item of the menu of the results of the search.
func (h *harness) searchMenu(item string) {
	h.t.Helper()
	h.tt.Frame()
	r, ok := h.tt.Find("搜索结果")
	if !ok {
		h.t.Fatalf("no results in %q", h.tt.Texts())
	}
	h.tt.RightClickAt(r.X+r.W/2, r.Y+r.H-8)
	if err := h.tt.ChooseMenuItem(item); err != nil {
		h.t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.settle()
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
	if g := h.find("file"); g.total != 0 {
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
