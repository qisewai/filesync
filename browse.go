package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"filesync/config"
	"filesync/fileindex"

	"github.com/egoist/mygo/ui"
)

const requestTimeout = 20 * time.Second

// browser shows a directory of a root of a server, or the results of a
// search there.
type browser struct {
	server string // the server's ID
	root   fileindex.Root
	path   string // in the root, with slashes

	items     []fileindex.Entry
	loading   bool
	operating bool
	err       string
	gen       int // drops the answers of loads a newer one replaced

	live bool // list from the disk rather than the index
	grid bool

	query    string
	filter   searchFilter
	here     bool        // the search is of the folder shown, not of all the server
	debounce *time.Timer // the search as typed, after a moment
	typing   bool        // the search has the keyboard focus
	results  bool        // items are search results
	total    int         // of the results, which may be more than shown
	// page is how many pages of the results are shown; more is set while
	// the next loads, which moreCancel stops.
	page       int
	more       bool
	moreCancel context.CancelFunc

	sel       ui.Selection[string] // by path
	cursor    int
	list      ui.ListState
	sort      tableSort
	gridState ui.GridState
	gridLast  int // the last item the grid built

	// dialogs
	naming   bool
	newName  string
	renaming bool
	renameOf string
	renameIn int64 // the root of renameOf
	renameTo string
	// toDelete are the entries the delete dialog asks about, nil while
	// it is closed; forever asks to delete them, not to the trash.
	toDelete  []fileindex.Entry
	forever   bool
	opLabel   string // what the operation under way does
	confirm   bool
	confirmT  string
	confirmM  string
	confirmOK string
	onConfirm func()

	refreshPending bool
	cancel         context.CancelFunc
	keepSelection  map[string]bool
	thumbCtx       context.Context
	thumbCancel    context.CancelFunc
}

// openRoot shows a root of a server.
func (a *app) openRoot(server string, root fileindex.Root) {
	if a.br != nil && a.br.cancel != nil {
		a.br.cancel()
	}
	if a.br != nil && a.br.moreCancel != nil {
		a.br.moreCancel()
	}
	if a.br != nil && a.br.thumbCancel != nil {
		a.br.thumbCancel()
	}
	a.br = &browser{server: server, root: root, cursor: -1}
	b := a.br
	a.br.list.Key = func(i int) any { return b.items[i].Path }
	a.br.list.Selection = &a.br.sel
	a.br.list.Selected = &a.br.cursor
	a.br.sort.by = ui.SortOrder{Column: colName}
	a.br.list.Sort = &a.br.sort.by
	a.br.gridState.Key = a.br.list.Key
	a.br.gridState.Selection = &a.br.sel
	a.br.gridState.Selected = &a.br.cursor
	a.browse("")
}

func (a *app) brClient() (*fileindex.Client, *config.Server) {
	if a.br == nil {
		return nil, nil
	}
	sv := a.store.Server(a.br.server)
	if sv == nil {
		return nil, nil
	}
	return fileindex.New(sv.Addr, sv.Token), sv
}

// browse shows the directory p of the root.
func (a *app) browse(p string, choose ...string) {
	b := a.br
	if b.thumbCancel != nil {
		b.thumbCancel()
	}
	b.thumbCtx, b.thumbCancel = context.WithCancel(context.Background())
	a.thumbs = map[string]*thumbOf{}
	if b.path != p || b.results {
		b.items = nil
		b.sel.Clear()
		b.cursor = -1
	}
	if b.debounce != nil {
		b.debounce.Stop()
		b.debounce = nil
	}
	if b.results {
		b.filter = searchFilter{}
	}
	b.path, b.results, b.query = p, false, ""
	a.revealDirectory(b)
	root, live := b.root.ID, b.live
	a.load(func(ctx context.Context, c *fileindex.Client) ([]fileindex.Entry, int, error) {
		items, err := c.List(ctx, root, p, live)
		return items, len(items), err
	}, choose...)
}

// browseIn shows a directory of a root of the server shown: of the root
// shown, or of another, which a search of all the server found.
func (a *app) browseIn(root int64, p string, choose ...string) {
	b := a.br
	if root == 0 || root == b.root.ID {
		a.browse(p, choose...)
		return
	}
	for _, r := range a.roots[b.server] {
		if r.ID == root {
			a.showDirectory(b.server, r, p, choose...)
			return
		}
	}
}

