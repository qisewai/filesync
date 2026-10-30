package main

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"filemanager/remoteclient"
	"github.com/egoist/mygo/ui"
)

type duplicateRow struct{ group, file int }

func (m *adminPage) dupeRunning() bool {
	for _, t := range m.tasks {
		if t.Kind == "dupes" && !t.Finished() {
			return true
		}
	}
	return false
}
func (a *app) rootWritable(server string, root int64) bool {
	for _, r := range a.roots[server] {
		if r.ID == root {
			return !r.ReadOnly
		}
	}
	return false
}
func (a *app) selectDuplicates(m *adminPage, newest bool) {
	selected := map[int64]bool{}
	if m.dupes != nil {
		for _, g := range m.dupes.Groups {
			order := append([]fileindex.Entry(nil), g.Files...)
			sort.SliceStable(order, func(i, j int) bool {
				if order[i].MTime != order[j].MTime {
					return order[i].MTime < order[j].MTime
				}
				return order[i].Path < order[j].Path
			})
			keep := 0
			if newest {
				keep = len(order) - 1
			}
			for i, e := range order {
				if i != keep && a.rootWritable(m.server, e.Root) {
					selected[e.ID] = true
				}
			}
		}
	}
	m.selected = selected
}
func (m *adminPage) lastCopy(g fileindex.DupeGroup, e fileindex.Entry) bool {
	if m.selected[e.ID] {
		return false
	}
	for _, other := range g.Files {
		if other.ID != e.ID && !m.selected[other.ID] {
			return false
		}
	}
	return true
}
func (a *app) duplicateSelection(m *adminPage) ([]fileindex.Entry, error) {
	if m.dupes == nil {
		return nil, nil
	}
	var selected []fileindex.Entry
	known := map[int64]bool{}
	for _, g := range m.dupes.Groups {
		count := 0
		for _, e := range g.Files {
			if m.selected[e.ID] {
				if !a.rootWritable(m.server, e.Root) {
					return nil, fmt.Errorf("只读目录中的副本不能删除")
				}
				selected = append(selected, e)
				known[e.ID] = true
				count++
			}
		}
		if count > 0 && count >= len(g.Files) {
			return nil, fmt.Errorf("每组重复文件必须至少保留一份")
		}
	}
	for id, checked := range m.selected {
		if checked && !known[id] {
			return nil, fmt.Errorf("查重结果已变化，请刷新后重新选择")
		}
	}
	return selected, nil
}
func (a *app) removeDuplicates(m *adminPage) {
	entries, err := a.duplicateSelection(m)
	if err != nil {
		m.err = err.Error()
		return
	}
	if len(entries) == 0 {
		return
	}
	byRoot := map[int64][]string{}
	for _, e := range entries {
		byRoot[e.Root] = append(byRoot[e.Root], e.Path)
	}
	a.adminAction(m, func(ctx context.Context, c *fileindex.Client) error {
		var ids []int64
		for id := range byRoot {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, root := range ids {
			if err := c.Trash(ctx, root, byRoot[root]); err != nil {
				return err
			}
		}
		return nil
	}, fmt.Sprintf("已移到回收站 %d 个重复副本", len(entries)), func() {
		m.page = 1
		m.selected = map[int64]bool{}
		a.trashRefreshSoon(m.server)
		for root := range byRoot {
			a.invalidateTree(m.server, root)
		}
		if a.br != nil && a.br.server == m.server {
			a.reloadKeeping()
		}
	})
}
func (a *app) findDuplicates(m *adminPage) {
	sv := a.store.Server(m.server)
	if sv == nil || sv.Token == "" {
		return
	}
	c := fileindex.New(sv.Addr, sv.Token)
	root, minSize := a.adminScope(m), dupeMin(m.minSize)
	server := m.server
	tr := a.addTransfer("查重", m.scope)
	tr.server = server
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	tr.cancel = cancel
	go func() {
		defer cancel()
		task, err := c.FindDuplicates(ctx, root, minSize)
		a.post(func() {
			if err != nil {
				a.finishTransfer(tr, err, nil)
				if !a.sourceCurrent(server, c) {
					return
				}
				if errors.Is(err, fileindex.ErrAuth) {
					a.signedOut(sv)
					return
				}
				if a.admin == m {
					m.err = err.Error()
				}
				a.adminRefreshSoon(server)
				return
			}
			tr.task = task.ID
			tr.after = func() { a.adminRefreshSoon(server) }
			if a.admin == m {
				m.tasks = append(m.tasks, *task)
			}
			a.trackServerTask(c, tr, task)
		})
	}()
}
func (a *app) openManagedEntry(server string, e fileindex.Entry, group []fileindex.Entry) {
	sv := a.store.Server(server)
	if sv == nil || sv.Token == "" {
		return
	}
	c := fileindex.New(sv.Addr, sv.Token)
	if previewKind(e) != "" {
		var supported []fileindex.Entry
		for _, it := range group {
			if previewKind(it) != "" {
				supported = append(supported, it)
			}
		}
		a.showPreview(server, c, e, supported)
	} else {
		a.systemOpenSource(server, c, e, false)
	}
}
func (a *app) locateManagedEntry(server string, e fileindex.Entry) {
	a.goPlace(rootPlace(server, e.Root))
	if a.br != nil && a.br.server == server && a.br.root.ID == e.Root {
		a.browse(e.Parent, e.Name)
	}
}
func (a *app) dupesView(c *ui.Context, m *adminPage) {
	ui.Row(c).Gap(8).Wrap().Children(func() {
		a.scopePicker(c, m)
		if ui.Select(c.Key("dupe-min"), &m.minSize, []string{"全部非空文件", "≥ 1 MB", "≥ 10 MB", "≥ 100 MB", "≥ 1 GB"}).Width(140).Label("查重最小大小").Disabled(m.busy).Changed() {
			m.page = 1
			a.reloadAdmin()
		}
		ui.Button(c, "开始查重").Disabled(m.loading || m.busy || m.dupeRunning() || a.hasDupeTransfer(m.server)).OnClick(func() { a.findDuplicates(m) })
	})
	if m.dupes != nil {
		s := m.dupes.Summary
		ui.Text(c, fmt.Sprintf("%d 组重复文件 · %d 个文件 · 多余副本占用 %s", s.Groups, s.Files, formatBytes(s.Wasted))).Bold()
	}
	ui.Text(c, "查重会读取大小相同的文件并比对完整内容。结果来自上次查重，删除副本会移到回收站。").FontSize(12).TextColor(c.Theme().TextMuted)
	if m.lastDupeRun > 0 {
		ui.Text(c, "上次查重："+formatWhen(m.lastDupeRun)).FontSize(12).TextColor(c.Theme().TextMuted)
	}
	ui.Row(c).Gap(6).Wrap().Children(func() {
		ui.Button(c, "本页保留最早").Disabled(m.loading || m.busy).OnClick(func() { a.selectDuplicates(m, false) })
		ui.Button(c, "本页保留最新").Disabled(m.loading || m.busy).OnClick(func() { a.selectDuplicates(m, true) })
		ui.Button(c, "清除副本勾选").Disabled(m.busy).OnClick(func() { m.selected = map[int64]bool{} })
		entries, err := a.duplicateSelection(m)
		ui.Button(c, fmt.Sprintf("删除勾选副本… (%d)", len(entries))).Disabled(m.loading || m.busy || err != nil || len(entries) == 0).OnClick(func() { m.deletingDupes = true })
	})
	ui.List(c.Key("duplicate-files"), &m.dupeList, len(m.dupeRows)).Grow(1).MinHeight(0).Label("重复文件列表").Rows(func(row ui.ListRow) {
		r := m.dupeRows[row.Index]
		g := m.dupes.Groups[r.group]
		if r.file < 0 {
			ui.Text(c, fmt.Sprintf("%s · %s × %d · 多占 %s", g.Files[0].Name, formatBytes(g.Size), g.Count, formatBytes(g.Wasted))).Bold().SingleLine().Padding(8, 0)
			return
		}
		e := g.Files[r.file]
		checked := m.selected[e.ID]
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(4, 0).Children(func() {
			ui.Checkbox(c.Key(fmt.Sprintf("dupe-check/%d", e.ID)), &checked, "删除副本").Disabled(m.loading || m.busy || !a.rootWritable(m.server, e.Root) || m.lastCopy(g, e)).OnClick(func() { m.selected[e.ID] = checked })
			ui.Button(c.Key("dupe-open/"+fmt.Sprint(e.ID)), e.RootName+"/"+e.Path).Grow(1).Shrink(1).MinWidth(0).OnClick(func() { a.openManagedEntry(m.server, e, g.Files) })
			ui.Text(c, formatWhen(e.MTime)).FontSize(12).TextColor(c.Theme().TextMuted)
			ui.Button(c.Key("dupe-locate/"+fmt.Sprint(e.ID)), "所在位置").OnClick(func() { a.locateManagedEntry(m.server, e) })
		})
	})
	ui.Row(c).Gap(8).Children(func() {
		ui.Button(c, "上一页重复文件").Disabled(m.page <= 1 || m.loading || m.busy).OnClick(func() { m.page--; a.reloadAdmin() })
		ui.Text(c, fmt.Sprintf("第 %d 页", m.page))
		ui.Button(c, "下一页重复文件").Disabled(m.dupes == nil || int64(m.page*50) >= m.dupes.Summary.Groups || m.loading || m.busy).OnClick(func() { m.page++; a.reloadAdmin() })
	})
	if m.deletingDupes {
		entries, err := a.duplicateSelection(m)
		if err != nil || len(entries) == 0 {
			m.deletingDupes = false
			return
		}
		if ui.AlertDialog(c.Key("delete-duplicate-files"), &m.deletingDupes, fmt.Sprintf("删除 %d 个重复副本？", len(entries)), "所选副本会移到回收站，每组至少保留一份。", "取消", "确认移到回收站") == 1 {
			a.removeDuplicates(m)
		}
	}
}
func (a *app) hasDupeTransfer(server string) bool {
	for _, tr := range a.transfers {
		if tr.server == server && tr.kind == "查重" && tr.running() {
			return true
		}
	}
	return false
}
