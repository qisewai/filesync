package main

import (
	"context"
	"errors"
	"fmt"
	"path"
	"time"

	"filesync/config"
	"filesync/engine"
	"filesync/fileindex"

	"github.com/egoist/mygo/ui"
)

type jobState int

const (
	jobScanning jobState = iota
	jobPreview
	jobRunning
	jobDone
)

// job is the preview and then the backup of a task.
type job struct {
	taskID string
	state  jobState
	cancel context.CancelFunc

	status  string // what the scan is doing
	scanned int
	plan    *engine.Plan
	err     error // why the scan failed

	filter      int   // the kinds of actions the preview lists
	shown       []int // indices of the actions listed
	confirmMass bool

	progress engine.Progress
	started  time.Time
	speed    speedometer
	result   *engine.Result
	logPath  string
	logErr   error
	errList  ui.ListState
}

// filters are the preview's segments, and the action kinds each lists.
var filters = []struct {
	label string
	kinds []engine.ActionKind
}{
	{"全部", nil},
	{"新增", []engine.ActionKind{engine.ActCreate, engine.ActMkdir}},
	{"更新", []engine.ActionKind{engine.ActUpdate}},
	{"删除", []engine.ActionKind{engine.ActDelete, engine.ActRmdir, engine.ActCleanup}},
	{"跳过", []engine.ActionKind{engine.ActSkip}},
}

func (j *job) applyFilter() {
	j.shown = j.shown[:0]
	kinds := filters[j.filter].kinds
	for i, a := range j.plan.Actions {
		if kinds == nil {
			j.shown = append(j.shown, i)
			continue
		}
		for _, k := range kinds {
			if a.Kind == k {
				j.shown = append(j.shown, i)
				break
			}
		}
	}
}

func (a *app) startPreview(task *config.Task) {
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{taskID: task.ID, state: jobScanning, cancel: cancel}
	a.job = j
	a.errorMsg = ""
	opts, err := a.options(task)
	go func() {
		var plan *engine.Plan
		if err == nil {
			plan, err = a.prepare(ctx, j, opts)
		}
		a.post(func() {
			if a.job != j {
				return
			}
			if ctx.Err() != nil {
				a.job = nil
				return
			}
			j.state = jobPreview
			j.plan, j.err = plan, err
			if plan != nil {
				j.applyFilter()
			}
		})
	}()
}

// options are the engine options of a task, with its server as target.
func (a *app) options(task *config.Task) (engine.Options, error) {
	sv := a.store.Server(task.Server)
	if sv == nil {
		return engine.Options{}, errors.New("这个任务的服务器已被删除，请编辑任务，重新选择备份到的目录")
	}
	loc := fileindex.Location{Addr: sv.Addr, Root: task.Root, Path: task.BackupPath()}
	opts := engine.Options{
		Source:   task.Source,
		Mode:     task.Mode,
		Excludes: task.Excludes,
		Dest:     fileindex.NewTarget(loc, sv.Token),
	}
	if task.Mode == engine.Archive {
		prefix := ""
		if task.WithDir {
			prefix = config.SourceName(task.Source)
		}
		opts.Dest = fileindex.NewArchive(loc, sv.Token, config.ArchiveName(task.Source, time.Now(), task.Compress), prefix, task.Compress)
	}
	return opts, nil
}

// prepare scans the source and the target.
func (a *app) prepare(ctx context.Context, j *job, opts engine.Options) (*engine.Plan, error) {
	a.post(func() { j.status = "正在比较本机目录和 NAS 上的目录…" })
	return engine.Prepare(ctx, opts, func(p engine.ScanProgress) {
		a.post(func() { j.scanned = p.Entries })
	})
}

func (a *app) startBackup(task *config.Task) {
	j := a.job
	ctx, cancel := context.WithCancel(context.Background())
	j.state = jobRunning
	j.cancel = cancel
	j.started = time.Now()
	j.progress = engine.Progress{Total: j.plan.Summary.Changes(), TotalBytes: j.plan.Summary.CopyBytes}
	j.speed = speedometer{}
	plan := j.plan
	logDir := a.store.LogDir()
	taskCopy := *task
	target := a.target(task.Server, task.RootName, task.BackupPath())
	release := a.keepAwake("正在备份：" + task.Name)
	go func() {
		defer release()
		log, logErr := openRunLog(logDir, &taskCopy, target, plan)
		result := engine.Execute(ctx, plan, engine.Hooks{
			Progress: func(p engine.Progress) {
				a.post(func() {
					j.progress = p
					j.speed.add(time.Now(), p.DoneBytes)
				})
			},
			Done: func(act engine.Action, err error) {
				if log != nil {
					log.action(act, err)
				}
			},
		})
		logPath := ""
		if log != nil {
			logPath = log.path
			if err := log.finish(result); err != nil && logErr == nil {
				logErr = err
			}
		}
		a.post(func() {
			j.state = jobDone
			j.result = result
			j.logPath = logPath
			j.logErr = logErr
			a.recordRun(j, &taskCopy)
			if a.quitWhenDone {
				a.closeWindow()
			}
		})
	}()
}

