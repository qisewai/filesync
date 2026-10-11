package main

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// The window's navigation: the history of the places shown, which the
// toolbar's back and forward buttons, Alt+Left and Alt+Right and the side
// buttons of a mouse move through.
//
// The places stay where the app keeps them (a.place, the browsers); the
// router keeps their history as locations:
//
//	/                          nothing chosen
//	/local?dir=<dir>           a directory of this computer
//	/nas/<server>/<root>?path= a directory of a root of a server
//	/search?q=<query>          the search of every place
//	/backup                    the backup tasks
//	/admin/<server>/<tab>      the management of a server
//	/trash/<server>            the trash of a server
//
// A directory is in the query, so that the directories of a place are one
// page of the router, which stays as they change.

// location is the location of the place shown.
func (a *app) location() string {
	switch {
	case a.place == "settings":
		return "/settings"
	case a.place == "backup" && a.taskShown != "":
		return "/backup?task=" + url.QueryEscape(a.taskShown)
	case a.place == "backup":
		return "/backup"
	case a.place == "search" && a.gsearch != nil:
		return "/search?q=" + url.QueryEscape(a.gsearch.query) + "&in=" + url.QueryEscape(a.gsearch.scope)
	case a.place == localPlace && a.local != nil:
		return "/local?dir=" + url.QueryEscape(a.local.dir)
	case a.admin != nil && a.place == adminPlace(a.admin.server, a.admin.tab):
		return "/admin/" + url.PathEscape(a.admin.server) + "/" + a.admin.tab
	case a.recycle != nil && a.place == trashPlace(a.recycle.server):
		return "/trash/" + url.PathEscape(a.recycle.server)
	case a.br != nil && a.place == rootPlace(a.br.server, a.br.root.ID):
		loc := "/nas/" + url.PathEscape(a.br.server) + "/" + strconv.FormatInt(a.br.root.ID, 10)
		if a.br.path != "" {
			loc += "?path=" + url.QueryEscape(a.br.path)
		}
		return loc
	}
	return "/"
}

