package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filesync/fileindex/fitest"

	"github.com/egoist/mygo/ui"
)

// TestShots saves screenshots of the main pages into
// $FILESYNC_SHOTS_DIR, for looking at the design; skipped without it.
func TestShots(t *testing.T) {
	dir := os.Getenv("FILESYNC_SHOTS_DIR")
	if dir == "" {
		t.Skip("FILESYNC_SHOTS_DIR not set")
	}
	f := fitest.New(t)
	for _, d := range []string{"项目/子目录", "项目/设计稿", "照片/2025", "照片/2026", "文档/合同", "备份"} {
		os.MkdirAll(filepath.Join(f.Root, d), 0o755)
	}
	for name, size := range map[string]int{
		"项目/报告.txt": 6000, "项目/预算.xlsx": 48000, "项目/方案.pdf": 1200000, "项目/封面.png": 30000,
		"项目/会议记录.md": 900, "项目/演示.mp4": 3000000, "项目/资料.zip": 800000,
	} {
		os.WriteFile(filepath.Join(f.Root, name), make([]byte, size), 0o644)
	}
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "文档"), 0o755)
	os.MkdirAll(filepath.Join(home, "下载"), 0o755)
	os.WriteFile(filepath.Join(home, "笔记.txt"), []byte("hi"), 0o644)

	h := newHarness(t, signedIn(t, f))
	h.a.localRoots = []localRoot{{"主目录", home}}
	h.tt = ui.NewTester(h.a.view, 1400, 900)
	h.settle()
	shot := func(name string) {
		h.settle()
		h.tt.Advance(2 * time.Second) // transitions run on the tester's clock
		out, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		png.Encode(out, h.tt.Image())
	}
	h.a.store.RecentSearches = []string{"报告", "照片 2025", "合同"}
	h.a.focusSearch = true
	shot("0-search")
	h.tt.Type("报告")
	shot("0b-search-results")
	h.click("清除搜索")
	h.click("文件夹 nas")
	h.click("文件夹 项目")
	shot("1-nas-list")
	h.clickPage("报告.txt")
	shot("2-nas-selected")
	h.tt.Key(ui.Ctrl, ui.KeyX)
	shot("2b-nas-cut")
	h.click("网格视图")
	shot("3-nas-grid")
	h.click("本机文件夹 主目录")
	shot("4-local")
	// Tabs: the folder of the server in a tab of its own.
	h.a.newTab(nasLocation(h.a.store.Servers[0].ID, 1, "照片"))
	h.a.newTab(nasLocation(h.a.store.Servers[0].ID, 1, "文档"))
	shot("5-tabs")

	h.a.sidebarFilter = "目"
	shot("6-filter")
	h.a.sidebarFilter = ""
	h.a.toggleDetails()
	shot("7-no-details")
}
