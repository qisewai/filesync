package main

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
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
	// The rest of the toolbar, as wide as it is.
	box := ui.Row(c.Key(key+"-box")).Grow(1).Shrink(1).MinWidth(260).AlignItems(ui.Center).Gap(6).
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

// The page of the search: of every place, or of one, this computer or a
// server, or of the folder the search began in. The scope is a tab of the
// page, which changes in place.

// globalSearch is the search, and its results.
type globalSearch struct {
	query  string
	filter searchFilter
	// scope is the tab shown: "all", "folder", localPlace or a server's ID.
	scope  string
	folder *searchFolder // the folder the search began in, if any
	groups []*searchGroup
	hits   []searchHit       // shown, in their order
	items  []fileindex.Entry // of the hits

	sel      ui.Selection[string]
	cursor   int
	list     ui.ListState
	gridSt   ui.GridState
	grid     bool
	gridLast int
	sort     tableSort
	awaitTop bool   // wait for the new results' scroll to commit
	opLabel  string // what an operation on results under way does

	gen      int
	debounce *time.Timer
	cancel   context.CancelFunc
}

// searchFolder is the folder a search began in: of this computer, or of a
// root of a server.
type searchFolder struct {
	source   string // localPlace, or a server's ID
	root     int64
	rootName string
	dir      string // the absolute path here, the path in the root there
	name     string
}

// busy reports whether the search waits for the typing, or for a place.
func (s *globalSearch) busy() bool {
	if s.debounce != nil || s.opLabel != "" {
		return true
	}
	for _, g := range s.groups {
		if g.loading || g.more {
			return true
		}
	}
	return false
}

// searchGroup is what a place found: the results read, page by page, and
// how many there are.
type searchGroup struct {
	key     string // the scope it is: "folder", localPlace or a server's ID
	source  string // localPlace, or a server's ID
	name    string
	hits    []searchHit
	total   int
	loading bool // the first page
	more    bool // a next page
	err     string

	local  *localindex.Results // this computer's results, which pages read
	client *fileindex.Client   // a server's, with the options of its search
	opts   fileindex.SearchOptions
	q      string
}

// searchHit is a result, and the place it is in.
type searchHit struct {
	source string
	e      fileindex.Entry
}

func (h searchHit) key() string {
	return fmt.Sprintf("%s\x00%d\x00%s", h.source, h.e.Root, h.e.Path)
}

// searchPage is how many results a place gives at once.
const searchPageSize = 200

// newSearch is the state of the search, made once.
func (a *app) newSearch() *globalSearch {
	if a.gsearch == nil {
		s := &globalSearch{cursor: -1, scope: "all"}
		// The table may ask for rows of the results it showed as a new
		// search empties them, before it shows them.
		s.list.Key = func(i int) any {
			if i < len(s.hits) {
				return s.hits[i].key()
			}
			return fmt.Sprint("\x00gone", i)
		}
		s.list.Selection = &s.sel
		s.list.Selected = &s.cursor
		s.sort.by = ui.SortOrder{Column: colName}
		s.list.Sort = &s.sort.by
		s.gridSt.Key = s.list.Key
		s.gridSt.Selection = &s.sel
		s.gridSt.Selected = &s.cursor
		a.gsearch = s
	}
	return a.gsearch
}

// openSearch shows the page of the search, searching q in the scope.
func (a *app) openSearch(q string) {
	s := a.newSearch()
	a.place = "search"
	if s.query != q || len(s.groups) == 0 {
		s.query = q
		a.runSearch(0)
	}
}

// searchFrom starts a search on the page of the files shown: of this
// computer or of the server shown, of the folder shown, or of every place.
func (a *app) searchFrom(q, scope string) {
	s := a.newSearch()
	s.folder = nil
	switch {
	case a.onLocal():
		s.folder = &searchFolder{source: localPlace, dir: a.local.dir, name: filepath.Base(a.local.dir)}
	case a.onRoot():
		b := a.br
		name := b.root.Name
		if b.path != "" {
			name = pathBase(b.path)
		}
		s.folder = &searchFolder{source: b.server, root: b.root.ID, rootName: b.root.Name, dir: b.path, name: name}
	}
	s.scope = scope
	s.filter = searchFilter{}
	s.groups = nil
	a.place = "search"
	s.query = q
	a.runSearch(0)
	a.focusSearch = true
}

