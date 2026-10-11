package main

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"filesync/fileindex"
	"filesync/localindex"

	"github.com/egoist/mygo/ui"
)

// Searching: the field of the toolbar searches the place shown, this
// computer or a server, or one of its folders, in place; or every place at
// once, on the page of the search (/search), which shows the first results
// of each, and goes on in the place chosen.

// searchFilter narrows a search: by the kind of file, the extensions, the
// time of the last change and the size. Each is the label shown.
type searchFilter struct {
	kind, exts, when, size string
}

var (
	searchKinds = []string{"全部类型", "文件夹", "文件", "图片", "视频", "音频", "文档", "压缩包", "磁盘镜像", "代码"}
	kindKeys    = map[string]string{"文件夹": "dir", "文件": "file", "图片": "image", "视频": "video", "音频": "audio", "文档": "doc", "压缩包": "archive", "磁盘镜像": "disk", "代码": "code"}
	searchTimes = []string{"任何时间", "今天", "最近 7 天", "最近 30 天", "最近一年"}
	searchSizes = []string{"任何大小", "小于 1 MB", "1 MB 以上", "100 MB 以上", "1 GB 以上"}
)

// active reports whether the filter narrows anything.
func (f searchFilter) active() bool {
	return kindKeys[f.kind] != "" || len(f.extList()) > 0 || f.since() > 0 || f.size != "" && f.size != searchSizes[0]
}

