package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"filemanager/remoteclient/fitest"

	"github.com/egoist/mygo/ui"
)

func tabsFixture(t *testing.T) (*fitest.Server, *harness) {
	f := fitest.New(t)
	for _, d := range []string{"照片/2025", "文档"} {
		os.MkdirAll(filepath.Join(f.Root, d), 0o755)
	}
	os.WriteFile(filepath.Join(f.Root, "文档", "a.txt"), []byte("a"), 0o644)
	h := browserHarness(t, f)
	h.click("文件夹 文档")
	return f, h
}

// A folder opens in a tab of its own, with a history of its own; the tabs
// keep their places as another shows.
func TestTabsOpenSwitchAndKeepTheirPlaces(t *testing.T) {
	_, h := tabsFixture(t)
	h.menu("文件夹 照片", "在新标签页中打开")
	if len(h.a.tabs) != 2 || h.a.tab != 1 || !h.a.onRoot() || h.a.br.path != "照片" {
		t.Fatalf("tabs %d, shown %d, at %q", len(h.a.tabs), h.a.tab, h.a.location())
	}
	if h.a.nav.CanGoBack() {
		t.Error("a new tab has the history of the one it came from")
	}
	h.click("文件夹 2025")
	if h.a.br.path != "照片/2025" {
		t.Fatalf("went to %q", h.a.br.path)
	}
	h.want("标签页 文档") // the other tab, by its folder's name

	// The first tab, as it was.
	h.click("标签页 文档")
	h.settle()
	if h.a.tab != 0 || h.a.br.path != "文档" {
		t.Fatalf("shown %d at %q", h.a.tab, h.a.location())
	}
	h.want("a.txt")
	// Back, in the second, goes where it went before.
	h.tt.Key(ui.Ctrl, ui.KeyTab)
	h.settle()
	if h.a.tab != 1 || h.a.br.path != "照片/2025" {
		t.Fatalf("Ctrl+Tab: shown %d at %q", h.a.tab, h.a.location())
	}
	h.tt.Key(ui.Alt, ui.KeyLeft)
	h.settle()
	if h.a.br.path != "照片" {
		t.Fatalf("back went to %q", h.a.br.path)
	}
	h.tt.Key(ui.Ctrl|ui.Shift, ui.KeyTab)
	h.settle()
	if h.a.tab != 0 || h.a.br.path != "文档" {
		t.Fatalf("Ctrl+Shift+Tab: shown %d at %q", h.a.tab, h.a.location())
	}
}

// Ctrl+T opens a tab where the one shown is, Ctrl+W closes it, as its
// cross does; the last tab stays.
func TestTabsNewAndClose(t *testing.T) {
	_, h := tabsFixture(t)
	h.tt.Key(ui.Ctrl, ui.KeyT)
	h.settle()
	if len(h.a.tabs) != 2 || h.a.tab != 1 || h.a.br.path != "文档" {
		t.Fatalf("Ctrl+T: %d tabs, at %q", len(h.a.tabs), h.a.location())
	}
	h.click("新建标签页")
	h.settle()
	if len(h.a.tabs) != 3 || h.a.tab != 2 {
		t.Fatalf("new tab button: %d tabs, shown %d", len(h.a.tabs), h.a.tab)
	}
	h.tt.Key(ui.Ctrl, ui.KeyW)
	h.settle()
	if len(h.a.tabs) != 2 || h.a.tab != 1 || h.a.br.path != "文档" {
		t.Fatalf("Ctrl+W: %d tabs, shown %d", len(h.a.tabs), h.a.tab)
	}
	h.click("关闭标签页 文档")
	h.settle()
	if len(h.a.tabs) != 1 {
		t.Fatalf("cross: %d tabs", len(h.a.tabs))
	}
	h.tt.Key(ui.Ctrl, ui.KeyW)
	h.settle()
	if len(h.a.tabs) != 1 || !h.a.onRoot() {
		t.Fatal("the last tab closed")
	}
	if h.tt.HasText("关闭标签页 文档") {
		t.Error("the last tab offers to close")
	}
}

