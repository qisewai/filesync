package main

import (
	"fmt"
	"path"
	"strings"
	"time"

	"filesync/config"
	"filesync/engine"
	"filesync/fileindex"

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
	// pickFiles asks for files, nil when canceled; it blocks.
	pickFiles func(title string) ([]string, error)
	// openPath opens a file with the system's default app, and openURL a
	// web page in the browser.
	openPath func(string)
	openURL  func(string)
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

	nas    nasBrowser
	nasGen int

	// place is what the sidebar chose: "backup", or a root as
	// "root/<server>/<id>".
	place string
	open  map[string]*bool // the sidebar's sections shown open, by server
	roots map[string][]fileindex.Root
	// rootsLoading are the servers whose roots are being read.
	rootsLoading map[string]*fileindex.Client
	tree         map[string]*directoryNode

	br      *browser // the root shown
	preview *preview
	proxy   *fileindex.LocalProxy
	recycle *recycleBrowser

	transfers     []*transfer
	transferSeq   int
	showTransfers bool
	watching      map[string]func() // stops following a server's events
	noEvents      bool              // tests: no events followed
	closed        bool

	thumbs     map[string]*thumbOf
	thumbSlots chan struct{}
	thumbExts  map[string]map[string]bool // by server
	thumbSeq   uint64
}

// draft is a task being created or edited.
type draft struct {
	id           string // "" for a new task
	name, source string
	// The target: a directory of a root of a server.
	server         string
	root           int64
	rootName, path string
	withDir        bool   // back up the source directory itself
	compress       string // of an archive

	mode                    int
	excludes                string
	nameErr, srcErr, dstErr string
}