func (f searchFilter) extList() []string {
	var out []string
	for _, e := range strings.Fields(strings.NewReplacer(",", " ", "，", " ", ";", " ").Replace(f.exts)) {
		if e = strings.ToLower(strings.TrimPrefix(e, ".")); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// since is the earliest time of the last change asked, in Unix seconds, 0
// for any.
func (f searchFilter) since() int64 {
	now := time.Now()
	switch f.when {
	case "今天":
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()).Unix()
	case "最近 7 天":
		return now.AddDate(0, 0, -7).Unix()
	case "最近 30 天":
		return now.AddDate(0, 0, -30).Unix()
	case "最近一年":
		return now.AddDate(-1, 0, 0).Unix()
	}
	return 0
}

// sizes are the bounds of the size asked, 0 for none.
func (f searchFilter) sizes() (lo, hi int64) {
	switch f.size {
	case "小于 1 MB":
		return 0, 1<<20 - 1
	case "1 MB 以上":
		return 1 << 20, 0
	case "100 MB 以上":
		return 100 << 20, 0
	case "1 GB 以上":
		return 1 << 30, 0
	}
	return 0, 0
}

// local puts the filter in a query of the index of this computer.
func (f searchFilter) local(q *localindex.Query) {
	q.Category = kindKeys[f.kind]
	q.Exts = f.extList()
	q.From = f.since()
	q.MinSize, q.MaxSize = f.sizes()
}

// nas puts the filter in a search of a server.
func (f searchFilter) nas(o *fileindex.SearchOptions) {
	switch k := kindKeys[f.kind]; k {
	case "dir", "file":
		o.Type = k
	default:
		o.Category = k
	}
	o.Exts = f.extList()
	o.From = f.since()
	o.MinSize, o.MaxSize = f.sizes()
	if o.MinSize > 0 || o.MaxSize > 0 {
		o.Type = "file" // folders have no size of their own
	}
}

// filterBar shows the filter, calling changed as it changes; typing the
// extensions waits a moment.
func filterBar(c *ui.Context, key string, f *searchFilter, changed func(wait bool)) {
	if f.kind == "" {
		f.kind, f.when, f.size = searchKinds[0], searchTimes[0], searchSizes[0]
	}
	if ui.Select(c.Key(key+"-kind"), &f.kind, searchKinds).MinWidth(0).Width(106).Label("文件类型").Changed() {
		changed(false)
	}
	if ui.TextInput(c.Key(key+"-exts"), &f.exts).Width(72).Label("扩展名").Placeholder("扩展名").Tooltip("多个扩展名用空格分隔，例如 pdf docx").Changed() {
		changed(true)
	}
	if ui.Select(c.Key(key+"-when"), &f.when, searchTimes).MinWidth(0).Width(112).Label("修改时间").Changed() {
		changed(false)
	}
	if ui.Select(c.Key(key+"-size"), &f.size, searchSizes).MinWidth(0).Width(118).Label("文件大小").Changed() {
		changed(false)
	}
}

// searchBox is the field of the toolbar: the chip at its start says what
// it searches, and chooses it with menu. It reports a change, Enter, and
// the field cleared, with its button or Escape.
func (a *app) searchBox(c *ui.Context, key, label string, query *string, scope, placeholder string, menu func(m *ui.Menu)) (changed, submitted, cleared bool) {
	t := c.Theme()
	var in ui.Element
	box := ui.Row(c.Key(key+"-box")).Width(340).Shrink(1).MinWidth(200).AlignItems(ui.Center).Gap(6).
		Padding(3, 6, 3, 3).Radius(t.Radius).Background(t.Surface).Border(1, t.Border).Cursor(ui.CursorText)
	box.Children(func() {
		chip := ui.ButtonBase(c.Key(key+"-scope")).Padding(2, 6).Gap(4).AlignItems(ui.Center).Radius(t.Radius * 0.75).
			Background(t.Accent.Alpha(0.1)).Label("搜索范围：" + scope).Tooltip("选择搜索范围").Menu(menu)
		chip.Children(func() {
			ui.Text(c, scope).FontSize(12).SingleLine().MaxWidth(110).TextColor(t.Accent)
			ui.Icon(c, iconChevronDown).FontSize(11).TextColor(t.Accent)
		})
		ui.Icon(c, iconSearch).FontSize(14).TextColor(t.TextMuted)
		in = ui.TextInputBase(c.Key(key), query).Label(label).Placeholder(placeholder).Grow(1).Shrink(1).MinWidth(0).Bind(&a.searchFocus)
		if *query != "" {
			ui.ButtonBase(c.Key(key+"-clear")).Size(18, 18).Radius(9).Background(t.TextMuted.Alpha(0.3)).
				Label("清除搜索").Tooltip("清除搜索（Esc）").OnClick(func() { cleared = true }).Children(func() {
				ui.Icon(c, iconClose).FontSize(10).TextColor(t.Background)
			})
		} else {
			ui.Row(c).Size(18, 18)
		}
	})
	if in.Focused() {
		box.BorderColor(t.Accent)
	}
	if box.Clicked() {
		in.Focus()
	}
	if a.focusSearch {
		a.focusSearch = false
		in.Focus()
	}
	// Each reports once.
	changed, submitted = in.Changed(), in.Submitted()
	if in.Shortcut(0, ui.KeyEscape) && *query != "" {
		cleared = true
	}
	if cleared {
		*query = ""
		changed = false
	}
	a.searchTyping = in.Focused()
	return changed, submitted, cleared
}

// searchShortcuts focus the field of the toolbar, for Ctrl+F and Ctrl+K,
// on the page of the search where the page has none.
func (a *app) searchShortcuts(c *ui.Context) {
	if c.Shortcut(ui.Ctrl, ui.KeyF) || c.Shortcut(ui.Ctrl, ui.KeyK) {
		if !a.onLocal() && !a.onRoot() && a.place != "search" {
			a.openSearch("")
		}
		a.focusSearch = true
	}
}

// remember keeps a search among the recent ones.
func (a *app) remember(q string) {
	if q = strings.TrimSpace(q); q != "" && (len(a.store.RecentSearches) == 0 || a.store.RecentSearches[0] != q) {
		a.store.AddRecentSearch(q)
		a.save()
	}
}

// The page of the search of every place.

// globalSearch is the search of every place: this computer, and each
// server signed in to, of which it shows the first results.
type globalSearch struct {
	query  string
	filter searchFilter
	groups []*searchGroup
	hits   []searchHit
	items  []fileindex.Entry // of the hits, in their order

	sel      ui.Selection[string]
	cursor   int
	list     ui.ListState
	gridSt   ui.GridState
	grid     bool
	gridLast int
	sort     tableSort

	gen      int
	debounce *time.Timer
	cancel   context.CancelFunc
}

// busy reports whether the search waits for the typing, or for a place.
func (s *globalSearch) busy() bool {
	if s.debounce != nil {
		return true
	}
	for _, g := range s.groups {
		if g.loading {
			return true
		}
	}
	return false
}

// searchGroup is what a place found: its first results, and how many.
type searchGroup struct {
	source  string // localPlace, or a server's ID
	name    string
	hits    []searchHit
	total   int
	loading bool
	err     string
}

// searchHit is a result, and the place it is in.
type searchHit struct {
	source string
	e      fileindex.Entry
}

func (h searchHit) key() string {
	return fmt.Sprintf("%s\x00%d\x00%s", h.source, h.e.Root, h.e.Path)
}

// searchPerPlace is how many results the search of every place shows of
// each.
const searchPerPlace = 200

// openSearch shows the page of the search of every place, searching q.
func (a *app) openSearch(q string) {
	if a.gsearch == nil {
		s := &globalSearch{cursor: -1}
		s.list.Key = func(i int) any { return s.hits[i].key() }
		s.list.Selection = &s.sel
		s.list.Selected = &s.cursor
		s.sort.by = ui.SortOrder{Column: colName}
		s.list.Sort = &s.sort.by
		s.gridSt.Key = s.list.Key
		s.gridSt.Selection = &s.sel
		s.gridSt.Selected = &s.cursor
		a.gsearch = s
	}
	a.place = "search"
	if s := a.gsearch; s.query != q || len(s.groups) == 0 {
		s.query = q
		a.runSearch(0)
	}
}

// runSearch searches every place, after a wait while typing.
func (a *app) runSearch(wait time.Duration) {
	s := a.gsearch
	s.gen++
	gen := s.gen
	if s.debounce != nil {
		s.debounce.Stop()
		s.debounce = nil
	}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	q := strings.TrimSpace(s.query)
	if q == "" && !s.filter.active() {
		s.groups, s.hits, s.items = nil, nil, nil
		s.sel.Clear()
		return
	}
	start := func() {
		if a.closed || a.gsearch != s || s.gen != gen {
			return
		}
		s.debounce = nil
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		s.cancel = cancel
		s.groups = nil
		s.sel.Clear()
		s.cursor = -1
		s.list.ScrollTo(0, ui.Start)
		if a.ensureLocalIndex(); a.localIdx != nil {
			g := &searchGroup{source: localPlace, name: "本机", loading: true}
			s.groups = append(s.groups, g)
			x := a.localIdx.index
			lq := localindex.Query{Text: q, Sort: "name"}
			s.filter.local(&lq)
			go func() {
				res, err := x.Search(ctx, lq)
				var es []localindex.Entry
				if err == nil {
					es, err = res.Page(ctx, 0, searchPerPlace)
				}
				a.post(func() {
					if a.closed || a.gsearch != s || s.gen != gen {
						return
					}
					g.loading = false
					if err != nil {
						g.err = err.Error()
					} else {
						g.total = res.Total()
						for _, e := range localEntries(es) {
							g.hits = append(g.hits, searchHit{localPlace, e})
						}
					}
					a.mergeSearch()
				})
			}()
		}
		for _, sv := range a.store.Servers {
			if sv.Token == "" {
				continue
			}
			g := &searchGroup{source: sv.ID, name: sv.Title(), loading: true}
			s.groups = append(s.groups, g)
			c := fileindex.New(sv.Addr, sv.Token)
			o := fileindex.SearchOptions{Page: 1, Size: searchPerPlace, Sort: "name"}
			s.filter.nas(&o)
			go func() {
				items, total, err := c.Search(ctx, q, o)
				a.post(func() {
					if a.closed || a.gsearch != s || s.gen != gen {
						return
					}
					g.loading = false
					switch {
					case errors.Is(err, fileindex.ErrAuth):
						g.err = "需要重新登录"
						a.signedOut(sv)
					case err != nil:
						g.err = connectError(err)
					default:
						g.total = total
						for _, e := range items {
							g.hits = append(g.hits, searchHit{sv.ID, e})
						}
					}
					a.mergeSearch()
				})
			}()
		}
		a.mergeSearch()
	}
	if wait <= 0 {
		start()
	} else {
		s.debounce = time.AfterFunc(wait, func() { a.post(start) })
	}
}

// mergeSearch puts the results of every place together, in the order of
// the table.
func (a *app) mergeSearch() {
	s := a.gsearch
	s.hits = s.hits[:0]
	for _, g := range s.groups {
		s.hits = append(s.hits, g.hits...)
	}
	s.sort.applied = s.sort.by
	desc := s.sort.by.Descending
	slices.SortStableFunc(s.hits, func(x, y searchHit) int {
		c := compareEntries(x.e, y.e, s.sort.by.Column, desc)
		if s.sort.by.Column == colWhere {
			c = naturalCompare(a.hitWhere(x), a.hitWhere(y))
		}
		if c == 0 {
			c = naturalCompare(x.e.Name, y.e.Name)
		}
		if desc {
			c = -c
		}
		return c
	})
	s.items = s.items[:0]
	for _, h := range s.hits {
		s.items = append(s.items, h.e)
	}
}

// hitWhere is where a result is: its place, and its folder.
func (a *app) hitWhere(h searchHit) string {
	if h.source == localPlace {
		return "本机 · " + homeTilde(h.e.Parent)
	}
	name := h.source
	if sv := a.store.Server(h.source); sv != nil {
		name = sv.Title()
	}
	return name + " · " + path.Join(h.e.RootName, h.e.Parent)
}

// hitOf is the result of an entry shown.
func (a *app) hitOf(e fileindex.Entry) (searchHit, bool) {
	for _, h := range a.gsearch.hits {
		if h.e.Root == e.Root && h.e.Path == e.Path && h.e.Name == e.Name {
			return h, true
		}
	}
	return searchHit{}, false
}

// openHit opens a result: a folder in its place, a file with its app or
// the preview; reveal shows it, chosen, in its folder instead.
func (a *app) openHit(h searchHit, reveal bool) {
	a.remember(a.gsearch.query)
	e := h.e
	if h.source == localPlace {
		switch {
		case e.IsDir && !reveal:
			a.openLocal(e.Path)
		case reveal:
			a.openLocal(e.Parent, e.Name)
		default:
			a.openPath(e.Path)
		}
		return
	}
	for _, r := range a.roots[h.source] {
		if r.ID != e.Root {
			continue
		}
		switch {
		case e.IsDir && !reveal:
			a.showDirectory(h.source, r, e.Path)
		default:
			a.showDirectory(h.source, r, e.Parent, e.Name)
			if !reveal {
				a.openEntry(e)
			}
		}
		return
	}
	a.errorMsg = "找不到这个结果所在的目录，可能需要重新连接服务器"
}

// narrowSearch goes on with the search in one place: this computer, or a
// server, where all its results show.
func (a *app) narrowSearch(source string) {
	s := a.gsearch
	q, f := s.query, s.filter
	a.remember(q)
	if source == localPlace {
		a.openLocal(a.startLocal())
		b := a.local
		b.query, b.filter, b.here = q, f, false
		a.queueLocalSearch(0)
		return
	}
	roots := a.roots[source]
	if len(roots) == 0 {
		a.errorMsg = "这个服务器还没有可以浏览的目录"
		return
	}
	a.goPlace(rootPlace(source, roots[0].ID))
	if b := a.br; b != nil && b.server == source {
		b.filter, b.here = f, false
		a.search(q)
	}
}

// searchHeader is, in the toolbar of the page of the search, its field.
func (a *app) searchHeader(c *ui.Context) {
	s := a.gsearch
	ui.Text(c, "搜索").FontSize(15).Bold().Padding(0, 6)
	ui.Spacer(c)
	changed, submitted, cleared := a.searchBox(c, "global-search", "全局搜索", &s.query, "全部位置", "搜索本机和所有服务器", func(m *ui.Menu) {
		m.Item("全部位置").Checked(true)
		m.Separator()
		if m.Item("本机").Chosen() {
			a.narrowSearch(localPlace)
		}
		for _, sv := range a.store.Servers {
			if sv.Token != "" && m.Item(sv.Title()).Chosen() {
				a.narrowSearch(sv.ID)
			}
		}
	})
	switch {
	case cleared:
		a.runSearch(0)
	case changed:
		a.runSearch(250 * time.Millisecond)
	case submitted:
		a.remember(s.query)
		a.runSearch(0)
	}
}

// searchPage shows what every place found.
func (a *app) searchPage(c *ui.Context) {
	s := a.gsearch
	es := []fileindex.Entry{}
	var chosen []searchHit
	for _, h := range s.hits {
		if s.sel.Has(h.key()) {
			chosen = append(chosen, h)
			es = append(es, h.e)
		}
	}
	n := len(chosen)
	if n > 0 && !a.searchTyping && c.Shortcut(0, ui.KeyEscape) {
		s.sel.Clear()
	}
	commandBar(c, n, filesSize(es), func() { s.sel.Clear() }, func() {
		filterBar(c, "global", &s.filter, func(wait bool) {
			if wait {
				a.runSearch(300 * time.Millisecond)
			} else {
				a.runSearch(0)
			}
		})
	}, func() {
		if n == 1 {
			toolButton(c, "hit-open", iconOpenOut, "打开", "", false, func() { a.openHit(chosen[0], false) })
			toolButton(c, "hit-reveal", iconFolder, "打开所在位置", "", false, func() { a.openHit(chosen[0], true) })
		}
	}, func() {
		viewToggle(c, "search-view-toggle", &s.grid)
	})
	if len(s.groups) > 0 {
		a.searchPlaces(c)
	}
	if s.sort.stale() {
		a.mergeSearch()
	}
	loading := false
	for _, g := range s.groups {
		loading = loading || g.loading
	}
	zone := a.fileList(c, &fileView{
		key: "search-results", label: "搜索结果", items: s.items, sel: &s.sel, cursor: &s.cursor,
		list: &s.list, gridSt: &s.gridSt, grid: s.grid, sort: &s.sort,
		loading: loading, results: true,
		where: func(e fileindex.Entry) string {
			if h, ok := a.hitOf(e); ok {
				return a.hitWhere(h)
			}
			return e.Parent
		},
		thumb: func(e fileindex.Entry) *ui.Bitmap {
			if e.Root == 0 && a.local != nil {
				return a.localThumb(e)
			}
			return nil
		},
		open: func(e fileindex.Entry) {
			if h, ok := a.hitOf(e); ok {
				a.openHit(h, false)
			}
		},
		resort:   a.mergeSearch,
		gridLast: &s.gridLast,
		empty:    func() { a.searchEmpty(c) },
	})
	zone.ContextMenu(func(m *ui.Menu) {
		if n != 1 {
			return
		}
		if m.Item("打开").Chosen() {
			a.openHit(chosen[0], false)
		}
		if m.Item("打开所在位置").Chosen() {
			a.openHit(chosen[0], true)
		}
	})
}

// searchPlaces shows how many results each place found, and goes on in
// one, where all show, as it is clicked.
func (a *app) searchPlaces(c *ui.Context) {
	s := a.gsearch
	t := c.Theme()
	more := false
	ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
		for _, g := range s.groups {
			more = more || g.total > len(g.hits)
			chip := ui.ButtonBase(c.Key("place/"+g.source)).Padding(3, 10).Gap(6).AlignItems(ui.Center).Radius(99).
				Border(1, t.Border).Label("在" + g.name + "中查看全部").Tooltip("在" + g.name + "中查看全部结果")
			if chip.Hovered() {
				chip.Background(t.SurfaceHover)
			}
			chip.Children(func() {
				svg := iconServer
				if g.source == localPlace {
					svg = iconLaptop
				}
				ui.Icon(c, svg).FontSize(13).TextColor(t.TextMuted)
				ui.Text(c, g.name).FontSize(12).SingleLine()
				switch {
				case g.loading:
					ui.Spinner(c).FontSize(11).Label("正在搜索 " + g.name)
				case g.err != "":
					ui.Icon(c, iconAlert).FontSize(12).TextColor(t.Danger).Tooltip(g.err)
				default:
					ui.Text(c, groupDigits(int64(g.total))).FontSize(12).FontFeatures("tnum").TextColor(t.TextMuted)
				}
			})
			if chip.Clicked() && g.err == "" {
				a.narrowSearch(g.source)
			}
		}
		if more {
			ui.Text(c, fmt.Sprintf("每个位置先显示前 %d 项，点位置查看全部", searchPerPlace)).FontSize(12).TextColor(t.TextMuted)
		}
	})
}