// load replaces the items with what fetch gets, off the main thread.
func (a *app) load(fetch func(ctx context.Context, c *fileindex.Client) ([]fileindex.Entry, int, error), choose ...string) {
	b := a.br
	c, sv := a.brClient()
	if c == nil {
		b.err = "服务器已删除"
		return
	}
	b.gen++
	if b.cancel != nil {
		b.cancel()
	}
	if b.moreCancel != nil {
		b.moreCancel()
	}
	b.more = false
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	b.cancel = cancel
	gen := b.gen
	b.loading, b.err = true, ""
	b.keepSelection = nil
	go func() {
		defer cancel()
		items, total, err := fetch(ctx, c)
		a.post(func() {
			if a.closed || a.br != b || b.gen != gen || sv.Token != c.Token || sv.Addr != c.Addr {
				return
			}
			b.loading = false
			if errors.Is(err, fileindex.ErrAuth) {
				b.err = "登录已过期，请重新登录"
				a.errorMsg = b.err
				a.signedOut(sv)
				return
			}
			if err != nil {
				b.err = err.Error()
				return
			}
			if !b.results {
				a.cacheDirectories(b.server, b.root.ID, b.path, items)
				sortEntries(&b.sort, items, nil)
			}
			b.items, b.total = items, total
			b.sel.Clear()
			b.cursor = -1
			for i, e := range items {
				if b.keepSelection[e.Path] {
					b.sel.Add(e.Path)
					b.cursor = i
				}
				if len(choose) > 0 && e.Name == choose[0] {
					b.sel.Add(e.Path)
					b.cursor = i
					b.list.ScrollIntoView(i)
				}
			}
		})
	}()
}

func (a *app) refresh() {
	b := a.br
	if b.results {
		a.search(b.query)
		return
	}
	a.browse(b.path)
}

// refreshSoon refreshes once events stop coming for a moment, as many come
// while files are written.
func (a *app) refreshSoon() {
	b := a.br
	if b.refreshPending {
		return
	}
	b.refreshPending = true
	go func() {
		time.Sleep(700 * time.Millisecond)
		a.post(func() {
			b.refreshPending = false
			if !a.closed && a.br == b && !b.loading && !b.naming && !b.renaming {
				a.reloadKeeping()
			}
		})
	}()
}

// reloadKeeping lists the directory again, keeping the choice.
func (a *app) reloadKeeping() {
	b := a.br
	if b.results {
		a.search(b.query)
		return
	}
	kept := map[string]bool{}
	for p := range b.sel.All() {
		kept[p] = true
	}
	p := b.path
	root, live := b.root.ID, b.live
	a.load(func(ctx context.Context, c *fileindex.Client) ([]fileindex.Entry, int, error) {
		items, err := c.List(ctx, root, p, live)
		return items, len(items), err
	})
	b.keepSelection = kept
}

func (a *app) search(q string) {
	b := a.br
	q = strings.TrimSpace(q)
	if b.debounce != nil {
		b.debounce.Stop()
		b.debounce = nil
	}
	if q == "" && !b.filter.active() {
		a.browse(b.path)
		return
	}
	b.results, b.query, b.page = true, q, 1
	b.list.ScrollTo(0, ui.Start) // the first page, from its top
	// The server orders the results, all of them, before it pages them.
	b.sort.applied = b.sort.by
	opts := a.searchOptions(1)
	a.load(func(ctx context.Context, c *fileindex.Client) ([]fileindex.Entry, int, error) {
		return c.Search(ctx, q, opts)
	})
}

// searchOptions ask for a page of the results of the search shown, in the
// order of the table.
func (a *app) searchOptions(page int) fileindex.SearchOptions {
	b := a.br
	o := fileindex.SearchOptions{Desc: b.sort.by.Descending, Page: page, Size: fileindex.SearchPageSize}
	if b.here {
		o.Root, o.Dir = b.root.ID, b.path
	}
	b.filter.nas(&o)
	switch b.sort.by.Column {
	case colSize:
		o.Sort = "size"
	case colTime:
		o.Sort = "mtime"
	case colWhere:
		o.Sort = "path"
	default:
		o.Sort = "name"
	}
	return o
}

// loadMore adds the next page of the results of the search, if any.
func (a *app) loadMore() {
	b := a.br
	if b == nil || !b.results || b.loading || b.more || len(b.items) >= b.total {
		return
	}
	c, sv := a.brClient()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	b.more, b.moreCancel = true, cancel
	gen, page, q, opts := b.gen, b.page+1, b.query, a.searchOptions(b.page+1)
	go func() {
		defer cancel()
		items, total, err := c.Search(ctx, q, opts)
		a.post(func() {
			if a.closed || a.br != b || b.gen != gen {
				return
			}
			b.more = false
			switch {
			case errors.Is(err, fileindex.ErrAuth):
				b.err = "登录已过期，请重新登录"
				a.signedOut(sv)
			case err != nil:
				b.err = "无法加载更多结果: " + err.Error()
			default:
				// What moved between pages as the index changed shows once.
				seen := make(map[string]bool, len(b.items))
				for _, e := range b.items {
					seen[e.Path] = true
				}
				for _, e := range items {
					if !seen[e.Path] {
						b.items = append(b.items, e)
						seen[e.Path] = true
					}
				}
				b.page, b.total = page, total
				if len(items) == 0 {
					b.total = len(b.items) // the server has no more after all
				}
			}
		})
	}()
}

