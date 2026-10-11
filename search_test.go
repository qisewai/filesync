package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"filesync/config"
	"filesync/fileindex/fitest"

	"github.com/egoist/mygo/ui"
)

// searchHarness has a server, signed in, and an index of a folder of this
// computer, both with reports and a picture.
func searchHarness(t *testing.T) (*harness, *fitest.Server, string) {
	t.Helper()
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "项目"), 0o755)
	os.WriteFile(filepath.Join(f.Root, "项目", "年度报告.pdf"), []byte("nas report"), 0o644)
	os.WriteFile(filepath.Join(f.Root, "报告封面.jpg"), []byte("img"), 0o644)
	os.WriteFile(filepath.Join(f.ReadOnly, "旧报告.pdf"), []byte("old"), 0o644)
	h := browserHarness(t, f)
	local := t.TempDir()
	localWrite(t, local, "文档/本机报告.pdf", 4)
	localWrite(t, local, "其他.txt", 1)
	h.a.store.LocalIndex = &config.LocalIndexSettings{Roots: []string{local}}
	h.a.openLocal(local)
	h.settle()
	h.indexMenu("更新索引")
	return h, f, local
}

// Ctrl+F, anywhere, searches every place: this computer and each server,
// whose results show together, with where each is, and how many each
// place found.
func TestSearchEverywhere(t *testing.T) {
	h, _, _ := searchHarness(t)
	h.click("备份任务")
	h.tt.Key(ui.Ctrl, ui.KeyF)
	h.settle()
	if h.a.place != "search" {
		t.Fatalf("Ctrl+F went to %q", h.a.place)
	}
	h.tt.Type("报告")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	s := h.a.gsearch
	var names []string
	for _, e := range s.items {
		names = append(names, e.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"年度报告.pdf", "报告封面.jpg", "旧报告.pdf", "本机报告.pdf"}) {
		t.Fatalf("results %q", names)
	}
	h.want("家里 · 只读")
	h.want("家里 · nas/项目")
	h.want("找到 4 项")
	if len(s.groups) != 2 || s.groups[0].total != 1 || s.groups[1].total != 3 {
		t.Fatalf("groups %+v", s.groups)
	}
	if got := h.a.store.RecentSearches; !slices.Equal(got, []string{"报告"}) {
		t.Fatalf("recent %q", got)
	}

	// The filters narrow every place.
	s.filter.kind = "图片"
	h.a.runSearch(0)
	h.settle()
	if len(s.items) != 1 || s.items[0].Name != "报告封面.jpg" {
		t.Fatalf("pictures %+v", s.items)
	}
	s.filter = searchFilter{}
	h.a.runSearch(0)
	h.settle()

	// A result opens in its folder.
	for i, e := range s.items {
		if e.Name == "年度报告.pdf" {
			h.a.openHit(s.hits[i], true)
		}
	}
	h.settle()
	if !h.a.onRoot() || h.a.br.path != "项目" || !h.a.br.sel.Has("项目/年度报告.pdf") {
		t.Fatalf("revealed at %q", h.a.location())
	}
	// Back goes to the search, as it was.
	h.tt.Key(ui.Alt, ui.KeyLeft)
	h.settle()
	if !h.a.onSearch() || s.query != "报告" || len(s.items) != 4 {
		t.Fatalf("back: %q, %d", h.a.location(), len(s.items))
	}

	// A place's chip goes on there, with all its results.
	h.click("在本机中查看全部")
	if !h.a.onLocal() || !h.a.local.results || h.a.local.total != 1 || h.a.local.query != "报告" {
		t.Fatalf("local: %+v", h.a.local.items)
	}
}

// The search of a server is of all of it, or of the folder shown, with
// the same filters.
func TestServerSearchScopeAndFilters(t *testing.T) {
	h, _, _ := searchHarness(t)
	h.click("文件夹 项目")
	h.typeInto("搜索文件", "报告")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	b := h.a.br
	if !b.results || b.total != 3 {
		t.Fatalf("all the server: %d", b.total)
	}
	roots := map[string]bool{}
	for _, e := range b.items {
		roots[e.RootName] = true
	}
	if !roots["只读"] || !roots["nas"] {
		t.Fatalf("roots %v", roots)
	}
	h.want("文件类型")
	b.filter.kind = "图片"
	h.a.search(b.query)
	h.settle()
	if b.total != 1 || b.items[0].Name != "报告封面.jpg" {
		t.Fatalf("pictures %+v", b.items)
	}
	b.filter = searchFilter{}
	b.here = true
	h.a.search(b.query)
	h.settle()
	if b.total != 1 || b.items[0].Name != "年度报告.pdf" {
		t.Fatalf("the folder: %+v", b.items)
	}
	// A folder found in another root opens there.
	b.here = false
	b.filter.kind = "文件夹"
	h.a.search("")
	h.settle()
	h.a.openEntry(b.items[0])
	h.settle()
	if h.a.br.root.ID != b.items[0].Root && h.a.br.path != b.items[0].Path {
		t.Fatalf("opened %q", h.a.location())
	}

	// Clearing the search goes back to the folder.
	h.typeInto("搜索文件", "x")
	h.settle()
	h.tt.Key(0, ui.KeyEscape)
	h.settle()
	if h.a.br.results || h.a.br.query != "" {
		t.Fatal("Escape kept the search")
	}
}

// Typing the search of every place replaces its place in the history,
// which keeps the search shown.
func TestSearchHistoryAndRecent(t *testing.T) {
	h, _, _ := searchHarness(t)
	h.a.store.RecentSearches = []string{"合同"}
	h.click("搜索")
	h.want("最近搜索")
	h.want("合同")
	h.tt.Type("报")
	h.settle()
	h.tt.Type("告")
	h.settle()
	h.tt.Key(ui.Alt, ui.KeyLeft)
	h.settle()
	if h.a.onSearch() {
		t.Fatalf("back stayed on the search: %q", h.a.location())
	}
	h.tt.Key(ui.Alt, ui.KeyRight)
	h.settle()
	if !h.a.onSearch() || h.a.gsearch.query != "报告" {
		t.Fatalf("forward: %q", h.a.location())
	}
	h.a.remember("报告")
	loaded, err := config.Load(h.a.store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(loaded.RecentSearches, []string{"报告", "合同"}) {
		t.Fatalf("saved %q", loaded.RecentSearches)
	}
}
