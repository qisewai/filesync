package main

import (
	"context"
	"errors"
	"path"
	"slices"
	"strings"
	"time"

	"filesync/config"
	"filesync/fileindex"

	"github.com/egoist/mygo/ui"
)

// Only expanded directories are listed; collapsed branches never wake disks.
type directoryNode struct {
	open, loaded, loading bool
	dirs                  []fileindex.Entry
	err                   string
	gen                   int
	loadStart             time.Time
}

// leaf reports whether the directory is known to hold no directories:
// it shows no arrow to open.
func (n *directoryNode) leaf() bool { return n.loaded && !n.loading && n.err == "" && len(n.dirs) == 0 }

// openState and children are those of its TreeItem: none for a leaf.
func (n *directoryNode) openState() *bool {
	if n.leaf() {
		n.open = false
		return nil
	}
	return &n.open
}

func (n *directoryNode) children(fn func()) func() {
	if n.leaf() {
		return nil
	}
	return fn
}

// loadingText shows the directory being read, once it takes a moment:
// one read at once shows nothing that flashes.
func (n *directoryNode) loadingText(c *ui.Context) {
	if wait := 300*time.Millisecond - time.Since(n.loadStart); wait > 0 {
		c.After(wait)
		return
	}
	ui.Text(c, "正在读取…").FontSize(12).TextColor(c.Theme().TextMuted).Padding(0, 20)
}

func treeKey(server string, root int64, dir string) string {
	return rootPlace(server, root) + "/" + dir
}

func (a *app) treeNode(server string, root int64, dir string) *directoryNode {
	key := treeKey(server, root, dir)
	if n := a.tree[key]; n != nil {
		return n
	}
	n := &directoryNode{}
	a.tree[key] = n
	return n
}

func (a *app) revealDirectory(b *browser) {
	dir := ""
	a.treeNode(b.server, b.root.ID, dir).open = true
	if b.path == "" {
		return
	}
	for _, part := range strings.Split(b.path, "/") {
		dir = path.Join(dir, part)
		a.treeNode(b.server, b.root.ID, dir).open = true
	}
}

func (a *app) cacheDirectories(server string, root int64, dir string, items []fileindex.Entry) {
	n := a.treeNode(server, root, dir)
	n.gen++ // a browser listing supersedes any older tree request
	n.dirs, n.err, n.loaded, n.loading = nil, "", true, false
	for _, e := range items {
		if !e.IsDir {
			continue
		}
		n.dirs = append(n.dirs, e)
		// The server tells it holds no directories: a leaf, before it is
		// opened.
		if e.HasDirs == nil {
			continue
		}
		switch child := a.treeNode(server, root, e.Path); {
		case !*e.HasDirs && !child.loaded && !child.loading:
			child.dirs, child.err, child.loaded = nil, "", true
		case *e.HasDirs && child.leaf():
			child.loaded = false // it has some since: to read when opened
		}
	}
	slices.SortStableFunc(n.dirs, func(x, y fileindex.Entry) int { return naturalCompare(x.Name, y.Name) })
}

func (a *app) directoryView(c *ui.Context, sv *config.Server, root fileindex.Root, dir, name string) {
	key := treeKey(sv.ID, root.ID, dir)
	n := a.treeNode(sv.ID, root.ID, dir)
	item := ui.TreeItem(c.Key(key), "", n.openState(), n.children(func() {
		if !n.loaded && !n.loading {
			a.loadDirectory(sv, root.ID, dir, n)
		}
		switch {
		case n.loading:
			n.loadingText(c)
		case n.err != "":
			ui.Button(c.Key(key+"/retry"), "重试读取目录").Tooltip(n.err).OnClick(func() { n.loaded = false })
		default:
			for _, e := range n.dirs {
				a.directoryView(c, sv, root, e.Path, e.Name)
			}
		}
	}))
	chosen := a.br != nil && a.place == rootPlace(sv.ID, root.ID) && a.br.server == sv.ID && a.br.root.ID == root.ID && a.br.path == dir
	color := ui.Hex("#d99a2b")
	if chosen {
		color = c.Theme().AccentText
	}
	item.Children(func() { labeled(c, iconFolder, name, color) }).Label("文件夹 " + name)
	item.ContextMenu(func(m *ui.Menu) { a.directoryMenu(m, sv, root, dir, name) })
	item.Selected(a.br != nil && a.place == rootPlace(sv.ID, root.ID) && a.br.server == sv.ID && a.br.root.ID == root.ID && a.br.path == dir).
		OnClick(func() {
			a.goPlace(rootPlace(sv.ID, root.ID))
			if a.br != nil && a.br.path != dir {
				a.browse(dir)
			}
		})
}

