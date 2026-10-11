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
	// The sidebar, then the page: the divider between them drags.
	split := ui.Split(c.Key("main-split"), &a.sidebarW, func() { a.sidebar(c) }, func() {
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Fill().Padding(20, 24).Gap(12).Children(func() {
			if a.errorMsg != "" && a.place != "" && a.place != "backup" {
				closableNotice(c, c.Theme().Danger, a.errorMsg, func() { a.errorMsg = "" })
			}
			switch {
			case a.admin != nil && a.place == adminPlace(a.admin.server, a.admin.tab):
				a.adminView(c)
			case a.place == "backup":
				a.backupPage(c)
			case a.place == localPlace && a.local != nil:
				a.localView(c)
			case a.recycle != nil && a.place == trashPlace(a.recycle.server):
				a.trashView(c)
			case a.place == "transfers":
				ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "传输任务").FontSize(22).Bold().Grow(1)
					ui.Button(c.Key("clear-transfers"), "清除已完成").Disabled(a.activeTransfers() == len(a.transfers)).OnClick(a.clearTransfers)
				})
				if len(a.transfers) == 0 {
					ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
						ui.Icon(c, iconMoves).FontSize(36).TextColor(c.Theme().TextMuted)
						ui.Text(c, "还没有传输任务").Bold()
						ui.Text(c, "上传、下载、复制和移动都会显示在这里。").FontSize(12).TextColor(c.Theme().TextMuted)
					})
				}
				ui.Scroll(c).Grow(1).MinHeight(0).Gap(8).Children(func() {
					for _, tr := range a.transfers {
						a.transferRow(c, tr)
					}
				})
			case a.br != nil && a.place == rootPlace(a.br.server, a.br.root.ID):
				a.browserView(c)
			default:
				a.welcome(c)
			}
			if a.place != "transfers" {
				a.transfersPanel(c)
			}
		})
	}).Fill()
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
	a.filePreviewView(c)
	a.taskDialog(c)
	a.nasView(c)
	if a.confirmRemove {
		if task := a.task(); task != nil {
			if ui.AlertDialog(c, &a.confirmRemove, fmt.Sprintf("删除任务“%s”？", task.Name),
				"只删除任务设置和运行记录，不会删除本地或 NAS 上的任何文件。", "取消", "删除") == 1 {
				a.removeTask()
			}
		}
	}
}

// The sidebar's width, which the user drags between these.
const (
	defaultSidebar = 232
	minSidebar     = 180
	maxSidebar     = 480
)

func rootPlace(server string, root int64) string { return fmt.Sprintf("root/%s/%d", server, root) }

// backupPage is the backup tasks: their list, and the one chosen. Tasks
// are made from the page of the computer's files.
func (a *app) backupPage(c *ui.Context) {
	t := c.Theme()
	if len(a.store.Tasks) == 0 {
		ui.Column(c).Grow(1).Center().Gap(10).Children(func() {
			ui.Icon(c, iconTasks).FontSize(36).TextColor(t.TextMuted)
			ui.Text(c, "还没有备份任务").FontSize(18).Bold()
			ui.Text(c, "在左侧“本机”中找到要备份的文件夹或文件，选中后点“创建备份任务…”。").TextColor(t.TextMuted)
			if ui.PrimaryButton(c, "浏览本机文件").Clicked() {
				a.openLocal(a.startLocal())
			}
		})
		return
	}
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Gap(20).Children(func() {
		ui.Column(c).Width(220).Gap(8).Children(func() {
			ui.Text(c, "备份任务").Bold()
			a.taskList(c)
			ui.Text(c, "新任务在“本机”文件页中创建。").FontSize(12).TextColor(t.TextMuted)
		})
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(16).Children(func() {
			switch {
			case a.task() == nil:
				ui.Column(c).Grow(1).Center().Children(func() {
					ui.Text(c, "从左边选择一个任务。").TextColor(t.TextMuted)
				})
			case a.job != nil && a.job.taskID == a.task().ID:
				a.jobView(c)
			default:
				a.detail(c)
			}
		})
	})
}

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
		if a.errorMsg != "" {
			closableNotice(c, t.Danger, a.errorMsg, func() { a.errorMsg = "" })
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
		ui.Scroll(c).Grow(1).MinHeight(0).Padding(10, 8).Children(func() {
			ui.Tree(c.Key("servers-tree"), func() {
				for _, sv := range a.store.Servers {
					node := ui.TreeItem(c.Key("server/"+sv.ID), "", a.sectionOpen("server/"+sv.ID), func() {
						a.serverItems(c, sv)
					})
					node.Children(func() {
						labeled(c, iconServer, sv.Title(), t.Text)
						a.serverMark(c, sv)
					}).Label(sv.Title()).Tooltip(sv.Addr)
					node.ContextMenu(func(m *ui.Menu) { a.serverMenu(m, sv) })
				}
				local := ui.TreeItem(c.Key("local"), "", a.sectionOpen("local"), func() { a.localItems(c) })
				local.Children(func() { labeled(c, iconLaptop, "本机", t.Text) }).Label("本机")
				tasks := ui.TreeItem(c.Key("tasks-section"), "", a.sectionOpen("tasks"), func() {
					bk := a.navItem(c, "backup", iconBackup, "备份任务", func() { a.place = "backup" })
					if n := len(a.store.Tasks); n > 0 {
						color := t.TextMuted
						if a.place == "backup" {
							color = t.AccentText
						}
						bk.Children(func() { ui.Text(c, fmt.Sprint(n)).FontSize(11).TextColor(color).Padding(0, 4) })
					}
					tr := a.navItem(c, "transfers", iconMoves, "传输任务", func() { a.place = "transfers" })
					if n := a.activeTransfers(); n > 0 {
						tr.Children(func() { ui.Badge(c, fmt.Sprint(n)) })
					}
				})
				tasks.Children(func() { labeled(c, iconTasks, "任务", t.Text) }).Label("任务")
			}).Label("位置")
		})
		ui.Row(c).Padding(10).Gap(6).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			if ui.Button(c, "服务器…").Grow(1).Clicked() {
				a.openServers()
			}
			ui.MenuButton(c.Key("web-management"), "网页管理", func(m *ui.Menu) {
				for _, sv := range a.store.Servers {
					if m.Item(sv.Title() + " · 设置").Chosen() {
						a.openURL("http://" + sv.Addr + "/settings")
					}
				}
			}).Disabled(len(a.store.Servers) == 0).Tooltip("在浏览器中打开 fileindex 网页版")
		})
	})
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

