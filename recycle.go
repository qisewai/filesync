package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"filesync/fileindex"
	"github.com/egoist/mygo/ui"
)

type recycleBrowser struct {
	server                      string
	items, shown                []fileindex.TrashItem
	query, err, note            string
	loading, operating, confirm bool
	pending                     []int64
	sel                         ui.Selection[int64]
	cursor                      int
	list                        ui.ListState
	cancel                      context.CancelFunc
	gen                         int
	refreshPending              bool
}

func trashPlace(server string) string { return "trash/" + server }
func (a *app) openTrash(server string) {
	sv := a.store.Server(server)
	if sv == nil {
		return
	}
	if sv.Token == "" {
		a.openLogin(server, func() { a.openTrash(server) })
		return
	}
	if a.recycle != nil && a.recycle.cancel != nil {
		a.recycle.cancel()
	}
	b := &recycleBrowser{server: server, cursor: -1}
	b.list.Key = func(i int) any { return b.shown[i].ID }
	b.list.Selection = &b.sel
	b.list.Selected = &b.cursor
	a.recycle = b
	a.place = trashPlace(server)
	a.loadTrash()
}
func (a *app) loadTrash() {
	b := a.recycle
	if b == nil || b.operating {
		return
	}
	sv := a.store.Server(b.server)
	if sv == nil || sv.Token == "" {
		return
	}
	if b.cancel != nil {
		b.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	b.cancel = cancel
	c := fileindex.New(sv.Addr, sv.Token)
	b.gen++
	gen := b.gen
	b.loading = true
	b.err = ""
	go func() {
		defer cancel()
		items, err := c.ListTrash(ctx)
		a.post(func() {
			if a.recycle != b || b.gen != gen || !a.sourceCurrent(b.server, c) {
				return
			}
			b.loading = false
			if errors.Is(err, fileindex.ErrAuth) {
				a.signedOut(sv)
				return
			}
			if err != nil {
				b.err = err.Error()
				return
			}
			b.items = items
			b.filter()
		})
	}()
}
func (b *recycleBrowser) filter() {
	b.shown = nil
	q := strings.ToLower(strings.TrimSpace(b.query))
	keep := map[int64]bool{}
	for _, it := range b.items {
		if q == "" || strings.Contains(strings.ToLower(it.Name+" "+it.RootName+"/"+it.OrigPath), q) {
			b.shown = append(b.shown, it)
			keep[it.ID] = true
		}
	}
	for id := range b.sel.All() {
		if !keep[id] {
			b.sel.Remove(id)
		}
	}
	b.cursor = -1
}
func (a *app) trashWritable(b *recycleBrowser) bool {
	if b.loading || b.operating || b.sel.Len() == 0 {
		return false
	}
	for _, it := range b.shown {
		if b.sel.Has(it.ID) {
			found := false
			for _, root := range a.roots[b.server] {
				if root.ID == it.RootID {
					if root.ReadOnly {
						return false
					}
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}
func (b *recycleBrowser) chosen() []int64 {
	var ids []int64
	for _, it := range b.shown {
		if b.sel.Has(it.ID) {
			ids = append(ids, it.ID)
		}
	}
	return ids
}
func (a *app) restoreTrash(ids []int64) {
	a.trashOperation(ids, false)
}
func (a *app) trashOperation(ids []int64, purge bool) {
	b := a.recycle
	if b == nil || b.operating || len(ids) == 0 {
		return
	}
	sv := a.store.Server(b.server)
	if sv == nil || sv.Token == "" {
		return
	}
	c := fileindex.New(sv.Addr, sv.Token)
	server := b.server
	b.operating = true
	b.note = ""
	b.err = ""
	ids = append([]int64(nil), ids...)
	tr := a.addTransfer("恢复", fmt.Sprintf("回收站 %d 项", len(ids)))
	tr.server = server
	if purge {
		tr.kind = "彻底删除"
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	tr.cancel = cancel
	go func() {
		var restored []string
		var task *fileindex.Task
		var err error
		if purge {
			task, err = c.PurgeTrash(ctx, ids)
		} else {
			restored, err = c.RestoreTrash(ctx, ids)
		}
		a.post(func() {
			defer cancel()
			if a.recycle == b {
				b.operating = false
			}
			if err == nil && a.sourceCurrent(server, c) {
				tr.after = func() {
					if a.recycle != nil && a.recycle.server == server {
						a.recycle.note = fmt.Sprintf("已彻底删除 %d 项", len(ids))
						if !purge {
							a.recycle.note = fmt.Sprintf("已恢复 %d 项；同名文件由服务器自动编号", len(restored))
						}
						a.loadTrash()
					}
					for _, root := range a.roots[server] {
						a.invalidateTree(server, root.ID)
					}
					if a.br != nil && a.br.server == server {
						a.reloadKeeping()
					}
				}
			}
			if err != nil {
				a.finishTransfer(tr, err, nil)
				if a.recycle == b {
					b.err = err.Error()
				}
				if errors.Is(err, fileindex.ErrAuth) && a.sourceCurrent(server, c) {
					a.signedOut(sv)
				}
			} else if task != nil {
				tr.task = task.ID
				a.trackServerTask(c, tr, task)
			} else {
				a.finishTransfer(tr, nil, nil)
			}
			// Partial restores/deletes can already have changed the server even when
			// the response failed. Reconcile instead of retrying obsolete IDs.
			if a.recycle == b && a.sourceCurrent(server, c) {
				a.loadTrash()
			}
		})
	}()
}
func (a *app) trashView(c *ui.Context) {
	b := a.recycle
	t := c.Theme()
	sv := a.store.Server(b.server)
	title := "回收站"
	if sv != nil {
		title += " · " + sv.Title()
	}
	ui.Text(c, title).FontSize(22).Bold()
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		if ui.TextInput(c.Key("trash-search"), &b.query).Placeholder("筛选名称或原位置…").Label("筛选回收站").Grow(1).Changed() {
			b.filter()
		}
		ui.Button(c, "刷新回收站").Disabled(b.operating).OnClick(a.loadTrash)
		ui.Button(c, "恢复所选").Disabled(!a.trashWritable(b)).OnClick(func() { a.restoreTrash(b.chosen()) })
		ui.Button(c, "彻底删除…").Disabled(!a.trashWritable(b)).OnClick(func() { b.pending = b.chosen(); b.confirm = true })
	})
	if b.err != "" {
		notice(c, t.Danger, b.err)
	}
	if b.note != "" {
		notice(c, t.Success, b.note)
	}
	if b.loading {
		ui.Text(c, "正在读取回收站…").TextColor(t.TextMuted)
	}
	cols := []ui.TableColumn{{Title: "名称"}, {Title: "原位置", Width: 240}, {Title: "大小", Width: 85}, {Title: "删除时间", Width: 140}}
	ui.Table(c.Key("trash-files"), &b.list, cols, len(b.shown), func(row, col int) {
		it := b.shown[row]
		switch col {
		case 0:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				fileIcon(c, fileindex.Entry{Name: it.Name, IsDir: it.IsDir, Ext: extOf(it.Name)}, 16)
				ui.Text(c, it.Name).SingleLine().Grow(1).Shrink(1)
			})
		case 1:
			ui.Text(c, it.RootName+"/"+it.OrigPath).SingleLine().TextColor(t.TextMuted)
		case 2:
			ui.Text(c, formatBytes(it.Size)).SingleLine()
		case 3:
			ui.Text(c, time.Unix(it.DeletedAt, 0).Format("2006-01-02 15:04")).SingleLine()
		}
	}).Grow(1).MinHeight(0).Label("回收站文件").Children(func() {
		if !b.loading && len(b.shown) == 0 {
			ui.Text(c, "回收站中没有匹配的文件").Padding(16).TextColor(t.TextMuted)
		}
	})
	ui.Text(c, fmt.Sprintf("显示 %d / %d 项 · 已选 %d 项 · 只读目录中的项不能恢复或删除", len(b.shown), len(b.items), b.sel.Len())).FontSize(12).TextColor(t.TextMuted)
	if b.confirm {
		if ui.AlertDialog(c, &b.confirm, fmt.Sprintf("彻底删除回收站中的 %d 项？", len(b.pending)), "这些文件和目录将永久删除，无法恢复。", "取消", "确认永久删除") == 1 {
			ids := b.pending
			b.pending = nil
			a.trashOperation(ids, true)
		}
	}
}
func (a *app) trashRefreshSoon(server string) {
	b := a.recycle
	if b == nil || b.server != server || b.refreshPending {
		return
	}
	b.refreshPending = true
	time.AfterFunc(300*time.Millisecond, func() {
		a.post(func() {
			b.refreshPending = false
			if !a.closed && a.recycle == b {
				if b.operating {
					a.trashRefreshSoon(server)
				} else {
					a.loadTrash()
				}
			}
		})
	})
}
