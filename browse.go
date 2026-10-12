package main

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"filesync/config"
	"filesync/fileindex"
	"filesync/localtrash"

	"github.com/egoist/mygo/ui"
)

const requestTimeout = 20 * time.Second

// browser shows a directory of a root of a server.
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

	sel       ui.Selection[string] // by path
	cursor    int
	list      ui.ListState
	sort      tableSort
	gridState ui.GridState
	gridLast  int // the last item the grid built

	// dialogs
	naming  bool
	newName string
	opLabel string // what the operation under way does

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
	if b.path != p {
		b.items = nil
		b.sel.Clear()
		b.cursor = -1
	}
	b.path = p
	a.revealDirectory(b)
	root, live := b.root.ID, b.live
	a.load(func(ctx context.Context, c *fileindex.Client) ([]fileindex.Entry, int, error) {
		items, err := c.List(ctx, root, p, live)
		return items, len(items), err
	}, choose...)
}

// browseIn shows a directory of a root of the server shown: of the root
// shown, or of another.
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
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	b.cancel = cancel
	gen := b.gen
	b.loading, b.err = true, ""
	b.keepSelection = nil
	go func() {
		defer cancel()
		items, _, err := fetch(ctx, c)
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
			a.cacheDirectories(b.server, b.root.ID, b.path, items)
			sortEntries(&b.sort, items, nil)
			b.items = items
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

func (a *app) refresh() { a.browse(a.br.path) }

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
			if !a.closed && a.br == b && !b.loading && !b.naming && a.renaming == nil {
				a.reloadKeeping()
			}
		})
	}()
}

// reloadKeeping lists the directory again, keeping the choice.
func (a *app) reloadKeeping() {
	b := a.br
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

// browserPath is, under the toolbar, where the page is.
func (a *app) browserPath(c *ui.Context) {
	b := a.br
	a.serverPathBar(c, b.server, b.root, b.path)
	if b.root.ReadOnly {
		ui.Badge(c, "只读")
	}
	a.favoriteButton(c)
}

// browserView is the page of a root: its commands, and its files.
func (a *app) browserView(c *ui.Context) {
	b := a.br
	t := c.Theme()
	writable := !b.root.ReadOnly && !b.loading && !b.operating

	// Not while typing the search, in the toolbar, built before.
	typing := a.searchTyping
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
	if !typing && a.deleting == nil {
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
		toolButton(c, "new-folder", iconNewDir, "新建文件夹", "", !writable, func() {
			b.naming, b.newName = true, ""
		})
		up := toolButton(c, "upload", iconUpload, "上传", "上传文件或文件夹；也可以把它们拖进窗口", !writable, func() {})
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
		toolButton(c, "copy-to", iconCopy, "复制到…", tip("复制到这台或其他服务器上"), !oneRoot, func() { a.transferTo(false, es) })
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
		loading:  b.loading,
		where:    func(e fileindex.Entry) string { return path.Join(e.RootName, e.Parent) },
		thumb:    a.thumb,
		open:     a.openEntry,
		resort:   func() { sortEntries(&b.sort, b.items, &b.cursor) },
		gridLast: &b.gridLast,
		empty: func() {
			switch {
			case b.root.ReadOnly:
				emptyFolder(c, "")
			default:
				emptyFolder(c, "可以把文件拖到这里上传")
			}
		},
	})
	if writable {
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
	case b.loading && len(b.items) > 0:
		busy = "正在读取…"
	}
	if busy != "" {
		ui.Spinner(c).FontSize(12).Label(busy)
		ui.Text(c, busy).FontSize(12).TextColor(t.Accent)
	}
	statusText(c, status)
}

