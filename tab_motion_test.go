package main

import (
	"github.com/egoist/mygo/ui"
	"os"
	"testing"
	"time"
)

func TestTabCreationAndCloseMotion(t *testing.T) {
	for _, reduced := range []bool{false, true} {
		t.Run(map[bool]string{false: "animated", true: "reduced"}[reduced], func(t *testing.T) {
			_, h := tabsFixture(t)
			h.tt.SetPreferences(ui.Preferences{ReduceMotion: reduced})
			h.settle()
			plusBefore, _ := h.tt.Find("新建标签页")
			h.a.newTab(nasLocation(h.a.br.server, h.a.br.root.ID, "照片"))
			h.tt.Frame()
			h.tt.Advance(70 * time.Millisecond)
			middle, ok := h.tt.Find("标签页 照片")
			if !ok {
				t.Fatal("new tab missing")
			}
			plusMiddle, _ := h.tt.Find("新建标签页")
			if out := os.Getenv("FILESYNC_TAB_ENTER_SNAPSHOT"); out != "" && !reduced {
				saveResumeSnapshot(t, h, out)
			}
			h.settle()
			full, _ := h.tt.Find("标签页 照片")
			plusAfter, _ := h.tt.Find("新建标签页")
			if !reduced && !(middle.W > 0 && middle.W < full.W && plusMiddle.X > plusBefore.X && plusMiddle.X < plusAfter.X) {
				t.Fatalf("no insertion motion: tab %v -> %v, button %v -> %v -> %v", middle, full, plusBefore, plusMiddle, plusAfter)
			}
			if reduced && middle.W != full.W {
				t.Fatal("reduced motion still animates")
			}
			selectedID := h.a.currentTab().id
			h.a.closeTab(0)
			h.tt.Frame()
			h.tt.Advance(70 * time.Millisecond)
			closing, _ := h.tt.Find("标签页 照片")
			if _, ok := h.tt.Find("标签页 文档"); ok {
				t.Fatal("closing snapshot is still interactive")
			}
			if out := os.Getenv("FILESYNC_TAB_EXIT_SNAPSHOT"); out != "" && !reduced {
				saveResumeSnapshot(t, h, out)
			}
			h.settle()
			end, _ := h.tt.Find("标签页 照片")
			if !reduced && !(closing.X < full.X && closing.X > end.X) {
				t.Fatalf("neighbor jumped: %v -> %v -> %v", full, closing, end)
			}
			if h.a.currentTab().id != selectedID || h.a.br.path != "照片" {
				t.Fatal("closing another tab changed active page")
			}
			// New tabs may be closed before their entrance animation finishes.
			for range 3 {
				h.a.newTab("")
				h.tt.Frame()
				h.tt.Advance(30 * time.Millisecond)
				h.a.closeTab(h.a.tab)
				h.tt.Frame()
				h.tt.Advance(30 * time.Millisecond)
			}
			h.settle()
			if len(h.a.tabs) != 1 || h.a.currentTab().id != selectedID {
				t.Fatal("rapid changes lost the surviving tab")
			}
		})
	}
}

func TestTabEntranceDoesNotFlashScrollbar(t *testing.T) {
	_, h := tabsFixture(t)
	first, _ := h.tt.Find("标签页 文档")
	h.tt.Move(first.X+40, first.Y+20)
	h.a.newTab(nasLocation(h.a.br.server, h.a.br.root.ID, "照片"))
	h.tt.Frame()
	h.tt.Advance(70 * time.Millisecond)
	if out := os.Getenv("FILESYNC_TAB_HOVER_SNAPSHOT"); out != "" {
		saveResumeSnapshot(t, h, out)
	}
	for range 9 {
		img := h.tt.Image()
		x := int(first.X + 40)
		// Below the title, the hovered tab is a solid background. A native
		// horizontal scrollbar would paint over the lower sample.
		if img.At(x, int(first.Y+first.H-10)) != img.At(x, int(first.Y+first.H-6)) {
			t.Fatal("temporary overflow painted a scrollbar across the tab")
		}
		h.tt.Advance(20 * time.Millisecond)
	}
}