// chosen returns the entries chosen, in the order shown.
func (b *browser) chosen() []fileindex.Entry {
	var out []fileindex.Entry
	for _, e := range b.items {
		if b.sel.Has(e.Path) {
			out = append(out, e)
		}
	}
	return out
}

func paths(es []fileindex.Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Path
	}
	return out
}

// researchNAS searches again, as the scope changed, when it searches.
func (a *app) researchNAS() {
	if b := a.br; b.results || strings.TrimSpace(b.query) != "" {
		a.search(b.query)
	}
}

// browserPath is, under the toolbar, where the page is.
func (a *app) browserPath(c *ui.Context) {
	b := a.br
	crumbs := []string{b.root.Name}
	if b.path != "" {
		crumbs = append(crumbs, strings.Split(b.path, "/")...)
	}
	chosen := len(crumbs) - 1
	if b.results {
		crumbs = append(crumbs, "搜索“"+b.query+"”")
		chosen = len(crumbs) - 1
	}
	if ui.Breadcrumbs(c, crumbs, &chosen).Label("位置").Shrink(1).MinWidth(0).Changed() {
		if chosen == 0 {
			a.browse("")
		} else if chosen <= strings.Count(b.path, "/")+1 && b.path != "" {
			a.browse(strings.Join(crumbs[1:chosen+1], "/"))
		} else {
			a.browse(b.path)
		}
	}
	if b.root.ReadOnly {
		ui.Badge(c, "只读")
	}
}

// browserSearch is, in the toolbar, the search of the server.
func (a *app) browserSearch(c *ui.Context) {
	b := a.br
	server := "服务器"
	if sv := a.store.Server(b.server); sv != nil {
		server = sv.Title()
	}
	scope := server
	if b.here {
		scope = "此文件夹"
	}
	changed, submitted, cleared := a.searchBox(c, "search", "搜索文件", &b.query, scope, "搜索"+server+"上的文件", func(m *ui.Menu) {
		if m.Item(server+"（全部目录）").Checked(!b.here).Chosen() && b.here {
			b.here = false
			a.researchNAS()
		}
		if m.Item("当前文件夹").Checked(b.here).Chosen() && !b.here {
			b.here = true
			a.researchNAS()
		}
		m.Separator()
		if m.Item("全部位置").Chosen() {
			q, f := b.query, b.filter
			a.openSearch(q)
			a.gsearch.filter = f
		}
	})
	b.typing = a.searchTyping
	empty := strings.TrimSpace(b.query) == ""
	switch {
	case cleared, changed && empty:
		if b.results {
			a.browse(b.path)
		}
	case changed:
		// As typed, after a moment: the server answers each.
		if b.debounce != nil {
			b.debounce.Stop()
		}
		b.debounce = time.AfterFunc(300*time.Millisecond, func() {
			a.post(func() {
				if !a.closed && a.br == b && b.debounce != nil {
					b.debounce = nil
					a.search(b.query)
				}
			})
		})
	case submitted && (!empty || b.filter.active()):
		a.remember(b.query)
		a.search(b.query)
	}
}