// scopeName is the name of a scope of the search.
func (a *app) scopeName(scope string) string {
	s := a.gsearch
	switch scope {
	case "all":
		return "全部位置"
	case "folder":
		if s != nil && s.folder != nil {
			return "此文件夹 · " + s.folder.name
		}
		return "此文件夹"
	case localPlace:
		return "本机"
	}
	if sv := a.store.Server(scope); sv != nil {
		return sv.Title()
	}
	return scope
}

// setScope shows a tab of the search.
func (a *app) setScope(scope string) {
	s := a.gsearch
	if s.scope == scope {
		return
	}
	s.scope = scope
	s.sel.Clear()
	s.cursor = -1
	a.showResults()
	s.list.ScrollTo(0, ui.Start)
	s.awaitTop = true
}

// sortKey is how a search asks its results ordered.
func (s *globalSearch) sortKey() string {
	switch s.sort.by.Column {
	case colSize:
		return "size"
	case colTime:
		return "mtime"
	case colWhere:
		return "path"
	}
	return "name"
}

// runSearch searches, after a wait while typing: each place, for the first
// page of its results, and the folder the search began in.
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
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.groups = nil
		s.sel.Clear()
		s.cursor = -1
		s.sort.applied = s.sort.by
		s.list.ScrollTo(0, ui.Start)
		s.awaitTop = true
		if f := s.folder; f != nil {
			g := &searchGroup{key: "folder", source: f.source, name: a.scopeName("folder")}
			a.searchIn(ctx, g, q, f)
		}
		if a.ensureLocalIndex(); a.localIdx != nil {
			a.searchIn(ctx, &searchGroup{key: localPlace, source: localPlace, name: "本机"}, q, nil)
		}
		for _, sv := range a.store.Servers {
			if sv.Token != "" {
				a.searchIn(ctx, &searchGroup{key: sv.ID, source: sv.ID, name: sv.Title()}, q, nil)
			}
		}
		if s.scope == "folder" && s.folder == nil || s.scope != "all" && s.group(s.scope) == nil {
			s.scope = "all"
		}
		a.showResults()
	}
	if wait <= 0 {
		start()
	} else {
		s.debounce = time.AfterFunc(wait, func() { a.post(start) })
	}
}

// group is the group of a scope, nil for none.
func (s *globalSearch) group(key string) *searchGroup {
	for _, g := range s.groups {
		if g.key == key {
			return g
		}
	}
	return nil
}

// searchIn searches a place, in folder f if any, for the first page of its
// results.
func (a *app) searchIn(ctx context.Context, g *searchGroup, q string, f *searchFolder) {
	s := a.gsearch
	gen := s.gen
	g.loading, g.q = true, q
	s.groups = append(s.groups, g)
	done := func(hits []searchHit, total int, err error) {
		if a.closed || a.gsearch != s || s.gen != gen {
			return
		}
		g.loading = false
		switch {
		case errors.Is(err, context.Canceled):
		case errors.Is(err, fileindex.ErrAuth):
			g.err = "需要重新登录"
			if sv := a.store.Server(g.source); sv != nil {
				a.signedOut(sv)
			}
		case err != nil:
			g.err = connectError(err)
		default:
			g.total, g.hits = total, hits
		}
		a.showResults()
	}
	if g.source == localPlace {
		x := a.localIdx.index
		lq := localindex.Query{Text: q, Sort: s.sortKey(), Desc: s.sort.by.Descending}
		s.filter.local(&lq)
		if f != nil {
			lq.Dir = f.dir
		}
		go func() {
			res, err := x.Search(ctx, lq)
			var es []localindex.Entry
			if err == nil {
				es, err = res.Page(ctx, 0, searchPageSize)
			}
			a.post(func() {
				g.local = res
				done(localHits(es), res.Total(), err)
			})
		}()
		return
	}
	sv := a.store.Server(g.source)
	if sv == nil {
		g.loading, g.err = false, "服务器已删除"
		return
	}
	g.client = fileindex.New(sv.Addr, sv.Token)
	g.opts = fileindex.SearchOptions{Page: 1, Size: searchPageSize, Sort: s.sortKey(), Desc: s.sort.by.Descending}
	s.filter.nas(&g.opts)
	if f != nil {
		g.opts.Root, g.opts.Dir = f.root, f.dir
	}
	c, o := g.client, g.opts
	go func() {
		items, total, err := c.Search(ctx, q, o)
		a.post(func() { done(serverHits(g.source, items), total, err) })
	}()
}

