package main

import (
	"context"
	"fmt"
	"strings"

	"filesync/config"
	"filesync/engine"
	"filesync/places"

	"github.com/egoist/mygo/ui"
)

// app is the window's state.
type app struct {
	store *config.Store

	// post runs a function on the main thread and draws a new frame:
	// Window.Update in the app, a queue in tests.
	post func(func())
	// pickDir asks for a directory, "" when canceled. It blocks, and is
	// called from goroutines of its own.
	pickDir func(title, start string) (string, error)
	// openPath opens a file or directory with the system's default app.
	openPath func(string)
	// keepAwake keeps the computer from sleeping until release is called.
	keepAwake func(reason string) (release func())
	// closeWindow closes the window, which quits the app.
	closeWindow func()

	selected int // index of the task shown, -1 for none
	tasks    ui.ListState

	editing  bool
	draft    draft
	errorMsg string // shown on the task's page, as a failed save

	confirmRemove bool

	job *job // the preview or backup of the task shown, if any

	askQuit      bool // the window is closing during a backup
	quitWhenDone bool
}

// draft is a task being created or edited.
type draft struct {
	id                      string // "" for a new task
	name, source, target    string
	mode                    int
	excludes                string
	tolerance               float64
	nameErr, srcErr, dstErr string
}

func newApp(store *config.Store) *app {
	a := &app{
		store:       store,
		selected:    -1,
		post:        func(fn func()) { fn() },
		pickDir:     func(string, string) (string, error) { return "", nil },
		openPath:    func(string) {},
		keepAwake:   func(string) func() { return func() {} },
		closeWindow: func() {},
	}
	if len(store.Tasks) > 0 {
		a.selected = 0
	}
	a.tasks.Selected = &a.selected
	a.tasks.Key = func(i int) any { return a.store.Tasks[i].ID }
	a.tasks.Label = func(i int) string { return a.store.Tasks[i].Name }
	return a
}

func (a *app) task() *config.Task {
	if a.selected < 0 || a.selected >= len(a.store.Tasks) {
		return nil
	}
	return a.store.Tasks[a.selected]
}

// busy reports whether a scan or a backup is running, during which the
// tasks cannot change.
func (a *app) busy() bool {
	return a.job != nil && (a.job.state == jobScanning || a.job.state == jobRunning)
}

