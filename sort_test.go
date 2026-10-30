package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"filesync/config"
	"filemanager/remoteclient"
	"filemanager/remoteclient/fitest"
)

func TestNaturalCompare(t *testing.T) {
	names := []string{"文件10", "b", "文件2", "A", "文件02x", "文件1", "a10", "a9", "０全角", "文件"}
	slices.SortFunc(names, naturalCompare)
	want := []string{"A", "a9", "a10", "b", "文件", "文件1", "文件2", "文件02x", "文件10", "０全角"}
	if !slices.Equal(names, want) {
		t.Fatalf("got %q\nwant %q", names, want)
	}
}

func names(es []fileindex.Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Name)
	}
	return out
}

// A click on a header sorts the files by it, another reverses them;
// folders stay first, and the file chosen stays chosen.
func TestHeadersSortFiles(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "目录10"), 0o755)
	os.MkdirAll(filepath.Join(f.Root, "目录2"), 0o755)
	os.WriteFile(filepath.Join(f.Root, "大.bin"), make([]byte, 300), 0o644)
	os.WriteFile(filepath.Join(f.Root, "中.bin"), make([]byte, 200), 0o644)
	os.WriteFile(filepath.Join(f.Root, "小.bin"), make([]byte, 100), 0o644)
	h := browserHarness(t, f)
	h.click("文件夹 nas")
	b := h.a.br
	if got := names(b.items); !slices.Equal(got, []string{"目录2", "目录10", "中.bin", "大.bin", "小.bin"}) {
		t.Fatalf("by name %q", got)
	}
	h.clickPage("中.bin")
	h.clickPage(colSize)
	if got := names(b.items); !slices.Equal(got, []string{"目录2", "目录10", "小.bin", "中.bin", "大.bin"}) {
		t.Fatalf("by size %q", got)
	}
	h.clickPage(colSize)
	// Folders of a size stay first, the order of their names reversed.
	if got := names(b.items); !slices.Equal(got, []string{"目录10", "目录2", "大.bin", "中.bin", "小.bin"}) {
		t.Fatalf("by size, descending %q", got)
	}
	if b.cursor < 0 || b.items[b.cursor].Name != "中.bin" || !b.sel.Has(b.items[b.cursor].Path) {
		t.Fatalf("chose %d", b.cursor)
	}
	// Read again, the files keep the order.
	h.more("刷新")
	if got := names(b.items); !slices.Equal(got, []string{"目录10", "目录2", "大.bin", "中.bin", "小.bin"}) {
		t.Fatalf("refreshed %q", got)
	}
}

func TestHeadersSortLocalFiles(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "z目录"), 0o755)
	os.WriteFile(filepath.Join(dir, "b.txt"), make([]byte, 10), 0o644)
	os.WriteFile(filepath.Join(dir, "a.txt"), make([]byte, 20), 0o644)
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.openLocal(dir)
	h.settle()
	if got := names(h.a.local.items); !slices.Equal(got, []string{"z目录", "a.txt", "b.txt"}) {
		t.Fatalf("by name %q", got)
	}
	h.click(colName)
	if got := names(h.a.local.items); !slices.Equal(got, []string{"z目录", "b.txt", "a.txt"}) {
		t.Fatalf("by name, descending %q", got)
	}
	h.click(colSize)
	if got := names(h.a.local.items); !slices.Equal(got, []string{"z目录", "b.txt", "a.txt"}) {
		t.Fatalf("by size %q", got)
	}
}

func TestTrashSorts(t *testing.T) {
	b := &recycleBrowser{cursor: 1}
	b.items = []fileindex.TrashItem{{ID: 1, Name: "b", Size: 5, DeletedAt: 10}, {ID: 2, Name: "a", Size: 9, DeletedAt: 30}, {ID: 3, Name: "c", Size: 1, DeletedAt: 20}}
	b.sort.by.Column, b.sort.by.Descending = colDeleted, true
	b.filter()
	ids := func() (out []int64) {
		for _, it := range b.shown {
			out = append(out, it.ID)
		}
		return
	}
	if got := ids(); !slices.Equal(got, []int64{2, 3, 1}) {
		t.Fatalf("newest first %v", got)
	}
	b.cursor = 0
	b.sort.by.Column, b.sort.by.Descending = colSize, false
	b.sortShown()
	if got := ids(); !slices.Equal(got, []int64{3, 1, 2}) || b.shown[b.cursor].ID != 2 {
		t.Fatalf("by size %v, cursor %d", got, b.cursor)
	}
}

// The results of a server beyond a page load as the list scrolls to them;
// a click on a header asks the server for all of them in that order.
func TestSearchPagesAndSortsOnServer(t *testing.T) {
	f := fitest.New(t)
	const n = 650
	for i := range n {
		os.WriteFile(filepath.Join(f.Root, fmt.Sprintf("match-%04d.txt", i)), make([]byte, i), 0o644)
	}
	os.WriteFile(filepath.Join(f.Root, "other.txt"), nil, 0o644)
	h := browserHarness(t, f)
	h.click("文件夹 nas")
	g := h.find("match")
	s := h.a.gsearch
	h.want("找到 650 项 · 已显示 200 项")
	if s.items[0].Name != "match-0000.txt" || g.total != n {
		t.Fatalf("first %q of %d", s.items[0].Name, g.total)
	}

	// Scrolled to the end, the next pages load.
	for len(s.items) < n {
		shown := len(s.items)
		s.list.ScrollTo(shown-1, ui.End)
		h.tt.Frame()
		h.settle()
		if len(s.items) == shown {
			t.Fatalf("scrolled: %d results", shown)
		}
	}
	if s.items[n-1].Name != "match-0649.txt" {
		t.Fatalf("last %q", s.items[n-1].Name)
	}

	// Sorted by size, the largest of them all come first.
	before := f.Searches.Load()
	h.clickPage(colSize)
	h.clickPage(colSize)
	if f.Searches.Load() == before {
		t.Fatal("sorted without asking the server")
	}
	if len(s.items) != searchPageSize || s.items[0].Name != "match-0649.txt" || s.items[199].Name != "match-0450.txt" {
		t.Fatalf("by size, descending: %d, %q … %q", len(s.items), s.items[0].Name, s.items[len(s.items)-1].Name)
	}
}
