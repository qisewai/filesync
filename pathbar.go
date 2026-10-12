package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"filesync/config"
	"filesync/fileindex"

	"github.com/egoist/mygo/ui"
)

// The path bar under the toolbar: the folders down to the one shown, each
// a link, each with an arrow listing the folders in it, as file managers
// have; a click beside them, or Ctrl+L, edits the path as text.

// crumb is a folder of the path shown.
type crumb struct {
	name string
	// open goes to it, nil for the folder shown.
	open func()
	// dirs are the folders in it, ready false while they are read, which
	// it starts; nil for a folder whose folders are not listed.
	dirs func() (dirs []fileindex.Entry, ready bool)
	// openDir goes to one of them.
	openDir func(e fileindex.Entry)
}

// pathEdit is the path being edited as text, in the bar of key: a full
// path of this computer, or a server's name, a root's, then the folders.
type pathEdit struct {
	key  string
	text string
	// gen renews the input once a suggestion completes the text, its
	// caret at the end; open shows the suggestions that follow then.
	gen     int
	open    bool
	focused bool // the field had the focus: losing it ends the editing
}

// pathOption is a suggestion of the path bar: the text it completes to,
// and how it shows.
type pathOption struct {
	value, name, hint string
	svg               *ui.SVG
}

// pathSuggestMax is how many folders the suggestions list.
const pathSuggestMax = 50

// crumbMenuMax is how many folders the menu of a crumb lists.
const crumbMenuMax = 300

// pathBar shows the crumbs; editing shows the path as text, starting
// with text, with what it may go on to suggested.
func (a *app) pathBar(c *ui.Context, key, label string, crumbs []crumb, text string) {
	t := c.Theme()
	edit := func() {
		a.pathEditing, a.pathDirs = &pathEdit{key: key, text: text, open: true}, nil
	}
	if e := a.pathEditing; e != nil && e.key == key {
		a.pathEditor(c, e, label)
		return
	}
	if c.Shortcut(ui.Ctrl, ui.KeyL) {
		edit()
	}
	ui.Row(c.Key(key)).Gap(1).AlignItems(ui.Center).Shrink(1).MinWidth(0).Label(label).Children(func() {
		for i, cr := range crumbs {
			if cr.open == nil {
				// Where the path is: no link.
				ui.Row(c.Key(i)).Padding(4, 6).Shrink(0.5).MinWidth(24).Children(func() {
					ui.Text(c, cr.name).SingleLine().FontWeight(500)
				})
			} else {
				b := ui.ButtonBase(c.Key(i)).Padding(4, 6).Radius(t.Radius).Shrink(1).MinWidth(24).Label(cr.name).OnClick(cr.open)
				b.Children(func() { ui.Text(c, cr.name).SingleLine().TextColor(t.TextMuted) })
				if b.Hovered() {
					b.Background(t.SurfaceHover)
				}
			}
			if cr.dirs != nil {
				a.crumbArrow(c, i, cr)
			}
		}
	})
	// The rest of the bar edits the path.
	blank := ui.ButtonBase(c.Key(key + "/blank")).Grow(1).MinWidth(16).Height(28).Label("编辑路径").Tooltip("点击输入路径（Ctrl+L）").OnClick(edit)
	if blank.Hovered() {
		blank.Background(t.SurfaceHover.Alpha(0.5))
	}
}

// crumbArrow is the arrow after a crumb, a menu of the folders in it.
func (a *app) crumbArrow(c *ui.Context, i int, cr crumb) {
	t := c.Theme()
	arrow := ui.ButtonBase(c.Key(fmt.Sprintf("dirs/%d", i))).Size(18, 26).Radius(t.Radius).Label("“" + cr.name + "”中的文件夹")
	arrow.Children(func() { ui.Icon(c, iconNext).FontSize(12).TextColor(t.TextMuted) })
	// Read before the menu opens, as the pointer comes.
	if arrow.Hovered() || arrow.Focused() {
		arrow.Background(t.SurfaceHover)
		cr.dirs()
	}
	arrow.Menu(func(m *ui.Menu) {
		dirs, ready := cr.dirs()
		switch {
		case !ready:
			m.Item("正在读取…").Disabled(true)
		case len(dirs) == 0:
			m.Item("没有子文件夹").Disabled(true)
		}
		for j, e := range dirs {
			if j == crumbMenuMax {
				m.Item(fmt.Sprintf("还有 %d 个文件夹…", len(dirs)-j)).Disabled(true)
				break
			}
			if m.Item(e.Name).Chosen() {
				cr.openDir(e)
			}
		}
	})
}

