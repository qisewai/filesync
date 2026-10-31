package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"filemanager/remoteclient"
	"filesync/config"

	"github.com/egoist/mygo/ui"
)

func (a *app) view(c *ui.Context) {
	// Try saved sessions once, even with a collapsed or filtered sidebar.
	for _, sv := range a.store.Servers {
		if _, attempted := a.roots[sv.ID]; !attempted {
			a.loadRoots(sv)
		}
	}
	theme := appTheme(c.Theme())
	if s := c.Preferences().TextScale; a.plainText && s > 0 && s != 1 {
		theme.FontSize /= s
	}
	c.SetTheme(theme)
	a.followHistory()
	a.applyFilenameFilter()
	a.searchShortcuts(c)
	a.tabShortcuts(c)
	a.searchTyping = a.searchTyping || a.filenameFilterFocused(c)
	a.clipShortcuts(c)
	a.searchTyping = a.filenameFilterFocused(c) || a.sidebarFilterInput.Focused(c)
	c.OnShortcut(ui.Ctrl, ui.KeyB, a.toggleSidebar)
	c.OnShortcut(ui.Ctrl, ui.KeyI, a.toggleDetails)
	// Keep the window outline and viewer backdrop the same with either
	// layout. A collapsed sidebar takes no room and has no resize handle.
	content := ui.Row(c.Key("main-content")).Fill().Children(func() {
		animatedPanel(c, "sidebar-panel", !a.sidebarHidden, a.sidebarW, func() { a.sidebar(c) })
		if !a.sidebarHidden {
			a.panelDivider(c, "侧边栏宽度", &a.sidebarW, minSidebar, maxSidebar, false)
		}
		a.mainPane(c)
	})
	// A frameless window has no shadow on Wayland: an outline tells it
	// from what is behind it.
	if a.ownControls && !a.maximized {
		content.Border(1, c.Theme().Border)
	}
	if a.viewerCovers() {
		// Under the viewer's opaque backdrop, the page is built, keeping
		// its state, but not drawn: drawn, its rounded clips would be
		// masked on the CPU while the picture is scaled, a second at once.
		content.Opacity(0)
	}
	if a.askQuit {
		switch ui.AlertDialog(c, &a.askQuit, "任务正在进行，要停止并退出吗？",
			"已完成的文件会保留，本机传输会停止，下次备份可继续。复制、移动、查重和索引扫描会向服务器请求取消；服务器无法连接时，这些任务可能继续运行。", "继续任务", "停止并退出") {
		case 1:
			a.quit()
		}
	}
	a.syncTray()
	a.recordHistory()
	a.filePreviewView(c)
	a.fileDialogs(c)
	a.propsView(c)
	a.nameDialog(c)
	a.compressionView(c)
	a.favoriteDialogs(c)
	a.taskDialog(c)
	a.nasView(c)
	a.localIndexDialog(c)
	if a.confirmRemove {
		if task := a.task(); task != nil {
			if ui.AlertDialog(c, &a.confirmRemove, fmt.Sprintf("删除任务“%s”？", task.Name),
				"只删除任务设置和运行记录，不会删除本地或 NAS 上的任何文件。", "取消", "删除") == 1 {
				a.removeTask()
			}
		}
	}
}

func (a *app) pagePane(c *ui.Context) {
	ui.Column(c.Key("page-pane")).Label("文件浏览区域").Grow(1).Shrink(1).MinWidth(0).Fill().Children(func() {
		a.tabBar(c)
		a.topBar(c)
		a.nav.View(c, func(r *ui.Route) {
			r.Title(a.pageTitle())
			ui.Column(c).Grow(1).MinHeight(0).Padding(12, 16, 12, 16).Gap(10).Children(func() {
				if a.errorMsg != "" && a.place != "backup" {
					closableNotice(c, c.Theme().Danger, a.errorMsg, func() { a.errorMsg = "" })
				}
				a.page(c)
			})
		})
		a.statusBar(c)
	})
}

func (a *app) toggleSidebar() {
	a.sidebarHidden = !a.sidebarHidden
	a.store.SidebarHidden = a.sidebarHidden
	a.save()
}

// page builds the page of the place shown.
func (a *app) page(c *ui.Context) {
	switch {
	case a.admin != nil && a.place == adminPlace(a.admin.server, a.admin.tab):
		a.adminView(c)
	case a.place == "backup":
		a.backupPage(c)
	case a.place == "settings":
		a.settingsPage(c)
	case a.onSearch():
		a.searchPage(c)
	case a.onArchive():
		a.archiveView(c)
	case a.onLocal():
		a.localView(c)
	case a.recycle != nil && a.place == trashPlace(a.recycle.server):
		a.trashView(c)
	case a.onRoot():
		a.browserView(c)
	default:
		// Nothing else to show: the search, the first page, from the next
		// frame (the toolbar of this one has a field of its own).
		a.openSearch("")
	}
}

