package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"filesync/config"
	"filemanager/remoteclient"
	"filemanager/remoteclient/fitest"
)

func TestDiscoveredServersFillFormAndKeepSavedSession(t *testing.T) {
	f := fitest.New(t)
	saved := &config.Server{ID: "saved", Name: "已保存 NAS", Addr: f.Addr(), Token: "tok"}
	h := newHarness(t, &config.Store{Dir: t.TempDir(), Servers: []*config.Server{saved}})
	found := []fileindex.DiscoveredServer{
		{ID: "0123456789abcdef0123456789abcdef", Name: "家庭 NAS", Addr: "192.168.1.8:18090"},
		{ID: "1123456789abcdef0123456789abcdef", Name: "已保存 NAS", Addr: saved.Addr},
	}
	h.a.discover = func(ctx context.Context, update func([]fileindex.DiscoveredServer)) error { update(found); return nil }
	h.a.openServers()
	h.settle()
	h.want("局域网中的服务器")
	h.want("家庭 NAS")
	h.want("已添加 · 打开")
	if out := os.Getenv("FILESYNC_DISCOVERY_SNAPSHOT"); out != "" {
		saveResumeSnapshot(t, h, out)
	}
	h.click("选择发现的服务器 家庭 NAS 192.168.1.8:18090")
	if h.a.nas.name != "家庭 NAS" || h.a.nas.addr != "192.168.1.8:18090" || h.a.nas.screen != screenEdit {
		t.Fatalf("form not filled: %+v", h.a.nas)
	}
	if len(h.a.store.Servers) != 1 {
		t.Fatal("discovery saved an unselected server")
	}
	h.click("保存")
	if len(h.a.store.Servers) != 2 || h.a.store.ServerByAddr(found[0].Addr).Token != "" {
		t.Fatal("added server must still require login")
	}
	// An endpoint already in the user's list keeps its identity/session.
	h.a.selectDiscovered(found[1])
	if len(h.a.store.Servers) != 2 || saved.Token != "tok" || h.a.nas.server != saved {
		t.Fatal("saved server replaced")
	}
}

func TestDiscoveryRefreshAndCloseDropStaleCallbacks(t *testing.T) {
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	var late func([]fileindex.DiscoveredServer)
	h.a.discover = func(ctx context.Context, update func([]fileindex.DiscoveredServer)) error {
		h.queue <- func() { late = update }
		return errors.New("test network unavailable")
	}
	h.a.openServers()
	h.settle()
	h.want("无法发现服务器：test network unavailable")
	old := late
	h.a.discover = func(context.Context, func([]fileindex.DiscoveredServer)) error { return nil }
	h.click("重新发现")
	old([]fileindex.DiscoveredServer{{ID: "old", Name: "stale", Addr: "192.168.1.2:8090"}})
	h.settle()
	if len(h.a.lan.servers) != 0 {
		t.Fatal("stale scan repopulated refreshed results")
	}
	h.a.nas.open = false
	h.tt.Frame()
	if h.a.lan != nil {
		t.Fatal("closed dialog retained its scan")
	}
}
