package main

import (
	"fmt"
	"slices"
	"time"

	"filesync/config"
	"filesync/engine"

	"github.com/egoist/mygo/ui"
)

// The page of the backup tasks: a card for each, which backs it up at
// once, and shows it under way and how it ended; the preview, the results
// in full and the history of a task show in its place, from the card.

// backupPage is the backup tasks, or what of one shows in their place.
func (a *app) backupPage(c *ui.Context) {
	t := c.Theme()
	if len(a.store.Tasks) == 0 {
		emptyState(c, iconBackup, "还没有备份任务", "在左侧“本机”中找到要备份的文件夹或文件，选中后点“创建备份任务…”。", func() {
			if ui.PrimaryButton(c, "浏览本机文件").Clicked() {
				a.openLocal(a.startLocal())
			}
		})
		return
	}
	if j := a.job; j != nil && j.detail && a.chooseTask(j.taskID) {
		a.backToTasks(c, func() {
			if j.state == jobRunning || j.state == jobScanning {
				j.detail = false // it goes on, in its card
			} else {
				a.job = nil
			}
		})
		a.jobView(c)
		return
	}
	if a.taskShown != "" && a.chooseTask(a.taskShown) {
		a.backToTasks(c, func() { a.taskShown = "" })
		if a.errorMsg != "" {
			notice(c, t.Danger, a.errorMsg)
		}
		a.detail(c)
		return
	}
	a.taskShown = ""
	if a.errorMsg != "" {
		closableNotice(c, t.Danger, a.errorMsg, func() { a.errorMsg = "" })
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, fmt.Sprintf("%d 个备份任务", len(a.store.Tasks))).FontSize(12).TextColor(t.TextMuted)
		ui.Spacer(c)
		toolButton(c, "new-task", iconPlus, "新建任务…", "在本机中选择要备份的文件夹或文件", false, func() {
			a.openLocal(a.startLocal())
		})
	})
	ui.Scroll(c).Grow(1).MinHeight(0).Gap(10).Children(func() {
		for _, task := range a.store.Tasks {
			a.taskCard(c, task)
		}
	})
}

// chooseTask makes the task with the ID the one shown, if it is still.
func (a *app) chooseTask(id string) bool {
	i := slices.IndexFunc(a.store.Tasks, func(t *config.Task) bool { return t.ID == id })
	if i >= 0 {
		a.selected = i
	}
	return i >= 0
}

// backToTasks is the button going back to the list of the tasks.
func (a *app) backToTasks(c *ui.Context, back func()) {
	ui.Row(c).Children(func() {
		toolButton(c, "back-to-tasks", iconPrev, "返回任务列表", "", false, back)
	})
}

// taskCard is a task: what it backs up where, how it ran last, and the
// backup under way.
func (a *app) taskCard(c *ui.Context, task *config.Task) {
	t := c.Theme()
	var j *job
	if a.job != nil && a.job.taskID == task.ID {
		j = a.job
	}
	busy := j != nil && (j.state == jobScanning || j.state == jobRunning)
	ui.Column(c.Key("task/"+task.ID)).Padding(14, 16).Gap(10).Radius(t.Radius*2).Border(1, t.Border).Background(t.Background).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			a.taskMark(c, task, j)
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(3).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, task.Name).Bold().SingleLine().Shrink(1)
					ui.Badge(c, task.Mode.String())
				})
				where := task.Contents() + "  →  " + a.target(task.Server, task.RootName, task.BackupPath())
				ui.Text(c, where).FontSize(12).SingleLine().Tooltip(where).TextColor(t.TextMuted)
				ui.Text(c, lastRun(task)).FontSize(12).SingleLine().TextColor(t.TextMuted)
			})
			switch {
			case j != nil && j.state == jobRunning:
				toolButton(c, "stop/"+task.ID, iconStop, "停止", "停止备份；已复制的文件会保留", false, func() { j.cancel() })
			case j != nil && j.state == jobScanning:
				toolButton(c, "cancel/"+task.ID, iconClose, "取消", "", false, func() {
					j.cancel()
					a.job = nil
				})
			default:
				run := ui.PrimaryButton(c.Key("run/"+task.ID), "立即备份").Disabled(a.busy()).Tooltip("比较后直接备份；会删除 NAS 上的文件时先请你确认")
				if run.Clicked() {
					a.quickBackup(task)
				}
			}
			iconButton(c, "task-menu/"+task.ID, iconMore, "任务菜单 "+task.Name, "更多操作", false, func() {}).Menu(func(m *ui.Menu) {
				if m.Item("预览并备份…").Disabled(a.busy()).Chosen() {
					a.taskShown = ""
					a.chooseTask(task.ID)
					a.startPreview(task)
				}
				if m.Item("详情与运行记录").Chosen() {
					a.chooseTask(task.ID)
					a.taskShown = task.ID
				}
				if m.Item("编辑…").Disabled(busy).Chosen() {
					a.chooseTask(task.ID)
					a.startEditing(task)
				}
				m.Separator()
				if m.Item("删除任务…").Disabled(busy).Chosen() {
					a.chooseTask(task.ID)
					a.confirmRemove = true
				}
			})
		})
		if j != nil && !j.detail {
			a.jobInline(c, j, task)
		}
	})
}

