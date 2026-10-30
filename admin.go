package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"filemanager/remoteclient"
	"github.com/egoist/mygo/ui"
)

// adminPage holds data and widget state, never elements from a build pass.
type adminPage struct {
	server, tab                   string
	loading, busy                 bool
	err, note                     string
	gen                           int
	cancel, opCancel              context.CancelFunc
	dataRoots                     []string
	rootList                      ui.ListState
	rootCursor                    int
	confirmRemove, confirmRebuild bool
	draft                         *rootDraft
	removing, rebuilding          *fileindex.Root
	history                       bool
	historyRoot                   fileindex.Root
	jobs                          []fileindex.ScanJob
	historyLoading                bool
	historyGen                    int
	historyCancel                 context.CancelFunc
	scope                         string
	scopeID                       int64
	minSize                       string
	page                          int
	dupes                         *fileindex.Dupes
	lastDupeRun                   int64
	selected                      map[int64]bool
	dupeList                      ui.ListState
	dupeRows                      []duplicateRow
	deletingDupes                 bool
	overview                      *fileindex.Overview
	// catHover is the kind of files pointed at in the summary, yearPick
	// the year chosen in its chart; -1 for none.
	catHover, yearPick int
	largest            []fileindex.Entry
	largestSort        tableSort
	dirs               []fileindex.DirUsage
	dirList            ui.ListState
	usageRoot          int64
	usagePath          string
	statsDir           string
	largestList        ui.ListState
	largestCursor      int
	tasks              []fileindex.Task
	refreshPending     bool
}

