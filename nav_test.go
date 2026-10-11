package main

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"filesync/config"
	"filesync/fileindex/fitest"

	"github.com/egoist/mygo/ui"
)

// Back and forward go through the places shown: directories of this
// computer and of a server, and the pages between.
func TestHistoryBackAndForward(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "项目", "子目录"), 0755)
	h := browserHarness(t, f)
	local := t.TempDir()
	os.MkdirAll(filepath.Join(local, "文档"), 0755)

	h.click("文件夹 项目")
	h.a.browse("项目/子目录")
	h.settle()
	if h.a.br.path != "项目/子目录" {
		t.Fatalf("path %q", h.a.br.path)
	}
	h.a.openLocal(local)
	h.settle()
	h.a.openLocal(filepath.Join(local, "文档"))
	h.settle()
	h.click("备份任务")

	back := func(want string) {
		t.Helper()
		h.tt.Key(ui.Alt, ui.KeyLeft)
		h.settle()
		if got := h.a.location(); got != want {
			t.Fatalf("back: %q, want %q", got, want)
		}
	}
	back(h.locationOfLocal(filepath.Join(local, "文档")))
	back(h.locationOfLocal(local))
	back("/nas/" + h.a.store.Servers[0].ID + "/1?path=%E9%A1%B9%E7%9B%AE%2F%E5%AD%90%E7%9B%AE%E5%BD%95")
	if !h.a.onRoot() || h.a.br.path != "项目/子目录" {
		t.Fatalf("server directory not shown: %q", h.a.place)
	}
	back("/nas/" + h.a.store.Servers[0].ID + "/1?path=%E9%A1%B9%E7%9B%AE")
	if h.a.br.path != "项目" {
		t.Fatalf("path %q", h.a.br.path)
	}
	h.tt.Key(ui.Alt, ui.KeyRight)
	h.settle()
	if h.a.br.path != "项目/子目录" {
		t.Fatalf("forward: %q", h.a.br.path)
	}

	// Going up, from the toolbar, is a place of the history too.
	h.click("上级文件夹")
	if h.a.br.path != "项目" || !h.a.nav.CanGoBack() || h.a.nav.CanGoForward() {
		t.Fatalf("up: %q", h.a.br.path)
	}
}

func (h *harness) locationOfLocal(dir string) string { return "/local?dir=" + url.QueryEscape(dir) }

// The transfers show in a popover of the status bar, which the tray's menu
// opens too.
func TestTransfersPopover(t *testing.T) {
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.openLocal(t.TempDir())
	h.settle()
	tr := h.a.addTransfer("上传", "a.txt")
	tr.total, tr.done = 100, 40
	h.tt.Frame()
	h.want("1 个传输 · 40%")
	if h.tt.HasText("a.txt") {
		t.Fatal("the popover opened by itself")
	}
	h.click("传输任务")
	h.want("a.txt")
	h.a.finishTransfer(tr, nil, nil)
	h.tt.Frame()
	h.want("传输已完成")
	h.click("清除已完成")
	h.tt.Frame()
	if len(h.a.transfers) != 0 || h.tt.HasText("传输已完成") {
		t.Fatal("not cleared")
	}
}
