package main

import (
	fileindex "filemanager/remoteclient"
	"fmt"
	"github.com/egoist/mygo/ui"
	"testing"
	"time"
)

func BenchmarkBrowserSelectionDuringScroll(b *testing.B) {
	for _, selected := range []bool{false, true} {
		b.Run(fmt.Sprint(selected), func(b *testing.B) {
			browser := &localBrowser{items: make([]fileindex.Entry, 100000)}
			for i := range browser.items {
				browser.items[i].Path = fmt.Sprint(i)
			}
			if selected {
				browser.sel.Add("99999")
			}
			browser.chosen()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_ = browser.chosen()
			}
		})
	}
}

func TestSelectionCacheTracksDirectoryChanges(t *testing.T) {
	b := &localBrowser{items: []fileindex.Entry{{Path: "a", Size: 1}, {Path: "b", Size: 2}, {Path: "c", Size: 3}}}
	want := func(paths ...string) {
		t.Helper()
		got := b.chosen()
		if len(got) != len(paths) {
			t.Fatalf("got %v, want %v", got, paths)
		}
		for i, path := range paths {
			if got[i].Path != path {
				t.Fatalf("got %v, want %v", got, paths)
			}
		}
	}
	want()
	b.sel.Add("a")
	b.sel.Add("c")
	want("a", "c")
	// Same-size selection replacement, then in-place reordering.
	b.sel.Remove("a")
	b.sel.Add("b")
	want("b", "c")
	b.items[1], b.items[2] = b.items[2], b.items[1]
	b.sort.revision++
	want("c", "b")
	b.items[1].Size = 99
	got := b.chosen()
	if got[0].Size != 99 {
		t.Fatal("cached stale metadata")
	}
	got[0].Path = "caller modified copy"
	want("c", "b")
	// New listing of equal length, then a filter and a missing selection key.
	b.items = []fileindex.Entry{{Path: "b"}, {Path: "x"}, {Path: "c"}}
	want("b", "c")
	b.items = b.items[:1]
	b.sel.Add("missing")
	want("b")
	b.sel.Clear()
	want()
	if b.selectionCache.first != nil || b.selectionCache.keys != nil {
		t.Fatal("empty selection retains directory")
	}
	// Large multi-selections keep the displayed order without an unbounded cache.
	b.items = make([]fileindex.Entry, 300)
	for i := range b.items {
		b.items[i].Path = fmt.Sprint(i)
		b.sel.Add(b.items[i].Path)
	}
	if len(b.chosen()) != 300 || b.selectionCache.keys != nil {
		t.Fatal("large selection cache is not bounded")
	}
}

func TestFileViewsWheelGlides(t *testing.T) {
	for _, grid := range []bool{false, true} {
		for _, grouped := range []bool{false, true} {
			t.Run(fmt.Sprintf("grid=%t/grouped=%t", grid, grouped), func(t *testing.T) {
				dir := t.TempDir()
				for i := 0; i < 80; i++ {
					localWrite(t, dir, fmt.Sprintf("item-%03d.txt", i), 1)
				}
				h := localSearchHarness(t, dir)
				h.a.local.grid = grid
				if grouped {
					h.a.local.sort.group.by = colType
				}
				h.settle()
				name := "item-010.txt"
				start, ok := h.tt.Find(name)
				if !ok {
					t.Fatal("row not visible")
				}
				h.tt.Wheel(start.X+start.W/2, start.Y+start.H/2, 0, 60)
				h.tt.Advance(50 * time.Millisecond)
				middle, ok := h.tt.Find(name)
				if !ok {
					t.Fatal("row vanished during wheel glide")
				}
				h.tt.Advance(200 * time.Millisecond)
				end, ok := h.tt.Find(name)
				if !ok {
					t.Fatal("row vanished after wheel glide")
				}
				if !(start.Y > middle.Y && middle.Y > end.Y) {
					t.Fatalf("no smooth motion: %v -> %v -> %v", start, middle, end)
				}
				h.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
				h.tt.Wheel(end.X+end.W/2, end.Y+end.H/2, 0, -60)
				immediate, _ := h.tt.Find(name)
				h.tt.Advance(200 * time.Millisecond)
				settled, _ := h.tt.Find(name)
				if immediate.Y != settled.Y {
					t.Fatal("reduced motion still glides")
				}
			})
		}
	}
}