// goLocation shows the place at a location of the history.
func (a *app) goLocation(loc string) {
	u, err := url.Parse(loc)
	if err != nil {
		return
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	q := u.Query()
	switch {
	case parts[0] == "backup":
		a.place = "backup"
		a.taskShown = q.Get("task")
	case parts[0] == "search":
		a.openSearch(q.Get("q"))
		if in := q.Get("in"); in != "" {
			a.setScope(in)
		}
	case parts[0] == "settings":
		a.place = "settings"
	case parts[0] == "local":
		dir := q.Get("dir")
		if dir == "" || !filepath.IsAbs(dir) {
			dir = a.startLocal()
		}
		a.openLocal(dir)
	case parts[0] == "admin" && len(parts) == 3:
		a.openAdmin(parts[1], parts[2])
	case parts[0] == "trash" && len(parts) == 2:
		a.openTrash(parts[1])
	case parts[0] == "nas" && len(parts) == 3:
		for _, r := range a.roots[parts[1]] {
			if strconv.FormatInt(r.ID, 10) == parts[2] {
				a.showDirectory(parts[1], r, q.Get("path"))
			}
		}
	default:
		a.place = ""
	}
}

// followHistory shows the place the router went to, back or forward,
// since the last frame.
func (a *app) followHistory() {
	if loc := a.nav.Location(); loc != a.navShown {
		a.navShown = loc
		a.goLocation(loc)
	}
}

// recordHistory adds the place shown to the history, as the app went
// there: the first replaces the empty page the router starts with.
func (a *app) recordHistory() {
	loc := a.location()
	if loc == a.navShown {
		return
	}
	// The search as it is typed is one place.
	searching := strings.HasPrefix(loc, "/search") && strings.HasPrefix(a.navShown, "/search")
	if a.nav.Location() == "/" && !a.nav.CanGoBack() || searching {
		a.nav.Replace(loc)
	} else {
		a.nav.Push(loc)
	}
	a.navShown = loc
}

// pageTitle names the page shown: in the toolbar of the pages without a
// path, and for screen readers.
func (a *app) pageTitle() string {
	title := func(s, server string) string {
		if sv := a.store.Server(server); sv != nil {
			return s + " · " + sv.Title()
		}
		return s
	}
	switch {
	case a.place == "backup":
		return "备份任务"
	case a.place == "search":
		return "搜索"
	case a.place == "settings":
		return "设置"
	case a.place == localPlace && a.local != nil:
		return "本机 · " + filepath.Base(a.local.dir)
	case a.admin != nil && a.place == adminPlace(a.admin.server, a.admin.tab):
		return title("管理", a.admin.server)
	case a.recycle != nil && a.place == trashPlace(a.recycle.server):
		return title("回收站", a.recycle.server)
	case a.br != nil && a.place == rootPlace(a.br.server, a.br.root.ID):
		return a.br.root.Name
	}
	return "FileSync"
}

// onLocal and onRoot report whether the page shown is a directory of this
// computer, or of a server.
func (a *app) onLocal() bool { return a.place == localPlace && a.local != nil }
func (a *app) onAdmin() bool {
	return a.admin != nil && a.place == adminPlace(a.admin.server, a.admin.tab)
}
func (a *app) onSearch() bool { return a.place == "search" && a.gsearch != nil }
func (a *app) onRoot() bool {
	return a.br != nil && a.place == rootPlace(a.br.server, a.br.root.ID)
}

// topBar is the toolbar over the pages: back and forward, up, then the
// search, all along.
func (a *app) topBar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Padding(8, 16, 8, 10).Gap(8).AlignItems(ui.Center).MinHeight(52).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Toolbar(c, func() {
			ui.BackButton(c, a.nav).Tooltip("后退（Alt+←）")
			ui.ForwardButton(c, a.nav).Tooltip("前进（Alt+→）")
			up := a.upward()
			iconButton(c, "go-up", iconUp, "上级文件夹", "上级文件夹（Alt+↑）", up == nil, func() {
				if up != nil {
					up()
				}
			})
			if up != nil && c.Shortcut(ui.Alt, ui.KeyUp) {
				up()
			}
		}).Width(3*36 + 8).Shrink(0).Label("导航")
		switch {
		case a.onLocal():
			a.fileSearch(c, localPlace)
		case a.onRoot():
			a.fileSearch(c, a.br.server)
		case a.onSearch():
			a.searchHeader(c)
		default:
			a.anywhereSearch(c)
		}
	})
}

// locationBar is the row under the toolbar: where the page is, or its
// title; the management's tabs.
func (a *app) locationBar(c *ui.Context) {
	ui.Row(c).Padding(10, 24, 0, 24).Gap(8).AlignItems(ui.Center).MinHeight(40).Children(func() {
		switch {
		case a.onLocal():
			a.localPath(c)
		case a.onRoot():
			a.browserPath(c)
		case a.onAdmin():
			a.adminHeader(c)
		default:
			ui.Text(c, a.pageTitle()).FontSize(16).Bold().SingleLine().Shrink(1)
		}
	})
}

// anywhereSearch is the field of the toolbar of the pages without files,
// which searches every place.
func (a *app) anywhereSearch(c *ui.Context) {
	q := ""
	changed, submitted, _ := a.searchBox(c, "anywhere-search", "搜索", &q, "全部位置", "搜索本机和所有服务器", func(m *ui.Menu) {
		m.Item("全部位置").Checked(true)
	})
	if changed || submitted {
		a.openSearch(q)
		a.focusSearch = true
	}
}