// The sidebar's width, which the user drags between these.
const (
	defaultSidebar = 232
	minSidebar     = 180
	maxSidebar     = 480
)

func rootPlace(server string, root int64) string { return fmt.Sprintf("root/%s/%d", server, root) }

func (a *app) sidebar(c *ui.Context) {
	t := c.Theme()
	sidebar := ui.Column(c).Fill().Background(t.Surface).Focusable().Bind(&a.sidebarFocus).Label("侧边栏")
	a.bindSidebarTyping(c, sidebar)
	sidebar.Children(func() {
		a.sidebarHead(c)
		ui.Scroll(c).Grow(1).MinHeight(0).Padding(8, 8).Children(func() {
			if strings.TrimSpace(a.sidebarFilter) != "" {
				a.sidebarFilterResults(c)
				return
			}
			// Rows as roomy as a source list's.
			prev := c.Theme()
			roomy := *prev
			roomy.Spacing = 5
			c.SetTheme(&roomy)
			grayChoice(c, func() { a.sidebarTree(c) })
			c.SetTheme(prev)
		})
		a.sidebarFoot(c)
	})
}

// sidebarTree is the tree of the places, in the sidebar.
func (a *app) sidebarTree(c *ui.Context) {
	{
		{
			a.animatedTree(c.Key("servers-tree"), func() {
				a.navItem(c, "search", iconSearch, "搜索", func() {
					query := ""
					if a.gsearch != nil {
						query = a.gsearch.query
					}
					a.openSearch(query)
					a.focusSearch = true
				})
				favs := a.animatedTreeItem(c.Key("favorites"), "", a.sectionOpen("favorites"), func() { a.favoriteItems(c) })
				favs.Children(func() { labeled(c, iconStar, "收藏", c.Theme().TextMuted) }).Label("收藏")
				a.localItems(c)
				a.serverList(c)
				bk := a.navItem(c, "backup", iconBackup, "备份任务", func() { a.place = "backup" })
				a.backupMark(c, bk)
			}).Label("位置")
		}
	}
}

// serverList is the servers in the sidebar, each a section of its
// directories.
func (a *app) serverList(c *ui.Context) {
	if len(a.store.Servers) == 0 {
		a.navItem(c, "add-server", iconPlus, "添加服务器…", func() {
			a.openServers()
			a.nasStartEdit(nil)
		})
		return
	}
	for _, sv := range a.store.Servers {
		if !a.serverConnected(sv.ID) {
			continue
		}
		node := a.animatedTreeItem(c.Key("server/"+sv.ID), "", a.sectionOpen("server/"+sv.ID), func() {
			a.serverItems(c, sv)
		})
		tip := sv.Addr
		node.Children(func() {
			ui.Icon(c, iconServer).FontSize(16).TextColor(c.Theme().TextMuted).Margin(0, 4, 0, 0)
			sectionTitle(c, sv.Title())
			if a.unreachable(sv) {
				tip = sv.Addr + " · 无法连接，点右侧图标或右键重新连接"
			}
			a.serverMark(c, sv)
		}).Label(sv.Title()).Tooltip(tip)
		node.ContextMenu(func(m *ui.Menu) { a.serverMenu(m, sv) })
	}
}

// backupMark shows, beside the backup tasks in the sidebar, the backup
// under way, or how the last run of a task ended when it did not well.
func (a *app) backupMark(c *ui.Context, item ui.Element) {
	t := c.Theme()
	if a.busy() {
		item.Children(func() { ui.Spinner(c).FontSize(12).Label("正在备份") })
		return
	}
	failed := 0
	for _, task := range a.store.Tasks {
		if len(task.History) > 0 && task.History[0].Status != config.StatusOK {
			failed++
		}
	}
	if failed > 0 {
		item.Children(func() {
			ui.Icon(c, iconAlert).FontSize(13).TextColor(t.Warning).Tooltip(fmt.Sprintf("%d 个任务上次没有完全成功", failed))
		})
	}
}

// sectionOpen is whether a branch of the sidebar shows its items, open
// until closed.
func (a *app) sectionOpen(key string) *bool {
	open := a.open[key]
	if open == nil {
		open = new(bool)
		*open = true
		a.open[key] = open
	}
	return open
}

// navItem is a leaf of the sidebar going to place, highlighted there.
func (a *app) navItem(c *ui.Context, place string, svg *ui.SVG, label string, action func()) ui.Element {
	t := c.Theme()
	selected := a.place == place
	color := t.TextMuted
	if selected {
		color = t.AccentText // on the accent of the item chosen
	}
	item := a.animatedTreeItem(c.Key("nav/"+place), "", nil, nil)
	item.Children(func() { labeled(c, svg, label, color) }).Label(label)
	return item.Selected(selected).OnClick(action)
}

