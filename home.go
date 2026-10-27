package main

import (
	"fmt"

	"filesync/config"

	"github.com/egoist/mygo/ui"
)

// The home page: what the sidebar holds, as cards: the favorites, the
// places of this computer, the directories of each server, the backups.

func (a *app) homePage(c *ui.Context) {
	t := c.Theme()
	ui.Scroll(c.Key("home")).Grow(1).MinHeight(0).Padding(8, 4).Gap(22).Children(func() {
		if len(a.store.Servers) == 0 {
			ui.Column(c).Gap(8).Children(func() {
				ui.Text(c, "欢迎使用 FileSync").FontSize(18).Bold()
				ui.Text(c, "FileSync 是 NAS 上 fileindex 的客户端：浏览、搜索、上传下载 NAS 上的文件，并把本机目录备份过去。先添加 fileindex 服务器的地址。").TextColor(t.TextMuted)
				ui.Row(c).Children(func() {
					if ui.PrimaryButton(c, "添加服务器…").Clicked() {
						a.openServers()
						a.nasStartEdit(nil)
					}
				})
			})
		}
		a.homeSection(c, "home-common", "常用", nil, func() {
			a.homeTile(c, "home/search", iconSearch, t.Accent, "搜索", "在 NAS 和本机中查找文件", nil, func() {
				a.openSearch("")
				a.focusSearch = true
			})
			a.homeTile(c, "home/backup", iconBackup, t.Accent, "备份任务", a.backupSummary(), nil, func() { a.place = "backup" })
			a.homeTile(c, "home/settings", iconGear, t.TextMuted, "设置", "外观、启动与本机索引", nil, func() { a.place = "settings" })
		})
		a.homeFavorites(c)
		a.homeLocal(c)
		for _, sv := range a.store.Servers {
			a.homeServer(c, sv)
		}
	})
}

// homeSection is a titled section of the home page, its cards wrapped;
// head adds to the end of its title.
func (a *app) homeSection(c *ui.Context, key, title string, head, cards func()) {
	ui.Column(c.Key(key)).Gap(10).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).MinHeight(24).Children(func() {
			ui.Text(c, title).FontSize(13).Bold().TextColor(c.Theme().TextMuted).SingleLine().Shrink(1).MinWidth(0)
			if head != nil {
				head()
			}
		})
		ui.Row(c).Gap(10).Wrap().Children(cards)
	})
}

// homeTile is a card of the home page going somewhere; menu, when set,
// is its context menu.
func (a *app) homeTile(c *ui.Context, key string, svg *ui.SVG, color ui.Color, name, sub string, menu func(m *ui.Menu), click func()) {
	t := c.Theme()
	b := ui.ButtonBase(c.Key(key)).Width(200).Padding(10, 12).Gap(10).AlignItems(ui.Center).
		Radius(t.Radius*1.5).Border(1, t.Border).Label("打开 " + name).Tooltip(sub).OnClick(click)
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	} else {
		b.Background(t.Surface)
	}
	if menu != nil {
		b.ContextMenu(menu)
	}
	b.Children(func() {
		ui.Icon(c, svg).FontSize(22).TextColor(color)
		ui.Column(c).Gap(2).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Text(c, name).FontSize(13).Bold().SingleLine()
			if sub != "" {
				ui.Text(c, sub).FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
		})
	})
}

// newTabMenu is the menu of a card opening a place: in a tab of its own.
func (a *app) newTabMenu(loc string) func(m *ui.Menu) {
	return func(m *ui.Menu) {
		if m.Item("在新标签页中打开").Disabled(len(a.tabs) >= maxTabs).Chosen() {
			a.newTab(loc)
		}
	}
}