// browserView is the page of a root: its commands, and its files.
func (a *app) browserView(c *ui.Context) {
	b := a.br
	t := c.Theme()
	writable := !b.root.ReadOnly && !b.loading && !b.operating

	// Not while typing the search, in the toolbar, built before.
	typing := b.typing
	es := b.chosen()
	if b.loading || b.operating {
		es = nil
	}
	n := len(es)
	// A selection of the results of all the server may span roots, which
	// the operations do not.
	oneRoot := true
	for _, e := range es {
		oneRoot = oneRoot && e.Root == es[0].Root
	}
	// Delete asks to move the files chosen to the trash, Shift+Delete to
	// delete them for good, F2 renames, Escape lets go of them; not while
	// typing the search. (Dialogs over the page take the keys themselves.)
	if !typing && b.toDelete == nil {
		switch {
		case n > 0 && writable && oneRoot && c.Shortcut(ui.Shift, ui.KeyDelete):
			a.askDelete(es, true)
		case n > 0 && writable && oneRoot && c.Shortcut(0, ui.KeyDelete):
			a.askDelete(es, false)
		case n == 1 && writable && c.Shortcut(0, ui.KeyF2):
			a.startRename(es[0])
		case n > 0 && c.Shortcut(0, ui.KeyEscape):
			b.sel.Clear()
		}
	}
	if c.Shortcut(0, ui.KeyF5) {
		a.refresh()
	}
	commandBar(c, n, filesSize(es), func() { b.sel.Clear() }, func() {
		if b.results {
			filterBar(c, "nas", &b.filter, func(wait bool) {
				if wait {
					if b.debounce != nil {
						b.debounce.Stop()
					}
					b.debounce = time.AfterFunc(300*time.Millisecond, func() {
						a.post(func() {
							if !a.closed && a.br == b && b.debounce != nil {
								b.debounce = nil
								a.search(b.query)
							}
						})
					})
				} else {
					a.search(b.query)
				}
			})
			return
		}
		toolButton(c, "new-folder", iconNewDir, "新建文件夹", "", !writable || b.results, func() {
			b.naming, b.newName = true, ""
		})
		up := toolButton(c, "upload", iconUpload, "上传", "上传文件或文件夹；也可以把它们拖进窗口", !writable || b.results, func() {})
		up.Menu(func(m *ui.Menu) {
			if m.Item("上传文件…").Chosen() {
				a.pickUpload(false)
			}
			if m.Item("上传文件夹…").Chosen() {
				a.pickUpload(true)
			}
		})
	}, func() {
		tip := func(s string) string {
			if !oneRoot {
				return "选中的项来自不同的索引目录，请分别操作"
			}
			return s
		}
		toolButton(c, "download", iconSave, "下载…", tip("下载到本机"), !oneRoot, func() { a.download(es) })
		toolButton(c, "copy-to", iconCopy, "复制到…", tip("复制到这台服务器的其他位置"), !oneRoot, func() { a.transferTo(false, es) })
		toolButton(c, "move-to", iconMove, "移动到…", tip(""), !writable || !oneRoot, func() { a.transferTo(true, es) })
		if n == 1 {
			toolButton(c, "rename", iconRename, "重命名", "重命名（F2）", !writable, func() { a.startRename(es[0]) })
		}
		toolButton(c, "delete", iconTrash, "删除", tip("移到回收站（Delete），或彻底删除（Shift+Delete）"), !writable || !oneRoot, func() { a.askDelete(es, false) })
	}, func() {
		viewToggle(c, "view-toggle", &b.grid)
		moreMenu(c, "more", func(m *ui.Menu) {
			if m.Item("实时读取硬盘").Checked(b.live).Chosen() {
				b.live = !b.live
				a.refresh()
			}
			if m.Item("刷新").Shortcut(0, ui.KeyF5).Chosen() {
				a.refresh()
			}
		})
	})

	if b.err != "" {
		notice(c, t.Danger, b.err)
	}

	// The files, which take files dropped on them.
	zone := a.fileList(c, &fileView{
		key: "files", label: "文件", items: b.items, sel: &b.sel, cursor: &b.cursor,
		list: &b.list, gridSt: &b.gridState, grid: b.grid, sort: &b.sort,
		loading: b.loading, results: b.results,
		where: func(e fileindex.Entry) string { return path.Join(e.RootName, e.Parent) },
		thumb: a.thumb,
		open:  a.openEntry,
		resort: func() {
			if b.results {
				a.search(b.query) // sorted by the server, from the first page
			} else {
				sortEntries(&b.sort, b.items, &b.cursor)
			}
		},
		// Scrolled near the end of the results loaded, the next page loads.
		visible: func(_, last int) {
			if b.results && last >= len(b.items)-40 {
				a.loadMore()
			}
		},
		gridLast: &b.gridLast,
		empty: func() {
			switch {
			case b.results:
				emptyState(c, iconSearch, "没有找到", "换个关键词，或者到上一级文件夹里搜索。", nil)
			case b.root.ReadOnly:
				emptyFolder(c, "")
			default:
				emptyFolder(c, "可以把文件拖到这里上传")
			}
		},
	})
	if writable && !b.results {
		if files := zone.DroppedFiles(); files != nil {
			a.upload(files)
		}
		if zone.FileDragOver() {
			zone.Border(2, t.Accent)
		}
	}
	zone.ContextMenu(func(m *ui.Menu) { a.entryMenu(m, b.chosen(), writable) })

	a.browserDialogs(c)
}