func (a *app) view(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		a.sidebar(c)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Padding(24, 28).Gap(16).Children(func() {
			switch {
			case a.editing:
				a.editor(c)
			case a.task() == nil:
				ui.Column(c).Grow(1).Center().Gap(12).Children(func() {
					ui.Text(c, "还没有备份任务").FontSize(18).Bold()
					ui.Text(c, "新建一个任务，选择要备份的源目录和备份到的目标目录。").TextColor(t.TextMuted)
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
	if a.askQuit {
		switch ui.AlertDialog(c, &a.askQuit, "正在备份，要停止并退出吗？",
			"已复制完成的文件会保留，正在复制的文件不会写入。下次备份会接着复制剩下的文件。", "继续备份", "停止并退出") {
		case 1:
			a.quit()
		}
	}
	if a.confirmRemove {
		if task := a.task(); task != nil {
			if ui.AlertDialog(c, &a.confirmRemove, fmt.Sprintf("删除任务“%s”？", task.Name),
				"只删除任务设置和运行记录，不会删除源目录或目标目录中的任何文件。", "取消", "删除") == 1 {
				a.removeTask()
			}
		}
	}
}

func (a *app) sidebar(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Width(250).Background(t.Surface).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Text(c, "备份任务").Bold().Padding(18, 16, 10)
		list := ui.List(c.Key("tasks"), &a.tasks, len(a.store.Tasks)).Grow(1).Label("备份任务").Rows(func(row ui.ListRow) {
			task := a.store.Tasks[row.Index]
			muted := t.TextMuted
			if row.Selected() {
				muted = t.AccentText.Alpha(0.85)
			}
			ui.Column(c).Padding(8, 16).Gap(2).Children(func() {
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
		ui.Column(c).Padding(12).Children(func() {
			if ui.Button(c, "＋ 新建任务").Disabled(a.busy() || a.editing).Clicked() {
				a.startEditing(nil)
			}
		})
	})
}

// detail shows the task chosen.
func (a *app) detail(c *ui.Context) {
	t := c.Theme()
	task := a.task()
	ui.Row(c).Gap(12).Children(func() {
		ui.Text(c, task.Name).FontSize(22).Bold().Grow(1).Shrink(1)
		ui.Badge(c, task.Mode.String())
	})
	if a.errorMsg != "" {
		notice(c, t.Danger, a.errorMsg)
	}
	card(c, func() {
		ui.Form(c, func() {
			ui.Field(c, "源目录", func() { location(c, task.Source) })
			ui.Field(c, "目标目录", func() { location(c, task.Target) })
			ui.Field(c, "模式", func() { ui.Text(c, modeHelp[task.Mode]).TextColor(t.TextMuted) })
			excl := "无"
			if len(task.Excludes) > 0 {
				excl = strings.Join(task.Excludes, "   ")
			}
			ui.Field(c, "排除", func() { ui.Text(c, excl).TextColor(t.TextMuted) })
		})
	})
	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, "预览并备份…").Clicked() {
			a.startPreview(task)
		}
		if ui.Button(c, "编辑").Clicked() {
			a.startEditing(task)
		}
		ui.Spacer(c)
		if ui.Button(c, "删除任务").Clicked() {
			a.confirmRemove = true
		}
	})

	ui.Text(c, "运行记录").Bold().Margin(8, 0, 0, 0)
	if len(task.History) == 0 {
		ui.Text(c, "还没有运行过。").TextColor(t.TextMuted)
		return
	}
	ui.List(c.Key("history:"+task.ID), nil, len(task.History), func(i int) {
		r := task.History[i]
		ui.Row(c).Gap(12).Padding(8, 4).Children(func() {
			ui.Text(c, r.Started.Local().Format("2006-01-02 15:04")).Width(130).FontFeatures("tnum")
			ui.Text(c, runStatusShort(r)).Width(80).TextColor(statusColor(t, r.Status))
			ui.Text(c, runCounts(r)).Grow(1).Shrink(1).SingleLine().TextColor(t.TextMuted)
			if r.Log != "" {
				if ui.Button(c, "日志").Clicked() {
					a.openPath(r.Log)
				}
			}
		})
	}).Grow(1).Dividers(1, t.Border).Label("运行记录")
}

var modeHelp = map[engine.Mode]string{
	engine.Incremental: "复制新增和修改过的文件，目标中多出来的文件保留不动。",
	engine.Mirror:      "让目标与源完全相同：复制新增和修改过的文件，并删除目标中源没有的文件。",
}

func (a *app) startEditing(task *config.Task) {
	a.editing = true
	a.errorMsg = ""
	if task == nil {
		a.draft = draft{
			name:      fmt.Sprintf("备份任务 %d", len(a.store.Tasks)+1),
			excludes:  strings.Join(config.DefaultExcludes, "\n"),
			tolerance: engine.DefaultTimeTolerance.Seconds(),
		}
		return
	}
	tol := float64(task.TimeTolerance)
	if tol == 0 {
		tol = engine.DefaultTimeTolerance.Seconds()
	}
	a.draft = draft{
		id: task.ID, name: task.Name, source: task.Source, target: task.Target,
		mode: int(task.Mode), excludes: strings.Join(task.Excludes, "\n"), tolerance: tol,
	}
}

func (a *app) editor(c *ui.Context) {
	t := c.Theme()
	d := &a.draft
	title := "新建任务"
	if d.id != "" {
		title = "编辑任务"
	}
	ui.Text(c, title).FontSize(22).Bold()
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Gap(14).MaxWidth(720).Children(func() {
			ui.Field(c, "名称", func() {
				ui.TextInput(c.Key("name"), &d.name).AutoFocus()
			}).Error(d.nameErr)
			a.dirField(c, "源目录", "要备份的目录", &d.source, d.srcErr)
			a.dirField(c, "目标目录", "备份到的目录，网络共享请先在系统中挂载", &d.target, d.dstErr)
			ui.Field(c, "模式", func() {
				ui.RadioGroup(c, func() {
					ui.Radio(c, &d.mode, int(engine.Incremental), "增量备份")
					ui.Text(c, modeHelp[engine.Incremental]).FontSize(12).TextColor(t.TextMuted).Margin(0, 0, 6, 26)
					ui.Radio(c, &d.mode, int(engine.Mirror), "完全一致")
					ui.Text(c, modeHelp[engine.Mirror]).FontSize(12).TextColor(t.TextMuted).Margin(0, 0, 0, 26)
				}).Label("模式")
			})
			ui.Field(c, "排除规则", func() {
				ui.TextArea(c.Key("excludes"), &d.excludes).Lines(4, 10).Font("monospace")
			}).Description("每行一条。*.tmp 匹配任意位置的文件名；以 / 结尾只匹配目录，如 cache/；含 / 的规则从根目录匹配，如 /build/out。被排除的文件不会复制，也不会被删除。")
			ui.Field(c, "时间容差（秒）", func() {
				ui.NumberInput(c, &d.tolerance, 0, 3600, 1).Width(140)
			}).Description("修改时间相差不超过这么多秒、大小又相同的文件视为没有变化。FAT 盘或 FTP 挂载的时间精度较低，可以调大，例如 FTP 用 60。")
		})
	})
	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, "保存").Clicked() {
			a.saveDraft()
		}
		if ui.Button(c, "取消").Clicked() || c.Shortcut(0, ui.KeyEscape) {
			a.editing = false
		}
	})
}