func adminPlace(server, tab string) string { return "admin/" + server + "/" + tab }
func (m *adminPage) stop() {
	for _, cancel := range []context.CancelFunc{m.cancel, m.opCancel, m.historyCancel} {
		if cancel != nil {
			cancel()
		}
	}
	if m.draft != nil && m.draft.picker != nil && m.draft.picker.cancel != nil {
		m.draft.picker.cancel()
	}
}
func (a *app) openAdmin(server, tab string) {
	sv := a.store.Server(server)
	if sv == nil {
		return
	}
	if sv.Token == "" {
		a.openLogin(server, func() { a.openAdmin(server, tab) })
		return
	}
	if a.admin != nil {
		a.admin.stop()
	}
	m := &adminPage{server: server, tab: tab, rootCursor: -1, largestCursor: -1, catHover: -1, yearPick: -1, scope: "全部目录", minSize: "≥ 1 MB", page: 1, selected: map[int64]bool{}}
	m.rootList.Selected = &m.rootCursor
	m.rootList.Key = func(i int) any { return a.roots[server][i].ID }
	m.largestList.Selected = &m.largestCursor
	m.largestList.Key = func(i int) any { return fmt.Sprintf("%d/%s", m.largest[i].Root, m.largest[i].Path) }
	m.largestSort.by = ui.SortOrder{Column: colSize, Descending: true}
	m.largestList.Sort = &m.largestSort.by
	m.dirList.Key = func(i int) any { return m.dirs[i].Path }
	m.dupeList.Key = func(i int) any {
		r := m.dupeRows[i]
		g := m.dupes.Groups[r.group]
		if r.file < 0 {
			return "group/" + g.Hash
		}
		return fmt.Sprintf("file/%d", g.Files[r.file].ID)
	}
	a.admin = m
	a.place = adminPlace(server, tab)
	a.reloadAdmin()
}
func rootOption(r fileindex.Root) string     { return fmt.Sprintf("%s (#%d)", r.Name, r.ID) }
func (a *app) adminScope(m *adminPage) int64 { return m.scopeID }
func dupeMin(label string) int64 {
	switch label {
	case "≥ 1 MB":
		return 1 << 20
	case "≥ 10 MB":
		return 10 << 20
	case "≥ 100 MB":
		return 100 << 20
	case "≥ 1 GB":
		return 1 << 30
	}
	return 1
}
func (a *app) reloadAdmin() {
	m := a.admin
	if m == nil || m.busy {
		return
	}
	sv := a.store.Server(m.server)
	if sv == nil || sv.Token == "" {
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	m.cancel = cancel
	c := fileindex.New(sv.Addr, sv.Token)
	scope := a.adminScope(m)
	minSize, page, dir, tab := dupeMin(m.minSize), m.page, m.statsDir, m.tab
	m.gen++
	gen := m.gen
	m.loading = true
	m.err = ""
	go func() {
		defer cancel()
		roots, err := c.RootDetails(ctx)
		if err == nil && scope > 0 {
			found := false
			for _, root := range roots.Roots {
				if root.ID == scope {
					found = true
					break
				}
			}
			if !found {
				scope = 0
				dir = ""
			}
		}
		var dupes *fileindex.Dupes
		var info *fileindex.Info
		var overview *fileindex.Overview
		var largest []fileindex.Entry
		var dirs []fileindex.DirUsage
		var tasks []fileindex.Task
		if err == nil && tab == "dupes" {
			dupes, err = c.DuplicateGroups(ctx, scope, minSize, page)
			if err == nil {
				info, err = c.Info(ctx)
			}
			if err == nil {
				tasks, err = c.Tasks(ctx)
			}
		}
		if err == nil && tab == "stats" {
			overview, err = c.Statistics(ctx, scope)
			if err == nil {
				largest, err = c.LargestFiles(ctx, scope)
			}
			if err == nil && scope > 0 {
				dirs, err = c.DirectoryUsage(ctx, scope, dir)
			}
		}
		a.post(func() {
			if a.admin != m || m.gen != gen || !a.sourceCurrent(m.server, c) {
				return
			}
			m.loading = false
			if err != nil {
				m.err = err.Error()
				if errors.Is(err, fileindex.ErrAuth) {
					a.signedOut(sv)
				}
				return
			}
			a.applyRoots(m.server, roots.Roots)
			m.dataRoots = roots.DataRoots
			m.scopeID = scope
			m.scope = "全部目录"
			for _, root := range roots.Roots {
				if root.ID == scope {
					m.scope = rootOption(root)
				}
			}
			m.statsDir = dir
			if tab == "dupes" {
				if page > 1 && int64((page-1)*50) >= dupes.Summary.Groups {
					m.page = 1
					a.reloadAdmin()
					return
				}
				m.dupes = dupes
				m.lastDupeRun = info.DupesLastRun
				m.tasks = tasks
				// A newly loaded result must not inherit a deletion choice from an older
				// result, even when the same file ID appears in a different hash group.
				m.selected = map[int64]bool{}
				m.dupeRows = nil
				for i, g := range dupes.Groups {
					if len(g.Files) == 0 {
						continue
					}
					m.dupeRows = append(m.dupeRows, duplicateRow{i, -1})
					for j := range g.Files {
						m.dupeRows = append(m.dupeRows, duplicateRow{i, j})
					}
				}
			}
			if tab == "stats" {
				m.overview = overview
				sortEntries(&m.largestSort, largest, nil)
				m.largest = largest
				m.dirs = dirs
				if m.usageRoot != scope || m.usagePath != dir {
					m.dirList.ScrollTo(0, ui.Start)
				}
				m.usageRoot, m.usagePath = scope, dir
				m.largestCursor = -1
			}
			a.watch(sv)
		})
	}()
}

// applyRoots keeps browsing permissions in sync with management edits and
// forgets trees/links when a root is removed or its source changes remotely.
func (a *app) applyRoots(server string, roots []fileindex.Root) {
	old := a.roots[server]
	var selectedID int64
	if m := a.admin; m != nil && m.server == server && m.rootCursor >= 0 && m.rootCursor < len(old) {
		selectedID = old[m.rootCursor].ID
	}
	for i := range roots {
		for _, prev := range old {
			if prev.ID != roots[i].ID || prev.Path != roots[i].Path {
				continue
			}
			// Root snapshots may have been read before a newer live scan event.
			if prev.LastJob != nil && (roots[i].LastJob == nil || prev.LastJob.ID > roots[i].LastJob.ID || prev.LastJob.ID == roots[i].LastJob.ID && prev.LastJob.Finished() && !roots[i].LastJob.Finished()) {
				roots[i].LastJob = prev.LastJob
			}
			active := roots[i].Active
			if active != nil && active.Job != nil && prev.LastJob != nil && prev.LastJob.Finished() && prev.LastJob.ID >= active.Job.ID {
				roots[i].Active = nil
			} else if prev.Active != nil && prev.Active.Job != nil && (roots[i].LastJob == nil || roots[i].LastJob.ID < prev.Active.Job.ID || roots[i].LastJob.ID == prev.Active.Job.ID && !roots[i].LastJob.Finished()) {
				if active == nil || active.Job == nil || prev.Active.Job.ID > active.Job.ID || prev.Active.Job.ID == active.Job.ID && prev.Active.Job.Scanned > active.Job.Scanned {
					roots[i].Active = prev.Active
				}
			}
			break
		}
	}
	a.roots[server] = roots
	if m := a.admin; m != nil && m.server == server {
		m.rootCursor = -1
		for i, root := range roots {
			if root.ID == selectedID {
				m.rootCursor = i
				break
			}
		}
	}
	for _, prev := range old {
		var found *fileindex.Root
		for i := range roots {
			if roots[i].ID == prev.ID {
				found = &roots[i]
				break
			}
		}
		if found == nil || found.Path != prev.Path {
			prefix := rootPlace(server, prev.ID) + "/"
			for key := range a.tree {
				if strings.HasPrefix(key, prefix) {
					delete(a.tree, key)
				}
			}
			if a.proxy != nil {
				a.proxy.Revoke(server)
			}
			if a.preview != nil && a.preview.server == server && a.preview.entry.Root == prev.ID {
				a.closePreview()
			}
		}
	}
	if b := a.br; b != nil && b.server == server {
		for _, root := range roots {
			if root.ID == b.root.ID {
				if root.Path != b.root.Path {
					a.openRoot(server, root)
					return
				}
				b.root = root
				return
			}
		}
		if b.cancel != nil {
			b.cancel()
		}
		if b.thumbCancel != nil {
			b.thumbCancel()
		}
		a.br = nil
		if strings.HasPrefix(a.place, "root/"+server+"/") {
			a.openAdmin(server, "roots")
		}
	}
}
func (a *app) adminAction(m *adminPage, fn func(context.Context, *fileindex.Client) error, note string, after func()) {
	if m.busy {
		return
	}
	sv := a.store.Server(m.server)
	if sv == nil || sv.Token == "" {
		return
	}
	c := fileindex.New(sv.Addr, sv.Token)
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	m.opCancel = cancel
	m.busy = true
	m.err = ""
	m.note = ""
	go func() {
		defer cancel()
		err := fn(ctx, c)
		a.post(func() {
			if a.admin != m || !a.sourceCurrent(m.server, c) {
				return
			}
			m.busy = false
			if err != nil {
				if errors.Is(err, fileindex.ErrAuth) {
					a.signedOut(sv)
					return
				}
				// A batch or a timed-out request may have changed the server before
				// returning an error. Reconcile data before another deletion attempt.
				m.selected = map[int64]bool{}
				a.trashRefreshSoon(m.server)
				for _, root := range a.roots[m.server] {
					a.invalidateTree(m.server, root.ID)
				}
				if a.br != nil && a.br.server == m.server {
					a.reloadKeeping()
				}
				a.reloadAdmin()
				m.err = err.Error()
				return
			}
			m.note = note
			if after != nil {
				after()
			}
			a.reloadAdmin()
		})
	}()
}
func (a *app) adminRoot(m *adminPage) *fileindex.Root {
	roots := a.roots[m.server]
	if m.rootCursor < 0 || m.rootCursor >= len(roots) {
		return nil
	}
	r := roots[m.rootCursor]
	return &r
}

// adminHeader is, under the toolbar, the server managed, and the tabs of
// its management.
func (a *app) adminHeader(c *ui.Context) {
	m := a.admin
	t := c.Theme()
	name := m.server
	if sv := a.store.Server(m.server); sv != nil {
		name = sv.Title()
	}
	ui.Row(c).Gap(6).AlignItems(ui.Center).Padding(0, 6, 0, 0).Children(func() {
		ui.Icon(c, iconServer).FontSize(16).TextColor(t.TextMuted)
		ui.Text(c, name).FontSize(16).Bold().SingleLine()
	})
	tabs := []string{"roots", "dupes", "stats"}
	at := max(0, slices.Index(tabs, m.tab))
	seg := ui.Segmented(c.Key("admin-tabs"), &at, "目录管理", "查重", "空间统计").Label("管理页面").Disabled(m.busy)
	if seg.Changed() && tabs[at] != m.tab {
		a.openAdmin(m.server, tabs[at])
	}
	ui.Spacer(c)
	iconButton(c, "admin-refresh", iconRetry, "刷新", "重新读取管理数据（F5）", m.busy, a.reloadAdmin)
}

// adminView is the management of a server, in the tab chosen.
func (a *app) adminView(c *ui.Context) {
	m := a.admin
	t := c.Theme()
	if !m.busy && c.Shortcut(0, ui.KeyF5) {
		a.reloadAdmin()
	}
	if m.err != "" {
		notice(c, t.Danger, m.err)
	}
	if m.note != "" {
		notice(c, t.Success, m.note)
	}
	if m.loading {
		ui.Text(c, "正在读取管理数据…").FontSize(12).TextColor(t.TextMuted)
	}
	switch m.tab {
	case "roots":
		a.rootsView(c, m)
	case "dupes":
		a.dupesView(c, m)
	case "stats":
		a.statsView(c, m)
	}
	a.adminDialogs(c, m)
}
func (a *app) rootsView(c *ui.Context, m *adminPage) {
	roots := a.roots[m.server]
	r := a.adminRoot(m)
	busy := m.loading || m.busy
	scanning := r != nil && r.Active != nil && r.Active.Job != nil && !r.Active.Job.Finished()
	pendingScan := r != nil && a.hasScanTransfer(m.server, r.ID)
	n := 0
	if r != nil {
		n = 1
	}
	commandBar(c, n, 0, func() { m.rootCursor = -1 }, func() {
		toolButton(c, "add-root", iconPlus, "添加索引目录…", "", busy, func() { m.draft = newRootDraft(nil) })
	}, func() {
		if scanning {
			toolButton(c, "cancel-scan", iconStop, "取消扫描", "", busy, func() { a.cancelRootScan(m, *r) })
		} else {
			toolButton(c, "scan-root", iconRetry, "更新索引", "扫描有变化的文件", busy || pendingScan, func() { a.startScan(m, *r, false) })
		}
		toolButton(c, "open-root", iconFolder, "打开目录", "", busy, func() { a.goPlace(rootPlace(m.server, r.ID)) })
		toolButton(c, "edit-root", iconRename, "编辑目录…", "", busy, func() { m.draft = newRootDraft(r) })
		toolButton(c, "remove-root", iconTrash, "移除索引…", "只移除索引，不删除文件", busy || scanning || a.serverHasTransfers(m.server), func() { m.removing = r; m.confirmRemove = true })
		// Seldom used: icons, with their names in their tips.
		iconButton(c, "scan-history", iconList, "扫描记录", "扫描记录", busy, func() { a.loadScanHistory(m, *r) })
		iconButton(c, "verify-root", iconCheck, "完整校验…", "完整校验：重新读取全部文件，较慢", busy || scanning || pendingScan || r.LastScanAt == 0, func() { m.rebuilding = r; m.confirmRebuild = true })
	}, func() {})
	cols := []ui.TableColumn{{Title: "名称 / 路径"}, {Title: "索引内容", Width: 175}, {Title: "状态 / 自动更新", Width: 265}}
	ui.Table(c.Key("managed-roots"), &m.rootList, cols, len(roots), func(row, col int) {
		root := roots[row]
		muted := c.Theme().TextMuted
		if row == m.rootCursor {
			muted = c.Theme().AccentText.Alpha(0.85)
		}
		ui.Column(c).Gap(4).Padding(4, 0).Children(func() {
			switch col {
			case 0:
				ui.Text(c, root.Name).Bold().SingleLine()
				ui.Text(c, root.Path).SingleLine().FontSize(12).TextColor(muted)
				if root.ReadOnly {
					ui.Badge(c, "只读")
				}
				if len(root.Excludes) > 0 {
					ui.Text(c, "排除："+strings.Join(root.Excludes, "，")).SingleLine().FontSize(12)
				}
			case 1:
				if root.LastScanAt == 0 {
					ui.Text(c, "未建立索引")
				} else {
					ui.Text(c, fmt.Sprintf("%d 个文件 · %d 个文件夹", root.FileCount, root.DirCount)).SingleLine().FontSize(12)
					ui.Text(c, formatBytes(root.TotalSize))
				}
			case 2:
				if root.Active != nil && root.Active.Job != nil {
					j := root.Active.Job
					rolling(ui.Text(c, scanStateText(j.Status)+fmt.Sprintf(" · 已扫描 %d 项", j.Scanned)).SingleLine())
					ui.Text(c, root.Active.Current).SingleLine().FontSize(12)
				} else if root.LastJob != nil {
					j := root.LastJob
					ui.Text(c, scanStateText(j.Status)+" · "+formatWhen(j.FinishedAt)).SingleLine().FontSize(12)
					ui.Text(c, fmt.Sprintf("新增 %d · 更新 %d · 删除 %d · 错误 %d", j.Added, j.Updated, j.Deleted, j.Errors)).SingleLine().FontSize(12)
				}
				ui.Text(c, scheduleLabel(root.Schedule)).FontSize(12).SingleLine().TextColor(muted)
				if root.NextScanAt > 0 {
					ui.Text(c, "下次 "+formatWhen(root.NextScanAt)).FontSize(12).SingleLine()
				}
			}
		})
	}).Grow(1).MinHeight(0).Label("索引目录管理列表").Children(func() {
		if len(roots) == 0 && !m.loading {
			ui.Text(c, "还没有索引目录，点击添加索引目录开始。").Padding(16)
		}
	})
	ui.Text(c, "索引目录路径是 NAS 服务器中的路径；更新索引和完整校验会读取硬盘。").FontSize(12).TextColor(c.Theme().TextMuted)
}
func scanStateText(s string) string {
	switch s {
	case "queued":
		return "排队中"
	case "running":
		return "扫描中"
	case "done":
		return "完成"
	case "failed":
		return "失败"
	case "canceled":
		return "已取消"
	}
	return s
}
func formatWhen(unix int64) string {
	if unix == 0 {
		return "—"
	}
	return time.Unix(unix, 0).Format("2006-01-02 15:04")
}
func (a *app) scopePicker(c *ui.Context, m *adminPage) {
	opts := []string{"全部目录"}
	for _, r := range a.roots[m.server] {
		opts = append(opts, rootOption(r))
	}
	if ui.Select(c.Key("admin-scope"), &m.scope, opts).Width(210).Label("管理范围").Disabled(m.busy).Changed() {
		m.scopeID = 0
		for _, root := range a.roots[m.server] {
			if rootOption(root) == m.scope {
				m.scopeID = root.ID
			}
		}
		m.page = 1
		m.statsDir = ""
		m.selected = map[int64]bool{}
		a.reloadAdmin()
	}
}
func (a *app) adminRefreshSoon(server string) {
	m := a.admin
	if m == nil || m.server != server || m.refreshPending {
		return
	}
	m.refreshPending = true
	time.AfterFunc(300*time.Millisecond, func() {
		a.post(func() {
			m.refreshPending = false
			if a.closed || a.admin != m {
				return
			}
			if m.busy {
				a.adminRefreshSoon(server)
			} else {
				a.reloadAdmin()
			}
		})
	})
}