// browserStatus is, in the status bar, what is under way, what the page
// shows and has chosen.
func (a *app) browserStatus(c *ui.Context) {
	b := a.br
	t := c.Theme()
	es := b.chosen()
	if b.loading || b.operating {
		es = nil
	}
	n := len(es)
	status := fmt.Sprintf("%d 项", len(b.items))
	if b.results {
		status = fmt.Sprintf("找到 %s 项", groupDigits(int64(b.total)))
		if b.total > len(b.items) {
			status += fmt.Sprintf("，已显示 %s 项", groupDigits(int64(len(b.items))))
		}
	}
	if n > 0 {
		var size int64
		for _, e := range es {
			size += e.Size
		}
		status += fmt.Sprintf(" · 已选 %d 项", n)
		if size > 0 {
			status += "，" + formatBytes(size)
		}
	}
	// What is under way, which can take a while on a large folder.
	busy := b.opLabel
	switch {
	case busy != "":
	case b.loading && b.results:
		busy = "正在搜索…"
	case b.loading && len(b.items) > 0:
		busy = "正在读取…"
	}
	if busy != "" {
		ui.Spinner(c).FontSize(12).Label(busy)
		ui.Text(c, busy).FontSize(12).TextColor(t.Accent)
	}
	statusText(c, status)
	if b.results && !b.loading && b.total > len(b.items) {
		label := fmt.Sprintf("加载更多（还有 %s 项）", groupDigits(int64(b.total-len(b.items))))
		if b.more {
			label = "正在加载…"
		}
		ui.Button(c.Key("load-more"), label).Disabled(b.more).OnClick(a.loadMore)
	}
}

func (a *app) entryMenu(m *ui.Menu, es []fileindex.Entry, writable bool) {
	b := a.br
	n := len(es)
	if n == 1 && m.Item("打开").Chosen() {
		a.openEntry(es[0])
	}
	if n == 1 && !es[0].IsDir {
		if m.Item("系统程序打开").Chosen() {
			if c, sv := a.brClient(); c != nil {
				a.systemOpenSource(sv.ID, c, es[0], false)
			}
		}
		if mediaFile(es[0]) && m.Item("下载后打开").Chosen() {
			if c, sv := a.brClient(); c != nil {
				a.systemOpenSource(sv.ID, c, es[0], true)
			}
		}
	}
	if b.results && n == 1 && m.Item("打开所在位置").Chosen() {
		a.browse(es[0].Parent, es[0].Name)
	}
	if m.Item("下载…").Disabled(n == 0).Chosen() {
		a.download(es)
	}
	m.Separator()
	if m.Item("复制到…").Disabled(n == 0).Chosen() {
		a.transferTo(false, es)
	}
	if m.Item("移动到…").Disabled(n == 0 || !writable).Chosen() {
		a.transferTo(true, es)
	}
	if m.Item("重命名").Disabled(n != 1 || !writable).Chosen() {
		a.startRename(es[0])
	}
	m.Separator()
	if m.Item("删除…").Disabled(n == 0 || !writable).Chosen() {
		a.askDelete(es, false)
	}
	if m.Item("彻底删除…").Disabled(n == 0 || !writable).Chosen() {
		a.askDelete(es, true)
	}
}

// fileColumns are the columns of a table of files, each sorting it.
func fileColumns() []ui.TableColumn {
	return []ui.TableColumn{
		{Title: colName, Sortable: true},
		{Title: colSize, Width: 90, Align: ui.End, Sortable: true},
		{Title: colTime, Width: 140, Sortable: true},
	}
}

// thumbOf is a thumbnail, fetched once.
type thumbOf struct {
	bmp     *ui.Bitmap
	loading bool
	used    uint64
	cancel  context.CancelFunc
}

// thumb returns the thumbnail of a file, fetching it when it shows first;
// nil while it comes, or for files without one.
func (a *app) thumb(e fileindex.Entry) *ui.Bitmap {
	if e.IsDir || !a.thumbExt(e.Ext) {
		return nil
	}
	c, _ := a.brClient()
	if c == nil {
		return nil
	}
	key := fmt.Sprintf("%s/%d/%s/%d/%d", a.br.server, e.Root, e.Path, e.MTime, e.Size)
	return a.thumbCached(key, a.br.thumbCtx, func(ctx context.Context) (*ui.Bitmap, error) {
		data, err := c.Thumb(ctx, e.Root, e.Path)
		if err != nil {
			return nil, err
		}
		return ui.DecodeBitmap(data)
	})
}

func (a *app) thumbExt(ext string) bool {
	if ext == "" {
		return false
	}
	exts := a.thumbExts[a.br.server]
	if exts == nil {
		// Asked once per server; until then, the usual pictures.
		a.thumbExts[a.br.server] = map[string]bool{"jpg": true, "jpeg": true, "png": true}
		if c, sv := a.brClient(); c != nil {
			server := a.br.server
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
				defer cancel()
				if info, err := c.Info(ctx); err == nil {
					m := map[string]bool{}
					for _, x := range info.ThumbExts {
						m[x] = true
					}
					a.post(func() {
						if !a.closed && sv.Token == c.Token && sv.Addr == c.Addr {
							a.thumbExts[server] = m
						}
					})
				}
			}()
		}
		exts = a.thumbExts[a.br.server]
	}
	return exts[ext]
}