// localPathBar is the path bar of a folder of this computer.
func (a *app) localPathBar(c *ui.Context, dir string) {
	names, dirs := localCrumbs(dir)
	crumbs := make([]crumb, len(names))
	for i := range names {
		d := dirs[i]
		crumbs[i] = crumb{name: names[i], dirs: a.localDirs(d), openDir: func(e fileindex.Entry) { a.openLocal(e.Path) }}
		if i < len(names)-1 {
			crumbs[i].open = func() { a.openLocal(d, filepath.Base(dirs[i+1])) }
		}
	}
	text := dir
	if !strings.HasSuffix(text, string(filepath.Separator)) {
		text += string(filepath.Separator)
	}
	a.pathBar(c, "local-path", "本机位置", crumbs, text)
}

// localDirs lists the folders of a local directory for its crumb, from
// the nodes of the sidebar's tree, read as they are asked for.
func (a *app) localDirs(dir string) func() ([]fileindex.Entry, bool) {
	top := a.localTop(dir)
	if top == "" {
		return nil
	}
	return func() ([]fileindex.Entry, bool) {
		n := a.localNode(top, dir)
		if !n.loaded && !n.loading {
			a.loadLocalDirectory(top, dir, n)
		}
		return n.dirs, n.loaded && !n.loading
	}
}

// serverPathBar is the path bar of a directory of a server.
func (a *app) serverPathBar(c *ui.Context, server string, root fileindex.Root, dir string) {
	sv := a.store.Server(server)
	if sv == nil {
		return
	}
	names, dirs := []string{root.Name}, []string{""}
	if dir != "" {
		for _, part := range strings.Split(dir, "/") {
			names, dirs = append(names, part), append(dirs, path.Join(dirs[len(dirs)-1], part))
		}
	}
	crumbs := make([]crumb, len(names))
	for i := range names {
		d := dirs[i]
		crumbs[i] = crumb{
			name: names[i],
			dirs: func() ([]fileindex.Entry, bool) {
				n := a.treeNode(server, root.ID, d)
				if !n.loaded && !n.loading {
					a.loadDirectory(sv, root.ID, d, n)
				}
				return n.dirs, n.loaded && !n.loading
			},
			openDir: func(e fileindex.Entry) { a.showDirectory(server, root, e.Path) },
		}
		if i < len(names)-1 {
			crumbs[i].open = func() { a.showDirectory(server, root, d, pathBase(dirs[i+1])) }
		}
	}
	a.pathBar(c, "server-path", "位置", crumbs, strings.TrimSuffix(sv.Title()+"/"+path.Join(root.Name, dir), "/")+"/")
}

// pathEditor is the bar editing a path, suggesting what it may go on to,
// with a button going there.
func (a *app) pathEditor(c *ui.Context, e *pathEdit, label string) {
	t := c.Theme()
	was := e.focused
	within := false // the focus is in the bar: its input or buttons
	ui.Row(c.Key(e.key + "/editing")).Gap(6).AlignItems(ui.Center).Grow(1).Shrink(1).MinWidth(0).Children(func() {
		options, problem := a.pathOptions(e.text)
		cb := ui.ComboboxBase(c.Key(fmt.Sprintf("input/%d", e.gen)), &e.text)
		border := t.Accent
		if problem != "" {
			border = t.Danger
		}
		in := cb.Input.Grow(1).Shrink(1).MinWidth(80).Padding(5, 8).Radius(t.Radius).Background(t.Surface).Border(1, border).
			Label("编辑" + label).Placeholder("服务器/索引目录/文件夹，或本机的完整路径").AutoFocus()
		if e.open {
			e.open = false
			cb.SetOpen(true)
		}
		cb.Popup(func(panel ui.Element) {
			panel.Padding(4).Radius(t.Radius*1.5).Background(t.Background).Border(1, t.Border).MaxHeight(280)
			ui.Scroll(c).MaxHeight(272).Children(func() {
				for _, o := range options {
					item := cb.Item(o.value).Padding(5, 8).Radius(t.Radius).Gap(6).AlignItems(ui.Center)
					muted := t.TextMuted
					if item.Highlighted() {
						item.Background(t.Accent).TextColor(t.AccentText)
						muted = t.AccentText.Alpha(0.8)
					}
					item.Children(func() {
						color := ui.Hex("#d99a2b")
						if o.svg != iconFolder {
							color = muted
						}
						ui.Icon(c, o.svg).FontSize(14).TextColor(color)
						ui.Text(c, o.name).SingleLine().Grow(1)
						if o.hint != "" {
							ui.Text(c, o.hint).FontSize(11).SingleLine().TextColor(muted)
						}
					})
				}
			})
		})
		focused := in.Focused()
		a.searchTyping = a.searchTyping || focused
		complete := func(v string) {
			e.text, e.gen, e.open, e.focused = v, e.gen+1, true, false
		}
		switch v, ok := cb.Chosen(); {
		case ok:
			complete(v)
		case in.Shortcut(0, ui.KeyTab) && len(options) > 0:
			complete(options[0].value)
		case in.Submitted():
			a.pathEditing = nil
			a.goPath(strings.TrimSpace(e.text))
		case !cb.Open() && in.Shortcut(0, ui.KeyEscape):
			a.pathEditing = nil
		}
		if problem != "" {
			ui.Text(c, problem).FontSize(12).TextColor(t.Danger).SingleLine().Shrink(2).MinWidth(0).Tooltip(problem)
		}
		goBtn := iconButton(c, "go", iconNext, "前往", "前往（Enter）", false, func() {
			a.pathEditing = nil
			a.goPath(strings.TrimSpace(e.text))
		})
		cancel := iconButton(c, "cancel", iconClose, "取消编辑路径", "取消（Esc）", false, func() { a.pathEditing = nil })
		within = focused || goBtn.Focused() || cancel.Focused()
		e.focused = e.focused || focused
	})
	// Leaving the bar, not for its buttons, ends the editing.
	if was && !within && a.pathEditing == e {
		a.pathEditing = nil
	}
}

