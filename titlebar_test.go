package main

import (
	"fmt"
	"testing"

	"filesync/config"
	"filesync/fileindex/fitest"
	"github.com/egoist/mygo/ui"
)

// Without the system's title bar, the toolbar and the head of the sidebar
// keep clear of the window controls, on either side.
func TestHiddenTitleBarKeepsClearOfControls(t *testing.T) {
	f := fitest.New(t)
	h := newHarness(t, signedIn(t, f))
	h.a.hiddenTitleBar = true
	h.tt.SetTitleBar(ui.TitleBar{Height: 60, Right: 120})
	h.settle()
	h.a.goPlace(rootPlace(h.a.store.Servers[0].ID, 1))
	h.settle()
	h.want("FileSync")
	// The tab bar is the title bar: its last button keeps clear of the
	// controls, in the bar as high as them.
	toggle, ok := h.tt.Find("显示详情")
	if !ok {
		toggle, ok = h.tt.Find("隐藏详情")
	}
	if !ok {
		t.Fatalf("no details toggle in %q", h.tt.Texts())
	}
	if right := toggle.X + toggle.W; right > 980-120 {
		t.Errorf("toggle reaches %v, under the controls", right)
	}
	if toggle.Y+toggle.H > 60 || toggle.Y < 0 {
		t.Errorf("toggle at %+v, not in the bar as high as the controls", toggle)
	}

	h.tt.SetTitleBar(ui.TitleBar{Height: 52, Left: 90})
	h.tt.Frame()
	name, _ := h.tt.Find("FileSync")
	if name.X < 90 {
		t.Errorf("app name at %v, under the controls", name.X)
	}
}

// The system's title bar is a setting, for the next start.
func TestSystemTitleBarSetting(t *testing.T) {
	store := &config.Store{Dir: t.TempDir()}
	h := newHarness(t, store)
	h.a.hiddenTitleBar = true
	h.a.place = "settings"
	h.settle()
	if h.tt.HasText("重新启动 FileSync 后生效。") {
		t.Fatal("asked to restart before any change")
	}
	h.click("系统标题栏")
	if !store.SystemTitleBar {
		t.Fatal("setting not changed")
	}
	if again, _ := config.Load(store.Dir); !again.SystemTitleBar {
		t.Fatal("setting not saved")
	}
	h.want("重新启动 FileSync 后生效。")
	if h.tt.HasText("FileSync") && !h.a.hiddenTitleBar {
		t.Fatal("head shown with the title bar")
	}
}

// A frameless window's toolbar has the window controls, which act on the
// window.
func TestOwnWindowControls(t *testing.T) {
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.hiddenTitleBar, h.a.ownControls = true, true
	var did []string
	h.a.minimizeWindow = func() { did = append(did, "minimize") }
	h.a.maximizeWindow = func(on bool) { did = append(did, map[bool]string{true: "maximize", false: "unmaximize"}[on]) }
	h.a.closeButton = func() { did = append(did, "close") }
	h.settle()
	h.click("最小化")
	h.click("最大化")
	h.a.maximized = true // as the window says
	h.tt.Frame()
	h.click("还原")
	h.click("关闭")
	if got := fmt.Sprint(did); got != "[minimize maximize unmaximize close]" {
		t.Fatalf("did %s", got)
	}
}
