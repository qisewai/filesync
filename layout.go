package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"filesync/config"
	"filesync/fileindex"

	"github.com/egoist/mygo/ui"
)

func (a *app) view(c *ui.Context) {
	if s := c.Preferences().TextScale; a.plainText && s > 0 && s != 1 {
		t := *c.Theme()
		t.FontSize /= s
		c.SetTheme(&t)
	}
	a.followHistory()
	a.searchShortcuts(c)
	// The sidebar, then the page under its toolbar: the divider between
	// them drags.
	split := ui.Split(c.Key("main-split"), &a.sidebarW, func() { a.sidebar(c) }, func() {
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Fill().Children(func() {
			a.topBar(c)
			a.locationBar(c)
			a.nav.View(c, func(r *ui.Route) {
				r.Title(a.pageTitle())
				ui.Column(c).Grow(1).MinHeight(0).Padding(10, 24, 16, 24).Gap(12).Children(func() {
					if a.errorMsg != "" && a.place != "backup" {
						closableNotice(c, c.Theme().Danger, a.errorMsg, func() { a.errorMsg = "" })
					}
					a.page(c)
				})
			})
			a.statusBar(c)
		})
	}).Fill()
	// A frameless window has no shadow on Wayland: an outline tells it
	// from what is behind it.
	if a.ownControls && !a.maximized {
		split.Border(1, c.Theme().Border)
	}
	if a.viewerCovers() {
		// Under the viewer's opaque backdrop, the page is built, keeping
		// its state, but not drawn: drawn, its rounded clips would be
		// masked on the CPU while the picture is scaled, a second at once.
		split.Opacity(0)
	}
	if split.Changed() {
		a.sidebarW = min(max(a.sidebarW, minSidebar), maxSidebar)
		a.store.SidebarWidth = a.sidebarW
		a.sidebarMoved = true
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
		a.welcome(c)
	}
}

// The sidebar's width, which the user drags between these.
const (
	defaultSidebar = 232
	minSidebar     = 180
	maxSidebar     = 480
)

func rootPlace(server string, root int64) string { return fmt.Sprintf("root/%s/%d", server, root) }

// welcome is shown while nothing is chosen.
func (a *app) welcome(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Grow(1).Center().Gap(12).Children(func() {
		if len(a.store.Servers) == 0 {
			ui.Text(c, "欢迎使用 FileSync").FontSize(18).Bold()
			ui.Text(c, "FileSync 是 NAS 上 fileindex 的客户端：浏览、搜索、上传下载 NAS 上的文件，并把本机目录备份过去。先添加 fileindex 服务器的地址。").TextColor(t.TextMuted)
			if ui.PrimaryButton(c, "添加服务器…").Clicked() {
				a.openServers()
				a.nasStartEdit(nil)
			}
			return
		}
		ui.Text(c, "从左侧选择 NAS 上的目录，或在“本机”中选择要备份的文件。").TextColor(t.TextMuted)
		if ui.Button(c, "浏览本机文件").Clicked() {
			a.openLocal(a.startLocal())
		}
	})
}

func (a *app) sidebar(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Background(t.Surface).Children(func() {
		if a.hiddenTitleBar {
			a.sidebarHead(c)
		}
		ui.Scroll(c).Grow(1).MinHeight(0).Padding(10, 8).Children(func() {
			ui.Tree(c.Key("servers-tree"), func() {
				a.navItem(c, "search", iconSearch, "搜索", func() {
					query := ""
					if a.gsearch != nil {
						query = a.gsearch.query
					}
					a.openSearch(query)
					a.focusSearch = true
				})
				favs := ui.TreeItem(c.Key("favorites"), "", a.sectionOpen("favorites"), func() { a.favoriteItems(c) })
				// The sections, and the servers, have no page: a click opens
				// or closes them, as their arrows do.
				favs.Children(func() { labeled(c, iconStar, "收藏", t.Text) }).Label("收藏").OnClick(a.toggleSection("favorites"))
				local := ui.TreeItem(c.Key("local"), "", a.sectionOpen("local"), func() { a.localItems(c) })
				local.Children(func() { labeled(c, iconLaptop, "本机", t.Text) }).Label("本机").OnClick(a.toggleSection("local"))
				servers := ui.TreeItem(c.Key("servers"), "", a.sectionOpen("servers"), func() { a.serverList(c) })
				servers.Children(func() { labeled(c, iconRoots, "服务器", t.Text) }).Label("服务器").OnClick(a.toggleSection("servers"))
				bk := a.navItem(c, "backup", iconBackup, "备份任务", func() { a.place = "backup" })
				a.backupMark(c, bk)
				a.navItem(c, "transfer-history", iconTasks, "传输记录", func() { a.showTransfers = true })
			}).Label("位置")
		})
		ui.Row(c).Padding(8).Gap(6).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			settings := toolButton(c, "settings", iconGear, "设置", "服务器、本机索引与其他设置", false, func() { a.place = "settings" })
			if a.place == "settings" {
				settings.Background(c.Theme().SurfaceHover)
			}
			ui.Spacer(c)
			iconButton(c, "add-server", iconPlus, "添加服务器", "添加 fileindex 服务器", false, func() {
				a.openServers()
				a.nasStartEdit(nil)
			})
		})
	})
}