func (a *app) entryMenu(m *ui.Menu, es []fileindex.Entry, writable bool) {
	n := len(es)
	if n == 1 && archiveEntry(es[0]) {
		if m.Item("打开压缩包").Chosen() {
			a.openEntry(es[0])
		}
		if m.Item("解压到…").Chosen() {
			if c, sv := a.brClient(); c != nil {
				a.extractArchive(sv.ID, c, es[0])
			}
		}
	}
	if n == 1 && m.Item("打开").Chosen() {
		a.openEntry(es[0])
	}
	if n == 1 && es[0].IsDir && a.br != nil {
		a.favoriteMenuItem(m, favoriteOf(a.br.server, a.br.root, es[0].Path))
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
	return a.serverThumb(a.br.server, e)
}

// serverThumb is the thumbnail of a file of a server, nil while it comes
// or for none.
func (a *app) serverThumb(server string, e fileindex.Entry) *ui.Bitmap {
	sv := a.store.Server(server)
	if e.IsDir || sv == nil || !a.thumbExt(server, e.Ext) {
		return nil
	}
	c := fileindex.New(sv.Addr, sv.Token)
	var parent context.Context
	if a.br != nil && a.br.server == server {
		parent = a.br.thumbCtx
	}
	key := fmt.Sprintf("%s/%d/%s/%d/%d", server, e.Root, e.Path, e.MTime, e.Size)
	return a.thumbCached(key, parent, func(ctx context.Context) (*ui.Bitmap, error) {
		data, err := c.Thumb(ctx, e.Root, e.Path)
		if err != nil {
			return nil, err
		}
		return ui.DecodeBitmap(data)
	})
}

func (a *app) thumbExt(server, ext string) bool {
	if ext == "" {
		return false
	}
	exts := a.thumbExts[server]
	if exts == nil {
		// Asked once per server; until then, the usual pictures.
		a.thumbExts[server] = map[string]bool{"jpg": true, "jpeg": true, "png": true}
		if sv := a.store.Server(server); sv != nil {
			c := fileindex.New(sv.Addr, sv.Token)
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
		exts = a.thumbExts[server]
	}
	return exts[ext]
}

// openEntry opens directories, previews images/text, and routes other files
// through the local proxy to a system application.
func (a *app) openEntry(e fileindex.Entry) {
	if archiveEntry(e) {
		if c, sv := a.brClient(); c != nil {
			a.openArchive(sv.ID, c, e, "")
		}
		return
	}
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
			if a.closed || a.br != b || b.path != dir {
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
	if b == nil || b.root.ReadOnly || b.loading || b.operating || len(files) == 0 {
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
	if b == nil || b.root.ReadOnly || b.loading || b.operating || len(files) == 0 {
		return
	}
	dir := b.path
	a.startUploadTo(b.server, b.root.ID, path.Join(b.root.Name, b.path), b.path, files, false, func() {
		if a.br == b && b.path == dir {
			a.reloadKeeping()
		}
	})
}

// startUploadTo sends files and folders of this computer into dir of a
// root of a server, where showing it; with move, what landed whole then
// goes to the trash of this computer. after runs once it ended well.
func (a *app) startUploadTo(server string, root int64, where, dir string, files []string, move bool, after func()) {
	sv := a.store.Server(server)
	if sv == nil || len(files) == 0 {
		return
	}
	c := fileindex.New(sv.Addr, sv.Token)
	title := filepath.Base(files[0])
	if len(files) > 1 {
		title = fmt.Sprintf("%s 等 %d 项", title, len(files))
	}
	kind := "上传"
	if move {
		kind = "移动"
	}
	tr := a.addTransfer(kind, title+" → "+where)
	tr.server = server
	ctx, cancel := context.WithCancel(context.Background())
	tr.cancel = cancel
	loc := fileindex.Location{Addr: sv.Addr, Root: root, Path: dir}
	go func() {
		res := c.Upload(ctx, loc, files, hooks(a, tr))
		var failed []string
		broken := map[string]bool{} // the items with an entry not landed
		for _, e := range res.Errors {
			failed = append(failed, e.Action.Path+"："+e.Err.Error())
			top, _, _ := strings.Cut(e.Action.Path, "/")
			broken[top] = true
		}
		err := res.Fatal
		if res.Canceled {
			err = context.Canceled
		}
		if move && err == nil {
			for _, f := range files {
				if broken[filepath.Base(f)] {
					failed = append(failed, filepath.Base(f)+"：没有完整上传，保留在本机")
				} else if terr := localtrash.Move(f); terr != nil {
					failed = append(failed, filepath.Base(f)+"：已上传，但本机文件未能移入回收站："+terr.Error())
				}
			}
		}
		a.post(func() {
			tr.after = after
			a.finishTransfer(tr, err, failed)
			if move {
				a.localChanged(filepath.Dir(files[0]))
			}
			if errors.Is(err, fileindex.ErrAuth) {
				a.signedOut(sv)
			}
		})
	}()
}

// download asks where, then downloads the entries there.
func (a *app) download(es []fileindex.Entry) { a.downloadIn(a.brFiles(), es) }

// startDownloadFrom downloads the entries into dest; with move, what came
// whole then goes to the trash of the server.
func (a *app) startDownloadFrom(c *fileindex.Client, f *nasFiles, es []fileindex.Entry, dest string, move bool) {
	if len(es) == 0 {
		return
	}
	title := es[0].Name
	if len(es) > 1 {
		title = fmt.Sprintf("%s 等 %d 项", title, len(es))
	}
	kind := "下载"
	if move {
		kind = "移动"
	}
	tr := a.addTransfer(kind, title+" → "+dest)
	tr.server = f.server
	tr.localDir, tr.folderReady = dest, true
	if len(es) == 1 && !es[0].IsDir {
		tr.localFile = filepath.Join(dest, es[0].Name)
	}
	if len(es) == 1 && !es[0].IsDir {
		tr.total = es[0].Size
	}
	ctx, cancel := context.WithCancel(context.Background())
	tr.cancel = cancel
	a.saveTransferHistory()
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
		// A move leaves the server only once all came whole.
		moved := false
		if move && err == nil && len(failed) == 0 {
			if err = c.Trash(ctx, root, paths(es)); err != nil {
				err = fmt.Errorf("已下载到本机，但服务器上的文件未能移入回收站：%w", err)
			} else {
				moved = true
			}
		} else if move && err == nil {
			failed = append(failed, "有文件没有完整下载，服务器上的文件全部保留")
		}
		a.post(func() {
			a.finishTransfer(tr, err, failed)
			if moved {
				f.changed("")
			}
		})
	}()
}

// transferTo asks for a directory of the same server, then copies or moves
// the entries there, as a task of the server.
func (a *app) transferTo(move bool, es []fileindex.Entry) { a.transferIn(a.brFiles(), move, es) }

func (a *app) startRename(e fileindex.Entry) { a.renameIn(a.brFiles(), e) }

// op runs a quick operation of the server, then lists the directory again.
func (a *app) op(label string, fn func(ctx context.Context, c *fileindex.Client) error, choose string) {
	a.opIn(a.brFiles(), label, fn, choose)
}

// askDelete asks before deleting es, to the trash or, with forever, not.
func (a *app) askDelete(es []fileindex.Entry, forever bool) { a.askDeleteIn(a.brFiles(), es, forever) }

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
}

// signedOut forgets a server's session the server no longer takes.
func (a *app) signedOut(sv *config.Server) {
	sv.Token = ""
	a.save()
	a.serverChanged(sv.ID)
}