// openEntry opens directories, previews images/text, and routes other files
// through the local proxy to a system application.
func (a *app) openEntry(e fileindex.Entry) {
	if e.IsDir {
		a.browseIn(e.Root, e.Path)
		return
	}
	c, sv := a.brClient()
	if c == nil {
		return
	}
	if previewKind(e) != "" {
		var group []fileindex.Entry
		for _, it := range a.br.items {
			if previewKind(it) != "" {
				group = append(group, it)
			}
		}
		a.showPreview(sv.ID, c, e, group)
	} else {
		a.systemOpenSource(sv.ID, c, e, false)
	}
}

// pickUpload asks for files, or a folder, to upload here.
func (a *app) pickUpload(folder bool) {
	b, dir := a.br, a.br.path
	go func() {
		var picked []string
		var err error
		if folder {
			var dir string
			dir, err = a.pickDir("上传文件夹", "")
			if dir != "" {
				picked = []string{dir}
			}
		} else {
			picked, err = a.pickFiles("上传文件")
		}
		a.post(func() {
			if a.closed || a.br != b || b.path != dir || b.results {
				return
			}
			if err != nil {
				a.br.err = err.Error()
			} else if len(picked) > 0 {
				a.upload(picked)
			}
		})
	}()
}

// upload sends files and folders of this computer into the directory
// shown, asking first to replace what has their names.
func (a *app) upload(files []string) {
	b := a.br
	if b == nil || b.root.ReadOnly || b.results || b.loading || b.operating || len(files) == 0 {
		return
	}
	have := map[string]bool{}
	for _, e := range b.items {
		have[e.Name] = true
	}
	var taken []string
	for _, f := range files {
		if have[filepath.Base(f)] {
			taken = append(taken, filepath.Base(f))
		}
	}
	start := func() { a.startUpload(files) }
	if len(taken) == 0 {
		start()
		return
	}
	a.ask("替换同名的项？", fmt.Sprintf("这里已经有 %s 等 %d 项与上传的同名。文件会被替换，文件夹会合并。", strings.Join(taken[:min(3, len(taken))], "、"), len(taken)), "替换", start)
}

func (a *app) startUpload(files []string) {
	b := a.br
	if b == nil || b.root.ReadOnly || b.results || b.loading || b.operating || len(files) == 0 {
		return
	}
	c, sv := a.brClient()
	if c == nil {
		return
	}
	title := filepath.Base(files[0])
	if len(files) > 1 {
		title = fmt.Sprintf("%s 等 %d 项", title, len(files))
	}
	where := path.Join(b.root.Name, b.path)
	tr := a.addTransfer("上传", title+" → "+where)
	tr.server = b.server
	ctx, cancel := context.WithCancel(context.Background())
	tr.cancel = cancel
	loc := fileindex.Location{Addr: sv.Addr, Root: b.root.ID, Path: b.path}
	dir := b.path
	go func() {
		res := c.Upload(ctx, loc, files, hooks(a, tr))
		a.post(func() {
			var failed []string
			for _, e := range res.Errors {
				failed = append(failed, e.Action.Path+"："+e.Err.Error())
			}
			err := res.Fatal
			if res.Canceled {
				err = context.Canceled
			}
			if a.br == b && b.path == dir && !b.results {
				tr.after = a.reloadKeeping
			}
			a.finishTransfer(tr, err, failed)
			if errors.Is(err, fileindex.ErrAuth) {
				a.signedOut(sv)
			}
		})
	}()
}

// download asks where, then downloads the entries there.
func (a *app) download(es []fileindex.Entry) {
	if len(es) == 0 {
		return
	}
	b := a.br
	c, _ := a.brClient()
	if c == nil {
		return
	}
	go func() {
		dest, err := a.pickDir("下载到", "")
		a.post(func() {
			if a.closed {
				return
			}
			if err != nil {
				b.err = err.Error()
				return
			}
			if dest == "" {
				return
			}
			var taken []string
			for _, e := range es {
				if _, err := os.Lstat(filepath.Join(dest, e.Name)); err == nil {
					taken = append(taken, e.Name)
				}
			}
			start := func() { a.startDownloadFrom(c, b.server, es, dest) }
			if len(taken) == 0 {
				start()
				return
			}
			if a.br != b {
				return
			}
			a.ask("替换本机同名的项？", fmt.Sprintf("%s 中已经有 %s 等 %d 项与下载的同名。文件会被替换，文件夹会合并。", dest, strings.Join(taken[:min(3, len(taken))], "、"), len(taken)), "替换", start)
		})
	}()
}

func (a *app) startDownload(es []fileindex.Entry, dest string) {
	c, _ := a.brClient()
	if c == nil {
		return
	}
	a.startDownloadFrom(c, a.br.server, es, dest)
}