func (a *app) homeFavorites(c *ui.Context) {
	if len(a.store.Favorites) == 0 {
		return
	}
	a.checkFavorites()
	a.homeSection(c, "home-favorites", "收藏", nil, func() {
		for i, f := range a.store.Favorites {
			if sv := a.store.Server(f.Server); sv != nil && sv.Token != "" {
				if _, ok := a.roots[sv.ID]; !ok {
					a.loadRoots(sv)
				}
			}
			sub := a.favoriteWhere(f)
			color := folderColor
			if why := a.favoriteProblem(f); why != "" {
				sub, color = why, c.Theme().TextMuted.Alpha(0.55)
			}
			svg := iconStarFilled
			a.homeTile(c, fmt.Sprintf("home/favorite/%d", i), svg, color, f.Name, sub, a.newTabMenu(favoriteLocation(f)), func() { a.openFavorite(f) })
		}
	})
}

func (a *app) homeLocal(c *ui.Context) {
	if a.localRoots == nil {
		a.localRoots = localRoots()
	}
	a.homeSection(c, "home-local", "本机", nil, func() {
		for _, r := range a.localRoots {
			a.homeTile(c, "home/local/"+r.dir, localRootIcon(r.name), folderColor, r.name, r.dir, a.newTabMenu(localLocation(r.dir)), func() { a.openLocal(r.dir) })
		}
	})
}

// homeServer is a server's section: its directories, its trash and its
// management, or what keeps them from showing.
func (a *app) homeServer(c *ui.Context, sv *config.Server) {
	t := c.Theme()
	roots, ok := a.roots[sv.ID]
	if sv.Token != "" && !ok {
		a.loadRoots(sv)
	}
	head := func() {
		ui.Text(c, sv.Addr).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1).MinWidth(0)
		a.serverMark(c, sv)
		ui.Spacer(c)
		moreMenu(c, "home-server-more/"+sv.ID, func(m *ui.Menu) { a.serverMenu(m, sv) })
	}
	a.homeSection(c, "home-server/"+sv.ID, sv.Title(), head, func() {
		switch {
		case sv.Token == "":
			a.homeTile(c, "home/login/"+sv.ID, iconServer, t.TextMuted, "登录 "+sv.Title(), "登录后浏览这台服务器的目录", nil, func() { a.openLogin(sv.ID, func() {}) })
			return
		case !ok || a.rootsLoading[sv.ID] != nil:
			ui.Text(c, "正在连接…").FontSize(12).TextColor(t.TextMuted)
			return
		case a.serverErr[sv.ID] != nil:
			a.homeTile(c, "home/reconnect/"+sv.ID, iconRetry, t.Danger, "重新连接", "无法连接："+connectError(a.serverErr[sv.ID]), nil, func() { a.reconnect(sv) })
			return
		}
		for _, r := range roots {
			sub := "目录"
			if r.ReadOnly {
				sub = "只读目录"
			}
			a.homeTile(c, fmt.Sprintf("home/root/%s/%d", sv.ID, r.ID), iconFolder, folderColor, r.Name, sub,
				a.newTabMenu(nasLocation(sv.ID, r.ID, "")), func() { a.goPlace(rootPlace(sv.ID, r.ID)) })
		}
		a.homeTile(c, "home/trash/"+sv.ID, iconTrash, t.TextMuted, "回收站", "删除的文件，可恢复", nil, func() { a.openTrash(sv.ID) })
		a.homeTile(c, "home/admin/"+sv.ID, iconGear, t.TextMuted, "管理", "目录、查重与空间统计", nil, func() { a.openAdmin(sv.ID, "roots") })
	})
}

// backupSummary sums up the backup tasks, for their card.
func (a *app) backupSummary() string {
	if a.busy() {
		return "正在备份…"
	}
	if len(a.store.Tasks) == 0 {
		return "把本机目录备份到 NAS"
	}
	failed := 0
	for _, task := range a.store.Tasks {
		if len(task.History) > 0 && task.History[0].Status != config.StatusOK {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Sprintf("%d 个任务，%d 个上次没有完全成功", len(a.store.Tasks), failed)
	}
	return fmt.Sprintf("%d 个任务", len(a.store.Tasks))
}