// The menu of a tab closes the others, and moves it.
func TestTabMenu(t *testing.T) {
	_, h := tabsFixture(t)
	h.menu("文件夹 照片", "在新标签页中打开")
	h.menu("文件夹 2025", "在新标签页中打开")
	if len(h.a.tabs) != 3 {
		t.Fatalf("%d tabs", len(h.a.tabs))
	}
	h.menu("标签页 照片", "向左移动")
	if h.a.tabs[0].title != "照片" || h.a.currentTab().title != "2025" {
		t.Fatalf("moved: %q %q %q", h.a.tabs[0].title, h.a.tabs[1].title, h.a.tabs[2].title)
	}
	h.menu("标签页 文档", "关闭其他标签页")
	if len(h.a.tabs) != 1 || h.a.br.path != "文档" {
		t.Fatalf("closed others: %d tabs at %q", len(h.a.tabs), h.a.location())
	}
}

// A server signed out leaves every tab, the hidden ones too.
func TestTabsLeaveAServerSignedOut(t *testing.T) {
	_, h := tabsFixture(t)
	h.menu("文件夹 照片", "在新标签页中打开")
	h.a.newTab(localLocation(t.TempDir()))
	h.settle()
	h.a.signedOut(h.a.store.Servers[0])
	h.settle()
	for i, tab := range h.a.tabs {
		if i != h.a.tab && tab.br != nil {
			t.Errorf("tab %d keeps the server's folder", i)
		}
	}
	for i := range h.a.tabs {
		h.a.switchTab(i)
		h.settle()
		if h.a.onRoot() || h.a.br != nil {
			t.Fatalf("tab %d still shows the server signed out", i)
		}
	}
}

// The details show what is chosen, or the folder shown.
func TestDetailsPanel(t *testing.T) {
	_, h := tabsFixture(t)
	h.tt = ui.NewTester(h.a.view, 1400, 800)
	h.settle()
	h.want("详情")
	h.want("当前文件夹")
	h.clickPage("a.txt")
	h.settle()
	h.want("TXT 文件")
	h.want("1 字节")
	h.click("隐藏详情")
	if _, visible := h.tt.Find("详情"); !h.a.detailsHidden || visible {
		t.Fatal("details still shown")
	}
	h.tt.Key(ui.Ctrl, ui.KeyI)
	h.settle()
	h.want("详情")
}

// The filter of the sidebar finds the folders read in its tree.
func TestSidebarFilter(t *testing.T) {
	_, h := tabsFixture(t)
	h.typeInto("过滤侧边栏", "照")
	h.want("找到的文件夹")
	h.click("照片 · 家里 › nas")
	h.settle()
	if h.a.br.path != "照片" {
		t.Fatalf("went to %q", h.a.br.path)
	}
	h.click("清除过滤")
	h.typeInto("过滤侧边栏", "没有这个")
	h.want("没有找到名称包含“没有这个”的文件夹。只在已展开过的目录中查找。")
}

// Overflow keeps the selected tab and the window actions reachable.
func TestTabStripOverflow(t *testing.T) {
	_, h := tabsFixture(t)
	h.menu("文件夹 照片", "在新标签页中打开")
	if out := os.Getenv("FILESYNC_TABS_SNAPSHOT"); out != "" {
		saveResumeSnapshot(t, h, out)
	}
	for len(h.a.tabs) < maxTabs {
		h.a.newTab("")
		h.settle()
	}
	for i, tab := range h.a.tabs[:maxTabs-1] {
		tab.title = fmt.Sprintf("目录 %d", i)
	}
	h.settle()
	check := func() {
		t.Helper()
		selected, ok := h.tt.Find("标签页 照片")
		plus, plusOK := h.tt.Find("新建标签页")
		if !ok || !plusOK || selected.W < 170 || selected.H < 40 || selected.X+selected.W > plus.X || plus.X+plus.W > 980 {
			t.Fatalf("tab or actions clipped: selected=%v (%t), new=%v (%t)", selected, ok, plus, plusOK)
		}
	}
	check()
	h.tt.Key(ui.Ctrl|ui.Shift, ui.KeyTab)
	h.settle()
	check()
	if out := os.Getenv("FILESYNC_TABS_OVERFLOW_SNAPSHOT"); out != "" {
		saveResumeSnapshot(t, h, out)
	}
}