// serverItems are the roots of a server, then its management.
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
			ui.Text(c, "无法连接").FontSize(12).TextColor(t.Danger)
			ui.Text(c, connectError(err)).FontSize(11).MaxLines(3).TextColor(t.TextMuted).Tooltip(err.Error())
		})
		return
	}
	if len(roots) == 0 {
		ui.Text(c, "还没有索引目录，可在“管理 → 目录管理”中添加").FontSize(11).MaxLines(2).TextColor(t.TextMuted).Padding(2, 8, 4, 28)
	}
	for _, r := range roots {
		a.directoryView(c, sv, r, "", r.Name)
	}
	manage := ui.TreeItem(c.Key("manage/"+sv.ID), "", a.sectionOpen("manage/"+sv.ID), func() {
		a.navItem(c, adminPlace(sv.ID, "roots"), iconRoots, "目录管理", func() { a.openAdmin(sv.ID, "roots") })
		a.navItem(c, adminPlace(sv.ID, "dupes"), iconDupes, "查重", func() { a.openAdmin(sv.ID, "dupes") })
		a.navItem(c, adminPlace(sv.ID, "stats"), iconStats, "空间统计", func() { a.openAdmin(sv.ID, "stats") })
		a.navItem(c, trashPlace(sv.ID), iconTrash, "回收站", func() { a.openTrash(sv.ID) })
	})
	manage.Children(func() { labeled(c, iconManage, "管理", t.TextMuted) }).Label("管理")
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

// taskList is the list of the backup tasks.
func (a *app) taskList(c *ui.Context) {
	t := c.Theme()
	list := ui.List(c.Key("tasks"), &a.tasks, len(a.store.Tasks)).Grow(1).Label("备份任务列表").Radius(t.Radius*2).Border(1, t.Border).Clip().Rows(func(row ui.ListRow) {
		task := a.store.Tasks[row.Index]
		muted := t.TextMuted
		if row.Selected() {
			muted = t.AccentText.Alpha(0.85)
		}
		ui.Column(c).Padding(8, 12).Gap(2).Children(func() {
			ui.Text(c, task.Name).SingleLine()
			status := task.Mode.String()
			if len(task.History) > 0 {
				status += " · " + runStatusShort(task.History[0])
			}
			ui.Text(c, status).FontSize(12).TextColor(muted).SingleLine()
		})
	})
	if list.Changed() {
		switch {
		case a.busy():
			// The task being scanned or backed up stays chosen.
			for i, task := range a.store.Tasks {
				if task.ID == a.job.taskID {
					a.selected = i
				}
			}
		default:
			a.editing = false
			a.errorMsg = ""
			a.job = nil
		}
	}
}