// recordRun adds the run to its task's history.
func (a *app) recordRun(j *job, ran *config.Task) {
	task := a.store.Task(j.taskID)
	if task == nil {
		return
	}
	r := j.result
	run := config.Run{
		Started:  r.Started,
		Duration: r.Finished.Sub(r.Started).Seconds(),
		Mode:     ran.Mode,
		Copied:   r.Created + r.Updated,
		Deleted:  r.Deleted,
		Errors:   len(r.Errors),
		Bytes:    r.Bytes,
		Log:      j.logPath,
	}
	switch {
	case r.Fatal != nil:
		run.Status = config.StatusFailed
		run.Message = r.Fatal.Error()
	case r.Canceled:
		run.Status = config.StatusCanceled
	case len(r.Errors) > 0:
		run.Status = config.StatusErrors
	default:
		run.Status = config.StatusOK
	}
	task.AddRun(run)
	a.save()
}

func (a *app) jobView(c *ui.Context) {
	j := a.job
	task := a.task()
	switch j.state {
	case jobScanning:
		a.scanningView(c, j, task)
	case jobPreview:
		a.previewView(c, j, task)
	case jobRunning:
		a.runningView(c, j, task)
	case jobDone:
		a.doneView(c, j, task)
	}
}

func (a *app) scanningView(c *ui.Context, j *job, task *config.Task) {
	t := c.Theme()
	ui.Text(c, task.Name).FontSize(22).Bold()
	ui.Column(c).Grow(1).Center().Gap(14).Children(func() {
		ui.Spinner(c).FontSize(28).Label("正在扫描")
		ui.Text(c, j.status)
		ui.Textf(c, "已扫描 %d 项", j.scanned).TextColor(t.TextMuted).FontFeatures("tnum")
		if ui.Button(c, "取消").Clicked() {
			j.cancel()
			a.job = nil
		}
	})
}

