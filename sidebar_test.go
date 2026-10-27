package main

import (
	"testing"

	"filesync/config"
	"filesync/fileindex/fitest"
	"github.com/egoist/mygo/ui"
)

func TestHomeSidebarEntryAndHistory(t *testing.T) {
	f := fitest.New(t)
	h := browserHarness(t, f)
	before := h.a.location()
	h.click("首页")
	if h.a.place != "home" || h.a.location() != "/" || h.a.pageTitle() != "首页" {
		t.Fatalf("home: %q, %q", h.a.place, h.a.location())
	}
	// The places of the sidebar, as cards.
	h.want("打开 搜索")
	h.want("打开 备份任务")
	h.want("打开 nas")
	h.click("打开 nas")
	if !h.a.onRoot() || h.a.br.root.Name != "nas" {
		t.Fatalf("the card went to %q", h.a.place)
	}
	h.tt.Key(ui.Alt, ui.KeyLeft)
	h.settle()
	h.tt.Key(ui.Alt, ui.KeyLeft)
	h.settle()
	if h.a.location() != before {
		t.Fatalf("back: %q, want %q", h.a.location(), before)
	}
	h.tt.Key(ui.Alt, ui.KeyRight)
	h.settle()
	if h.a.place != "home" {
		t.Fatalf("forward: %q", h.a.place)
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
	if !h.a.sidebarHidden || h.tt.HasText("首页") || h.tt.HasText("隐藏侧边栏") {
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