func newApp(store *config.Store) *app {
	a := &app{
		store:       store,
		selected:    -1,
		post:        func(fn func()) { fn() },
		pickDir:     func(string, string) (string, error) { return "", nil },
		pickFiles:   func(string) ([]string, error) { return nil, nil },
		openPath:    func(string) {},
		openURL:     func(string) {},
		keepAwake:   func(string) func() { return func() {} },
		closeWindow: func() {},

		open:         map[string]*bool{},
		roots:        map[string][]fileindex.Root{},
		rootsLoading: map[string]*fileindex.Client{},
		tree:         map[string]*directoryNode{},
		watching:     map[string]func(){},
		thumbs:       map[string]*thumbOf{},
		thumbSlots:   make(chan struct{}, 4),
		thumbExts:    map[string]map[string]bool{},
	}
	if len(store.Servers) == 0 && len(store.Tasks) > 0 {
		a.place = "backup"
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

// target describes where a task backs up to.
func (a *app) target(server string, rootName, p string) string {
	sv := a.store.Server(server)
	if sv == nil {
		return "（服务器已删除）"
	}
	return sv.Title() + " · " + path.Join(rootName, p)
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
			ui.Field(c, "源目录", func() { ui.Text(c, task.Source).Selectable() })
			ui.Field(c, "备份到", func() {
				ui.Column(c).Gap(2).Children(func() {
					where := a.target(task.Server, task.RootName, task.BackupPath())
					if task.Mode == engine.Archive {
						ext := config.ArchiveExt(task.Compress)
						name := strings.TrimSuffix(config.ArchiveName(task.Source, time.Time{}, task.Compress), "-00010101-000000"+ext)
						where += "（每次生成一个 " + name + "-日期时间" + ext + "）"
					}
					ui.Text(c, where).Selectable()
					if sv := a.store.Server(task.Server); sv != nil {
						ui.Text(c, "fileindex "+sv.Addr+"；完全一致模式删除的文件会进入 fileindex 回收站").FontSize(12).TextColor(t.TextMuted)
					}
				})
			})
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
	engine.Incremental: "上传新增和修改过的文件，NAS 上多出来的文件保留不动。",
	engine.Mirror:      "让 NAS 上的目录与本机完全相同：上传新增和修改过的文件，并把本机没有的文件移到 fileindex 回收站。",
	engine.Archive:     "把整个源目录打包成一个 tar 文件保存到 NAS，每次备份生成一个新文件（文件名带日期时间），不和 NAS 上已有的内容比较。",
}

func (a *app) startEditing(task *config.Task) {
	a.editing = true
	a.errorMsg = ""
	if task == nil {
		a.draft = draft{
			name:     fmt.Sprintf("备份任务 %d", len(a.store.Tasks)+1),
			excludes: strings.Join(config.DefaultExcludes, "\n"),
			withDir:  true,
			compress: "gzip",
		}
		return
	}
	a.draft = draft{
		id: task.ID, name: task.Name, source: task.Source,
		server: task.Server, root: task.Root, rootName: task.RootName, path: task.Path, withDir: task.WithDir, compress: task.Compress,
		mode: int(task.Mode), excludes: strings.Join(task.Excludes, "\n"),
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
	if a.errorMsg != "" {
		notice(c, t.Danger, a.errorMsg)
	}
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Gap(14).MaxWidth(720).Children(func() {
			ui.Field(c, "名称", func() {
				ui.TextInput(c.Key("name"), &d.name).AutoFocus()
			}).Error(d.nameErr)
			ui.Field(c, "源目录", func() {
				ui.Row(c).Gap(8).Children(func() {
					ui.TextInput(c.Key("source"), &d.source).Placeholder("本机要备份的目录").Grow(1).Shrink(1).MinWidth(0)
					if ui.Button(c, "浏览…").Clicked() {
						a.browseSource()
					}
				})
			}).Error(d.srcErr)
			ui.Field(c, "备份到", func() {
				ui.Row(c).Gap(8).Children(func() {
					name, color := "未选择", t.TextMuted
					if d.server != "" {
						name, color = a.target(d.server, d.rootName, d.path), t.Text
					}
					ui.Text(c, name).TextColor(color).Grow(1).Shrink(1).SingleLine().Tooltip(name)
					if ui.Button(c, "选择…").Clicked() {
						a.openPicker(d.server, d.root, d.path, func(server string, root int64, rootName, p string) {
							d.server, d.root, d.rootName, d.path = server, root, rootName, p
							d.dstErr = ""
						})
					}
				})
			}).Error(d.dstErr)
			name := config.SourceName(d.source)
			label := "在目标中创建与源目录同名的文件夹"
			switch {
			case d.mode == int(engine.Archive) && name != "":
				label = "归档里包含“" + name + "”文件夹"
			case d.mode == int(engine.Archive):
				label = "归档里包含与源目录同名的文件夹"
			case name != "":
				label = "在目标中创建“" + name + "”文件夹"
			}
			ui.Column(c).Gap(4).Children(func() {
				ui.Checkbox(c.Key("with-dir"), &d.withDir, label).Disabled(name == "")
				ui.Text(c, a.landing(d)).FontSize(12).TextColor(t.TextMuted).Margin(0, 0, 0, 26)
			})
			ui.Field(c, "模式", func() {
				ui.RadioGroup(c, func() {
					ui.Radio(c, &d.mode, int(engine.Incremental), "增量备份")
					ui.Text(c, modeHelp[engine.Incremental]).FontSize(12).TextColor(t.TextMuted).Margin(0, 0, 6, 26)
					ui.Radio(c, &d.mode, int(engine.Mirror), "完全一致")
					ui.Text(c, modeHelp[engine.Mirror]).FontSize(12).TextColor(t.TextMuted).Margin(0, 0, 6, 26)
					ui.Radio(c, &d.mode, int(engine.Archive), "tar 归档")
					ui.Text(c, modeHelp[engine.Archive]).FontSize(12).TextColor(t.TextMuted).Margin(0, 0, 0, 26)
				}).Label("模式")
			})
			if d.mode == int(engine.Archive) {
				ui.Field(c, "压缩", func() {
					ui.RadioGroup(c, func() {
						ui.Radio(c, &d.compress, "gzip", "gzip（.tar.gz，推荐）")
						ui.Radio(c, &d.compress, "", "不压缩（.tar）")
						ui.Radio(c, &d.compress, "zstd", "zstd（.tar.zst，压得更小但稍慢，较旧的解压软件可能打不开）")
					}).Label("压缩")
				}).Description("多核并行压缩，不会拖慢备份。代码、文本通常能压缩一半左右；照片、视频、压缩包几乎压不动。")
			}
			ui.Field(c, "排除规则", func() {
				ui.TextArea(c.Key("excludes"), &d.excludes).Lines(4, 10).Font("monospace")
			}).Description("每行一条。*.tmp 匹配任意位置的文件名；以 / 结尾只匹配目录，如 cache/；含 / 的规则从根目录匹配，如 /build/out。被排除的文件不会上传，NAS 上的也不会被删除。")
		})
	})
	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, "保存").Clicked() {
			a.saveDraft()
		}
		if ui.Button(c, "取消").Clicked() || c.Shortcut(0, ui.KeyEscape) && !a.nas.open {
			a.editing = false
			a.errorMsg = ""
		}
	})
}