// localEntries are entries of the index of this computer as entries of
// the files shown.
func localEntries(es []localindex.Entry) []fileindex.Entry {
	out := make([]fileindex.Entry, len(es))
	for i, e := range es {
		out[i] = fileindex.Entry{Name: e.Name, Path: e.Path, Parent: e.Parent, Size: e.Size, MTime: e.MTime, IsDir: e.IsDir, Ext: e.Ext}
	}
	return out
}

func localHits(es []localindex.Entry) []searchHit {
	out := make([]searchHit, 0, len(es))
	for _, e := range localEntries(es) {
		out = append(out, searchHit{localPlace, e})
	}
	return out
}

func serverHits(server string, es []fileindex.Entry) []searchHit {
	out := make([]searchHit, 0, len(es))
	for _, e := range es {
		out = append(out, searchHit{server, e})
	}
	return out
}

// moreResults reads the next page of the results of the place shown, as
// the table nears their end.
func (a *app) moreResults() {
	s := a.gsearch
	g := s.group(s.scope)
	if g == nil || g.loading || g.more || g.err != "" || len(g.hits) >= g.total {
		return
	}
	g.more = true
	gen, offset := s.gen, len(g.hits)
	finish := func(hits []searchHit, err error) {
		if a.closed || a.gsearch != s || s.gen != gen {
			return
		}
		g.more = false
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				g.err = err.Error()
			}
			return
		}
		g.hits = append(g.hits, hits...)
		a.showResults()
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	if g.local != nil {
		res := g.local
		go func() {
			defer cancel()
			es, err := res.Page(ctx, offset, searchPageSize)
			a.post(func() { finish(localHits(es), err) })
		}()
		return
	}
	c, o := g.client, g.opts
	o.Page = offset/searchPageSize + 1
	go func() {
		defer cancel()
		items, _, err := c.Search(ctx, g.q, o)
		a.post(func() { finish(serverHits(g.source, items), err) })
	}()
}

