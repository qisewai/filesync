package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"filesync/fileindex"
	"filesync/localindex"

	"github.com/egoist/mygo/ui"
)

// localPlace is the page of a local directory.
const localPlace = "local"

// localBrowser shows a directory of this computer, to back up what is
// chosen in it.
type localBrowser struct {
	dir                         string
	items                       []fileindex.Entry // Path is the full path
	loading, opening            bool
	err                         string
	gen                         int
	hidden                      bool // shows the files whose names start with a dot
	query                       string
	filter                      searchFilter
	here                        bool // searches the folder shown, not all the index
	results, queryPending, more bool
	awaitTop                    bool // wait for the new result's scroll request to commit
	searching                   bool // the search has the keyboard focus
	total                       int
	matched                     *localindex.Results
	searchCancel                context.CancelFunc
	debounce                    *time.Timer

	grid        bool
	gridSt      ui.GridState
	gridLast    int // the last item the grid built
	thumbCtx    context.Context
	thumbCancel context.CancelFunc

	sel    ui.Selection[string] // by path
	cursor int
	list   ui.ListState
	sort   tableSort
}

// localRoot is a place the sidebar starts the computer's directories at.
type localRoot struct {
	name, dir string
}

// localRoots are the home directory, the disks and the mounted shares.
func localRoots() []localRoot {
	var out []localRoot
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, localRoot{"主目录", home})
	}
	if runtime.GOOS == "windows" {
		for d := 'A'; d <= 'Z'; d++ {
			dir := string(d) + `:\`
			if _, err := os.Stat(dir); err == nil {
				out = append(out, localRoot{string(d) + ": 盘", dir})
			}
		}
		return out
	}
	out = append(out, localRoot{"文件系统", "/"})
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		dir := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\134`, `\`).Replace(fields[1])
		kind := fields[2]
		name := filepath.Base(dir)
		switch {
		case strings.Contains(dir, "/kio-fuse-"):
			name = "网络位置（KDE）"
		case kind == "cifs" || kind == "smb3" || strings.HasPrefix(kind, "nfs"):
		case strings.HasPrefix(dir, "/run/media/") || strings.HasPrefix(dir, "/media/") || strings.HasPrefix(dir, "/mnt/"):
		default:
			continue
		}
		if !slices.ContainsFunc(out, func(r localRoot) bool { return r.dir == dir }) {
			out = append(out, localRoot{name, dir})
		}
	}
	return out
}

// networkMounts are the directories network shares are mounted on, where
// reading a directory goes over the network.
func networkMounts() []string {
	out := []string{}
	if runtime.GOOS == "windows" {
		return out
	}
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		dir := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\134`, `\`).Replace(fields[1])
		switch kind := fields[2]; {
		case kind == "cifs", kind == "smb3", strings.HasPrefix(kind, "nfs"), kind == "davfs",
			strings.HasPrefix(kind, "fuse.") && kind != "fuse.portal" && kind != "fuse.gvfsd-fuse" && kind != "fuse.ntfs-3g" && kind != "fuse.ntfs":
			out = append(out, dir)
		}
	}
	return out
}

// onNetwork reports whether reading dir goes over the network.
func (a *app) onNetwork(dir string) bool {
	if a.netMounts == nil {
		a.netMounts = networkMounts()
	}
	return slices.ContainsFunc(a.netMounts, func(m string) bool { return within(dir, m) })
}

// probeSubdirs reports, of the first directories of dirs, which hold
// directories of their own: the sidebar shows the others as leaves, with
// no arrow to open. Those on the network are not read, nor asked about.
func probeSubdirs(dirs []fileindex.Entry, hidden bool, network func(string) bool) map[string]bool {
	out := map[string]bool{}
	for _, e := range dirs[:min(len(dirs), 300)] {
		if network(e.Path) {
			continue
		}
		f, err := os.Open(e.Path)
		if err != nil {
			continue // shown openable, which tells why it cannot be read
		}
		has := false
		for !has {
			ents, err := f.ReadDir(64)
			for _, d := range ents {
				if (hidden || !strings.HasPrefix(d.Name(), ".")) && (d.IsDir() || d.Type()&os.ModeSymlink != 0) {
					has = true // a link may be to a directory
					break
				}
			}
			if err != nil {
				break
			}
		}
		f.Close()
		out[e.Path] = has
	}
	return out
}

// openLocal shows a local directory.
func (a *app) openLocal(dir string, choose ...string) {
	if a.local == nil {
		a.local = &localBrowser{cursor: -1}
		b := a.local
		b.list.Key = func(i int) any { return b.items[i].Path }
		b.list.Selection = &b.sel
		b.list.Selected = &b.cursor
		b.sort.by = ui.SortOrder{Column: colName}
		b.list.Sort = &b.sort.by
		b.gridSt.Key = b.list.Key
		b.gridSt.Selection = &b.sel
		b.gridSt.Selected = &b.cursor
	}
	b := a.local
	if b.thumbCancel != nil {
		b.thumbCancel()
	}
	b.thumbCtx, b.thumbCancel = context.WithCancel(context.Background())
	a.ensureLocalIndex()
	a.cancelLocalSearch()
	b.results, b.query, b.matched = false, "", nil
	b.filter = searchFilter{}
	a.place = localPlace
	if b.dir != dir {
		b.items = nil
		b.sel.Clear()
		b.cursor = -1
	}
	b.dir = dir
	a.revealLocal(dir)
	b.gen++
	gen := b.gen
	b.loading, b.err = true, ""
	hidden := b.hidden
	network := a.networkTest()
	go func() {
		items, err := readLocal(dir, false, hidden)
		probed := probeSubdirs(dirsOf(items), hidden, network)
		a.post(func() {
			if a.closed || a.local != b || b.gen != gen {
				return
			}
			b.loading = false
			if err != nil {
				b.err = "无法读取 " + dir + ": " + err.Error()
				return
			}
			a.cacheLocal(dir, items, probed)
			sortEntries(&b.sort, items, nil)
			b.items = items
			// The selection keeps what is still there.
			for p := range b.sel.All() {
				if !slices.ContainsFunc(items, func(e fileindex.Entry) bool { return e.Path == p }) {
					b.sel.Remove(p)
				}
			}
			for _, name := range choose {
				b.sel.Add(filepath.Join(dir, name))
			}
		})
	}()
}

// readLocal lists a directory, directories first then by name; with
// dirsOnly only its directories.
func readLocal(dir string, dirsOnly, hidden bool) ([]fileindex.Entry, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]fileindex.Entry, 0, len(ents))
	for _, d := range ents {
		name := d.Name()
		if !hidden && strings.HasPrefix(name, ".") {
			continue
		}
		p := filepath.Join(dir, name)
		e := fileindex.Entry{Name: name, Path: p, Parent: dir, IsDir: d.IsDir()}
		if info, err := d.Info(); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				// A link to a directory opens as one.
				if st, err := os.Stat(p); err == nil && st.IsDir() {
					e.IsDir = true
				}
			}
			e.Size, e.MTime = info.Size(), info.ModTime().Unix()
		}
		if dirsOnly && !e.IsDir {
			continue
		}
		if !e.IsDir {
			e.Ext = extOf(name)
		}
		out = append(out, e)
	}
	slices.SortStableFunc(out, func(x, y fileindex.Entry) int {
		if x.IsDir != y.IsDir {
			if x.IsDir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})
	return out, nil
}

// chosen returns the entries chosen, in the order shown.
func (b *localBrowser) chosen() []fileindex.Entry {
	var out []fileindex.Entry
	for _, e := range b.items {
		if b.sel.Has(e.Path) {
			out = append(out, e)
		}
	}
	return out
}

// localPath is, under the toolbar, where the page is.
func (a *app) localPath(c *ui.Context) {
	b := a.local
	crumbs, dirs := localCrumbs(b.dir)
	if b.results {
		crumbs = append(crumbs, "搜索结果")
	}
	chosen := len(crumbs) - 1
	if ui.Breadcrumbs(c, crumbs, &chosen).Label("本机位置").Shrink(1).MinWidth(0).Changed() && chosen < len(dirs) {
		a.openLocal(dirs[chosen])
	}
}

// localView is the page of a local directory, or of what the search of
// this computer found: its commands, and its files.
func (a *app) localView(c *ui.Context) {
	b := a.local
	t := c.Theme()
	busy := b.loading || b.queryPending
	es := b.chosen()
	if busy {
		es = nil
	}
	n := len(es)
	if n > 0 && !b.searching && c.Shortcut(0, ui.KeyEscape) {
		b.sel.Clear()
	}
	if c.Shortcut(0, ui.KeyF5) {
		a.refreshLocal()
	}
	// Backing up and showing in the file manager are in the menu of the
	// files.
	commandBar(c, n, filesSize(es), func() { b.sel.Clear() }, func() {
		if b.results {
			filterBar(c, "local", &b.filter, func(wait bool) {
				if wait {
					a.queueLocalSearch(150 * time.Millisecond)
				} else {
					a.queueLocalSearch(0)
				}
			})
		}
	}, nil, func() {
		viewToggle(c, "local-view-toggle", &b.grid)
		moreMenu(c, "local-more", func(m *ui.Menu) {
			if m.Item("显示隐藏文件").Checked(b.hidden).Chosen() {
				b.hidden = !b.hidden
				if b.results {
					a.queueLocalSearch(0)
				} else {
					a.invalidateLocalTree()
					a.openLocal(b.dir)
				}
			}
			if m.Item("刷新").Shortcut(0, ui.KeyF5).Chosen() {
				a.refreshLocal()
			}
			if idx := a.localIdx; idx != nil {
				m.Separator()
				if m.Item("更新本机索引").Disabled(idx.running || idx.loading).Chosen() {
					a.startLocalScan()
				}
				if m.Item("本机索引范围…").Disabled(idx.running || idx.loading).Chosen() {
					a.editLocalIndex()
				}
			}
		})
	})
	if b.err != "" {
		notice(c, t.Danger, b.err)
	}
	if m := a.localIdx; m != nil && m.err != "" {
		closableNotice(c, t.Danger, m.err, func() { m.err = "" })
	}
	open := func(e fileindex.Entry) {
		switch {
		case busy:
		case b.results:
			a.openIndexedLocal(e, false, false)
		default:
			a.openLocalEntry(e)
		}
	}
	zone := a.fileList(c, &fileView{
		key: "local-files", label: "本机文件", items: b.items, sel: &b.sel, cursor: &b.cursor,
		list: &b.list, gridSt: &b.gridSt, grid: b.grid, sort: &b.sort,
		loading: b.loading, results: b.results,
		where: func(e fileindex.Entry) string { return homeTilde(e.Parent) },
		thumb: a.localThumb,
		open:  open,
		resort: func() {
			if b.results {
				a.queueLocalSearch(0)
			} else {
				sortEntries(&b.sort, b.items, &b.cursor)
			}
		},
		visible: func(first, last int) {
			if b.awaitTop && (first == 0 || last < 0) {
				b.awaitTop = false
			}
			if b.results && !b.awaitTop && last >= len(b.items)-40 {
				a.loadMoreLocal()
			}
		},
		gridLast: &b.gridLast,
		empty: func() {
			switch {
			case busy || b.err != "":
			case b.results:
				a.localSearchEmpty(c)
			default:
				emptyFolder(c, "")
			}
		},
	})
	zone.ContextMenu(func(m *ui.Menu) {
		es := b.chosen()
		if busy {
			es = nil
		}
		if len(es) == 1 && m.Item("打开").Chosen() {
			open(es[0])
		}
		if b.results && len(es) == 1 && m.Item("打开所在目录").Chosen() {
			a.openIndexedLocal(es[0], false, true)
		}
		if len(es) == 1 {
			m.Separator()
		}
		label := "备份当前文件夹…"
		if len(es) > 0 {
			label = "创建备份任务…"
		}
		if (!b.results || len(es) > 0) && m.Item(label).Disabled(busy || b.err != "").Chosen() {
			a.backupLocalSelection(es)
		}
		switch {
		case b.results && len(es) == 1:
			if m.Item("在文件管理器中显示").Chosen() {
				a.openIndexedLocal(es[0], true, false)
			}
		case !b.results:
			if m.Item("在文件管理器中显示").Disabled(busy).Chosen() {
				a.openPath(b.dir)
			}
		}
	})
}

// refreshLocal reads the directory shown again, or searches again.
func (a *app) refreshLocal() {
	if b := a.local; b.results {
		a.queueLocalSearch(0)
	} else {
		a.openLocal(b.dir)
	}
}

// localStatus is, in the status bar, what the page shows and has chosen.
func (a *app) localStatus(c *ui.Context) {
	b := a.local
	es := b.chosen()
	if b.loading || b.queryPending {
		es = nil
	}
	n := len(es)
	status := ""
	switch {
	case b.results && (b.loading || b.queryPending) && len(b.items) == 0:
		status = "正在搜索…"
	case b.results && b.total > len(b.items):
		status = fmt.Sprintf("找到 %d 项 · 已显示 %d 项", b.total, len(b.items))
	case b.results:
		status = fmt.Sprintf("找到 %d 项", b.total)
	case !b.loading || len(b.items) > 0:
		status = itemsCount(len(b.items))
	}
	if n > 0 {
		var size int64
		for _, e := range es {
			if !e.IsDir {
				size += e.Size
			}
		}
		status += " · 已选 " + itemsCount(n)
		if size > 0 {
			status += "，文件共 " + formatBytes(size)
		}
	}
	statusText(c, status)
}

func itemsCount(n int) string { return fmt.Sprintf("%d 项", n) }

// openLocalEntry goes into a directory, or opens a file with its app.
func (a *app) openLocalEntry(e fileindex.Entry) {
	if e.IsDir {
		a.openLocal(e.Path)
		return
	}
	a.openPath(e.Path)
}

// localCrumbs are the names of a directory and of its parents, from the
// top, and their paths.
func localCrumbs(dir string) (names, dirs []string) {
	for d := dir; ; {
		name := filepath.Base(d)
		if parent := filepath.Dir(d); parent == d {
			name = d
			if name == "/" {
				name = "文件系统"
			}
			names, dirs = append(names, name), append(dirs, d)
			break
		} else {
			names, dirs = append(names, name), append(dirs, d)
			d = parent
		}
	}
	slices.Reverse(names)
	slices.Reverse(dirs)
	return names, dirs
}

// The sidebar's tree of the computer's directories, read as it opens.

// A directory shows under each place of the sidebar it is in, as the
// home directory under the file system's root too: its node is of the
// place it is under, top, and of the directory.
func localKey(top, dir string) string { return "local/" + top + "\x00" + dir }

func (a *app) localNode(top, dir string) *directoryNode {
	key := localKey(top, dir)
	if n := a.tree[key]; n != nil {
		return n
	}
	n := &directoryNode{}
	a.tree[key] = n
	return n
}

// localTop is the nearest place of the sidebar dir is in, "" for none:
// where the sidebar shows it chosen.
func (a *app) localTop(dir string) string {
	var top string
	for _, r := range a.localRoots {
		if within(dir, r.dir) && len(r.dir) > len(top) {
			top = r.dir
		}
	}
	return top
}

// revealLocal opens the branches down to dir, from the nearest place it
// is in.
func (a *app) revealLocal(dir string) {
	top := a.localTop(dir)
	if top == "" {
		return
	}
	for d := dir; ; d = filepath.Dir(d) {
		if d != dir {
			a.localNode(top, d).open = true
		}
		if d == top || filepath.Dir(d) == d {
			break
		}
	}
	*a.sectionOpen("local") = true
}

// within reports whether dir is base or inside it.
func within(dir, base string) bool {
	rel, err := filepath.Rel(base, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// cacheLocal keeps the directories of dir, read, in its nodes under every
// place it is in; those probed to hold none become leaves.
func (a *app) cacheLocal(dir string, items []fileindex.Entry, probed map[string]bool) {
	dirs := dirsOf(items)
	slices.SortStableFunc(dirs, func(x, y fileindex.Entry) int { return naturalCompare(x.Name, y.Name) })
	for _, r := range a.localRoots {
		if !within(dir, r.dir) {
			continue
		}
		n := a.localNode(r.dir, dir)
		n.gen++
		n.dirs, n.err, n.loaded, n.loading = dirs, "", true, false
		for _, e := range dirs {
			if has, ok := probed[e.Path]; ok && !has {
				if child := a.localNode(r.dir, e.Path); !child.loaded && !child.loading {
					child.dirs, child.err, child.loaded = nil, "", true
				}
			}
		}
	}
}

func dirsOf(items []fileindex.Entry) []fileindex.Entry {
	var dirs []fileindex.Entry
	for _, e := range items {
		if e.IsDir {
			dirs = append(dirs, e)
		}
	}
	return dirs
}

// networkTest is onNetwork, off the main thread: the mounts read now.
func (a *app) networkTest() func(string) bool {
	a.onNetwork("")
	mounts := slices.Clone(a.netMounts)
	return func(dir string) bool {
		return slices.ContainsFunc(mounts, func(m string) bool { return within(dir, m) })
	}
}

// invalidateLocalTree reads the computer's directories of the sidebar
// again as they show, as when hidden files show or not.
func (a *app) invalidateLocalTree() {
	for key, n := range a.tree {
		if strings.HasPrefix(key, "local/") {
			n.gen++
			n.loaded, n.loading = false, false
		}
	}
}

// localDirectoryView is a directory of the computer in the sidebar, under
// the place top.
func (a *app) localDirectoryView(c *ui.Context, top, dir, name string, svg *ui.SVG) {
	key := localKey(top, dir)
	n := a.localNode(top, dir)
	item := ui.TreeItem(c.Key(key), "", n.openState(), n.children(func() {
		if !n.loaded && !n.loading {
			a.loadLocalDirectory(top, dir, n)
		}
		switch {
		case n.loading:
			n.loadingText(c)
		case n.err != "":
			ui.Button(c.Key(key+"/retry"), "重试读取目录").Tooltip(n.err).OnClick(func() { n.loaded = false })
		default:
			for _, e := range n.dirs {
				a.localDirectoryView(c, top, e.Path, e.Name, iconFolder)
			}
		}
	}))
	chosen := a.place == localPlace && a.local != nil && a.local.dir == dir && a.localTop(dir) == top
	color := ui.Hex("#d99a2b")
	if svg != iconFolder {
		color = c.Theme().TextMuted
	}
	if chosen {
		color = c.Theme().AccentText
	}
	item.Children(func() { labeled(c, svg, name, color) }).Label("本机文件夹 " + name).Tooltip(dir)
	item.ContextMenu(func(m *ui.Menu) {
		if m.Item("打开").Chosen() {
			a.openLocal(dir)
		}
		if m.Item("创建备份任务…").Chosen() {
			a.newTaskFrom(dir, nil)
		}
		if m.Item("在文件管理器中显示").Chosen() {
			a.openPath(dir)
		}
		m.Separator()
		if m.Item("刷新").Chosen() {
			n.loaded, n.open = false, true
			if a.place == localPlace && a.local != nil && a.local.dir == dir {
				a.openLocal(dir)
			}
		}
	})
	item.Selected(chosen).OnClick(func() {
		if !chosen {
			a.openLocal(dir)
		}
	})
}

func (a *app) loadLocalDirectory(top, dir string, n *directoryNode) {
	n.loading, n.loadStart = true, time.Now()
	n.gen++
	gen := n.gen
	key := localKey(top, dir)
	hidden := a.local != nil && a.local.hidden
	network := a.networkTest()
	go func() {
		items, err := readLocal(dir, true, hidden)
		probed := probeSubdirs(items, hidden, network)
		a.post(func() {
			if a.closed || a.tree[key] != n || n.gen != gen {
				return
			}
			n.loading, n.loaded = false, true
			if err != nil {
				n.err = err.Error()
				return
			}
			a.cacheLocal(dir, items, probed)
		})
	}()
}

// localItems are the computer's places in the sidebar.
func (a *app) localItems(c *ui.Context) {
	if a.localRoots == nil {
		a.localRoots = localRoots()
	}
	for _, r := range a.localRoots {
		svg := iconFolder
		switch {
		case r.name == "主目录":
			svg = iconHome
		case r.name == "文件系统" || strings.HasSuffix(r.name, " 盘"):
			svg = iconDisk
		case r.name == "网络位置（KDE）":
			svg = iconNetwork
		}
		a.localDirectoryView(c, r.dir, r.dir, r.name, svg)
	}
}

// startLocal is the directory the page of the computer opens at.
func (a *app) startLocal() string {
	if a.local != nil && a.local.dir != "" {
		return a.local.dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return string(filepath.Separator)
}