// taskMark is the round mark of a task: how it ran last, or that it runs.
func (a *app) taskMark(c *ui.Context, task *config.Task, j *job) {
	t := c.Theme()
	ui.Column(c).Size(34, 34).Radius(17).Center().Background(t.Surface).Children(func() {
		switch {
		case j != nil && (j.state == jobScanning || j.state == jobRunning):
			ui.Spinner(c).FontSize(16).Label("正在备份 " + task.Name)
		case len(task.History) == 0:
			ui.Icon(c, iconBackup).FontSize(16).TextColor(t.TextMuted)
		case task.History[0].Status == config.StatusOK:
			ui.Icon(c, iconCheck).FontSize(16).TextColor(t.Success)
		default:
			ui.Icon(c, iconAlert).FontSize(16).TextColor(statusColor(t, task.History[0].Status))
		}
	})
}

// lastRun says how a task ran last.
func lastRun(task *config.Task) string {
	if len(task.History) == 0 {
		return "还没有运行过"
	}
	r := task.History[0]
	return "上次 " + r.Started.Local().Format("2006-01-02 15:04") + " · " + runStatusShort(r) + " · " + runCounts(r)
}

// jobInline shows, in its card, the backup of a task under way, or how it
// ended.
func (a *app) jobInline(c *ui.Context, j *job, task *config.Task) {
	t := c.Theme()
	ui.Column(c).Gap(6).Padding(10, 0, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		switch j.state {
		case jobScanning:
			ui.Textf(c, "正在比较本机和 NAS 上的文件… 已扫描 %s 项", groupDigits(int64(j.scanned))).FontSize(12).FontFeatures("tnum").TextColor(t.TextMuted)
		case jobRunning:
			p := j.progress
			frac := 0.0
			if p.TotalBytes > 0 {
				frac = float64(p.DoneBytes) / float64(p.TotalBytes)
			} else if p.Total > 0 {
				frac = float64(p.Done) / float64(p.Total)
			}
			ui.Progress(c, min(frac, 1)).Label("备份进度 " + task.Name)
			info := fmt.Sprintf("%d / %d 项 · %s / %s · %s/s", p.Done, p.Total, formatBytes(p.DoneBytes), formatBytes(p.TotalBytes), formatBytes(int64(j.speed.rate())))
			if eta := j.speed.eta(p.TotalBytes - p.DoneBytes); eta > 0 {
				info += " · 剩余约 " + formatDuration(eta.Seconds())
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, info).FontSize(12).SingleLine().Shrink(1).FontFeatures("tnum").TextColor(t.TextMuted)
				ui.Spacer(c)
				ui.Button(c.Key("watch/"+task.ID), "详情").OnClick(func() { j.detail = true })
			})
			c.After(time.Second) // the speed and the time left
		case jobPreview:
			// A backup at once that had nothing to do.
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconCheck).FontSize(14).TextColor(t.Success)
				ui.Textf(c, "目标已是最新：%d 个文件都没有变化", j.plan.SourceFiles).FontSize(12).Grow(1).Shrink(1)
				ui.Button(c.Key("dismiss/"+task.ID), "关闭").OnClick(func() { a.job = nil })
			})
		case jobDone:
			r := j.result
			title, color, svg := "备份完成", t.Success, iconCheck
			switch {
			case r.Fatal != nil:
				title, color, svg = "备份失败："+r.Fatal.Error(), t.Danger, iconAlert
			case r.Canceled:
				title, color, svg = "备份已停止", t.Warning, iconAlert
			case len(r.Errors) > 0:
				title, color, svg = fmt.Sprintf("备份完成，但有 %d 个错误", len(r.Errors)), t.Warning, iconAlert
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, svg).FontSize(14).TextColor(color)
				ui.Text(c, title).FontSize(12).Bold().TextColor(color).SingleLine().Shrink(1).Tooltip(title)
				ui.Textf(c, "新增 %d · 更新 %d · 删除 %d · %s · 用时 %s", r.Created, r.Updated, r.Deleted, formatBytes(r.Bytes),
					formatDuration(r.Finished.Sub(r.Started).Seconds())).FontSize(12).SingleLine().Shrink(1).FontFeatures("tnum").TextColor(t.TextMuted)
				ui.Spacer(c)
				ui.Button(c.Key("result/"+task.ID), "详情").OnClick(func() { j.detail = true })
				ui.Button(c.Key("dismiss/"+task.ID), "关闭").OnClick(func() { a.job = nil })
			})
		}
	})
}

// quickBackup backs a task up at once: it compares, then copies, asking
// first only for what deletes on the NAS, or when the comparison failed,
// in the preview.
func (a *app) quickBackup(task *config.Task) {
	if a.busy() {
		return
	}
	a.chooseTask(task.ID)
	a.taskShown = ""
	a.startPreview(task)
	a.job.quick, a.job.detail = true, false
}

// previewed goes on with a backup at once, as its comparison ended.
func (a *app) previewed(j *job) {
	task := a.store.Task(j.taskID)
	switch {
	case !j.quick || task == nil:
	case j.err != nil:
		j.detail = true
	case j.plan.Summary.Changes() == 0:
		// Up to date: the card tells it.
	case hasDeletions(j.plan):
		j.detail = true // the deletions, to confirm
	default:
		a.startBackup(task)
	}
}

// hasDeletions reports whether a plan deletes on the NAS.
func hasDeletions(p *engine.Plan) bool { return p.Summary.Delete > 0 || p.MassDelete }
