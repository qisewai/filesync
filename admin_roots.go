package main

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"filesync/fileindex"
	"github.com/egoist/mygo/ui"
)

type rootDraft struct {
	open                                bool
	id                                  int64
	name, path, excludes                string
	readOnly                            bool
	scheduleKind, hours, clock, weekday string
	err                                 string
	picker                              *serverPicker
}
type serverPicker struct {
	open, loading bool
	path, err     string
	dirs          []fileindex.ServerDirectory
	gen           int
	cancel        context.CancelFunc
	list          ui.ListState
}

var weekdays = []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

func newRootDraft(r *fileindex.Root) *rootDraft {
	d := &rootDraft{open: true, scheduleKind: "不自动更新", hours: "24", clock: "02:00", weekday: "周日"}
	if r == nil {
		return d
	}
	d.id = r.ID
	d.name = r.Name
	d.path = r.Path
	d.readOnly = r.ReadOnly
	d.excludes = strings.Join(r.Excludes, "\n")
	parts := strings.Split(r.Schedule, ":")
	switch {
	case len(parts) == 2 && parts[0] == "hourly":
		d.scheduleKind = "每隔几小时"
		d.hours = parts[1]
	case len(parts) == 3 && parts[0] == "daily":
		d.scheduleKind = "每天"
		d.clock = strings.Join(parts[1:], ":")
	case len(parts) == 4 && parts[0] == "weekly":
		d.scheduleKind = "每周"
		day, _ := strconv.Atoi(parts[1])
		if day >= 0 && day < len(weekdays) {
			d.weekday = weekdays[day]
		}
		d.clock = strings.Join(parts[2:], ":")
	}
	return d
}
func (d *rootDraft) schedule() (string, error) {
	switch d.scheduleKind {
	case "不自动更新":
		return "", nil
	case "每隔几小时":
		n, err := strconv.Atoi(strings.TrimSpace(d.hours))
		if err != nil || n < 1 || n > 168 {
			return "", fmt.Errorf("小时数应在 1–168 之间")
		}
		return fmt.Sprintf("hourly:%d", n), nil
	}
	clock, err := time.Parse("15:04", strings.TrimSpace(d.clock))
	if err != nil {
		return "", fmt.Errorf("请输入 HH:MM 格式的时间")
	}
	if d.scheduleKind == "每天" {
		return "daily:" + clock.Format("15:04"), nil
	}
	for day, name := range weekdays {
		if name == d.weekday {
			return fmt.Sprintf("weekly:%d:%s", day, clock.Format("15:04")), nil
		}
	}
	return "", fmt.Errorf("请选择星期")
}
func scheduleLabel(spec string) string {
	if spec == "" {
		return "手动更新"
	}
	p := strings.Split(spec, ":")
	switch {
	case len(p) == 2 && p[0] == "hourly":
		return "每 " + p[1] + " 小时自动更新"
	case len(p) == 3 && p[0] == "daily":
		return "每天 " + strings.Join(p[1:], ":") + " 自动更新"
	case len(p) == 4 && p[0] == "weekly":
		day, _ := strconv.Atoi(p[1])
		if day >= 0 && day < len(weekdays) {
			return weekdays[day] + " " + strings.Join(p[2:], ":") + " 自动更新"
		}
	}
	return spec
}
func (a *app) saveManagedRoot(m *adminPage, d *rootDraft) {
	spec, err := d.schedule()
	if err != nil {
		d.err = err.Error()
		return
	}
	if !strings.HasPrefix(strings.TrimSpace(d.path), "/") {
		d.err = "请选择或输入 NAS 上的绝对目录路径"
		return
	}
	var excludes []string
	for _, line := range strings.Split(d.excludes, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			excludes = append(excludes, line)
		}
	}
	settings := fileindex.RootSettings{Name: strings.TrimSpace(d.name), Path: strings.TrimSpace(d.path), Excludes: excludes, ReadOnly: d.readOnly, Schedule: spec}
	id := d.id
	a.adminAction(m, func(ctx context.Context, c *fileindex.Client) error {
		_, err := c.SaveRoot(ctx, id, settings)
		return err
	}, "已保存目录设置", func() { m.draft = nil; m.rootCursor = -1 })
}
func (a *app) adminDialogs(c *ui.Context, m *adminPage) {
	if r := m.removing; r != nil {
		message := "只移除索引配置和扫描记录，不删除硬盘上的原文件。该目录的回收站记录也会移除，之后无法在客户端恢复。"
		for _, task := range a.store.Tasks {
			if task.Server == m.server && task.Root == r.ID {
				message += " 当前备份任务仍引用此目录，移除后需要重新设置备份目标。"
				break
			}
		}
		answer := ui.AlertDialog(c.Key("remove-managed-root"), &m.confirmRemove, "移除索引目录“"+r.Name+"”？", message, "取消", "确认移除索引")
		if !m.confirmRemove {
			m.removing = nil
		}
		if answer == 1 {
			id := r.ID
			a.adminAction(m, func(ctx context.Context, c *fileindex.Client) error { return c.RemoveRoot(ctx, id) }, "已移除索引目录", func() { m.rootCursor = -1; a.trashRefreshSoon(m.server) })
		}
	}
	if r := m.rebuilding; r != nil {
		answer := ui.AlertDialog(c.Key("rebuild-managed-root"), &m.confirmRebuild, "完整校验“"+r.Name+"”？", "扫描整个目录并清理索引中已不存在的条目，不删除硬盘文件。", "取消", "开始完整校验")
		if !m.confirmRebuild {
			m.rebuilding = nil
		}
		if answer == 1 {
			a.startScan(m, *r, true)
		}
	}
	if d := m.draft; d != nil {
		t := c.Theme()
		w, h := c.Size()
		ui.DialogBase(c.Key("root-editor"), &d.open, func(backdrop, panel ui.Element) {
			backdrop.Background(ui.RGBA(0, 0, 0, 0.4))
			panel.Width(min(620, w-32)).Height(min(600, h-32)).Padding(20).Gap(10).Radius(t.Radius*3).Background(t.Background).Border(1, t.Border)
			title := "添加索引目录"
			if d.id > 0 {
				title = "编辑索引目录"
			}
			ui.Text(c, title).FontSize(18).Bold()
			ui.Scroll(c).Grow(1).MinHeight(0).Gap(10).Children(func() {
				if d.err != "" {
					notice(c, t.Danger, d.err)
				}
				if m.err != "" {
					notice(c, t.Danger, m.err)
				}
				ui.Text(c, "NAS 路径")
				ui.Row(c).Gap(8).Children(func() {
					ui.TextInput(c.Key("root-path"), &d.path).Label("NAS 路径").Grow(1).Disabled(d.id > 0 || m.busy).Placeholder(strings.Join(m.dataRoots, " 或 "))
					ui.Button(c, "浏览服务器目录…").Disabled(d.id > 0 || m.busy).OnClick(func() { d.picker = &serverPicker{open: true}; a.loadServerPicker(m, d, d.path) })
				})
				ui.TextInput(c.Key("root-name"), &d.name).Label("目录名称").Placeholder("目录名称（留空使用文件夹名）").Disabled(m.busy)
				ui.Checkbox(c.Key("root-readonly"), &d.readOnly, "只读目录").Disabled(m.busy)
				ui.Text(c, "只读目录允许浏览、搜索和下载，禁止上传或修改文件。").FontSize(12).TextColor(t.TextMuted)
				ui.Select(c.Key("root-schedule"), &d.scheduleKind, []string{"不自动更新", "每隔几小时", "每天", "每周"}).Label("自动更新频率").Disabled(m.busy)
				ui.Row(c).Gap(8).Children(func() {
					switch d.scheduleKind {
					case "每隔几小时":
						ui.Text(c, "每")
						ui.TextInput(c.Key("root-hours"), &d.hours).Width(90).Label("间隔小时").Disabled(m.busy)
						ui.Text(c, "小时")
					case "每天", "每周":
						if d.scheduleKind == "每周" {
							ui.Select(c.Key("root-weekday"), &d.weekday, weekdays).Width(100).Disabled(m.busy).Label("更新星期")
						}
						ui.TextInput(c.Key("root-clock"), &d.clock).Width(100).Label("更新时间").Disabled(m.busy)
					}
				})
				ui.Text(c, "按服务器时区执行增量更新，会唤醒相关硬盘。").FontSize(12).TextColor(t.TextMuted)
				ui.Text(c, "排除规则（每行一个）")
				ui.TextArea(c.Key("root-excludes"), &d.excludes).Label("索引排除规则").Height(100).Disabled(m.busy).Placeholder("*.tmp\n@eaDir")
			})
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				ui.Button(c, "取消编辑").Disabled(m.busy).OnClick(func() { d.open = false })
				ui.PrimaryButton(c, "保存目录设置").Disabled(m.busy).OnClick(func() { a.saveManagedRoot(m, d) })
			})
		})
		if !d.open && !m.busy {
			if d.picker != nil && d.picker.cancel != nil {
				d.picker.cancel()
			}
			m.draft = nil
		}
		if d.picker != nil {
			a.serverPickerView(c, m, d)
		}
	}
	if m.history {
		a.historyView(c, m)
	}
}
func (a *app) loadServerPicker(m *adminPage, d *rootDraft, dir string) {
	p := d.picker
	sv := a.store.Server(m.server)
	if p == nil || sv == nil {
		return
	}
	if p.cancel != nil {
		p.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	p.cancel = cancel
	p.gen++
	gen := p.gen
	p.loading = true
	p.err = ""
	c := fileindex.New(sv.Addr, sv.Token)
	p.list.Key = func(i int) any { return p.dirs[i].Path }
	go func() {
		defer cancel()
		out, err := c.ServerDirs(ctx, dir)
		a.post(func() {
			if a.admin != m || m.draft != d || d.picker != p || gen != p.gen || !a.sourceCurrent(m.server, c) {
				return
			}
			p.loading = false
			if err != nil {
				p.err = err.Error()
				if errors.Is(err, fileindex.ErrAuth) {
					a.signedOut(sv)
				}
				return
			}
			p.path = out.Path
			p.dirs = out.Dirs
			p.list.ScrollTo(0, ui.Start)
		})
	}()
}
func (a *app) serverPickerView(c *ui.Context, m *adminPage, d *rootDraft) {
	p := d.picker
	t := c.Theme()
	w, h := c.Size()
	ui.DialogBase(c.Key("server-root-picker"), &p.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.4))
		panel.Width(min(620, w-32)).Height(min(500, h-32)).Padding(20).Gap(12).Radius(t.Radius*3).Background(t.Background).Border(1, t.Border)
		ui.Text(c, "选择 NAS 上的索引目录").Bold().FontSize(18)
		ui.Text(c, p.path).SingleLine()
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c, "服务器顶层").Disabled(p.loading).OnClick(func() { a.loadServerPicker(m, d, "") })
			ui.Button(c, "上一级目录").Disabled(p.loading || p.path == "" || !remoteParentAllowed(m.dataRoots, p.path)).OnClick(func() { a.loadServerPicker(m, d, path.Dir(p.path)) })
		})
		if p.err != "" {
			notice(c, t.Danger, p.err)
		}
		if p.loading {
			ui.Text(c, "正在列出服务器目录…")
		}
		ui.List(c.Key("server-directories"), &p.list, len(p.dirs)).Grow(1).MinHeight(0).Label("服务器目录列表").Rows(func(row ui.ListRow) {
			dir := p.dirs[row.Index]
			ui.Button(c.Key(dir.Path), "📁 "+dir.Name).Margin(3, 0).Disabled(p.loading).OnClick(func() { a.loadServerPicker(m, d, dir.Path) })
		})
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			ui.Button(c, "关闭目录选择").OnClick(func() { p.open = false })
			ui.PrimaryButton(c, "使用当前目录").Disabled(p.loading || p.path == "" || p.err != "").OnClick(func() { d.path = p.path; p.open = false })
		})
	})
	if !p.open {
		if p.cancel != nil {
			p.cancel()
		}
		d.picker = nil
	}
}
func remoteParentAllowed(mounts []string, dir string) bool {
	parent := path.Dir(dir)
	for _, mount := range mounts {
		if parent == mount || strings.HasPrefix(parent, strings.TrimRight(mount, "/")+"/") {
			return true
		}
	}
	return false
}
func (a *app) loadScanHistory(m *adminPage, r fileindex.Root) {
	if m.historyCancel != nil {
		m.historyCancel()
	}
	sv := a.store.Server(m.server)
	if sv == nil {
		return
	}
	c := fileindex.New(sv.Addr, sv.Token)
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	m.historyCancel = cancel
	m.historyGen++
	gen := m.historyGen
	m.history = true
	m.historyRoot = r
	m.historyLoading = true
	m.jobs = nil
	go func() {
		defer cancel()
		jobs, err := c.ScanJobs(ctx, r.ID)
		a.post(func() {
			if a.admin != m || !m.history || m.historyGen != gen || !a.sourceCurrent(m.server, c) {
				return
			}
			m.historyLoading = false
			if err != nil {
				m.err = err.Error()
				if errors.Is(err, fileindex.ErrAuth) {
					a.signedOut(sv)
				}
			} else {
				m.jobs = jobs
			}
		})
	}()
}
func (a *app) historyView(c *ui.Context, m *adminPage) {
	t := c.Theme()
	w, h := c.Size()
	ui.DialogBase(c.Key("scan-history"), &m.history, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.4))
		panel.Width(min(760, w-32)).Height(min(520, h-32)).Padding(20).Gap(10).Radius(t.Radius*3).Background(t.Background).Border(1, t.Border)
		ui.Text(c, "扫描记录 · "+m.historyRoot.Name).Bold().FontSize(18)
		ui.Scroll(c).Grow(1).MinHeight(0).Gap(10).Children(func() {
			if m.historyLoading {
				ui.Text(c, "正在读取扫描记录…")
			}
			for _, j := range m.jobs {
				ui.Column(c.Key(j.ID)).Border(1, t.Border).Radius(t.Radius).Padding(10).Gap(4).Children(func() {
					mode := "增量更新"
					if j.Mode == "rebuild" {
						mode = "完整校验"
					}
					ui.Text(c, fmt.Sprintf("#%d · %s · %s · %s", j.ID, mode, scanStateText(j.Status), formatWhen(j.CreatedAt))).Bold()
					ui.Text(c, fmt.Sprintf("扫描 %d · 新增 %d · 更新 %d · 删除 %d · 错误 %d", j.Scanned, j.Added, j.Updated, j.Deleted, j.Errors)).FontSize(12)
					if j.Message != "" {
						ui.Text(c, j.Message).Selectable().TextColor(t.Danger)
					}
				})
			}
		})
		ui.Button(c, "关闭扫描记录").OnClick(func() {
			m.history = false
			if m.historyCancel != nil {
				m.historyCancel()
			}
		})
	})
}