func (a *app) dirField(c *ui.Context, label, help string, value *string, errMsg string) {
	ui.Field(c, label, func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.TextInput(c.Key(label), value).Placeholder(help).Grow(1).Shrink(1).MinWidth(0)
			if ui.Button(c.Key(label+"-browse"), "浏览…").Clicked() {
				start := *value
				go func() {
					// A URL is opened first, for the dialog to start in it.
					dir, err := places.Resolve(context.Background(), start)
					if err != nil {
						dir = ""
					}
					if dir, err = a.pickDir("选择"+label, dir); err == nil && dir != "" {
						dir = places.Stable(context.Background(), dir)
					}
					a.post(func() {
						if err != nil {
							a.errorMsg = err.Error()
						} else if dir != "" {
							*value = dir
						}
					})
				}()
			}
		})
	}).Error(errMsg)
}

func (a *app) saveDraft() {
	d := &a.draft
	d.name = strings.TrimSpace(d.name)
	// A directory of kio-fuse is kept as its URL, as its path changes with
	// the session.
	d.source = places.Stable(context.Background(), strings.TrimSpace(d.source))
	d.target = places.Stable(context.Background(), strings.TrimSpace(d.target))
	d.nameErr, d.srcErr, d.dstErr = "", "", ""
	if d.name == "" {
		d.nameErr = "请输入名称。"
	}
	if d.source == "" {
		d.srcErr = "请选择源目录。"
	}
	if d.target == "" {
		d.dstErr = "请选择目标目录。"
	}
	var excludes []string
	for _, line := range strings.Split(d.excludes, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			excludes = append(excludes, line)
		}
	}
	if _, err := engine.NewFilter(excludes); err != nil {
		a.errorMsg = err.Error()
		return
	}
	if d.nameErr != "" || d.srcErr != "" || d.dstErr != "" {
		return
	}

	task := a.store.Task(d.id)
	if task == nil {
		task = &config.Task{}
		a.store.Add(task)
		a.selected = len(a.store.Tasks) - 1
	}
	task.Name, task.Source, task.Target = d.name, d.source, d.target
	task.Mode = engine.Mode(d.mode)
	task.Excludes = excludes
	task.TimeTolerance = int(d.tolerance)
	if d.tolerance == engine.DefaultTimeTolerance.Seconds() {
		task.TimeTolerance = 0
	}
	a.editing = false
	a.errorMsg = ""
	a.save()
}

func (a *app) removeTask() {
	task := a.task()
	if task == nil {
		return
	}
	a.store.Remove(task.ID)
	if a.selected >= len(a.store.Tasks) {
		a.selected = len(a.store.Tasks) - 1
	}
	a.save()
}

// requestClose is called as the window is about to close, and reports
// whether it may.
func (a *app) requestClose() bool {
	if a.job != nil && a.job.state == jobRunning {
		a.askQuit = true
		return false
	}
	if a.job != nil && a.job.cancel != nil {
		a.job.cancel()
	}
	return true
}

func (a *app) quit() {
	if a.job == nil || a.job.state != jobRunning {
		a.closeWindow()
		return
	}
	// The window closes once the backup has stopped and its run is recorded.
	a.quitWhenDone = true
	a.job.cancel()
}

func (a *app) save() {
	if err := a.store.Save(); err != nil {
		a.errorMsg = "无法保存设置: " + err.Error()
	}
}

// location shows a task's directory, and how a URL is reached.
func location(c *ui.Context, p string) {
	ui.Column(c).Gap(2).Children(func() {
		ui.Text(c, p).Selectable()
		if places.IsURL(p) {
			ui.Text(c, "网络位置，备份时通过 KDE 的 kio-fuse 打开（文件很多时较慢）").FontSize(12).TextColor(c.Theme().TextMuted)
		}
	})
}

// card is a bordered box.
func card(c *ui.Context, fn func()) {
	t := c.Theme()
	ui.Column(c).Padding(16).Gap(10).Radius(t.Radius*2).Border(1, t.Border).Background(t.Background).Children(fn)
}

// notice is a colored message.
func notice(c *ui.Context, color ui.Color, msg string) {
	ui.Row(c).Padding(10, 12).Radius(c.Theme().Radius*2).Background(color.Alpha(0.1)).Border(1, color.Alpha(0.4)).Children(func() {
		ui.Text(c, msg).Grow(1).Shrink(1).Selectable()
	})
}

func statusColor(t *ui.Theme, s config.Status) ui.Color {
	switch s {
	case config.StatusOK:
		return t.Success
	case config.StatusErrors, config.StatusCanceled:
		return t.Warning
	default:
		return t.Danger
	}
}

func runStatusShort(r config.Run) string {
	switch r.Status {
	case config.StatusOK:
		return "成功"
	case config.StatusErrors:
		return fmt.Sprintf("%d 个错误", r.Errors)
	case config.StatusCanceled:
		return "已停止"
	default:
		return "失败"
	}
}

func runCounts(r config.Run) string {
	if r.Status == config.StatusFailed && r.Message != "" {
		return r.Message
	}
	s := fmt.Sprintf("复制 %d 个文件（%s）", r.Copied, formatBytes(r.Bytes))
	if r.Deleted > 0 {
		s += fmt.Sprintf("，删除 %d 个", r.Deleted)
	}
	return s + "，用时 " + formatDuration(r.Duration)
}