// serverItems are the roots of a server, then its trash and its
// management.
func (a *app) serverItems(c *ui.Context, sv *config.Server) {
	t := c.Theme()
	if sv.Token == "" {
		a.navItem(c, "login/"+sv.ID, iconServer, "登录…", func() { a.openLogin(sv.ID, func() {}) })
		return
	}
	roots, ok := a.roots[sv.ID]
	if !ok {
		a.loadRoots(sv)
	}
	if !ok || a.rootsLoading[sv.ID] != nil {
		ui.Text(c, "正在连接…").FontSize(12).TextColor(t.TextMuted).Padding(4, 28)
		return
	}
	if err := a.serverErr[sv.ID]; err != nil {
		ui.Column(c).Padding(2, 8, 4, 28).Gap(2).Children(func() {
			dim := t.TextMuted.Alpha(0.7)
			ui.Text(c, "无法连接").FontSize(12).TextColor(dim)
			ui.Text(c, connectError(err)).FontSize(11).MaxLines(3).TextColor(dim).Tooltip(err.Error())
		})
		return
	}
	if len(roots) == 0 {
		ui.Text(c, "还没有索引目录，可在服务器的右键菜单“目录管理…”中添加").FontSize(11).MaxLines(2).TextColor(t.TextMuted).Padding(2, 8, 4, 28)
	}
	for _, r := range roots {
		a.directoryView(c, sv, r, "", r.Name)
	}
	if fileindex.IsPeer(sv.Addr) {
		return
	}
	a.navItem(c, trashPlace(sv.ID), iconTrash, "回收站", func() { a.openTrash(sv.ID) })
	// The management, in the tab shown last.
	tab := "roots"
	if a.admin != nil && a.admin.server == sv.ID {
		tab = a.admin.tab
	}
	a.navItem(c, adminPlace(sv.ID, tab), iconGear, "管理", func() { a.openAdmin(sv.ID, tab) }).Label("管理 " + sv.Title())
}

// unreachable reports whether a server could not be reached, the last
// time it was tried.
func (a *app) unreachable(sv *config.Server) bool {
	return sv.Token != "" && a.rootsLoading[sv.ID] == nil && a.serverErr[sv.ID] != nil
}

// serverMark shows, beside a server in the sidebar, whether it is
// connecting, signed out or unreachable; a click on the last reconnects.
func (a *app) serverMark(c *ui.Context, sv *config.Server) {
	t := c.Theme()
	switch {
	case sv.Token == "":
		ui.Text(c, "未登录").FontSize(11).TextColor(t.TextMuted)
	case a.rootsLoading[sv.ID] != nil:
		ui.Spinner(c).FontSize(12).Label("正在连接 " + sv.Title())
	case a.serverErr[sv.ID] != nil:
		mark := ui.ButtonBase(c.Key("reconnect/"+sv.ID)).Size(22, 22).Radius(t.Radius).Label("重新连接 " + sv.Title()).
			Tooltip("无法连接：" + connectError(a.serverErr[sv.ID]) + "\n点击重新连接")
		hover := mark.Hovered()
		mark.Children(func() {
			svg := iconAlert
			if hover {
				svg = iconRetry
			}
			ui.Icon(c, svg).FontSize(15).TextColor(t.Danger)
		})
		if hover {
			mark.Background(t.Danger.Alpha(0.12))
		}
		mark.OnClick(func() { a.reconnect(sv) })
	}
}

// serverMenu is the menu of a server in the sidebar.
func (a *app) serverMenu(m *ui.Menu, sv *config.Server) {
	if sv.Token == "" {
		if m.Item("登录…").Chosen() {
			a.openLogin(sv.ID, func() {})
		}
	} else if m.Item("重新连接").Disabled(a.rootsLoading[sv.ID] != nil).Chosen() {
		a.reconnect(sv)
	}
	if a.serverConnected(sv.ID) && !fileindex.IsPeer(sv.Addr) {
		m.Separator()
		if m.Item("目录管理…").Chosen() {
			a.openAdmin(sv.ID, "roots")
		}
		if m.Item("查重…").Chosen() {
			a.openAdmin(sv.ID, "dupes")
		}
		if m.Item("空间统计…").Chosen() {
			a.openAdmin(sv.ID, "stats")
		}
		if m.Item("回收站").Chosen() {
			a.openTrash(sv.ID)
		}
	}
	m.Separator()
	if m.Item("编辑服务器…").Chosen() {
		a.openServers()
		a.nasStartEdit(sv)
	}
	if !fileindex.IsPeer(sv.Addr) && m.Item("打开网页管理").Chosen() {
		a.openURL("http://" + sv.Addr + "/settings")
	}
	if sv.Token != "" {
		m.Separator()
		if m.Item("退出登录").Chosen() {
			a.nasLogout(sv)
		}
	}
}

