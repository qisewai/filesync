package main

import (
	"testing"

	"filesync/config"
	"filemanager/remoteclient/fitest"
)

// The settings show the servers and their state, the index of this
// computer, and how the app behaves; each edits as before.
func TestSettingsPage(t *testing.T) {
	f := fitest.New(t)
	store := signedIn(t, f)
	store.AddServer(&config.Server{Name: "办公室", Addr: "10.0.0.9:8090"})
	store.RecentSearches = []string{"合同"}
	h := newHarness(t, store)
	h.a.tray = func(trayModel) {}
	h.tt.SetSize(980, 1000) // all the cards in view
	h.settle()
	h.click("设置")
	if h.a.location() != "/settings" {
		t.Fatalf("location %q", h.a.location())
	}
	h.want("已登录")
	h.want("10.0.0.9:8090 · 未登录")
	h.want("本机索引")
	h.want("关闭窗口时最小化到托盘")

	h.click("最小化到托盘")
	if !h.a.store.QuitOnClose {
		t.Fatal("the switch did not change the setting")
	}
	h.click("清除")
	if h.a.store.RecentSearches != nil {
		t.Fatal("recent searches kept")
	}
	h.click("编辑索引范围…")
	if h.a.localIdx.draft == nil {
		t.Fatal("the index settings did not open")
	}
	h.click("取消索引设置")
	h.click("管理 家里")
	if !h.a.onAdmin() || h.a.admin.tab != "roots" {
		t.Fatalf("manage: %q", h.a.location())
	}
}

// Each server has its management in the sidebar, in the tab shown last,
// whose tabs are in the toolbar.
func TestManagementInSidebar(t *testing.T) {
	f := fitest.New(t)
	h := browserHarness(t, f)
	h.manage("空间统计")
	h.want("目录管理")
	h.click("备份任务")
	h.click("管理 家里")
	if !h.a.onAdmin() || h.a.admin.tab != "stats" {
		t.Fatalf("back to the management: %q", h.a.location())
	}
	h.click("查重")
	if h.a.admin.tab != "dupes" {
		t.Fatal("the tab did not change")
	}
}
