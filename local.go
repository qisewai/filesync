package main

import (
	"bufio"
	"context"
	"filemanager/api"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"filemanager/remoteclient"
	"filesync/config"

	"github.com/egoist/mygo/ui"
)

// localPlace is the page of a local directory.
const localPlace = "local"

// localBrowser shows a directory of this computer, to back up what is
// chosen in it.
type localBrowser struct {
	selectionCache fileSelectionCache

	filter  filenameFilter
	dir     string
	items   []fileindex.Entry // Path is the full path
	loading bool
	err     string
	gen     int
	hidden  bool // shows the files whose names start with a dot

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
		out = append(out, userDirs(home)...)
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

// userDirs are the folders of the desktop, documents, downloads and the
// like, those there: on Linux as user-dirs.dirs of the desktop sets them,
// on Windows in the profile.
func userDirs(home string) []localRoot {
	names := []struct{ key, name, fallback string }{
		{"DESKTOP", "桌面", "Desktop"},
		{"DOCUMENTS", "文档", "Documents"},
		{"DOWNLOAD", "下载", "Downloads"},
		{"PICTURES", "图片", "Pictures"},
		{"MUSIC", "音乐", "Music"},
		{"VIDEOS", "视频", "Videos"},
	}
	set := map[string]string{}
	if runtime.GOOS != "windows" {
		conf := os.Getenv("XDG_CONFIG_HOME")
		if conf == "" {
			conf = filepath.Join(home, ".config")
		}
		set = readUserDirs(filepath.Join(conf, "user-dirs.dirs"), home)
	}
	var out []localRoot
	for _, n := range names {
		dir, ok := set[n.key]
		if !ok {
			dir = filepath.Join(home, n.fallback)
		}
		// The desktop sets a folder it does not use to the home itself.
		if filepath.Clean(dir) == filepath.Clean(home) {
			continue
		}
		// Two set to one folder show once: the tree keys its nodes by it.
		if slices.ContainsFunc(out, func(r localRoot) bool { return r.dir == dir }) {
			continue
		}
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			out = append(out, localRoot{n.name, dir})
		}
	}
	return out
}

