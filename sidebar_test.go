package main

import (
	"os"
	"path/filepath"
	"testing"

	"filesync/config"
	"filesync/fileindex/fitest"
	"github.com/egoist/mygo/ui"
)

// The app starts on the search, its field in the middle and ready for
// typing; a search moves it to the top, over the results.
func TestStartsOnTheSearch(t *testing.T) {
	f := fitest.New(t)
	os.WriteFile(filepath.Join(f.Root, "报告.txt"), []byte("x"), 0o644)
	h := newHarness(t, signedIn(t, f))
	h.settle()
	if !h.a.onSearch() || !h.a.searchLanding() {
		t.Fatalf("started on %q", h.a.location())
	}
	h.want("一个搜索框，找遍本机和所有 NAS")
	h.want("在 家里 中搜索")
	if h.tt.HasText("首页") || h.tt.HasText("隐藏详情") {
		t.Fatal("a home page, or the details, beside the search")
	}
	field, _ := h.tt.Find("全局搜索")
	h.tt.Type("报告")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if h.a.searchLanding() || h.a.gsearch.query != "报告" {
		t.Fatalf("typing went elsewhere: %q", h.a.gsearch.query)
	}
	h.want("报告.txt")
	if top, _ := h.tt.Find("全局搜索"); top.Y >= field.Y {
		t.Fatalf("the field stayed at %v, from %v", top.Y, field.Y)
	}
	// Cleared, the search is the first page again.
	h.click("清除搜索")
	if !h.a.searchLanding() {
		t.Fatalf("cleared, the results stay: %q %v", h.a.gsearch.query, h.a.gsearch.filter)
	}
}

func TestSidebarHidesRestoresAndPersists(t *testing.T) {
	f := fitest.New(t)
	h := browserHarness(t, f)
	h.a.sidebarW = 300
	h.a.store.SidebarWidth = 300
	h.tt.Frame()
	before := h.a.location()
	search, _ := h.tt.Find("搜索文件")
	h.click("隐藏侧边栏")
	for range 3 {
		h.tt.Frame()
	}
	if !h.a.sidebarHidden || h.tt.HasText("过滤侧边栏") || h.tt.HasText("隐藏侧边栏") {
		t.Fatalf("sidebar still present (hidden=%v): %q", h.a.sidebarHidden, h.tt.Texts())
	}
	wide, ok := h.tt.Find("搜索文件")
	if !ok || wide.X >= search.X {
		t.Fatalf("page did not expand: before %+v, after %+v", search, wide)
	}
	if h.a.location() != before || h.a.sidebarW != 300 {
		t.Fatal("hiding changed the location or remembered width")
	}
	store, err := config.Load(h.a.store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	restarted := newHarness(t, store)
	restarted.settle()
	if !restarted.a.sidebarHidden || restarted.tt.HasText("隐藏侧边栏") || restarted.a.sidebarW != 300 {
		t.Fatal("sidebar was not hidden after restart")
	}
	restarted.want("显示侧边栏")
	h.tt.Key(ui.Ctrl, ui.KeyB)
	h.settle()
	for range 3 {
		h.tt.Frame()
	}
	if h.a.sidebarHidden || !h.tt.HasText("隐藏侧边栏") || h.a.sidebarW != 300 || h.a.location() != before {
		t.Fatal("sidebar did not restore the width and page")
	}
	store, err = config.Load(h.a.store.Dir)
	if err != nil || store.SidebarHidden {
		t.Fatalf("visible state was not saved: %+v, %v", store, err)
	}
}

func TestCollapsedSidebarKeepsClearOfTitleBarControls(t *testing.T) {
	f := fitest.New(t)
	h := browserHarness(t, f)
	h.a.hiddenTitleBar = true
	h.tt.SetTitleBar(ui.TitleBar{Height: 60, Left: 90, Right: 120})
	h.settle()
	h.click("隐藏侧边栏")
	button, ok := h.tt.Find("显示侧边栏")
	if !ok || button.X < 90 {
		t.Fatalf("toggle under the left window controls: %+v", button)
	}
	// The tab bar is the title bar: its buttons keep clear of the controls
	// on the right, as high as them.
	for _, label := range []string{"新建标签页", "显示详情", "隐藏详情"} {
		r, ok := h.tt.Find(label)
		if !ok {
			continue
		}
		if r.X+r.W > 980-120 || r.Y+r.H > 60 {
			t.Fatalf("%s under the window controls: %+v", label, r)
		}
	}
}