func (a *app) previewView(c *ui.Context, j *job, task *config.Task) {
	t := c.Theme()
	ui.Row(c).Gap(12).Children(func() {
		ui.Text(c, "预览："+task.Name).FontSize(22).Bold().Grow(1).Shrink(1)
		ui.Badge(c, task.Mode.String())
	})
	if j.err != nil {
		needLogin := errors.Is(j.err, fileindex.ErrAuth)
		if needLogin {
			notice(c, t.Warning, "需要登录 NAS 上的 fileindex，登录后会自动重新扫描。")
		} else {
			notice(c, t.Danger, j.err.Error())
		}
		ui.Spacer(c)
		ui.Row(c).Gap(8).Children(func() {
			if needLogin && ui.PrimaryButton(c, "登录 NAS…").Clicked() {
				a.openLogin(task.Server, func() { a.startPreview(task) })
			}
			if !needLogin && ui.PrimaryButton(c, "重新扫描").Clicked() {
				a.startPreview(task)
			}
			if ui.Button(c, "返回").Clicked() {
				a.job = nil
			}
		})
		return
	}
	p := j.plan
	s := p.Summary
	ui.Row(c).Gap(10).Wrap().Children(func() {
		stat(c, "新增", fmt.Sprint(s.Create), t.Success)
		stat(c, "更新", fmt.Sprint(s.Update), t.Accent)
		stat(c, "删除", fmt.Sprint(s.Delete), t.Danger)
		stat(c, "跳过", fmt.Sprint(s.Skip), t.Warning)
		stat(c, "需复制", formatBytes(s.CopyBytes), t.Text)
	})
	for _, w := range p.Warnings {
		notice(c, t.Warning, w)
	}

	if arc, ok := p.Options.Dest.(*fileindex.Archive); ok && s.Changes() > 0 {
		notice(c, t.Accent, "将打包为一个归档文件，保存到 "+a.target(task.Server, task.RootName, path.Join(task.Path, arc.Name)))
	}
	if s.Changes() == 0 {
		ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
			ui.Text(c, "目标已是最新").FontSize(18).Bold()
			ui.Textf(c, "源中的 %d 个文件在目标中都已存在且没有变化。", p.SourceFiles).TextColor(t.TextMuted)
		})
	} else {
		if ui.Segmented(c, &j.filter, filterLabels()...).Label("显示").Changed() {
			j.applyFilter()
		}
		ui.List(c.Key("plan"), nil, len(j.shown), func(i int) {
			act := p.Actions[j.shown[i]]
			ui.Row(c).Gap(10).Padding(5, 8).Children(func() {
				ui.Text(c, act.Kind.String()).Width(48).FontSize(12).Bold().TextColor(kindColor(t, act.Kind))
				ui.Text(c, act.Path).Grow(1).Shrink(1).SingleLine().Tooltip(act.Path)
				if act.Reason != "" {
					ui.Text(c, act.Reason).FontSize(12).TextColor(t.TextMuted).MaxWidth(260).SingleLine().Tooltip(act.Reason)
				}
				if act.Kind == engine.ActCreate || act.Kind == engine.ActUpdate {
					ui.Text(c, formatBytes(act.Size)).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum")
				}
			})
		}).Grow(1).Border(1, t.Border).Radius(t.Radius * 2).Clip().Label("计划的操作").Children(func() {
			if len(j.shown) == 0 {
				ui.Text(c, "没有这类操作。").TextColor(t.TextMuted).Padding(12)
			}
		})
	}

	ui.Row(c).Gap(8).Children(func() {
		start := ui.PrimaryButton(c, "开始备份").Disabled(s.Changes() == 0)
		if start.Clicked() {
			if p.MassDelete {
				j.confirmMass = true
			} else {
				a.startBackup(task)
			}
		}
		if ui.Button(c, "重新扫描").Clicked() {
			a.startPreview(task)
		}
		if ui.Button(c, "返回").Clicked() {
			a.job = nil
		}
	})
	if j.confirmMass {
		if ui.AlertDialog(c, &j.confirmMass, "确定要删除这么多文件吗？",
			fmt.Sprintf("完全一致模式将删除目标中 %d 个文件中的 %d 个。如果源目录是没挂载好的网络共享，请先取消并检查。", p.TargetFiles, s.Delete),
			"取消", "仍然备份") == 1 {
			a.startBackup(task)
		}
	}
}

func filterLabels() []string {
	out := make([]string, len(filters))
	for i, f := range filters {
		out[i] = f.label
	}
	return out
}

func (a *app) runningView(c *ui.Context, j *job, task *config.Task) {
	t := c.Theme()
	p := j.progress
	ui.Text(c, "正在备份："+task.Name).FontSize(22).Bold()
	ui.Column(c).Grow(1).Center().Gap(12).Children(func() {
		ui.Column(c).Width(520).MaxWidthPercent(100).Gap(10).Children(func() {
			frac := 0.0
			if p.TotalBytes > 0 {
				frac = float64(p.DoneBytes) / float64(p.TotalBytes)
			} else if p.Total > 0 {
				frac = float64(p.Done) / float64(p.Total)
			}
			ui.Progress(c, min(frac, 1)).Label("备份进度")
			ui.Row(c).Children(func() {
				ui.Textf(c, "%d / %d 项", p.Done, p.Total).FontFeatures("tnum").Grow(1)
				ui.Textf(c, "%s / %s", formatBytes(p.DoneBytes), formatBytes(p.TotalBytes)).FontFeatures("tnum")
			})
			info := "速度 " + formatBytes(int64(j.speed.rate())) + "/s"
			if eta := j.speed.eta(p.TotalBytes - p.DoneBytes); eta > 0 {
				info += " · 剩余约 " + formatDuration(eta.Seconds())
			}
			info += " · 已用 " + formatDuration(time.Since(j.started).Seconds())
			ui.Text(c, info).TextColor(t.TextMuted).FontFeatures("tnum")
			ui.Text(c, p.Current).FontSize(12).TextColor(t.TextMuted).SingleLine()
		})
		if ui.Button(c, "停止").Clicked() {
			j.cancel()
		}
	})
	c.After(time.Second) // the elapsed time
}