// showResults puts the results of the scope shown in the table: of one
// place, as it ordered them, or of every place, the first of each, in the
// order of the table.
func (a *app) showResults() {
	s := a.gsearch
	s.hits = s.hits[:0]
	if g := s.group(s.scope); g != nil {
		s.hits = append(s.hits, g.hits...)
	} else {
		for _, g := range s.groups {
			if g.key != "folder" {
				s.hits = append(s.hits, g.hits...)
			}
		}
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
	}
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

// scopeMenu lists the scopes of the search, choosing one.
func (a *app) scopeMenu(m *ui.Menu, scope string, choose func(string)) {
	var scopes []string
	if s := a.gsearch; s != nil && s.folder != nil && a.onSearch() || a.onLocal() || a.onRoot() {
		scopes = append(scopes, "folder")
	}
	scopes = append(scopes, "all", localPlace)
	for _, sv := range a.store.Servers {
		if sv.Token != "" {
			scopes = append(scopes, sv.ID)
		}
	}
	for _, k := range scopes {
		name := a.scopeName(k)
		if k == "folder" && !a.onSearch() {
			name = "此文件夹"
		}
		if m.Item(name).Checked(k == scope).Chosen() {
			choose(k)
		}
	}
}

// fileSearch is the field of the toolbar of the pages of files: typing in
// it starts a search on the page of the search, of the place shown unless
// its chip chose another scope.
func (a *app) fileSearch(c *ui.Context, place string) {
	scope := a.searchStart
	if scope == "" || scope != "all" && scope != "folder" {
		scope = place
	}
	q := ""
	label, placeholder := "本机搜索", "搜索本机文件"
	if place != localPlace {
		label, placeholder = "搜索文件", "搜索"+a.scopeName(place)+"上的文件"
	}
	name := a.scopeName(scope)
	if scope == "folder" {
		name = "此文件夹"
	}
	changed, submitted, _ := a.searchBox(c, "file-search", label, &q, name, placeholder, func(m *ui.Menu) {
		a.scopeMenu(m, scope, func(k string) {
			a.searchStart = k
			if k != "folder" && k != "all" {
				a.searchStart = ""
			}
			if k == "all" || k != place && k != "folder" {
				// Another place: there is nothing of it here to search.
				a.searchFrom("", k)
			}
		})
	})
	if changed || submitted {
		a.searchFrom(q, scope)
	}
}

// searchHeader is, in the toolbar of the page of the search, its field.
func (a *app) searchHeader(c *ui.Context) {
	s := a.gsearch
	changed, submitted, cleared := a.searchBox(c, "global-search", "全局搜索", &s.query, a.scopeName(s.scope), "搜索文件名", func(m *ui.Menu) {
		a.scopeMenu(m, s.scope, a.setScope)
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

// searchPage shows what the search found.
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
	acts := a.hitActions(chosen)
	if n > 0 && !a.searchTyping && a.deleting == nil {
		switch {
		case c.Shortcut(0, ui.KeyEscape):
			s.sel.Clear()
		case acts.writable && c.Shortcut(ui.Shift, ui.KeyDelete):
			a.askDeleteIn(acts.files, es, true)
		case acts.writable && c.Shortcut(0, ui.KeyDelete):
			a.askDeleteIn(acts.files, es, false)
		case acts.writable && n == 1 && c.Shortcut(0, ui.KeyF2):
			a.renameIn(acts.files, es[0])
		}
	}
	if len(s.groups) > 0 {
		a.searchScopes(c)
	}
	a.indexNotice(c)
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
			iconButton(c, "hit-reveal", iconFolder, "打开所在位置", "打开所在位置", false, func() { a.openHit(chosen[0], true) })
		}
		switch {
		case acts.files != nil:
			toolButton(c, "hit-download", iconSave, "下载…", "下载到本机", false, func() { a.downloadIn(acts.files, es) })
			toolButton(c, "hit-copy", iconCopy, "复制到…", "复制到这台服务器的其他位置", false, func() { a.transferIn(acts.files, false, es) })
			toolButton(c, "hit-move", iconMove, "移动到…", "", !acts.writable, func() { a.transferIn(acts.files, true, es) })
			if n == 1 {
				toolButton(c, "hit-rename", iconRename, "重命名", "重命名（F2）", !acts.writable, func() { a.renameIn(acts.files, es[0]) })
			}
			toolButton(c, "hit-delete", iconTrash, "删除", "移到回收站（Delete），或彻底删除（Shift+Delete）", !acts.writable, func() { a.askDeleteIn(acts.files, es, false) })
		case acts.local:
			toolButton(c, "hit-backup", iconBackup, "创建备份任务…", "把选中的项备份到 NAS", false, func() { a.backupLocal("", es) })
		}
	}, func() {
		viewToggle(c, "search-view-toggle", &s.grid)
	})
	loading := false
	for _, g := range s.groups {
		if s.scope == "all" && g.key != "folder" || g.key == s.scope {
			loading = loading || g.loading
		}
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
			if h, ok := a.hitOf(e); ok {
				return a.hitThumb(h)
			}
			return nil
		},
		open: func(e fileindex.Entry) {
			if h, ok := a.hitOf(e); ok {
				a.openHit(h, false)
			}
		},
		resort: func() { a.runSearch(0) },
		visible: func(first, last int) {
			if s.awaitTop && (first == 0 || last < 0) {
				s.awaitTop = false
			}
			if !s.awaitTop && s.scope != "all" && last >= len(s.items)-40 {
				a.moreResults()
			}
		},
		gridLast: &s.gridLast,
		empty:    func() { a.searchEmpty(c) },
	})
	zone.ContextMenu(func(m *ui.Menu) {
		if n == 1 {
			if m.Item("打开").Chosen() {
				a.openHit(chosen[0], false)
			}
			if m.Item("打开所在位置").Chosen() {
				a.openHit(chosen[0], true)
			}
			m.Separator()
		}
		switch {
		case acts.files != nil:
			if m.Item("下载…").Chosen() {
				a.downloadIn(acts.files, es)
			}
			if m.Item("复制到…").Chosen() {
				a.transferIn(acts.files, false, es)
			}
			if m.Item("移动到…").Disabled(!acts.writable).Chosen() {
				a.transferIn(acts.files, true, es)
			}
			if n == 1 && m.Item("重命名…").Disabled(!acts.writable).Chosen() {
				a.renameIn(acts.files, es[0])
			}
			m.Separator()
			if m.Item("删除…").Disabled(!acts.writable).Chosen() {
				a.askDeleteIn(acts.files, es, false)
			}
			if m.Item("彻底删除…").Disabled(!acts.writable).Chosen() {
				a.askDeleteIn(acts.files, es, true)
			}
		case acts.local:
			if m.Item("创建备份任务…").Chosen() {
				a.backupLocal("", es)
			}
			if n == 1 && m.Item("在文件管理器中显示").Chosen() {
				a.openPath(filepath.Dir(es[0].Path))
			}
		}
	})
}

