package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
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

	query   string
	results bool // items are search results
	total   int  // of the results, which may be more than shown

	sel       ui.Selection[string] // by path
	cursor    int
	list      ui.ListState
	gridState ui.GridState

	// dialogs
	naming    bool
	newName   string
	renaming  bool
	renameOf  string
	renameTo  string
	deleting  bool
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
	if a.br != nil && a.br.thumbCancel != nil {
		a.br.thumbCancel()
	}
	a.br = &browser{server: server, root: root, cursor: -1}
	b := a.br
	a.br.list.Key = func(i int) any { return b.items[i].Path }
	a.br.list.Selection = &a.br.sel
	a.br.list.Selected = &a.br.cursor
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
	b.path, b.results, b.query = p, false, ""
	a.revealDirectory(b)
	root, live := b.root.ID, b.live
	a.load(func(ctx context.Context, c *fileindex.Client) ([]fileindex.Entry, int, error) {
		items, err := c.List(ctx, root, p, live)
		return items, len(items), err
	}, choose...)
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
			b.items, b.total = items, total
			if !b.results {
				a.cacheDirectories(b.server, b.root.ID, b.path, items)
			}
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
	if q == "" {
		a.browse(b.path)
		return
	}
	b.results, b.query = true, q
	root, dir := b.root.ID, b.path
	a.load(func(ctx context.Context, c *fileindex.Client) ([]fileindex.Entry, int, error) {
		return c.Search(ctx, q, root, dir, 1000)
	})
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

// browserView is the page of a root.
func (a *app) browserView(c *ui.Context) {
	b := a.br
	t := c.Theme()
	writable := !b.root.ReadOnly && !b.loading && !b.operating

	// The bar: where, and the search.
	ui.Row(c).Gap(10).Children(func() {
		crumbs := []string{b.root.Name}
		if b.path != "" {
			crumbs = append(crumbs, strings.Split(b.path, "/")...)
		}
		chosen := len(crumbs) - 1
		if b.results {
			crumbs = append(crumbs, "搜索“"+b.query+"”")
			chosen = len(crumbs) - 1
		}
		if ui.Breadcrumbs(c, crumbs, &chosen).Label("位置").Grow(1).Shrink(1).Changed() {
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
		if ui.TextInput(c.Key("search"), &b.query).Placeholder("在这里搜索…").Label("搜索").Width(220).Submitted() {
			a.search(b.query)
		}
	})

	// The actions.
	es := b.chosen()
	if b.loading || b.operating {
		es = nil
	}
	n := len(es)
	ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
		up := ui.MenuButton(c.Key("upload"), "上传", func(m *ui.Menu) {
			if m.Item("上传文件…").Chosen() {
				a.pickUpload(false)
			}
			if m.Item("上传文件夹…").Chosen() {
				a.pickUpload(true)
			}
		}).Disabled(!writable || b.results)
		if writable {
			up.Tooltip("也可以把文件、文件夹拖进窗口")
		}
		if ui.Button(c, "新建文件夹").Disabled(!writable || b.results).Clicked() {
			b.naming, b.newName = true, ""
		}
		ui.Divider(c).Height(20).Width(1)
		if ui.Button(c, "下载…").Disabled(n == 0).Clicked() {
			a.download(es)
		}
		if ui.Button(c, "复制到…").Disabled(n == 0).Clicked() {
			a.transferTo(false, es)
		}
		if ui.Button(c, "移动到…").Disabled(n == 0 || !writable).Clicked() {
			a.transferTo(true, es)
		}
		if ui.Button(c, "重命名").Disabled(n != 1 || !writable).Clicked() {
			a.startRename(es[0])
		}
		if ui.Button(c, "删除").Disabled(n == 0 || !writable).Clicked() {
			b.deleting = true
		}
		ui.Spacer(c)
		live := ui.Switch(c.Key("live"), &b.live).Label("实时").Tooltip("开启后直接读取 NAS 硬盘上的当前内容；关闭时只读索引，不唤醒休眠的硬盘")
		if live.Changed() {
			a.refresh()
		}
		ui.Text(c, "实时").FontSize(12).TextColor(t.TextMuted)
		view := 0
		if b.grid {
			view = 1
		}
		if ui.Segmented(c, &view, "列表", "网格").Label("视图").Changed() {
			b.grid = view == 1
		}
		if ui.Button(c, "刷新").Clicked() {
			a.refresh()
		}
	})

	if b.err != "" {
		notice(c, t.Danger, b.err)
	}

	// The files, which take files dropped on them.
	zone := ui.Column(c).Grow(1).MinHeight(0).Radius(t.Radius*2).Border(1, t.Border).Clip().Children(func() {
		switch {
		case b.loading && len(b.items) == 0:
			ui.Column(c).Grow(1).Center().Children(func() { ui.Spinner(c).FontSize(22).Label("正在读取") })
		case b.grid:
			a.gridView(c)
		default:
			a.tableView(c)
		}
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

	status := fmt.Sprintf("%d 项", len(b.items))
	if b.results {
		status = fmt.Sprintf("找到 %d 项", b.total)
		if b.total > len(b.items) {
			status += fmt.Sprintf("，显示前 %d 项", len(b.items))
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
	ui.Text(c, status).FontSize(12).TextColor(t.TextMuted)

	a.browserDialogs(c)
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
	if m.Item("删除").Disabled(n == 0 || !writable).Chosen() {
		b.deleting = true
	}
}

func (a *app) tableView(c *ui.Context) {
	b := a.br
	t := c.Theme()
	cols := []ui.TableColumn{{Title: "名称"}, {Title: "大小", Width: 90, Align: ui.End}, {Title: "修改时间", Width: 140}}
	if b.results {
		cols = append(cols, ui.TableColumn{Title: "位置", Width: 220})
	}
	table := ui.Table(c.Key("files"), &b.list, cols, len(b.items), func(row, col int) {
		e := b.items[row]
		muted := t.TextMuted
		if b.sel.Has(e.Path) {
			muted = t.AccentText.Alpha(0.85)
		}
		switch col {
		case 0:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				fileIcon(c, e, 16)
				ui.Text(c, e.Name).SingleLine().Grow(1).Shrink(1)
			})
		case 1:
			if !e.IsDir {
				ui.Text(c, formatBytes(e.Size)).FontFeatures("tnum").TextColor(muted)
			}
		case 2:
			ui.Text(c, e.ModTime().Format("2006-01-02 15:04")).FontFeatures("tnum").TextColor(muted)
		case 3:
			ui.Text(c, path.Join(b.root.Name, e.Parent)).SingleLine().TextColor(muted)
		}
	}).Grow(1).Label("文件")
	table.Children(func() {
		if !b.loading && len(b.items) == 0 {
			msg := "空文件夹"
			if b.results {
				msg = "没有找到"
			} else if !b.root.ReadOnly {
				msg = "空文件夹，可以把文件拖到这里上传"
			}
			ui.Text(c, msg).TextColor(t.TextMuted).Padding(16)
		}
	})
	if table.Submitted() && b.cursor >= 0 && b.cursor < len(b.items) {
		a.openEntry(b.items[b.cursor])
	}
}

func (a *app) gridView(c *ui.Context) {
	b := a.br
	t := c.Theme()
	grid := ui.GridView(c.Key("grid"), &b.gridState, len(b.items), 120, 132, func(i int) {
		e := b.items[i]
		ui.Column(c).Grow(1).Padding(6).Gap(4).AlignItems(ui.Center).Children(func() {
			if bmp := a.thumb(e); bmp != nil {
				ui.Image(c, bmp).Size(96, 84).Fit(ui.Contain).Radius(4)
			} else {
				ui.Column(c).Size(96, 84).Center().Children(func() { fileIcon(c, e, 48) })
			}
			ui.Text(c, e.Name).FontSize(12).MaxLines(2).TextAlign(ui.Center)
		})
	}).Grow(1).Padding(8).Label("文件")
	if grid.Submitted() && b.cursor >= 0 && b.cursor < len(b.items) {
		a.openEntry(b.items[b.cursor])
	}
	grid.Children(func() {
		if !b.loading && len(b.items) == 0 {
			ui.Text(c, "空文件夹").TextColor(t.TextMuted).Padding(16)
		}
	})
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
	key := fmt.Sprintf("%s/%d/%s/%d/%d", a.br.server, e.Root, e.Path, e.MTime, e.Size)
	a.thumbSeq++
	if th := a.thumbs[key]; th != nil {
		th.used = a.thumbSeq
		return th.bmp
	}
	if len(a.thumbs) >= 256 {
		oldest := ""
		for k, v := range a.thumbs {
			if oldest == "" || v.used < a.thumbs[oldest].used {
				oldest = k
			}
		}
		if old := a.thumbs[oldest]; old != nil && old.cancel != nil {
			old.cancel()
		}
		delete(a.thumbs, oldest)
	}
	th := &thumbOf{loading: true, used: a.thumbSeq}
	a.thumbs[key] = th
	c, _ := a.brClient()
	if c == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.br.thumbCtx, requestTimeout)
	th.cancel = cancel
	go func() {
		defer cancel()
		select {
		case a.thumbSlots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-a.thumbSlots }()
		data, err := c.Thumb(ctx, e.Root, e.Path)
		var bmp *ui.Bitmap
		if err == nil {
			bmp, _ = ui.DecodeBitmap(data)
		}
		a.post(func() {
			if !a.closed {
				th.bmp, th.loading = bmp, false
			}
		})
	}()
	return nil
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
		a.browse(e.Path)
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
	b.renaming, b.renameOf, b.renameTo = true, e.Path, e.Name
}