// pathOptions are what a path typed may go on to, matching what was typed
// of its last part, or why it cannot go on: the servers and the places
// of this computer first, then the roots of the server named, then the
// folders.
func (a *app) pathOptions(text string) ([]pathOption, string) {
	if text == "~" {
		text += string(filepath.Separator)
	}
	if isLocalPath(text) || strings.HasPrefix(text, "~/") || strings.HasPrefix(text, `~\`) {
		return a.localOptions(text), ""
	}
	parts := strings.Split(text, "/")
	typed := parts[len(parts)-1]
	var opts []pathOption
	switch len(parts) {
	case 1: // a server, or a place of this computer
		for _, sv := range a.store.Servers {
			opts = append(opts, pathOption{value: sv.Title() + "/", name: sv.Title(), hint: sv.Addr, svg: iconServer})
		}
		for _, r := range a.localRoots {
			dir := r.dir
			if !strings.HasSuffix(dir, string(filepath.Separator)) {
				dir += string(filepath.Separator)
			}
			opts = append(opts, pathOption{value: dir, name: r.name, hint: r.dir, svg: iconLaptop})
		}
		return matchOptions(opts, typed), ""
	}
	sv := a.serverTitled(parts[0])
	if sv == nil {
		return nil, "没有叫“" + parts[0] + "”的服务器"
	}
	roots, ok := a.roots[sv.ID]
	if !ok || a.rootsLoading[sv.ID] != nil {
		if sv.Token != "" && !ok {
			a.loadRoots(sv)
		}
		if sv.Token == "" {
			return nil, "服务器“" + sv.Title() + "”未登录"
		}
		return nil, ""
	}
	if len(parts) == 2 { // a root
		for _, r := range roots {
			opts = append(opts, pathOption{value: sv.Title() + "/" + r.Name + "/", name: r.Name, hint: sv.Title(), svg: iconRoots})
		}
		return matchOptions(opts, typed), ""
	}
	root := a.rootNamed(roots, parts[1])
	if root == nil {
		return nil, "“" + sv.Title() + "”上没有索引目录“" + parts[1] + "”"
	}
	dir := strings.Join(parts[2:len(parts)-1], "/")
	n := a.treeNode(sv.ID, root.ID, dir)
	if !n.loaded && !n.loading {
		a.loadDirectory(sv, root.ID, dir, n)
	}
	if n.err != "" {
		return nil, "无法读取“" + dir + "”：" + n.err
	}
	prefix := strings.Join(parts[:len(parts)-1], "/") + "/"
	for _, d := range n.dirs {
		opts = append(opts, pathOption{value: prefix + d.Name + "/", name: d.Name, hint: path.Join(root.Name, dir), svg: iconFolder})
	}
	return matchOptions(opts, typed), ""
}

// localOptions are the folders of this computer a full path typed may go
// on to.
func (a *app) localOptions(text string) []pathOption {
	full := text
	if strings.HasPrefix(full, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			full = home + full[1:]
		}
	}
	i := strings.LastIndexAny(full, `/\`)
	if i < 0 {
		return nil
	}
	parent, typed := full[:i+1], full[i+1:]
	dirs, _ := a.localFolders(filepath.Clean(parent))
	var opts []pathOption
	for _, d := range dirs {
		// The hidden only for a dot typed.
		if strings.HasPrefix(d.Name, ".") && !strings.HasPrefix(typed, ".") {
			continue
		}
		opts = append(opts, pathOption{value: parent + d.Name + string(filepath.Separator), name: d.Name, hint: parent, svg: iconFolder})
	}
	return matchOptions(opts, typed)
}

// localFolders lists, off the main thread, the folders of a directory of
// this computer for the path bar, once while it edits.
func (a *app) localFolders(dir string) ([]fileindex.Entry, bool) {
	if a.pathDirs == nil {
		a.pathDirs = map[string]*directoryNode{}
	}
	n := a.pathDirs[dir]
	if n == nil {
		n = &directoryNode{loading: true}
		a.pathDirs[dir] = n
		go func() {
			items, err := readLocal(dir, true, true)
			a.post(func() {
				n.loading, n.loaded, n.dirs = false, true, items
				if err != nil {
					n.err = err.Error()
				}
			})
		}()
	}
	return n.dirs, n.loaded
}

// matchOptions are the options whose names match what was typed, ignoring
// case: those starting with it first, then those holding it.
func matchOptions(opts []pathOption, typed string) []pathOption {
	q := strings.ToLower(typed)
	var starts, holds []pathOption
	for _, o := range opts {
		switch l := strings.ToLower(o.name); {
		case strings.HasPrefix(l, q):
			starts = append(starts, o)
		case strings.Contains(l, q):
			holds = append(holds, o)
		}
	}
	out := append(starts, holds...)
	return out[:min(len(out), pathSuggestMax)]
}

// serverTitled is the server of a name, as the path bar shows it, nil for
// none; failing an exact match, one differing in case.
func (a *app) serverTitled(name string) *config.Server {
	for _, sv := range a.store.Servers {
		if sv.Title() == name {
			return sv
		}
	}
	for _, sv := range a.store.Servers {
		if strings.EqualFold(sv.Title(), name) {
			return sv
		}
	}
	return nil
}

func (a *app) rootNamed(roots []fileindex.Root, name string) *fileindex.Root {
	for i := range roots {
		if roots[i].Name == name {
			return &roots[i]
		}
	}
	for i := range roots {
		if strings.EqualFold(roots[i].Name, name) {
			return &roots[i]
		}
	}
	return nil
}

// goPath goes where a path typed says: a full path of this computer, a
// file to its folder, chosen there; or a server's name, a root's, then
// the folders.
func (a *app) goPath(p string) {
	if p == "" {
		return
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[1:])
		}
	}
	if isLocalPath(p) {
		a.goLocal(filepath.Clean(p))
		return
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	sv := a.serverTitled(parts[0])
	if sv == nil {
		a.errorMsg = "没有叫“" + parts[0] + "”的服务器。服务器路径写成“服务器/索引目录/文件夹”，本机写完整路径"
		return
	}
	roots, ok := a.roots[sv.ID]
	if !ok || a.rootsLoading[sv.ID] != nil {
		if sv.Token != "" {
			a.loadRoots(sv)
		}
		a.errorMsg = "服务器“" + sv.Title() + "”还没有连接，请稍后再试"
		return
	}
	if len(parts) < 2 || parts[1] == "" {
		a.errorMsg = "请在服务器名后写出索引目录，例如“" + sv.Title() + "/索引目录”"
		return
	}
	root := a.rootNamed(roots, parts[1])
	if root == nil {
		a.errorMsg = "“" + sv.Title() + "”上没有索引目录“" + parts[1] + "”"
		return
	}
	rel := strings.Join(parts[2:], "/")
	if rel != "" {
		rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	}
	// A folder known not to be there says so, rather than an empty page.
	if rel != "" {
		if n := a.tree[treeKey(sv.ID, root.ID, parentDir(rel))]; n != nil && n.loaded && !n.loading && n.err == "" &&
			!slices.ContainsFunc(n.dirs, func(d fileindex.Entry) bool { return d.Path == rel }) {
			a.errorMsg = "“" + sv.Title() + " › " + root.Name + "”中没有文件夹“" + rel + "”"
			return
		}
	}
	a.errorMsg = ""
	a.showDirectory(sv.ID, *root, rel)
}

// goLocal goes to a folder of this computer, or to the folder of a file,
// choosing it.
func (a *app) goLocal(p string) {
	fi, err := os.Stat(p)
	switch {
	case err != nil:
		a.errorMsg = "找不到本机路径：" + p
	case fi.IsDir():
		a.errorMsg = ""
		a.openLocal(p)
	default:
		a.errorMsg = ""
		a.openLocal(filepath.Dir(p), filepath.Base(p))
	}
}

// isLocalPath reports whether p is a path of this computer.
func isLocalPath(p string) bool {
	if runtime.GOOS == "windows" {
		return filepath.VolumeName(p) != "" || strings.HasPrefix(p, `\`)
	}
	return strings.HasPrefix(p, "/")
}
