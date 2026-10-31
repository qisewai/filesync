package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"filemanager/remoteclient/fitest"
	"github.com/egoist/mygo/ui"
)

func TestFilenamePrefixMatching(t *testing.T) {
	for _, tt := range []struct {
		name, query string
		want        bool
	}{
		{"work.txt", "w", true}, {"Work.txt", "wo", true}, {"awork.txt", "w", false},
		{"文档.txt", "w", true}, {"文档.txt", "wd", true}, {"文档.txt", "wend", true},
		{"文档.txt", "d", false}, {"文档.txt", "wx", false}, {"文档.txt", "文", true},
		{"重庆.txt", "cq", true}, {"重庆.txt", "chongq", true}, {"W文档.txt", "wwd", true},
		{"照片.jpg", "zp", true}, {"résumé.txt", "ré", true}, {"文件", "", true},
	} {
		if got := filenameMatches(tt.name, tt.query); got != tt.want {
			t.Errorf("%q %q = %v", tt.name, tt.query, got)
		}
	}
}

func TestFilenameFilterTyping(t *testing.T) {
	for _, mode := range []string{"list", "grid", "grouped-list", "grouped-grid"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"work.txt", "文档.txt", "其他.txt"} {
				localWrite(t, dir, name, 1)
			}
			h := localSearchHarness(t, dir)
			if mode == "grid" || mode == "grouped-grid" {
				h.click("网格视图")
			}
			if mode == "grouped-list" || mode == "grouped-grid" {
				chooseGrouping(h, "类型")
			}
			if _, ok := h.tt.Find("当前目录文件名过滤"); ok {
				t.Fatal("filter visible before typing")
			}
			h.clickPage("其他.txt")
			h.tt.Type("w")
			h.settle()
			if len(h.a.local.items) != 2 || h.a.local.filter.query != "w" {
				t.Fatalf("did not filter: %q %+v", h.a.local.filter.query, h.a.local.items)
			}
			if out := os.Getenv("FILESYNC_FILTER_SNAPSHOTS"); out != "" {
				h.tt.Advance(time.Second)
				saveResumeSnapshot(t, h, filepath.Join(out, mode+".png"))
			}
			if h.a.local.sel.Len() != 0 {
				t.Fatal("hidden selection survived")
			}
			if _, ok := h.tt.Find("当前目录文件名过滤"); !ok {
				t.Fatal("missing filter field")
			}
			h.tt.Type("d")
			h.settle()
			if len(h.a.local.items) != 1 || h.a.local.items[0].Name != "文档.txt" {
				t.Fatalf("initials: %+v", h.a.local.items)
			}
			h.tt.Key(0, ui.KeyBackspace)
			h.settle()
			if len(h.a.local.items) != 2 {
				t.Fatal("backspace did not restore matching files")
			}
			h.tt.Key(0, ui.KeyDown)
			h.settle()
			h.tt.Key(0, ui.KeyEscape)
			h.settle()
			if len(h.a.local.items) != 3 || h.a.local.filter.query != "" {
				t.Fatalf("escape did not clear: query=%q focused=%v focusFiles=%v items=%d", h.a.local.filter.query, h.a.local.filter.focused, h.a.local.filter.focusFiles, len(h.a.local.items))
			}
			if _, ok := h.tt.Find("当前目录文件名过滤"); ok {
				t.Fatal("empty filter field stayed visible")
			}
		})
	}
}

func TestFilenameFilterRefreshNavigationAndNoMatch(t *testing.T) {
	dir := t.TempDir()
	localWrite(t, dir, "文档.txt", 1)
	localWrite(t, dir, "其他.txt", 1)
	h := localSearchHarness(t, dir)
	h.clickPage("其他.txt")
	h.tt.Type("w")
	h.settle()
	localWrite(t, dir, "work.txt", 1)
	h.a.openLocal(dir)
	h.settle()
	if len(h.a.local.items) != 2 || h.a.local.filter.query != "w" {
		t.Fatal("refresh lost filter or new matching file")
	}
	h.replaceField("当前目录文件名过滤", "zzz")
	h.want("没有匹配的文件")
	h.click("清除文件名过滤")
	if len(h.a.local.items) != 3 {
		t.Fatal("clear lost original listing")
	}
	h.clickPage("work.txt")
	h.tt.Type("w")
	h.settle()
	other := t.TempDir()
	localWrite(t, other, "new.txt", 1)
	h.a.openLocal(other)
	h.settle()
	if h.a.local.filter.query != "" || len(h.a.local.items) != 1 {
		t.Fatal("filter leaked across directories")
	}
}

func TestRemoteFilenameFilter(t *testing.T) {
	f := fitest.New(t)
	for _, name := range []string{"文档.txt", "other.txt"} {
		if err := os.WriteFile(filepath.Join(f.Root, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	h := browserHarness(t, f)
	h.clickPage("other.txt")
	h.tt.Type("wd")
	h.settle()
	if len(h.a.br.items) != 1 || h.a.br.items[0].Name != "文档.txt" {
		t.Fatalf("remote filtering failed: %+v", h.a.br.items)
	}
	h.tt.Key(0, ui.KeyEscape)
	h.settle()
	if len(h.a.br.items) != 2 {
		t.Fatal("remote clear failed")
	}
}

func TestFilenameFilterDoesNotHijackAddressOrShortcuts(t *testing.T) {
	dir := t.TempDir()
	localWrite(t, dir, "work.txt", 1)
	localWrite(t, dir, "other.txt", 1)
	h := localSearchHarness(t, dir)
	h.clickPage("work.txt")
	h.tt.Key(ui.Ctrl, ui.KeyA)
	h.settle()
	if h.a.local.sel.Len() != 2 || h.a.local.filter.query != "" {
		t.Fatal("Ctrl+A entered filter")
	}
	h.tt.Key(ui.Ctrl, ui.KeyL)
	h.tt.Type("www")
	h.settle()
	if h.a.local.filter.query != "" {
		t.Fatal("address typing entered file filter")
	}
}

func TestFilenameFilterEditingDoesNotOperateOnSelection(t *testing.T) {
	dir := t.TempDir()
	localWrite(t, dir, "work.txt", 1)
	h := localSearchHarness(t, dir)
	h.clickPage("work.txt")
	h.tt.Type("w")
	h.settle()
	if h.a.local.sel.Len() != 1 {
		t.Fatal("expected matching selection to remain")
	}
	h.tt.Key(0, ui.KeyF2)
	h.settle()
	if h.a.naming != nil {
		t.Fatal("filter editing triggered rename")
	}
	h.tt.Key(0, ui.KeyDelete)
	h.settle()
	if h.a.asking != nil {
		t.Fatal("filter editing triggered deletion")
	}
	if _, err := os.Stat(filepath.Join(dir, "work.txt")); err != nil {
		t.Fatal(err)
	}
}