func (a *app) loadDirectory(sv *config.Server, root int64, dir string, n *directoryNode) {
	n.loading, n.loadStart = true, time.Now()
	n.gen++
	gen := n.gen
	c := fileindex.New(sv.Addr, sv.Token)
	key := treeKey(sv.ID, root, dir)
	live := a.br != nil && a.br.server == sv.ID && a.br.root.ID == root && a.br.live
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		items, err := c.List(ctx, root, dir, live)
		a.post(func() {
			if a.closed || a.tree[key] != n || n.gen != gen || sv.Token != c.Token || sv.Addr != c.Addr {
				return
			}
			n.loading, n.loaded = false, true
			if err != nil {
				n.err = err.Error()
				if errors.Is(err, fileindex.ErrAuth) {
					a.signedOut(sv)
				}
				return
			}
			a.cacheDirectories(sv.ID, root, dir, items)
		})
	}()
}

func (a *app) invalidateTree(server string, root int64) {
	prefix := rootPlace(server, root) + "/"
	for key, n := range a.tree {
		if strings.HasPrefix(key, prefix) {
			n.gen++
			n.loaded, n.loading = false, false
		}
	}
}

// showDirectory shows the directory dir of a root, choosing its entry
// choose, if any.
func (a *app) showDirectory(server string, root fileindex.Root, dir string, choose ...string) {
	a.goPlace(rootPlace(server, root.ID))
	if a.br == nil || a.br.server != server || a.br.root.ID != root.ID {
		return
	}
	if a.br.path != dir || len(choose) > 0 {
		a.browse(dir, choose...)
	}
}

// directoryMenu is the menu of a directory of the sidebar: what the files
// page does with it, shown there first.
func (a *app) directoryMenu(m *ui.Menu, sv *config.Server, root fileindex.Root, dir, name string) {
	busy := a.br != nil && (a.br.operating || a.br.loading) && a.br.server == sv.ID && a.br.root.ID == root.ID
	writable := !root.ReadOnly && !busy
	// Of the directory itself, inside it.
	inside := func(fn func()) func() {
		return func() {
			a.showDirectory(sv.ID, root, dir)
			if a.br != nil && a.br.path == dir {
				fn()
			}
		}
	}
	if m.Item("打开").Chosen() {
		a.showDirectory(sv.ID, root, dir)
	}
	a.favoriteMenuItem(m, favoriteOf(sv.ID, root, dir))
	m.Separator()
	if m.Item("新建文件夹…").Disabled(!writable).Chosen() {
		inside(func() { a.br.naming, a.br.newName = true, "" })()
	}
	if m.Item("上传文件到这里…").Disabled(!writable).Chosen() {
		inside(func() { a.pickUpload(false) })()
	}
	if m.Item("上传文件夹到这里…").Disabled(!writable).Chosen() {
		inside(func() { a.pickUpload(true) })()
	}
	if dir == "" {
		// The root itself: managed with the roots.
		m.Separator()
		if m.Item("刷新").Chosen() {
			a.refreshDirectory(sv, root, dir)
		}
		if m.Item("目录管理…").Chosen() {
			a.openAdmin(sv.ID, "roots")
		}
		return
	}
	// Of the directory as an entry of its parent, chosen there.
	e := fileindex.Entry{Root: root.ID, RootName: root.Name, Name: name, Path: dir, Parent: parentDir(dir), IsDir: true}
	as := func(fn func(e fileindex.Entry)) {
		a.showDirectory(sv.ID, root, e.Parent, e.Name)
		if a.br != nil && a.br.path == e.Parent {
			fn(e)
		}
	}
	m.Separator()
	if m.Item("下载…").Disabled(busy).Chosen() {
		as(func(e fileindex.Entry) { a.download([]fileindex.Entry{e}) })
	}
	if m.Item("复制到…").Disabled(busy).Chosen() {
		as(func(e fileindex.Entry) { a.transferTo(false, []fileindex.Entry{e}) })
	}
	if m.Item("移动到…").Disabled(!writable).Chosen() {
		as(func(e fileindex.Entry) { a.transferTo(true, []fileindex.Entry{e}) })
	}
	if m.Item("重命名…").Disabled(!writable).Chosen() {
		as(a.startRename)
	}
	m.Separator()
	if m.Item("删除…").Disabled(!writable).Chosen() {
		as(func(e fileindex.Entry) { a.askDelete([]fileindex.Entry{e}, false) })
	}
	if m.Item("彻底删除…").Disabled(!writable).Chosen() {
		as(func(e fileindex.Entry) { a.askDelete([]fileindex.Entry{e}, true) })
	}
	m.Separator()
	if m.Item("刷新").Chosen() {
		a.refreshDirectory(sv, root, dir)
	}
}

// refreshDirectory reads a directory of the sidebar again, and the files
// page if it shows it.
func (a *app) refreshDirectory(sv *config.Server, root fileindex.Root, dir string) {
	n := a.treeNode(sv.ID, root.ID, dir)
	n.loaded, n.open = false, true
	if a.br != nil && a.br.server == sv.ID && a.br.root.ID == root.ID && a.br.path == dir {
		a.refresh()
	}
}