// hitChoice is what the results chosen can be acted on with: the files of
// a server, of one root, writable or not, or of this computer.
type hitChoice struct {
	files    *nasFiles
	writable bool
	local    bool
}

func (a *app) hitActions(chosen []searchHit) hitChoice {
	var out hitChoice
	if len(chosen) == 0 {
		return out
	}
	first := chosen[0]
	for _, h := range chosen {
		if h.source != first.source || h.source != localPlace && h.e.Root != first.e.Root {
			return out // of several places: each in its own
		}
	}
	if first.source == localPlace {
		out.local = true
		return out
	}
	out.files = a.searchFiles(first.source, first.e.Root, first.e.Parent)
	for _, r := range a.roots[first.source] {
		if r.ID == first.e.Root {
			out.writable = !r.ReadOnly && a.gsearch.opLabel == ""
		}
	}
	return out
}

// searchFiles are files of a server found by the search, which searches
// again as they change.
func (a *app) searchFiles(server string, root int64, dir string) *nasFiles {
	s := a.gsearch
	return &nasFiles{
		server:  server,
		root:    root,
		dir:     dir,
		running: func() bool { return s.opLabel != "" },
		busy:    func(label string) { s.opLabel = label },
		fail:    func(msg string) { a.errorMsg = msg },
		changed: func(string) {
			if a.gsearch == s {
				a.runSearch(0)
			}
		},
		clear: func() { s.sel.Clear() },
	}
}

// indexNotice tells, on the page of the search, that the files of this
// computer cannot be found before their index is built.
func (a *app) indexNotice(c *ui.Context) {
	m := a.localIdx
	if m == nil || m.status.Count > 0 || m.running || m.loading {
		return
	}
	t := c.Theme()
	ui.Row(c).Gap(10).AlignItems(ui.Center).Padding(8, 10, 8, 12).Radius(t.Radius * 2).Background(t.Accent.Alpha(0.07)).Children(func() {
		ui.Icon(c, iconLaptop).FontSize(15).TextColor(t.Accent)
		ui.Text(c, "本机还没有建立索引，搜不到本机的文件。建立索引只读取文件名、大小和修改时间。").FontSize(12).Grow(1).Shrink(1)
		ui.Button(c, "索引范围…").OnClick(a.editLocalIndex)
		ui.PrimaryButton(c, "建立索引").OnClick(a.startLocalScan)
	})
}