// sidebarHead is the head of the sidebar in a window without the system's
// title bar: the app's name, as high as the toolbar beside it, moving the
// window, clear of window controls on the left.
func (a *app) sidebarHead(c *ui.Context) {
	t := c.Theme()
	ui.Row(c.Key("sidebar-head")).Height(a.toolbarHeight(c)).Padding(0, 12, 0, 16+c.TitleBar().Left).Gap(8).AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).DragWindow().Children(func() {
		ui.Icon(c, iconBackup).FontSize(18).TextColor(t.Accent)
		ui.Text(c, "FileSync").Bold().SingleLine().Shrink(1).MinWidth(0)
	})
}

// serverList is the servers in the sidebar, each with its directories.
func (a *app) serverList(c *ui.Context) {
	t := c.Theme()
	if len(a.store.Servers) == 0 {
		a.navItem(c, "add-server", iconPlus, "添加服务器…", func() {
			a.openServers()
			a.nasStartEdit(nil)
		})
		return
	}
	for _, sv := range a.store.Servers {
		node := ui.TreeItem(c.Key("server/"+sv.ID), "", a.sectionOpen("server/"+sv.ID), func() {
			a.serverItems(c, sv)
		})
		tip := sv.Addr
		node.Children(func() {
			if a.unreachable(sv) {
				// Grayed, as what cannot be used now.
				dim := t.TextMuted.Alpha(0.55)
				ui.Icon(c, iconServer).FontSize(15).TextColor(dim)
				ui.Text(c, sv.Title()).SingleLine().Grow(1).Shrink(1).MinWidth(0).TextColor(dim)
				tip = sv.Addr + " · 无法连接，点右侧图标或右键重新连接"
			} else {
				labeled(c, iconServer, sv.Title(), t.Text)
			}
			a.serverMark(c, sv)
		}).Label(sv.Title()).Tooltip(tip)
		node.ContextMenu(func(m *ui.Menu) { a.serverMenu(m, sv) })
		// Signed out, a click signs in; else it opens or closes it.
		node.OnClick(func() {
			if sv.Token == "" {
				a.openLogin(sv.ID, func() {})
				return
			}
			a.toggleSection("server/" + sv.ID)()
		})
	}
}

// toggleSection opens a branch of the sidebar, or closes it.
func (a *app) toggleSection(key string) func() {
	return func() {
		open := a.sectionOpen(key)
		*open = !*open
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
	color := t.TextMuted
	if a.place == place {
		color = t.AccentText // on the accent of the item chosen
	}
	item := ui.TreeItem(c.Key("nav/"+place), "", nil, nil)
	item.Children(func() { labeled(c, svg, label, color) }).Label(label)
	return item.Selected(a.place == place).OnClick(action)
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
	if sv.Token != "" {
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
	if m.Item("打开网页管理").Chosen() {
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

// loadRoots reads a server's roots, once.
func (a *app) loadRoots(sv *config.Server) {
	if a.rootsLoading[sv.ID] != nil {
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
				// Nothing chosen yet: the first root shows.
				if a.place == "" && len(roots) > 0 {
					a.goPlace(rootPlace(sv.ID, roots[0].ID))
				}
			}
		})
	}()
}

// serverChanged forgets what was read of a server, after its session or
// address changed or it was removed.
func (a *app) serverChanged(id string) {
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
	if a.proxy != nil {
		a.proxy.Revoke(id)
	}
	if a.preview != nil && a.preview.server == id {
		a.closePreview()
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