// readUserDirs reads the folders of user-dirs.dirs, by key, as DOWNLOAD,
// from lines as XDG_DOWNLOAD_DIR="$HOME/下载".
func readUserDirs(file, home string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(file)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || !strings.HasPrefix(key, "XDG_") || !strings.HasSuffix(key, "_DIR") {
			continue
		}
		value = strings.Trim(value, `"`)
		if rest, ok := strings.CutPrefix(value, "$HOME"); ok {
			value = home + rest
		}
		if filepath.IsAbs(value) {
			out[strings.TrimSuffix(strings.TrimPrefix(key, "XDG_"), "_DIR")] = value
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
func (a *app) probeSubdirs(dirs []fileindex.Entry, hidden bool, network func(string) bool) map[string]bool {
	out := map[string]bool{}
	for _, e := range dirs[:min(len(dirs), 300)] {
		if network(e.Path) {
			continue
		}
		children, err := a.readLocalProbe(e.Path, true, hidden, true)
		if err == nil {
			out[e.Path] = len(children) > 0
		}
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
	a.place = localPlace
	if b.dir != dir {
		b.items = nil
		b.filter = filenameFilter{}
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
		items, err := a.readLocal(dir, false, hidden)
		probed := a.probeSubdirs(dirsOf(items), hidden, network)
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
			b.filter.source(items)
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
func (a *app) readLocal(dir string, dirsOnly, hidden bool) ([]fileindex.Entry, error) {
	return a.readLocalProbe(dir, dirsOnly, hidden, false)
}

func (a *app) readLocalProbe(dir string, dirsOnly, hidden, probe bool) ([]fileindex.Entry, error) {
	if a.backend == nil {
		if a.backendErr != nil {
			return nil, a.backendErr
		}
		return nil, fmt.Errorf("本机 FileManager 后台未连接")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	entries, err := a.backend.List(ctx, api.ListRequest{Location: backendLocation(abs), Hidden: hidden, DirsOnly: dirsOnly, Probe: probe})
	if err != nil {
		return nil, err
	}
	out := make([]fileindex.Entry, 0, len(entries))
	for _, e := range entries {
		ext := ""
		if !e.IsDir {
			ext = extOf(e.Name)
		}
		out = append(out, fileindex.Entry{Name: e.Name, Path: filepath.Join(abs, e.Name), Parent: abs, IsDir: e.IsDir, Link: e.Link, Size: e.Size, MTime: e.MTime, Ext: ext})
	}
	return out, nil
}

// chosen returns the entries chosen, in the order shown.
func (b *localBrowser) chosen() []fileindex.Entry {
	return b.selectionCache.chosen(b.items, &b.sel, b.sort.revision)
}

// localPath is, under the toolbar, where the page is.
func (a *app) localPath(c *ui.Context) {
	a.localPathBar(c, a.local.dir)
	a.favoriteButton(c)
}

// localView is the page of a local directory: its commands, and its
// files.
func (a *app) localView(c *ui.Context) {
	b := a.local
	t := c.Theme()
	busy := b.loading
	es := b.chosen()
	if busy {
		es = nil
	}
	n := len(es)
	if n > 0 && !a.searchTyping && c.Shortcut(0, ui.KeyEscape) {
		b.sel.Clear()
	}
	if c.Shortcut(0, ui.KeyF5) {
		a.openLocal(b.dir)
	}
	// The keys of a file manager: Delete moves what is chosen to the
	// trash, Shift+Delete deletes it for good, F2 renames it, Ctrl+Shift+N
	// makes a folder; not while typing.
	if !busy && !a.searchTyping && a.naming == nil && a.asking == nil {
		switch {
		case n > 0 && c.Shortcut(ui.Shift, ui.KeyDelete):
			a.localDelete(entryPaths(es), true)
		case n > 0 && c.Shortcut(0, ui.KeyDelete):
			a.localDelete(entryPaths(es), false)
		case n == 1 && c.Shortcut(0, ui.KeyF2):
			a.localRename(es[0].Path)
		case c.Shortcut(ui.Ctrl|ui.Shift, ui.KeyN):
			a.localMkdir(b.dir)
		}
	}
	if b.err != "" {
		notice(c, t.Danger, b.err)
	}
	if m := a.localIdx; m != nil && m.err != "" {
		closableNotice(c, t.Danger, m.err, func() { m.err = "" })
	}
	open := func(e fileindex.Entry) {
		if !busy {
			a.openLocalEntry(e)
		}
	}
	zone := a.fileList(c, &fileView{
		filter: &b.filter,
		key:    "local-files", label: "本机文件", items: b.items, sel: &b.sel, cursor: &b.cursor,
		list: &b.list, gridSt: &b.gridSt, grid: b.grid, sort: &b.sort,
		loading:  b.loading,
		thumb:    a.localThumb,
		open:     open,
		cut:      a.clipCut(),
		resort:   func() { sortEntries(&b.sort, b.items, &b.cursor) },
		gridLast: &b.gridLast,
		empty: func() {
			if !busy && b.err == "" {
				emptyFolder(c, "")
			}
		},
	})
	zone.ContextMenu(func(m *ui.Menu) {
		groupMenu(m, &b.sort.group)
		es := b.chosen()
		if busy {
			es = nil
		}
		a.localMenu(c, m, es)
	})
}

// localMenu is the menu of the files of this computer chosen, or of the
// folder shown without any.
func (a *app) localMenu(c *ui.Context, m *ui.Menu, es []fileindex.Entry) {
	b := a.local
	busy := b.loading
	if len(es) == 0 {
		a.clipItems(c, m, nil)
		m.Separator()
		if m.Item("新建文件夹…").Shortcut(ui.Ctrl|ui.Shift, ui.KeyN).Disabled(busy || b.err != "").Chosen() {
			a.localMkdir(b.dir)
		}
		if m.Item("显示隐藏文件").Checked(b.hidden).Chosen() {
			b.hidden = !b.hidden
			a.invalidateLocalTree()
			a.openLocal(b.dir)
		}
		if m.Item("刷新").Shortcut(0, ui.KeyF5).Chosen() {
			a.openLocal(b.dir)
		}
		m.Separator()
		if m.Item("备份当前文件夹…").Disabled(busy || b.err != "").Chosen() {
			a.backupLocal(b.dir, nil)
		}
		if m.Item("在文件管理器中显示").Disabled(busy).Chosen() {
			a.openPath(b.dir)
		}
		if m.Item("文件夹属性").Disabled(busy || b.err != "").Chosen() {
			a.showLocalProps(b.dir)
		}
		return
	}
	if len(es) == 1 {
		if archiveEntry(es[0]) {
			if m.Item("打开压缩包").Chosen() {
				a.openLocalEntry(es[0])
			}
			if m.Item("解压到…").Chosen() {
				a.extractArchive(localPlace, nil, es[0])
			}
		}
		if m.Item("打开").Chosen() {
			a.openLocalEntry(es[0])
		}
		if es[0].IsDir && m.Item("在新标签页中打开").Disabled(len(a.tabs) >= maxTabs).Chosen() {
			a.openLocalInNewTab(es[0].Path)
		}
	}
	m.Separator()
	a.clipItems(c, m, es)
	m.Separator()
	if m.Item("重命名…").Shortcut(0, ui.KeyF2).Disabled(len(es) != 1).Chosen() {
		a.localRename(es[0].Path)
	}
	if m.Item("删除…").Shortcut(0, ui.KeyDelete).Chosen() {
		a.localDelete(entryPaths(es), false)
	}
	if m.Item("彻底删除…").Shortcut(ui.Shift, ui.KeyDelete).Chosen() {
		a.localDelete(entryPaths(es), true)
	}
	m.Separator()
	if len(es) == 1 && es[0].IsDir {
		a.favoriteMenuItem(m, &config.Favorite{Name: es[0].Name, Local: es[0].Path})
	}
	if m.Item("创建备份任务…").Chosen() {
		a.backupLocal(b.dir, es)
	}
	if m.Item("在文件管理器中显示").Chosen() {
		a.openPath(b.dir)
	}
	if len(es) == 1 && m.Item("属性").Chosen() {
		a.showLocalProps(es[0].Path)
	}
}

// entryPaths are the paths of entries of this computer.
func entryPaths(es []fileindex.Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Path
	}
	return out
}

// localStatus is, in the status bar, what the page shows and has chosen.
func (a *app) localStatus(c *ui.Context) {
	b := a.local
	es := b.chosen()
	if b.loading {
		es = nil
	}
	n := len(es)
	status := ""
	if !b.loading || len(b.items) > 0 {
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
	if archiveEntry(e) {
		a.openArchive(localPlace, nil, e, "")
		return
	}
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

// revealLocal opens the branches down to dir, and dir's, from the nearest
// place it is in, as the tree of a server does.
func (a *app) revealLocal(dir string) {
	top := a.localTop(dir)
	if top == "" {
		return
	}
	for d := dir; ; d = filepath.Dir(d) {
		a.localNode(top, d).open = true
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
	item := a.animatedTreeItem(c.Key(key), "", n.openState(), n.children(func() {
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
	color := folderColor
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
		if m.Item("在新标签页中打开").Disabled(len(a.tabs) >= maxTabs).Chosen() {
			a.openLocalInNewTab(dir)
		}
		a.favoriteMenuItem(m, &config.Favorite{Name: name, Local: dir})
		m.Separator()
		if m.Item("创建备份任务…").Chosen() {
			a.newTaskFrom(dir, nil)
		}
		if m.Item("在文件管理器中显示").Chosen() {
			a.openPath(dir)
		}
		if m.Item("属性").Chosen() {
			a.showLocalProps(dir)
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
		items, err := a.readLocal(dir, true, hidden)
		probed := a.probeSubdirs(items, hidden, network)
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
		a.localDirectoryView(c, r.dir, r.dir, r.name, localRootIcon(r.name))
	}
}

// localRootIcon is the icon of a place of the computer, by its name.
func localRootIcon(name string) *ui.SVG {
	switch {
	case name == "主目录":
		return iconHome
	case name == "文件系统" || strings.HasSuffix(name, " 盘"):
		return iconDisk
	case name == "网络位置（KDE）":
		return iconNetwork
	case name == "桌面":
		return iconLaptop
	case name == "文档":
		return iconDoc
	case name == "下载":
		return iconSave
	case name == "图片":
		return iconImage
	case name == "音乐":
		return iconAudio
	case name == "视频":
		return iconVideo
	}
	return iconFolder
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

// localChanged lists dir again, if the page shows it.
func (a *app) localChanged(dir string, choose ...string) {
	if a.place == localPlace && a.local != nil && a.local.dir == dir {
		a.openLocal(dir, choose...)
	}
}
