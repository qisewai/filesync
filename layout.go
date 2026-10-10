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
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		a.sidebar(c)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Padding(20, 24).Gap(12).Children(func() {
			if a.errorMsg != "" && a.place != "" && a.place != "backup" {
				notice(c, c.Theme().Danger, a.errorMsg)
			}
			switch {
			case a.place == "backup":
				a.backupPage(c)
			case a.recycle != nil && a.place == trashPlace(a.recycle.server):
				a.trashView(c)
			case a.place == "transfers":
				ui.Text(c, "传输任务").FontSize(22).Bold()
				ui.Button(c.Key("clear-transfers"), "清除已完成").Disabled(a.activeTransfers() == len(a.transfers)).OnClick(a.clearTransfers)
				if len(a.transfers) == 0 {
					ui.Text(c, "还没有传输任务")
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
	})
	if a.askQuit {
		switch ui.AlertDialog(c, &a.askQuit, "任务正在进行，要停止并退出吗？",
			"已完成的文件会保留，本机传输会停止，下次备份可继续。复制、移动会向服务器请求取消；服务器无法连接时，这些任务可能继续运行。", "继续任务", "停止并退出") {
		case 1:
			a.quit()
		}
	}
	a.filePreviewView(c)
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

func rootPlace(server string, root int64) string { return fmt.Sprintf("root/%s/%d", server, root) }

// backupPage is the backup tasks: their list, and the one chosen.
func (a *app) backupPage(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Gap(20).Children(func() {
		ui.Column(c).Width(220).Gap(8).Children(func() {
			ui.Text(c, "备份任务").Bold()
			a.taskList(c)
			if ui.Button(c, "＋ 新建任务").Disabled(a.busy() || a.editing).Clicked() {
				a.startEditing(nil)
			}
		})
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(16).Children(func() {
			switch {
			case a.editing:
				a.editor(c)
			case a.task() == nil:
				ui.Column(c).Grow(1).Center().Gap(12).Children(func() {
					ui.Text(c, "还没有备份任务").FontSize(18).Bold()
					ui.Text(c, "新建一个任务，选择本机要备份的目录，和 NAS 上备份到的目录。").TextColor(t.TextMuted)
					if ui.PrimaryButton(c, "新建任务").Clicked() {
						a.startEditing(nil)
					}
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
			notice(c, t.Danger, a.errorMsg)
		}
		ui.Text(c, "从左侧选择 NAS 上的目录，或打开备份任务。").TextColor(t.TextMuted)
		if ui.Button(c, "打开备份任务").Clicked() {
			a.place = "backup"
		}
	})
}

func (a *app) sidebar(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Width(220).Background(t.Surface).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Scroll(c).Grow(1).MinHeight(0).Padding(8).Children(func() {
			ui.Tree(c.Key("servers-tree"), func() {
				for _, sv := range a.store.Servers {
					open := a.open[sv.ID]
					if open == nil {
						open = new(bool)
						*open = true
						a.open[sv.ID] = open
					}
					ui.TreeItem(c.Key("server/"+sv.ID), "🖥 "+sv.Title(), open, func() {
						if sv.Token == "" {
							ui.TreeItem(c.Key("login/"+sv.ID), "登录…", nil, nil).OnClick(func() { a.openLogin(sv.ID, func() {}) })
							return
						}
						roots, ok := a.roots[sv.ID]
						if !ok {
							a.loadRoots(sv)
							ui.Text(c, "正在读取…")
							return
						}
						if len(roots) == 0 {
							if a.errorMsg != "" {
								ui.Text(c, a.errorMsg).FontSize(12).MaxLines(3).TextColor(t.Danger)
							}
							ui.Text(c, "（没有索引目录）")
							ui.Button(c.Key("retry-roots/"+sv.ID), "重新读取目录").OnClick(func() { delete(a.roots, sv.ID) })
						}
						ui.TreeItem(c.Key(trashPlace(sv.ID)), "回收站", nil, nil).OnClick(func() { a.openTrash(sv.ID) })
						for _, r := range roots {
							a.directoryView(c, sv, r, "", r.Name)
						}
					})
				}
			}).Label("位置")
		})
		ui.Column(c).Padding(12).Gap(6).Children(func() {
			ui.Button(c.Key("backup-page"), "备份任务").OnClick(func() { a.place = "backup" })
			ui.Button(c.Key("transfers-page"), "传输任务").OnClick(func() { a.place = "transfers" })
			ui.MenuButton(c.Key("web-management"), "网页管理", func(m *ui.Menu) {
				for _, sv := range a.store.Servers {
					if m.Item(sv.Title() + " · 索引目录").Chosen() {
						a.openURL("http://" + sv.Addr + "/roots")
					}
				}
			}).Disabled(len(a.store.Servers) == 0)
			if ui.Button(c, "服务器…").Clicked() {
				a.openServers()
			}
		})
	})
}

// goPlace shows what the sidebar chose.
func (a *app) goPlace(place string) {
	kind, rest, _ := strings.Cut(place, "/")
	switch kind {
	case "backup":
		a.place = place
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
				a.roots[sv.ID] = nil // shows none; reopening the servers retries
				a.errorMsg = sv.Title() + ": " + err.Error()
			default:
				a.roots[sv.ID] = roots
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
