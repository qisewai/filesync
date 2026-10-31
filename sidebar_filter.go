package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"filesync/config"

	"github.com/egoist/mygo/ui"
)

// The filter at the foot of the sidebar: typed, the sidebar lists the
// folders it knows whose names start with the typed text or pinyin (the favorites, the
// places of this computer, the roots of the servers and the folders read
// in its tree), each going there.

// filterHit is a folder the filter found.
type filterHit struct {
	loc   string // its location (nav.go), going there
	name  string
	where string // where it is, shown under its name
	icon  *ui.SVG
	color ui.Color
	order int // favorites first, then places, roots and the rest
}

// maxFilterHits keeps the list short: what is typed narrows it.
const maxFilterHits = 200

// filterHits match the same English/pinyin prefixes as the file browser.
func (a *app) filterHits(q string) []filterHit {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []filterHit
	add := func(h filterHit) {
		if seen[h.loc] || !filenameMatches(h.name, q) {
			return
		}
		seen[h.loc] = true
		out = append(out, h)
	}
	for _, f := range a.store.Favorites {
		if f.Local == "" && !a.serverConnected(f.Server) {
			continue
		}
		add(filterHit{loc: favoriteLocation(f), name: f.Name, where: a.favoriteWhere(f), icon: iconStar, color: ui.Hex("#d99a2b"), order: 0})
	}
	for _, r := range a.localRoots {
		add(filterHit{loc: localLocation(r.dir), name: r.name, where: r.dir, icon: iconLaptop, color: ui.Color{}, order: 1})
	}
	for _, sv := range a.store.Servers {
		if !a.serverConnected(sv.ID) {
			continue
		}
		for _, r := range a.roots[sv.ID] {
			add(filterHit{loc: nasLocation(sv.ID, r.ID, ""), name: r.Name, where: sv.Title(), icon: iconRoots, order: 2})
		}
	}
	for key, n := range a.tree {
		if !n.loaded {
			continue
		}
		server, root, ok := parseTreeKey(key)
		if ok && !a.serverConnected(server) {
			continue
		}
		for _, d := range n.dirs {
			if ok {
				add(filterHit{loc: nasLocation(server, root, d.Path), name: d.Name, where: a.nasWhere(server, root, parentDir(d.Path)), icon: iconFolder, color: folderColor, order: 3})
			} else {
				add(filterHit{loc: localLocation(d.Path), name: d.Name, where: filepath.Dir(d.Path), icon: iconFolder, color: folderColor, order: 3})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].order != out[j].order {
			return out[i].order < out[j].order
		}
		if out[i].name != out[j].name {
			return naturalCompare(out[i].name, out[j].name) < 0
		}
		return out[i].where < out[j].where
	})
	if len(out) > maxFilterHits {
		out = out[:maxFilterHits]
	}
	return out
}

// parseTreeKey reads the server and the root of a key of a folder of a
// server's tree (treeKey); ok is false for those of this computer.
func parseTreeKey(key string) (server string, root int64, ok bool) {
	rest, found := strings.CutPrefix(key, "root/")
	if !found {
		return "", 0, false
	}
	server, rest, found = strings.Cut(rest, "/")
	if !found {
		return "", 0, false
	}
	id, _, _ := strings.Cut(rest, "/")
	root, err := strconv.ParseInt(id, 10, 64)
	return server, root, err == nil
}

// nasWhere is a folder of a server as shown: server › root › folder.
func (a *app) nasWhere(server string, root int64, dir string) string {
	title := server
	if sv := a.store.Server(server); sv != nil {
		title = sv.Title()
	}
	rootName := fmt.Sprint(root)
	for _, r := range a.roots[server] {
		if r.ID == root {
			rootName = r.Name
		}
	}
	return strings.Join(nonEmpty(title, rootName, dir), " › ")
}

func nonEmpty(parts ...string) []string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// favoriteLocation is the location of a favorite.
func favoriteLocation(f *config.Favorite) string {
	if f.Local != "" {
		return localLocation(f.Local)
	}
	return nasLocation(f.Server, f.Root, f.Path)
}