// searchEmpty is what the page of the search shows without results: the
// recent searches, before any.
func (a *app) searchEmpty(c *ui.Context) {
	s := a.gsearch
	t := c.Theme()
	if strings.TrimSpace(s.query) != "" || s.filter.active() {
		emptyState(c, iconSearch, "没有找到匹配的文件", "换个关键词或筛选条件试试。本机的结果来自本机索引，文件有变化时需要更新索引。", nil)
		return
	}
	ui.Column(c).Fill().Center().Gap(10).Padding(32).Children(func() {
		ui.Icon(c, iconSearch).FontSize(36).TextColor(t.TextMuted.Alpha(0.6))
		ui.Text(c, "搜索本机和所有服务器").FontSize(16).Bold()
		ui.Text(c, "输入文件名的一部分，多个词用空格分开。任何页面按 Ctrl+F 都可以开始搜索。").FontSize(12).TextColor(t.TextMuted).TextAlign(ui.Center).MaxWidth(380)
		if recent := a.store.RecentSearches; len(recent) > 0 {
			ui.Text(c, "最近搜索").FontSize(12).Bold().TextColor(t.TextMuted).Margin(12, 0, 0, 0)
			ui.Row(c).Gap(6).Wrap().Justify(ui.Center).MaxWidth(480).Children(func() {
				for _, q := range recent {
					if ui.Button(c.Key("recent/"+q), q).Clicked() {
						s.query = q
						a.runSearch(0)
					}
				}
			})
		}
	})
}

// searchStatus is, in the status bar, how many results show, of how many.
func (a *app) searchStatus(c *ui.Context) {
	s := a.gsearch
	total := 0
	for _, g := range s.groups {
		total += g.total
	}
	text := ""
	switch {
	case len(s.groups) == 0:
	case total > len(s.hits):
		text = fmt.Sprintf("找到 %s 项 · 显示 %s 项", groupDigits(int64(total)), groupDigits(int64(len(s.hits))))
	default:
		text = fmt.Sprintf("找到 %s 项", groupDigits(int64(total)))
	}
	if n := s.sel.Len(); n > 0 {
		text += fmt.Sprintf(" · 已选 %d 项", n)
	}
	statusText(c, text)
}