// hitThumb is the thumbnail of a result, of a picture of this computer or
// of a server.
func (a *app) hitThumb(h searchHit) *ui.Bitmap {
	if h.source == localPlace {
		return a.localThumb(h.e)
	}
	return a.serverThumb(h.source, h.e)
}

// searchScopes are the tabs of the scopes of the search, with how many
// each found; a click shows one, in place.
func (a *app) searchScopes(c *ui.Context) {
	s := a.gsearch
	t := c.Theme()
	ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
		tab := func(key, name string, svg *ui.SVG, total int, loading bool, err string) {
			on := s.scope == key
			chip := ui.ButtonBase(c.Key("scope/"+key)).Padding(4, 12).Gap(6).AlignItems(ui.Center).Radius(99).
				Border(1, t.Border).Label("搜索范围 " + name)
			switch {
			case on:
				chip.Background(t.Accent.Alpha(0.12)).BorderColor(t.Accent.Alpha(0.5))
			case chip.Hovered():
				chip.Background(t.SurfaceHover)
			}
			chip.Children(func() {
				color := t.TextMuted
				if on {
					color = t.Accent
				}
				ui.Icon(c, svg).FontSize(13).TextColor(color)
				label := ui.Text(c, name).FontSize(12).SingleLine()
				if on {
					label.TextColor(t.Accent).Bold()
				}
				switch {
				case loading:
					ui.Spinner(c).FontSize(11).Label("正在搜索 " + name)
				case err != "":
					ui.Icon(c, iconAlert).FontSize(12).TextColor(t.Danger).Tooltip(err)
				default:
					rolling(ui.Text(c, groupDigits(int64(total))).FontSize(12).FontFeatures("tnum").TextColor(color))
				}
			})
			if chip.Clicked() {
				a.setScope(key)
			}
		}
		if g := s.group("folder"); g != nil {
			tab("folder", g.name, iconFolder, g.total, g.loading, g.err)
		}
		total, loading := 0, false
		for _, g := range s.groups {
			if g.key != "folder" {
				total += g.total
				loading = loading || g.loading
			}
		}
		tab("all", "全部位置", iconSearch, total, loading, "")
		for _, g := range s.groups {
			if g.key == "folder" {
				continue
			}
			svg := iconServer
			if g.source == localPlace {
				svg = iconLaptop
			}
			tab(g.key, g.name, svg, g.total, g.loading, g.err)
		}
	})
	if s.scope == "all" {
		for _, g := range s.groups {
			if g.key != "folder" && g.total > len(g.hits) {
				ui.Text(c, fmt.Sprintf("“全部位置”中每处先显示前 %d 项，选择一个位置查看它的全部结果。", searchPageSize)).FontSize(12).TextColor(t.TextMuted)
				break
			}
		}
	}
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
		if s.scope == "all" && g.key != "folder" || g.key == s.scope {
			total += g.total
		}
	}
	text := ""
	switch {
	case len(s.groups) == 0:
	case total > len(s.hits):
		text = fmt.Sprintf("找到 %s 项 · 已显示 %s 项", groupDigits(int64(total)), groupDigits(int64(len(s.hits))))
	default:
		text = fmt.Sprintf("找到 %s 项", groupDigits(int64(total)))
	}
	if n := s.sel.Len(); n > 0 {
		text += fmt.Sprintf(" · 已选 %d 项", n)
	}
	if s.opLabel != "" {
		ui.Spinner(c).FontSize(12).Label(s.opLabel)
		ui.Text(c, s.opLabel).FontSize(12).TextColor(c.Theme().Accent)
	}
	statusText(c, text)
}