func (a *app) startDownloadFrom(c *fileindex.Client, server string, es []fileindex.Entry, dest string) {
	if len(es) == 0 {
		return
	}
	title := es[0].Name
	if len(es) > 1 {
		title = fmt.Sprintf("%s 等 %d 项", title, len(es))
	}
	tr := a.addTransfer("下载", title+" → "+dest)
	tr.server = server
	if len(es) == 1 && !es[0].IsDir {
		tr.total = es[0].Size
	}
	ctx, cancel := context.WithCancel(context.Background())
	tr.cancel = cancel
	root := es[0].Root
	onProgress := func(p fileindex.Progress) {
		a.post(func() { tr.done, tr.current = p.Done, p.Current; tr.total = max(p.Total, tr.total) })
	}
	go func() {
		var failed []string
		var err error
		if len(es) == 1 && !es[0].IsDir {
			err = c.DownloadFile(ctx, root, es[0].Path, filepath.Join(dest, es[0].Name), onProgress)
		} else {
			var errs []error
			errs, err = c.Download(ctx, root, paths(es), dest, onProgress)
			for _, e := range errs {
				failed = append(failed, e.Error())
			}
		}
		a.post(func() { a.finishTransfer(tr, err, failed) })
	}()
}

// transferTo asks for a directory of the same server, then copies or moves
// the entries there, as a task of the server.
func (a *app) transferTo(move bool, es []fileindex.Entry) {
	b := a.br
	server := b.server
	c, _ := a.brClient()
	if c == nil || len(es) == 0 {
		return
	}
	a.openPickerIn(server, b.root.ID, b.path, func(sv string, root int64, rootName, dir string) {
		if sv != server {
			a.br.err = "只能复制或移动到同一个服务器上"
			return
		}
		kind := "复制"
		if move {
			kind = "移动"
		}
		title := es[0].Name
		if len(es) > 1 {
			title = fmt.Sprintf("%s 等 %d 项", title, len(es))
		}
		tr := a.addTransfer(kind, title+" → "+path.Join(rootName, dir))
		tr.server = server
		srcRoot := es[0].Root
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		tr.cancel = cancel
		go func() {
			defer cancel()
			task, err := c.Transfer(ctx, move, srcRoot, paths(es), root, dir)
			a.post(func() {
				if err != nil {
					a.finishTransfer(tr, err, nil)
					return
				}
				tr.task = task.ID
				tr.after = func() {
					if a.br == b {
						a.reloadKeeping()
					}
				}
				a.trackServerTask(c, tr, task)
			})
		}()
	})
	a.nas.sameServer = server
}

func (a *app) startRename(e fileindex.Entry) {
	b := a.br
	b.renaming, b.renameOf, b.renameIn, b.renameTo = true, e.Path, e.Root, e.Name
}

// ask asks to confirm an action, which runs if confirmed.
func (a *app) ask(title, msg, ok string, action func()) {
	b := a.br
	b.confirm, b.confirmT, b.confirmM, b.confirmOK, b.onConfirm = true, title, msg, ok, action
}

// op runs a quick operation of the server, then lists the directory again.
func (a *app) op(label string, fn func(ctx context.Context, c *fileindex.Client) error, choose string) {
	b := a.br
	if b.operating {
		return
	}
	c, sv := a.brClient()
	if c == nil {
		return
	}
	b.operating, b.opLabel = true, label
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		err := fn(ctx, c)
		a.post(func() {
			b.operating, b.opLabel = false, ""
			if a.br != b {
				return
			}
			if err != nil {
				b.err = err.Error()
				if errors.Is(err, fileindex.ErrAuth) {
					a.signedOut(sv)
				}
				return
			}
			b.err = ""
			if b.results {
				a.search(b.query)
				return
			}
			if choose != "" {
				a.browse(b.path, choose)
			} else {
				a.reloadKeeping()
			}
		})
	}()
}

// askDelete asks before deleting es, to the trash or, with forever, not.
func (a *app) askDelete(es []fileindex.Entry, forever bool) {
	if len(es) == 0 {
		return
	}
	a.br.toDelete, a.br.forever = slices.Clone(es), forever
}

// trash moves es to the trash.
func (a *app) trash(es []fileindex.Entry) {
	b := a.br
	label := fmt.Sprintf("正在把 %d 项移到回收站…", len(es))
	if len(es) == 1 {
		label = "正在把“" + es[0].Name + "”移到回收站…"
	}
	root, ps := es[0].Root, paths(es)
	a.op(label, func(ctx context.Context, c *fileindex.Client) error { return c.Trash(ctx, root, ps) }, "")
	b.sel.Clear()
}

