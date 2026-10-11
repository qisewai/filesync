package main

import (
	"fmt"
	"strings"

	"filesync/config"

	"github.com/egoist/mygo/ui"
)

// The page of the settings: the servers, the index of this computer, and
// how the app behaves. What edits them shows in dialogs, as it did.

// settingsPage shows the settings in cards.
func (a *app) settingsPage(c *ui.Context) {
	ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).Gap(16).MaxWidth(880).Padding(0, 4, 16, 0).Children(func() {
			a.serversCard(c)
			a.localIndexCard(c)
			a.generalCard(c)
		})
	})
}

// settingsCard is a section of the settings: a title, what it is, and its
// content.
func settingsCard(c *ui.Context, svg *ui.SVG, title, about string, fn func()) {
	t := c.Theme()
	ui.Column(c).Padding(16, 18).Gap(12).Radius(t.Radius*2).Border(1, t.Border).Background(t.Background).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, svg).FontSize(18).TextColor(t.Accent)
			ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
				ui.Text(c, title).Bold()
				if about != "" {
					ui.Text(c, about).FontSize(12).TextColor(t.TextMuted)
				}
			})
		})
		fn()
	})
}

// serversCard lists the servers, with their state and what to do with
// each.
func (a *app) serversCard(c *ui.Context) {
	t := c.Theme()
	settingsCard(c, iconServer, "服务器", "NAS 上运行的 fileindex。只保存登录状态，不保存密码。", func() {
		if len(a.store.Servers) == 0 {
			ui.Text(c, "还没有服务器。").FontSize(12).TextColor(t.TextMuted)
		}
		for i, sv := range a.store.Servers {
			ui.Row(c.Key("server-row/"+sv.ID)).Gap(10).AlignItems(ui.Center).Padding(8, 0, 0, 0).
				BorderWidth(float32(min(i, 1)), 0, 0, 0).BorderColor(t.Border).Children(func() {
				state, color := a.serverState(t, sv)
				statusDot(c, color)
				ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
					title := ui.Text(c, sv.Title()).SingleLine()
					if a.unreachable(sv) {
						title.TextColor(t.TextMuted)
					}
					detail := sv.Addr + " · " + state
					if roots, ok := a.roots[sv.ID]; ok && sv.Token != "" && a.serverErr[sv.ID] == nil {
						detail += fmt.Sprintf(" · %d 个索引目录", len(roots))
					}
					ui.Text(c, detail).FontSize(12).SingleLine().TextColor(t.TextMuted)
				})
				if sv.Token == "" {
					ui.Button(c.Key("login/"+sv.ID), "登录…").OnClick(func() { a.openLogin(sv.ID, func() {}) })
				} else {
					ui.Button(c.Key("manage/"+sv.ID), "管理").Label("管理 " + sv.Title()).OnClick(func() { a.openAdmin(sv.ID, "roots") })
				}
				iconButton(c, "server-menu/"+sv.ID, iconMore, "服务器菜单 "+sv.Title(), "更多操作", false, func() {}).
					Menu(func(m *ui.Menu) { a.serverMenu(m, sv) })
			})
		}
		ui.Row(c).Gap(8).Children(func() {
			toolButton(c, "add-server-settings", iconPlus, "添加服务器…", "", false, func() {
				a.openServers()
				a.nasStartEdit(nil)
			})
			ui.Button(c, "服务器列表…").Tooltip("添加、编辑、删除服务器，检查连接").OnClick(a.openServers)
		})
	})
}

// serverState says how a server is: signed in and reachable, or not.
func (a *app) serverState(t *ui.Theme, sv *config.Server) (string, ui.Color) {
	switch {
	case sv.Token == "":
		return "未登录", t.TextMuted
	case a.rootsLoading[sv.ID] != nil:
		return "正在连接", t.TextMuted
	case a.serverErr[sv.ID] != nil:
		return "无法连接：" + connectError(a.serverErr[sv.ID]), t.Danger
	}
	return "已登录", t.Success
}

// localIndexCard is the index of this computer: its state, what it holds,
// and how it is updated.
func (a *app) localIndexCard(c *ui.Context) {
	t := c.Theme()
	a.ensureLocalIndex()
	m := a.localIdx
	settingsCard(c, iconLaptop, "本机索引", "用来即时搜索本机文件，只记录文件名、大小和修改时间。索引只在你更新时变化。", func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			a.localIndexStatus(c)
		})
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, "索引目录").FontSize(12).Bold().TextColor(t.TextMuted)
			if len(m.settings.Roots) == 0 {
				ui.Text(c, "还没有索引目录").FontSize(12).TextColor(t.TextMuted)
			}
			for _, root := range m.settings.Roots {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, iconFolder).FontSize(14).TextColor(ui.Hex("#d99a2b"))
					ui.Text(c, homeTilde(root)).FontSize(13).SingleLine().Tooltip(root)
				})
			}
			if n := len(m.settings.Excludes); n > 0 {
				ui.Text(c, fmt.Sprintf("排除规则 %d 条：%s", n, strings.Join(m.settings.Excludes, "  "))).FontSize(12).SingleLine().TextColor(t.TextMuted)
			}
		})
		busy := m.running || m.loading
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c, "更新索引").Disabled(busy).OnClick(a.startLocalScan)
			ui.Button(c, "全部重建").Disabled(busy).OnClick(a.rebuildLocalIndex)
			ui.Button(c, "编辑索引范围…").Disabled(busy).OnClick(a.editLocalIndex)
		})
	})
}

// generalCard is how the app behaves, and where it keeps its files.
func (a *app) generalCard(c *ui.Context) {
	t := c.Theme()
	settingsCard(c, iconGear, "常规", "", func() {
		if a.tray != nil {
			inTray := !a.store.QuitOnClose
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					ui.Text(c, "关闭窗口时最小化到托盘")
					ui.Text(c, "备份和传输在后台继续；从托盘图标重新打开或退出。").FontSize(12).TextColor(t.TextMuted)
				})
				if ui.Switch(c.Key("quit-on-close"), &inTray).Label("最小化到托盘").Changed() {
					a.setQuitOnClose(!inTray)
				}
			})
		}
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, "设置和运行日志")
				ui.Text(c, a.store.Dir).FontSize(12).SingleLine().Selectable().TextColor(t.TextMuted)
			})
			ui.Button(c, "打开文件夹").OnClick(func() { a.openPath(a.store.Dir) })
		})
		if a.store.RecentSearches != nil {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					ui.Text(c, "最近搜索")
					ui.Text(c, fmt.Sprintf("保存了 %d 条", len(a.store.RecentSearches))).FontSize(12).TextColor(t.TextMuted)
				})
				ui.Button(c, "清除").OnClick(func() {
					a.store.RecentSearches = nil
					a.save()
				})
			})
		}
	})
}