// upward goes to the folder holding the one shown, nil where there is
// none.
func (a *app) upward() func() {
	switch {
	case a.onLocal():
		b := a.local
		if up := filepath.Dir(b.dir); up != b.dir {
			return func() { a.openLocal(up, filepath.Base(b.dir)) }
		}
	case a.onRoot():
		b := a.br
		if b.path != "" {
			return func() { a.browse(parentDir(b.path), pathBase(b.path)) }
		}
	}
	return nil
}

// pathBase is the last part of a path of a server.
func pathBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// statusBar is the bar under the pages: what the page shows and has
// chosen, then the transfers, and the state of the index of this
// computer's files; pages of neither, without transfers, have none.
func (a *app) statusBar(c *ui.Context) {
	t := c.Theme()
	onTrash := a.recycle != nil && a.place == trashPlace(a.recycle.server)
	searched := a.onSearch() && len(a.gsearch.groups) > 0
	if !a.onLocal() && !a.onRoot() && !onTrash && !searched && len(a.transfers) == 0 {
		a.showTransfers = false
		return
	}
	ui.Row(c).Padding(4, 16).Gap(10).AlignItems(ui.Center).MinHeight(34).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		switch {
		case a.onLocal():
			a.localStatus(c)
		case a.onRoot():
			a.browserStatus(c)
		case onTrash:
			a.trashStatus(c)
		case a.onSearch():
			a.searchStatus(c)
		}
		ui.Spacer(c)
		a.transferIndicator(c)
		if a.onLocal() {
			a.localIndexStatus(c)
		}
	})
}

// transferIndicator shows, in the status bar, the transfers under way or
// how the last ended; a click shows them in a popover.
func (a *app) transferIndicator(c *ui.Context) {
	t := c.Theme()
	if len(a.transfers) == 0 {
		a.showTransfers = false
		return
	}
	active, failed := 0, 0
	var done, total int64
	for _, tr := range a.transfers {
		switch {
		case tr.running():
			active++
			if tr.total > 0 {
				done, total = done+min(tr.done, tr.total), total+tr.total
			}
		case tr.state == "error":
			failed++
		}
	}
	text, color := "传输已完成", t.Success
	switch {
	case active > 0:
		text, color = fmt.Sprintf("%d 个传输", active), t.TextMuted
		if total > 0 {
			text += fmt.Sprintf(" · %d%%", done*100/total)
		}
	case failed > 0:
		text, color = fmt.Sprintf("%d 个传输失败", failed), t.Danger
	}
	anchor := ui.ButtonBase(c.Key("transfers-indicator")).Padding(3, 8).Gap(6).AlignItems(ui.Center).Radius(t.Radius).
		Label("传输任务").Tooltip("查看传输任务")
	if anchor.Hovered() || a.showTransfers {
		anchor.Background(t.SurfaceHover)
	}
	anchor.Children(func() {
		switch {
		case active > 0:
			ui.Spinner(c).FontSize(12).Label("正在传输")
		case failed > 0:
			ui.Icon(c, iconAlert).FontSize(13).TextColor(color)
		default:
			ui.Icon(c, iconCheck).FontSize(13).TextColor(color)
		}
		ui.Text(c, text).FontSize(12).SingleLine().FontFeatures("tnum").TextColor(color)
	})
	if anchor.Clicked() {
		a.showTransfers = !a.showTransfers
	}
	ui.PopoverBase(c, anchor, &a.showTransfers, func(panel ui.Element) {
		_, h := c.Size()
		panel.Width(460).MaxHeight(min(480, h-80)).Margin(6, 0).Padding(12).Gap(8).Radius(t.Radius*2).
			Background(t.Background).Border(1, t.Border).Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.18))
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "传输任务").Bold().Grow(1)
			ui.Button(c.Key("clear-transfers"), "清除已完成").Disabled(active == len(a.transfers)).OnClick(a.clearTransfers)
		})
		ui.Scroll(c).Shrink(1).MinHeight(0).Gap(6).Children(func() {
			for _, tr := range a.transfers {
				a.transferRow(c, tr)
			}
		})
	})
}
