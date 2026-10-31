package main

import (
	"github.com/egoist/mygo/ui"
	"os"
	"testing"
	"time"
)

func TestPanelVisibilityMotion(t *testing.T) {
	for _, reduced := range []bool{false, true} {
		t.Run(map[bool]string{false: "animated", true: "reduced"}[reduced], func(t *testing.T) {
			_, h := tabsFixture(t)
			h.tt.SetSize(1400, 800)
			h.tt.SetPreferences(ui.Preferences{ReduceMotion: reduced})
			h.settle()
			bounds := func() ui.Rect {
				r, ok := h.tt.Find("文件浏览区域")
				if !ok {
					t.Fatal("missing page")
				}
				return r
			}
			for _, left := range []bool{true, false} {
				before := bounds()
				if left {
					h.a.toggleSidebar()
				} else {
					h.a.toggleDetails()
				}
				h.tt.Frame()
				h.tt.Advance(70 * time.Millisecond)
				middle := bounds()
				if out := os.Getenv("FILESYNC_PANEL_SNAPSHOT"); out != "" && !reduced && left {
					saveResumeSnapshot(t, h, out)
				}
				h.tt.Advance(panelMotion)
				after := bounds()
				if after.W <= before.W {
					t.Fatalf("page did not expand: %v -> %v", before, after)
				}
				if !reduced && !(middle.W > before.W && middle.W < after.W) {
					t.Fatalf("missing intermediate width: %v -> %v -> %v", before, middle, after)
				}
				if reduced && middle.W != after.W {
					t.Fatal("reduced motion animated")
				}
				if left {
					h.a.toggleSidebar()
				} else {
					h.a.toggleDetails()
				}
				h.tt.Frame()
				h.tt.Advance(60 * time.Millisecond)
				// Reverse a running transition, then restore the same remembered width.
				if left {
					h.a.toggleSidebar()
				} else {
					h.a.toggleDetails()
				}
				h.tt.Frame()
				h.tt.Advance(40 * time.Millisecond)
				if left {
					h.a.toggleSidebar()
				} else {
					h.a.toggleDetails()
				}
				h.settle()
				if r := bounds(); r.W != before.W || r.X != before.X {
					t.Fatalf("restore changed layout: %v -> %v", before, r)
				}
			}
		})
	}
}

func TestSidebarTransientFilter(t *testing.T) {
	_, h := tabsFixture(t)
	if h.tt.HasText("过滤侧边栏") {
		t.Fatal("filter initially visible")
	}
	h.click("文件夹 文档")
	h.tt.Type("zp")
	h.settle()
	if h.a.sidebarFilter != "zp" || !h.tt.Focused("过滤侧边栏") {
		t.Fatal("typing did not focus sidebar filter")
	}
	h.want("照片 · 家里 › nas")
	if out := os.Getenv("FILESYNC_SIDEBAR_FILTER_SNAPSHOT"); out != "" {
		saveResumeSnapshot(t, h, out)
	}
	h.tt.Key(0, ui.KeyDown)
	h.settle()
	if !h.tt.Focused("照片 · 家里 › nas") {
		t.Fatal("Down did not focus filter results")
	}
	h.tt.Key(0, ui.KeyEscape)
	h.settle()
	if h.a.sidebarFilter != "" || h.tt.HasText("过滤侧边栏") {
		t.Fatal("escape did not hide filter")
	}
	h.tt.Type("w")
	h.settle()
	if h.a.sidebarFilter != "w" {
		t.Fatal("focus not restored after escape")
	}
	h.tt.Key(0, ui.KeyBackspace)
	h.settle()
	if h.tt.HasText("过滤侧边栏") {
		t.Fatal("empty input still visible")
	}
	h.tt.Type("z")
	h.settle()
	if h.a.sidebarFilter != "z" {
		t.Fatal("focus not restored after backspace")
	}
	h.click("清除过滤")
	h.clickPage("a.txt")
	h.tt.Type("a")
	h.settle()
	if h.a.sidebarFilter != "" || h.a.br.filter.query != "a" {
		t.Fatal("file list typing leaked into sidebar")
	}
}