// reconnect forgets what was read of a server and reads its directories
// again, which tries the connection.
func (a *app) reconnect(sv *config.Server) {
	delete(a.serverErr, sv.ID)
	delete(a.roots, sv.ID)
	delete(a.rootsLoading, sv.ID)
	a.unwatch(sv.ID)
	for key := range a.tree {
		if strings.HasPrefix(key, "root/"+sv.ID+"/") {
			delete(a.tree, key)
		}
	}
	*a.sectionOpen("server/" + sv.ID) = true
	a.loadRoots(sv)
}

// goPlace shows what the sidebar chose.
func (a *app) goPlace(place string) {
	kind, rest, _ := strings.Cut(place, "/")
	switch kind {
	case "backup":
		a.place = place
	case localPlace:
		a.openLocal(a.startLocal())
	case "admin":
		server, tab, _ := strings.Cut(rest, "/")
		a.openAdmin(server, tab)
	case "trash":
		a.openTrash(rest)
	case "login":
		a.openLogin(rest, func() {})
	case "root":
		server, id, _ := strings.Cut(rest, "/")
		for _, r := range a.roots[server] {
			if fmt.Sprint(r.ID) == id {
				a.place = place
				if a.br == nil || a.br.server != server || a.br.root.ID != r.ID {
					a.openRoot(server, r)
				}
			}
		}
	}
}

// serverConnected is true only after the saved session has answered successfully.
func (a *app) serverConnected(id string) bool {
	sv := a.store.Server(id)
	_, known := a.roots[id]
	return sv != nil && sv.Token != "" && known && a.serverErr[id] == nil && a.rootsLoading[id] == nil
}

// loadRoots reads a server's roots, once. Failed attempts remain latched until
// reconnect or a deliberate session/address change clears their state.
func (a *app) loadRoots(sv *config.Server) {
	if sv.Token == "" || a.serverErr[sv.ID] != nil || a.rootsLoading[sv.ID] != nil {
		return
	}
	c := fileindex.New(sv.Addr, sv.Token)
	a.rootsLoading[sv.ID] = c
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		roots, err := c.Roots(ctx)
		a.post(func() {
			if a.closed || a.rootsLoading[sv.ID] != c {
				return
			}
			delete(a.rootsLoading, sv.ID)
			if a.store.Server(sv.ID) != sv || sv.Token != c.Token || sv.Addr != c.Addr {
				return
			}
			switch {
			case errors.Is(err, fileindex.ErrAuth):
				a.signedOut(sv)
			case err != nil:
				a.roots[sv.ID] = nil // shows none until reconnected
				a.serverErr[sv.ID] = err
			default:
				delete(a.serverErr, sv.ID)
				a.applyRoots(sv.ID, roots)
				a.watch(sv)
			}
		})
	}()
}

// serverChanged forgets what was read of a server, after its session or
// address changed or it was removed.
func (a *app) serverChanged(id string) {
	if a.proxy != nil {
		a.proxy.Revoke(id)
	}
	if a.preview != nil && a.preview.server == id {
		a.closePreview()
	}
	delete(a.roots, id)
	delete(a.rootsLoading, id)
	delete(a.serverErr, id)
	a.unwatch(id)
	delete(a.thumbExts, id)
	for key := range a.tree {
		if strings.HasPrefix(key, "root/"+id+"/") {
			delete(a.tree, key)
		}
	}
	// What every tab shows of it.
	a.eachTab(func() { a.leaveServer(id) })
}

// leaveServer lets go of what the tab shown shows of a server whose
// session or address changed, or which was removed.
func (a *app) leaveServer(id string) {
	if a.arc != nil && a.arc.server == id {
		if a.arc.cancel != nil {
			a.arc.cancel()
		}
		a.arc = nil
		if a.onArchive() || a.place == "archive" {
			a.place = ""
		}
	}
	if a.admin != nil && a.admin.server == id {
		a.admin.stop()
		a.admin = nil
		if strings.HasPrefix(a.place, "admin/"+id+"/") {
			a.place = ""
		}
	}
	if a.recycle != nil && a.recycle.server == id {
		if a.recycle.cancel != nil {
			a.recycle.cancel()
		}
		a.recycle = nil
		if strings.HasPrefix(a.place, "trash/") {
			a.place = ""
		}
	}
	if a.br != nil && a.br.server == id {
		if sv := a.store.Server(id); sv == nil || sv.Token == "" {
			if a.br.cancel != nil {
				a.br.cancel()
			}
			if a.br.thumbCancel != nil {
				a.br.thumbCancel()
			}
			a.br = nil
			if strings.HasPrefix(a.place, "root/") {
				a.place = ""
			}
		}
	}
}
