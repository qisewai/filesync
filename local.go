package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"filesync/fileindex"

	"github.com/egoist/mygo/ui"
)

// localPlace is the page of a local directory.
const localPlace = "local"

// localBrowser shows a directory of this computer, to back up what is
// chosen in it.
type localBrowser struct {
	dir     string
	items   []fileindex.Entry // Path is the full path
	loading bool
	err     string
	gen     int
	hidden  bool // shows the files whose names start with a dot

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
	}
	b := a.local
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
	go func() {
		items, err := readLocal(dir, false, hidden)
		a.post(func() {
			if a.closed || a.local != b || b.gen != gen {
				return
			}
			b.loading = false
			if err != nil {
				b.err = "无法读取 " + dir + ": " + err.Error()
				return
			}
			a.cacheLocal(dir, items)
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

// localView is the page of a local directory.
func (a *app) localView(c *ui.Context) {
	b := a.local
	t := c.Theme()
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		up := filepath.Dir(b.dir)
		if ui.Button(c, "上级文件夹").Disabled(up == b.dir).Clicked() {
			a.openLocal(up, filepath.Base(b.dir))
		}
		crumbs, dirs := localCrumbs(b.dir)
		chosen := len(crumbs) - 1
		if ui.Breadcrumbs(c, crumbs, &chosen).Label("本机位置").Grow(1).Shrink(1).Changed() && chosen < len(dirs) {
			a.openLocal(dirs[chosen])
		}
	})

	es := b.chosen()
	if b.loading {
		es = nil
	}
	n := len(es)
	ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
		backup := ui.PrimaryButton(c, "创建备份任务…").Disabled(b.loading || b.err != "")
		if n == 0 {
			backup.Tooltip("备份当前文件夹；也可以先选中要备份的文件夹或文件")
		}
		if backup.Clicked() {
			a.newTaskFrom(b.dir, es)
		}
		if ui.Button(c, "在文件管理器中显示").Clicked() {
			a.openPath(b.dir)
		}
		ui.Spacer(c)
		if ui.Switch(c.Key("hidden"), &b.hidden).Label("显示隐藏文件").Changed() {
			a.openLocal(b.dir)
		}
		ui.Text(c, "隐藏文件").FontSize(12).TextColor(t.TextMuted)
		if ui.Button(c, "刷新").Clicked() {
			a.openLocal(b.dir)
		}
	})
	if b.err != "" {
		notice(c, t.Danger, b.err)
	}
	zone := ui.Column(c).Grow(1).MinHeight(0).Radius(t.Radius*2).Border(1, t.Border).Clip().Children(func() {
		if b.loading && len(b.items) == 0 {
			ui.Column(c).Grow(1).Center().Children(func() { ui.Spinner(c).FontSize(22).Label("正在读取") })
			return
		}
		if b.sort.stale() {
			sortEntries(&b.sort, b.items, &b.cursor)
		}
		table := ui.Table(c.Key("local-files"), &b.list, fileColumns(), len(b.items), func(row, col int) {
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
			}
		}).Grow(1).Label("本机文件")
		table.Children(func() {
			if !b.loading && b.err == "" && len(b.items) == 0 {
				ui.Text(c, "空文件夹").TextColor(t.TextMuted).Padding(16)
			}
		})
		if table.Submitted() && b.cursor >= 0 && b.cursor < len(b.items) {
			a.openLocalEntry(b.items[b.cursor])
		}
	})
	zone.ContextMenu(func(m *ui.Menu) {
		es := b.chosen()
		if len(es) == 1 && m.Item("打开").Chosen() {
			a.openLocalEntry(es[0])
		}
		label := "备份当前文件夹…"
		if len(es) > 0 {
			label = "创建备份任务…"
		}
		if m.Item(label).Chosen() {
			a.newTaskFrom(b.dir, es)
		}
	})

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
	ui.Text(c, status).FontSize(12).TextColor(t.TextMuted)
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

func localKey(dir string) string { return "local/" + dir }

func (a *app) localNode(dir string) *directoryNode {
	key := localKey(dir)
	if n := a.tree[key]; n != nil {
		return n
	}
	n := &directoryNode{}
	a.tree[key] = n
	return n
}

// revealLocal opens the branches down to dir, from the root it is in.
func (a *app) revealLocal(dir string) {
	var top string
	for _, r := range a.localRoots {
		if within(dir, r.dir) && len(r.dir) > len(top) {
			top = r.dir
		}
	}
	if top == "" {
		return
	}
	for d := dir; ; d = filepath.Dir(d) {
		if d != dir {
			a.localNode(d).open = true
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

func (a *app) cacheLocal(dir string, items []fileindex.Entry) {
	n := a.localNode(dir)
	n.gen++
	n.dirs, n.err, n.loaded, n.loading = nil, "", true, false
	for _, e := range items {
		if e.IsDir {
			n.dirs = append(n.dirs, e)
		}
	}
	slices.SortStableFunc(n.dirs, func(x, y fileindex.Entry) int { return naturalCompare(x.Name, y.Name) })
}

// localDirectoryView is a directory of the computer in the sidebar.
func (a *app) localDirectoryView(c *ui.Context, dir, name string, svg *ui.SVG) {
	key := localKey(dir)
	n := a.localNode(dir)
	item := ui.TreeItem(c.Key(key), "", &n.open, func() {
		if !n.loaded && !n.loading {
			a.loadLocalDirectory(dir, n)
		}
		switch {
		case n.loading:
			ui.Text(c, "正在读取…").FontSize(12).Padding(0, 20)
		case n.err != "":
			ui.Button(c.Key(key+"/retry"), "重试读取目录").Tooltip(n.err).OnClick(func() { n.loaded = false })
		default:
			for _, e := range n.dirs {
				a.localDirectoryView(c, e.Path, e.Name, iconFolder)
			}
		}
	})
	chosen := a.place == localPlace && a.local != nil && a.local.dir == dir
	color := ui.Hex("#d99a2b")
	if svg != iconFolder {
		color = c.Theme().TextMuted
	}
	if chosen {
		color = c.Theme().AccentText
	}
	item.Children(func() { labeled(c, svg, name, color) }).Label("本机文件夹 " + name).Tooltip(dir)
	item.Selected(chosen).OnClick(func() {
		if !chosen {
			a.openLocal(dir)
		}
	})
}

func (a *app) loadLocalDirectory(dir string, n *directoryNode) {
	n.loading = true
	n.gen++
	gen := n.gen
	key := localKey(dir)
	hidden := a.local != nil && a.local.hidden
	go func() {
		items, err := readLocal(dir, true, hidden)
		a.post(func() {
			if a.closed || a.tree[key] != n || n.gen != gen {
				return
			}
			n.loading, n.loaded = false, true
			if err != nil {
				n.err = err.Error()
				return
			}
			a.cacheLocal(dir, items)
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
		a.localDirectoryView(c, r.dir, r.name, svg)
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