// landing says where the draft's files go.
func (a *app) landing(d *draft) string {
	src := config.SourceName(d.source)
	if src == "" {
		src = "源目录"
	}
	if d.server == "" {
		if d.withDir {
			return "源目录本身会备份为目标中的一个文件夹。"
		}
		return "只备份源目录里的内容，直接放在目标目录中。"
	}
	if d.mode == int(engine.Archive) {
		where := a.target(d.server, d.rootName, path.Join(d.path, config.ArchiveName(d.source, time.Now(), d.compress)))
		if d.withDir && config.SourceName(d.source) != "" {
			return "每次备份生成一个归档，如 " + where + "，解开后得到“" + src + "”文件夹"
		}
		return "每次备份生成一个归档，如 " + where + "，解开后直接是“" + src + "”里的内容"
	}
	where := a.target(d.server, d.rootName, config.BackupPath(d.path, d.source, d.withDir))
	if d.withDir && config.SourceName(d.source) != "" {
		return "“" + src + "”会备份为 " + where
	}
	return "“" + src + "”里的内容会直接放在 " + where + " 中"
}

func (a *app) browseSource() {
	start := a.draft.source
	go func() {
		dir, err := a.pickDir("选择源目录", start)
		a.post(func() {
			if err != nil {
				a.errorMsg = err.Error()
			} else if dir != "" {
				a.draft.source = dir
			}
		})
	}()
}

func (a *app) saveDraft() {
	d := &a.draft
	d.name = strings.TrimSpace(d.name)
	d.source = strings.TrimSpace(d.source)
	d.nameErr, d.srcErr, d.dstErr = "", "", ""
	if d.name == "" {
		d.nameErr = "请输入名称。"
	}
	if d.source == "" {
		d.srcErr = "请选择源目录。"
	}
	if d.server == "" || a.store.Server(d.server) == nil {
		d.dstErr = "请选择 NAS 上备份到的目录。"
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
	task.Name, task.Source = d.name, d.source
	task.Server, task.Root, task.RootName, task.Path = d.server, d.root, d.rootName, d.path
	task.WithDir = d.withDir
	task.Compress = ""
	if engine.Mode(d.mode) == engine.Archive {
		task.Compress = d.compress
	}
	task.Mode = engine.Mode(d.mode)
	task.Excludes = excludes
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
	if a.job != nil && a.job.state == jobRunning || a.activeTransfers() > 0 {
		a.askQuit = true
		return false
	}
	if a.job != nil && a.job.cancel != nil {
		a.job.cancel()
	}
	a.stopBackground()
	return true
}

func (a *app) quit() {
	// The window closes once the backup has stopped and its run is recorded.
	a.quitWhenDone = true
	if a.job != nil && a.job.cancel != nil {
		a.job.cancel()
	}
	for _, tr := range a.transfers {
		if tr.running() && tr.cancel != nil {
			tr.cancel()
		}
	}
	a.maybeClose()
}

func (a *app) maybeClose() {
	if a.quitWhenDone && !a.busy() && a.activeTransfers() == 0 {
		a.stopBackground()
		a.closeWindow()
	}
}

func (a *app) stopBackground() {
	a.closed = true
	a.closePreview()
	if a.proxy != nil {
		a.proxy.Close()
	}
	if a.recycle != nil && a.recycle.cancel != nil {
		a.recycle.cancel()
	}
	if a.br != nil && a.br.cancel != nil {
		a.br.cancel()
	}
	if a.br != nil && a.br.thumbCancel != nil {
		a.br.thumbCancel()
	}
	for server := range a.watching {
		a.unwatch(server)
	}
	for _, tr := range a.transfers {
		if tr.stop != nil {
			tr.stop()
		}
	}
}

func (a *app) save() {
	if err := a.store.Save(); err != nil {
		a.errorMsg = "无法保存设置: " + err.Error()
	}
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
	s := fmt.Sprintf("上传 %d 个文件（%s）", r.Copied, formatBytes(r.Bytes))
	if r.Deleted > 0 {
		s += fmt.Sprintf("，删除 %d 个", r.Deleted)
	}
	return s + "，用时 " + formatDuration(r.Duration)
}