// deleteForever deletes es, not to the trash, as a task of the server
// that the transfers show.
func (a *app) deleteForever(es []fileindex.Entry) {
	b := a.br
	c, _ := a.brClient()
	if c == nil {
		return
	}
	title := es[0].Name
	if len(es) > 1 {
		title = fmt.Sprintf("%s 等 %d 项", title, len(es))
	}
	tr := a.addTransfer("彻底删除", title)
	tr.server = b.server
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	tr.cancel = cancel
	root, ps := es[0].Root, paths(es)
	b.sel.Clear()
	go func() {
		defer cancel()
		task, err := c.DeleteForever(ctx, root, ps)
		a.post(func() {
			if err != nil {
				a.finishTransfer(tr, err, nil)
				return
			}
			tr.task = task.ID
			tr.after = func() {
				if a.br == b {
					a.reloadKeeping()
				}
			}
			a.trackServerTask(c, tr, task)
		})
	}()
}

func (a *app) browserDialogs(c *ui.Context) {
	b := a.br
	if b.naming {
		ui.Modal(c, &b.naming, func() {
			ui.Text(c, "新建文件夹").Bold()
			done := ui.TextInput(c.Key("new-name"), &b.newName).Placeholder("名称").AutoFocus().Width(320).Submitted()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "取消").Clicked() {
					b.naming = false
				}
				name := strings.TrimSpace(b.newName)
				if ui.PrimaryButton(c, "创建").Disabled(name == "").Clicked() || done && name != "" {
					b.naming = false
					root, dir := b.root.ID, b.path
					a.op("正在新建文件夹“"+name+"”…", func(ctx context.Context, c *fileindex.Client) error { return c.Mkdir(ctx, root, dir, name) }, name)
				}
			})
		})
	}
	if b.renaming {
		ui.Modal(c, &b.renaming, func() {
			ui.Text(c, "重命名").Bold()
			done := ui.TextInput(c.Key("rename"), &b.renameTo).AutoFocus().Width(320).Submitted()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "取消").Clicked() {
					b.renaming = false
				}
				name := strings.TrimSpace(b.renameTo)
				ok := name != "" && name != path.Base(b.renameOf)
				if ui.PrimaryButton(c, "重命名").Disabled(!ok).Clicked() || done && ok {
					b.renaming = false
					root, p := b.renameIn, b.renameOf
					a.op("正在重命名为“"+name+"”…", func(ctx context.Context, c *fileindex.Client) error { return c.Rename(ctx, root, p, name) }, name)
				}
			})
		})
	}
	if es := b.toDelete; es != nil {
		open := true
		title := fmt.Sprintf("删除所选的 %d 项？", len(es))
		if len(es) == 1 {
			title = "删除“" + es[0].Name + "”？"
		}
		if b.forever {
			msg := "彻底删除后无法恢复：不会进入回收站。"
			if len(es) > 1 || es[0].IsDir {
				msg = "文件夹里的所有内容也会一起删除。" + msg
			}
			// In red, the focus on Cancel: Enter deletes nothing.
			t := c.Theme()
			ui.Modal(c.Key("delete-forever"), &open, func() {
				ui.Column(c).Width(340).Gap(10).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, iconAlert).FontSize(18).TextColor(t.Danger)
						ui.Text(c, title).Bold().Grow(1).Shrink(1).MaxLines(2)
					})
					ui.Text(c, msg).TextColor(t.TextMuted)
					ui.Row(c).Gap(8).Justify(ui.End).Margin(6, 0, 0, 0).Children(func() {
						if ui.Button(c, "取消").AutoFocus().Clicked() {
							open = false
						}
						del := ui.PrimaryButton(c, "彻底删除").Background(t.Danger)
						if del.Hovered() {
							del.Background(t.Danger.Mix(ui.RGBA(0, 0, 0, 1), 0.12))
						}
						if del.Clicked() {
							open = false
							a.deleteForever(es)
						}
					})
				})
			})
		} else {
			msg := fmt.Sprintf("%d 项会移到 fileindex 的回收站，可以在左侧的回收站中恢复。", len(es))
			switch ui.AlertDialog(c, &open, title, msg, "取消", "彻底删除…", "移到回收站") {
			case 1:
				b.forever = true
				open = true
			case 2:
				a.trash(es)
			}
		}
		if !open {
			b.toDelete = nil
		}
	}
	if b.confirm {
		if ui.AlertDialog(c, &b.confirm, b.confirmT, b.confirmM, "取消", b.confirmOK) == 1 && b.onConfirm != nil {
			b.onConfirm()
		}
	}
}

// signedOut forgets a server's session the server no longer takes.
func (a *app) signedOut(sv *config.Server) {
	sv.Token = ""
	a.save()
	a.serverChanged(sv.ID)
}