func (a *app) doneView(c *ui.Context, j *job, task *config.Task) {
	t := c.Theme()
	r := j.result
	title, color := "备份完成", t.Success
	switch {
	case r.Fatal != nil:
		title, color = "备份失败", t.Danger
	case r.Canceled:
		title, color = "备份已停止", t.Warning
	case len(r.Errors) > 0:
		title, color = fmt.Sprintf("备份完成，但有 %d 个错误", len(r.Errors)), t.Warning
	}
	ui.Text(c, title).FontSize(22).Bold().TextColor(color)
	if r.Fatal != nil {
		notice(c, t.Danger, r.Fatal.Error())
	}
	ui.Row(c).Gap(10).Wrap().Children(func() {
		stat(c, "新增", fmt.Sprint(r.Created), t.Success)
		stat(c, "更新", fmt.Sprint(r.Updated), t.Accent)
		stat(c, "删除", fmt.Sprint(r.Deleted), t.Danger)
		stat(c, "已复制", formatBytes(r.Bytes), t.Text)
		stat(c, "用时", formatDuration(r.Finished.Sub(r.Started).Seconds()), t.Text)
	})
	if arc, ok := j.plan.Options.Dest.(*fileindex.Archive); ok && arc.Saved() != "" {
		notice(c, t.Success, "已保存为 "+a.target(task.Server, task.RootName, arc.Saved())+"（fileindex 中路径）")
	}
	if r.Canceled {
		notice(c, t.Warning, "已复制完成的文件保留在目标中；正在复制的文件没有写入。再次备份会接着复制剩下的文件。")
	}
	if r.TimesLost > 0 {
		notice(c, t.Warning, fmt.Sprintf("目标没有保留 %d 个文件的修改时间，下次备份会再次复制它们。FTP 等挂载常有这个问题，可以在任务中调大时间容差。", r.TimesLost))
	}
	if r.Kept > 0 {
		notice(c, t.Warning, fmt.Sprintf("有 %d 个目录因为含有被排除的文件而保留。", r.Kept))
	}
	if j.logErr != nil {
		notice(c, t.Warning, "无法写入日志: "+j.logErr.Error())
	}
	if len(r.Errors) > 0 {
		ui.Text(c, "出错的文件").Bold()
		ui.List(c.Key("errors"), &j.errList, len(r.Errors), func(i int) {
			e := r.Errors[i]
			ui.Column(c).Padding(6, 10).Gap(2).Children(func() {
				ui.Text(c, e.Action.Kind.String()+"  "+e.Action.Path).SingleLine()
				ui.Text(c, e.Err.Error()).FontSize(12).TextColor(t.Danger).MaxLines(2)
			})
		}).Grow(1).Border(1, t.Border).Radius(t.Radius*2).Clip().Dividers(1, t.Border).Label("出错的文件")
	} else {
		ui.Spacer(c)
	}
	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, "完成").Clicked() {
			a.job = nil
		}
		if j.logPath != "" && ui.Button(c, "打开日志").Clicked() {
			a.openPath(j.logPath)
		}
		if sv := a.store.Server(task.Server); sv != nil && ui.Button(c, "打开 fileindex").Clicked() {
			a.openURL("http://" + sv.Addr + "/")
		}
	})
}

func stat(c *ui.Context, label, value string, color ui.Color) {
	t := c.Theme()
	ui.Column(c).Padding(10, 14).Gap(2).MinWidth(96).Radius(t.Radius*2).Border(1, t.Border).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, value).FontSize(18).Bold().TextColor(color).FontFeatures("tnum")
	})
}

func kindColor(t *ui.Theme, k engine.ActionKind) ui.Color {
	switch k {
	case engine.ActCreate, engine.ActMkdir:
		return t.Success
	case engine.ActUpdate:
		return t.Accent
	case engine.ActDelete, engine.ActRmdir, engine.ActCleanup:
		return t.Danger
	default:
		return t.Warning
	}
}

// speedometer measures the copy's rate over the last seconds.
type speedometer struct {
	samples []sample
}

type sample struct {
	at    time.Time
	bytes int64
}

func (s *speedometer) add(at time.Time, bytes int64) {
	s.samples = append(s.samples, sample{at, bytes})
	for len(s.samples) > 2 && at.Sub(s.samples[0].at) > 5*time.Second {
		s.samples = s.samples[1:]
	}
}

// rate is in bytes a second.
func (s *speedometer) rate() float64 {
	if len(s.samples) < 2 {
		return 0
	}
	first, last := s.samples[0], s.samples[len(s.samples)-1]
	d := last.at.Sub(first.at).Seconds()
	if d <= 0 {
		return 0
	}
	return float64(last.bytes-first.bytes) / d
}

func (s *speedometer) eta(left int64) time.Duration {
	r := s.rate()
	if r <= 0 || left <= 0 {
		return 0
	}
	return time.Duration(float64(left) / r * float64(time.Second))
}