// sidebarFilterResults lists the folders the filter found, in place of
// the tree.
func (a *app) sidebarFilterResults(c *ui.Context) {
	t := c.Theme()
	hits := a.filterHits(a.sidebarFilter)
	if len(hits) == 0 {
		ui.Text(c, "没有找到名称以“"+strings.TrimSpace(a.sidebarFilter)+"”开头的文件夹。支持英文或拼音前缀，只在已读取的目录中查找。").
			FontSize(12).TextColor(t.TextMuted).Padding(8, 10)
		return
	}
	shown := a.location()
	ui.Column(c.Key("filter-hits")).Gap(1).Label("找到的文件夹").Children(func() {
		for i, h := range hits {
			item := ui.ButtonBase(c.Key("filter/"+h.loc)).Padding(5, 8).Gap(8).AlignItems(ui.Center).Radius(t.Radius).
				Label(h.name + " · " + h.where).Tooltip(h.where).OnClick(func() { a.goLocation(h.loc) })
			if i == 0 {
				item.Bind(&a.sidebarFilterResult)
			}
			switch {
			case h.loc == shown:
				item.Background(chosenGray(t))
			case item.Hovered():
				item.Background(t.SurfaceHover)
			}
			a.bindSidebarTyping(c, item)
			item.ContextMenu(func(m *ui.Menu) {
				if m.Item("打开").Chosen() {
					a.goLocation(h.loc)
				}
				if m.Item("在新标签页中打开").Chosen() {
					a.newTab(h.loc)
				}
			})
			item.Children(func() {
				color := h.color
				if color == (ui.Color{}) {
					color = t.TextMuted
				}
				ui.Icon(c, h.icon).FontSize(15).TextColor(color)
				ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
					ui.Text(c, h.name).SingleLine()
					ui.Text(c, h.where).FontSize(11).SingleLine().TextColor(t.TextMuted)
				})
			})
		}
	})
}

// sidebarFoot is the foot of the sidebar: the filter, the settings and
// adding a server.
func (a *app) sidebarFoot(c *ui.Context) {
	t := c.Theme()
	ui.Row(c.Key("sidebar-foot")).Padding(8).Gap(4).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		if a.sidebarFilter == "" {
			ui.Spacer(c)
		} else {
			in := ui.TextInput(c.Key("sidebar-filter"), &a.sidebarFilter).Bind(&a.sidebarFilterInput).
				Label("过滤侧边栏").Placeholder("英文 / 拼音前缀").FontSize(13).Grow(1).Shrink(1).MinWidth(0).
				OnTextKey(func(k ui.TextKey) bool {
					if k.Key == ui.KeyEscape && k.Mods == 0 {
						k.SetText("")
						a.clearSidebarFilter(c)
						return true
					}
					if k.Mods == 0 && (k.Key == ui.KeyEnter || k.Key == ui.KeyDown) && len(a.filterHits(a.sidebarFilter)) > 0 {
						a.sidebarFilterResult.Focus(c)
						return true
					}
					return false
				})
			if a.sidebarFilterFocus {
				in.Focus()
				n := len([]rune(a.sidebarFilter))
				in.SetTextSelection(n, n)
				a.sidebarFilterFocus = false
			}
			if in.Changed() {
				if a.sidebarFilter == "" {
					a.clearSidebarFilter(c)
				}
				c.Invalidate()
			}
			a.searchTyping = a.searchTyping || in.Focused()
			flatIcon(c, "sidebar-filter-clear", iconClose, "清除过滤", "清除过滤（Esc）", false, func() { a.clearSidebarFilter(c) })
		}
		settings := flatIcon(c, "settings", iconGear, "设置", "服务器、本机索引与其他设置", false, func() { a.place = "settings" })
		if a.place == "settings" {
			settings.Background(chosenGray(t))
		}
		flatIcon(c, "add-server", iconPlus, "添加服务器", "添加 fileindex 服务器", false, func() {
			a.openServers()
			a.nasStartEdit(nil)
		})
	})
}

// Typing belongs to the focused sidebar node, never the address/search fields
// or the file list. Once started, the normal text input handles IME and editing.
func (a *app) bindSidebarTyping(c *ui.Context, e ui.Element) {
	if a.sidebarFilter != "" {
		e.OnShortcut(0, ui.KeyEscape, func() { a.clearSidebarFilter(c) })
	}
	e.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind != ui.InputText {
			return false
		}
		text := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, ev.Text)
		if text == "" {
			return false
		}
		a.sidebarFilter += text
		a.sidebarFilterFocus = true
		return true
	}).TextCaret(ui.Rect{X: 8, Y: 8, H: 20}).InputMethod(false)
}

func (a *app) clearSidebarFilter(c *ui.Context) {
	a.sidebarFilter = ""
	a.sidebarFilterFocus = false
	a.sidebarFocus.Focus(c)
}