// ask asks to confirm an action, which runs if confirmed.
func (a *app) ask(title, msg, ok string, action func()) {
	b := a.br
	b.confirm, b.confirmT, b.confirmM, b.confirmOK, b.onConfirm = true, title, msg, ok, action
}

// op runs a quick operation of the server, then lists the directory again.
func (a *app) op(fn func(ctx context.Context, c *fileindex.Client) error, choose string) {
	b := a.br
	if b.operating {
		return
	}
	c, sv := a.brClient()
	if c == nil {
		return
	}
	b.operating = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		err := fn(ctx, c)
		a.post(func() {
			b.operating = false
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
					a.op(func(ctx context.Context, c *fileindex.Client) error { return c.Mkdir(ctx, root, dir, name) }, name)
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
					root, p := b.root.ID, b.renameOf
					a.op(func(ctx context.Context, c *fileindex.Client) error { return c.Rename(ctx, root, p, name) }, name)
				}
			})
		})
	}
	if b.deleting {
		es := b.chosen()
		msg := fmt.Sprintf("%d 项会移到 fileindex 的回收站，可以在左侧的回收站中恢复。", len(es))
		title := "删除所选的项？"
		if len(es) == 1 {
			title = "删除“" + es[0].Name + "”？"
		}
		if ui.AlertDialog(c, &b.deleting, title, msg, "取消", "移到回收站") == 1 {
			root, ps := b.root.ID, paths(es)
			a.op(func(ctx context.Context, c *fileindex.Client) error { return c.Trash(ctx, root, ps) }, "")
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
