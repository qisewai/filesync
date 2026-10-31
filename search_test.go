package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"filemanager/remoteclient"
	"filemanager/remoteclient/fitest"
	"filesync/config"

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

	// A place's tab shows all its results, in place.
	h.chooseSearchScope("本机")
	if !h.a.onSearch() || s.scope != localPlace || len(s.items) != 1 || s.items[0].Name != "本机报告.pdf" {
		t.Fatalf("local: %q, %+v", h.a.location(), s.items)
	}
	h.chooseSearchScope("全部位置")
	if len(s.items) != 4 {
		t.Fatalf("all: %d", len(s.items))
	}
}

// The search of a server, from its page, is of all of it, or of the
// folder shown, with the same filters.
func TestServerSearchScopeAndFilters(t *testing.T) {
	h, _, _ := searchHarness(t)
	h.click("文件夹 项目")
	g := h.find("报告")
	s := h.a.gsearch
	if s.scope != "folder" || g.total != 1 {
		t.Fatalf("current folder: %q %d", s.scope, g.total)
	}
	h.click("搜索")
	h.chooseSearchScope("家里")
	roots := map[string]bool{}
	for _, e := range s.items {
		roots[e.RootName] = true
	}
	if !roots["只读"] || !roots["nas"] {
		t.Fatalf("roots %v", roots)
	}
	h.want("文件类型")
	s.filter.kind = "图片"
	h.a.runSearch(0)
	h.settle()
	if g = s.group(s.scope); g.total != 1 || s.items[0].Name != "报告封面.jpg" {
		t.Fatalf("pictures %+v", s.items)
	}
	s.filter = searchFilter{}
	h.a.runSearch(0)
	h.settle()
	// Global search can find a folder in another root.
	s.filter.kind = "文件夹"
	h.a.runSearch(0)
	h.replaceField("全局搜索", "")
	h.settle()
	s.query = ""
	h.a.runSearch(0)
	h.settle()
	for _, hit := range s.hits {
		if hit.e.RootName == "nas" && hit.e.Name == "项目" {
			h.a.openHit(hit, false)
		}
	}
	h.settle()
	if !h.a.onRoot() || h.a.br.path != "项目" {
		t.Fatalf("opened %q", h.a.location())
	}
}

// Typing the search of every place replaces its place in the history,
// which keeps the search shown.
func TestSearchHistoryAndRecent(t *testing.T) {
	h, _, _ := searchHarness(t)
	h.a.store.RecentSearches = []string{"合同"}
	h.click("搜索")
	h.want("最近")
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

// The results of a server are acted on in place: renamed, deleted, and
// the search follows; chosen across places, they are not.
func TestSearchResultOperations(t *testing.T) {
	h, f, _ := searchHarness(t)
	h.click("文件夹 项目")
	h.find("报告")
	s := h.a.gsearch
	choose := func(names ...string) {
		s.sel.Clear()
		for _, hit := range s.hits {
			if slices.Contains(names, hit.e.Name) {
				s.sel.Add(hit.key())
			}
		}
		h.tt.Frame()
	}
	choose("年度报告.pdf")
	h.menu("年度报告.pdf", "重命名…")
	h.tt.Key(ui.Ctrl, ui.KeyA) // the name, which has the focus
	h.tt.Type("年度报告-终稿.pdf")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if _, err := os.Stat(filepath.Join(f.Root, "项目", "年度报告-终稿.pdf")); err != nil {
		t.Fatal("not renamed:", err)
	}
	if !slices.ContainsFunc(s.items, func(e fileindex.Entry) bool { return e.Name == "年度报告-终稿.pdf" }) {
		t.Fatal("the search did not follow")
	}

	h.a.openSearch("报告")
	h.settle()
	h.chooseSearchScope("家里")
	choose("报告封面.jpg")
	h.tt.Key(0, ui.KeyDelete)
	h.want("1 项会移到 fileindex 的回收站，可以在左侧的回收站中恢复。")
	h.click("移到回收站")
	h.settle()
	if _, err := os.Stat(filepath.Join(f.Trash, "报告封面.jpg")); err != nil {
		t.Fatal("not in the trash:", err)
	}

	// Of two roots, or of a server and this computer: only where they are.
	h.chooseSearchScope("全部位置")
	choose("旧报告.pdf", "本机报告.pdf")
	h.tt.Key(ui.Ctrl, ui.KeyC)
	if !strings.Contains(h.a.errorMsg, "来自不同位置") {
		t.Fatalf("copied results of several places at once: %q", h.a.errorMsg)
	}
}

func TestSearchPatternsAndInvalidRegexUI(t *testing.T) {
	h, _, _ := searchHarness(t)
	h.a.store.Servers = nil
	h.a.openSearch("本机*.pdf")
	h.settle()
	s := h.a.gsearch
	if len(s.items) != 1 || s.items[0].Name != "本机报告.pdf" {
		t.Fatalf("glob %+v", s.items)
	}
	h.want("正则表达式")
	h.click("正则表达式")
	s.query = `^本机.*\.pdf$`
	h.a.runSearch(0)
	h.settle()
	if !s.filter.regex || len(s.items) != 1 {
		t.Fatalf("regex %v %+v", s.filter.regex, s.items)
	}
	s.query = "["
	h.a.runSearch(0)
	h.settle()
	if s.queryError == "" || len(s.groups) != 0 || len(s.items) != 0 || s.cancel != nil {
		t.Fatal("invalid regex started a query or kept stale results")
	}
	s.query = "本机"
	h.click("正则表达式")
	h.a.runSearch(0)
	h.settle()
	if s.queryError != "" || len(s.items) != 1 {
		t.Fatal("failed to recover from invalid regex")
	}
}

func TestSearchMovesIntoNavigationToolbar(t *testing.T) {
	h, _, _ := searchHarness(t)
	h.a.openSearch("")
	h.a.focusSearch = true
	h.settle()
	center, ok := h.tt.Find("全局搜索")
	if !ok {
		t.Fatal("missing landing field")
	}
	h.tt.Type("报告")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	field, ok := h.tt.Find("全局搜索")
	if !ok {
		t.Fatal("missing toolbar field")
	}
	nav, ok := h.tt.Find("导航")
	if !ok {
		t.Fatal("missing navigation")
	}
	if field.Y >= center.Y || field.Y+field.H/2 < nav.Y || field.Y+field.H/2 > nav.Y+nav.H {
		t.Fatalf("search not beside navigation: field=%+v navigation=%+v landing=%+v", field, nav, center)
	}
	h.tt.Type("甲")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if h.a.gsearch.query != "报告甲" {
		t.Fatalf("focus lost on move: %q", h.a.gsearch.query)
	}
	h.click("清除搜索")
	h.settle()
	if h.a.searchInToolbar {
		t.Fatal("empty search did not return to landing")
	}
	h.tt.Type("报告")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if h.a.gsearch.query != "报告" {
		t.Fatal("focus lost when clearing")
	}
}
