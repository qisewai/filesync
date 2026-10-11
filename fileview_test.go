package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filesync/config"

	"github.com/egoist/mygo/ui"
)

// The grid of this computer's files shows the pictures, made small.
func TestLocalGridThumbnails(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "大图.png"))
	if err != nil {
		t.Fatal(err)
	}
	png.Encode(f, image.NewRGBA(image.Rect(0, 0, 1200, 900)))
	f.Close()
	os.WriteFile(filepath.Join(dir, "坏图.png"), []byte("not a picture"), 0o644)
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.openLocal(dir)
	h.settle()
	h.click("网格视图")
	loading := func() bool {
		for _, th := range h.a.thumbs {
			if th.loading {
				return true
			}
		}
		return len(h.a.thumbs) == 0
	}
	for deadline := time.Now().Add(10 * time.Second); loading() && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
		h.settle()
	}
	shown := 0
	for _, th := range h.a.thumbs {
		if th.loading {
			t.Fatal("a thumbnail is still loading")
		}
		if th.bmp != nil {
			shown++
			if w, h := th.bmp.Size(); w > 192 || h > 168 {
				t.Fatalf("thumbnail %dx%d", w, h)
			}
		}
	}
	if len(h.a.thumbs) != 2 || shown != 1 {
		t.Fatalf("thumbnails %d, shown %d", len(h.a.thumbs), shown)
	}
}

// With files chosen, the bar over them shows what to do with them, in
// place of the page's commands, and Escape lets go of them.
func TestSelectionBar(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("abc"), 0o644)
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.openLocal(dir)
	h.settle()
	h.want("创建备份任务…")
	if h.tt.HasText("取消选择") {
		t.Fatal("selection bar without a selection")
	}
	h.clickPage("a.txt")
	h.want("已选 1 项 · 3 B")
	h.want("在文件管理器中显示")
	h.tt.Key(0, ui.KeyEscape)
	h.settle()
	if h.a.local.sel.Len() != 0 || h.tt.HasText("取消选择") {
		t.Fatal("Escape kept the selection")
	}
}

// Long names without spaces wrap in the grid, at marks and inside long
// runs, rather than spill over their neighbors.
func TestBreakableNames(t *testing.T) {
	for name, want := range map[string]string{
		"short.txt":                  "short.\u200btxt",
		"a_b":                        "a_\u200bb",
		"abcdefghijklmnopqrstuvwxyz": "abcdefghijklmn\u200bopqrstuvwxyz",
		"中文名称很长很长很长很长很长很长": "中文名称很长很长很长很长很长很长",
		"two words": "two words",
	} {
		if got := breakable(name); got != want {
			t.Errorf("breakable(%q) = %q, want %q", name, got, want)
		}
	}
}
