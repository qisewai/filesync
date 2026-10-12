package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"filesync/config"
	"filesync/engine"
	"filesync/fileindex"

	"github.com/egoist/mygo/ui"
)

// transfer is an upload, a download, or a copy or move the server runs.
type transfer struct {
	id       int
	kind     string // 上传 | 下载 | 打开 | 复制 | 移动
	title    string
	state    string // running | done | failed | canceled | interrupted
	err      string
	note     string   // server completion details, including skipped items
	errors   []string // the files that failed
	cancel   func()
	started  time.Time
	finished time.Time

	done, total int64 // bytes, total 0 when unknown
	files       int
	current     string
	localFile   string // one complete downloaded file, available to reopen
	localDir    string // download destination or the system-open cache directory
	folderReady bool   // the worker has created the cache directory, or the user picked it

	// A task of the server, followed by its events.
	server            string
	peer              string // the other server of a copy between two
	task              int64
	scanRoot, scanJob int64
	// after runs on the main thread once it ends well, as a refresh.
	after func()
	stop  context.CancelFunc
}

func (t *transfer) running() bool { return t.state == "running" }

// addTransfer starts tracking a transfer, shown in the panel.
func (a *app) addTransfer(kind, title string) *transfer {
	a.transferSeq++
	t := &transfer{id: a.transferSeq, kind: kind, title: title, state: "running", started: time.Now()}
	a.transfers = append([]*transfer{t}, a.transfers...)
	a.saveTransferHistory()
	return t
}

// finish ends a transfer, on the main thread.
func (a *app) finishTransfer(t *transfer, err error, failed []string) {
	if t.stop != nil {
		t.stop()
		t.stop = nil
	}
	switch {
	case t.state == "canceled":
	case errors.Is(err, context.Canceled):
		t.state = "canceled"
	case err != nil:
		t.state, t.err = "failed", err.Error()
	case len(failed) > 0:
		t.state, t.err = "failed", fmt.Sprintf("%d 项没有成功", len(failed))
	default:
		t.state = "done"
		t.folderReady = t.localDir != ""
		if t.after != nil {
			t.after()
		}
	}
	t.errors = failed
	t.current = ""
	t.finished = time.Now()
	a.saveTransferHistory()
	a.maybeClose()
}

func (a *app) applyTask(tr *transfer, task *fileindex.Task) {
	if !tr.running() {
		return
	}
	tr.done, tr.total, tr.current = task.DoneBytes, task.TotalBytes, task.Current
	if !task.Finished() {
		return
	}
	tr.note = task.Message
	var err error
	switch task.Status {
	case "failed":
		message := task.Message
		if message == "" {
			message = "服务器任务失败"
		}
		err = errors.New(message)
	case "canceled":
		err = context.Canceled
	}
	a.finishTransfer(tr, err, nil)
}

func (a *app) trackServerTask(c *fileindex.Client, tr *transfer, task *fileindex.Task) {
	a.saveTransferHistory()
	a.applyTask(tr, task)
	if !tr.running() {
		return
	}
	ctx, stop := context.WithCancel(context.Background())
	tr.stop = stop
	tr.cancel = func() {
		go func() {
			req, cancel := context.WithTimeout(ctx, requestTimeout)
			defer cancel()
			err := c.CancelTask(req, task.ID)
			a.post(func() {
				if tr.running() && err != nil {
					tr.current = "取消失败：" + err.Error()
					if a.quitWhenDone {
						a.finishTransfer(tr, fmt.Errorf("无法请求服务器取消任务，任务可能继续运行：%w", err), nil)
					}
				}
			})
		}()
	}
	if a.quitWhenDone {
		tr.cancel()
	}
	// Events provide immediate progress; snapshots also cover events dropped
	// before the POST returned or during a connection outage.
	go func() {
		for {
			req, cancel := context.WithTimeout(ctx, requestTimeout)
			tasks, err := c.Tasks(req)
			cancel()
			if err == nil {
				a.post(func() {
					if a.closed {
						return
					}
					for i := range tasks {
						if tasks[i].ID == tr.task {
							a.applyTask(tr, &tasks[i])
							return
						}
					}
				})
			} else if errors.Is(err, fileindex.ErrAuth) {
				a.post(func() {
					if tr.running() {
						a.finishTransfer(tr, err, nil)
					}
				})
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}()
}

func (a *app) activeTransfers() int {
	n := 0
	for _, t := range a.transfers {
		if t.running() {
			n++
		}
	}
	return n
}

func (a *app) serverHasTransfers(id string) bool {
	for _, tr := range a.transfers {
		if (tr.server == id || tr.peer == id) && tr.running() {
			return true
		}
	}
	return false
}

func (a *app) transferRow(c *ui.Context, tr *transfer) {
	t := c.Theme()
	ui.Row(c.Key(tr.id)).Gap(10).Padding(4, 2).Children(func() {
		kindTip := tr.kind
		if tr.kind == "下载并打开" {
			kindTip = "下载到本机缓存后，用系统程序打开"
		}
		ui.Row(c.Key("kind")).Size(30, 30).Shrink(0).Center().Radius(t.Radius).
			Background(t.Accent.Alpha(0.08)).Label(tr.kind).Tooltip(kindTip).Children(func() {
			ui.Icon(c, transferIcon(tr.kind)).FontSize(18).TextColor(t.Accent)
		})
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(3).Children(func() {
			ui.Text(c, tr.title).SingleLine().Tooltip(tr.title)
			switch tr.state {
			case "running":
				frac := -1.0
				if tr.total > 0 {
					frac = min(float64(tr.done)/float64(tr.total), 1)
				}
				ui.Progress(c, frac).Label(tr.title)
				info := formatBytes(tr.done)
				if tr.scanRoot > 0 {
					info = fmt.Sprintf("已扫描 %d 项", tr.files)
				}
				if tr.total > 0 {
					info += " / " + formatBytes(tr.total)
				}
				if s := time.Since(tr.started).Seconds(); s > 1 && tr.done > 0 {
					info += " · " + formatBytes(int64(float64(tr.done)/s)) + "/s"
				}
				if tr.current != "" {
					info += " · " + tr.current
				}
				rolling(ui.Text(c, info).FontSize(12).TextColor(t.TextMuted).SingleLine().FontFeatures("tnum"))
			case "done":
				at := tr.finished
				if at.IsZero() {
					at = tr.started
				}
				message := "完成 · " + at.Local().Format("2006-01-02 15:04")
				color := t.Success
				if tr.note != "" {
					message += " · " + tr.note
					color = t.Warning
				}
				ui.Text(c, message).FontSize(12).TextColor(color).MaxLines(2).Tooltip(message)
			case "canceled":
				ui.Text(c, "已取消 · "+tr.started.Local().Format("2006-01-02 15:04")).FontSize(12).TextColor(t.Warning)
			case "interrupted":
				ui.Text(c, "上次未完成 · "+tr.started.Local().Format("2006-01-02 15:04")).FontSize(12).TextColor(t.Warning).Tooltip(tr.err)
			default:
				msg := tr.err
				if len(tr.errors) > 0 {
					msg += "：" + tr.errors[0]
				}
				ui.Text(c, msg+" · "+tr.started.Local().Format("2006-01-02 15:04")).FontSize(12).TextColor(t.Danger).MaxLines(2).Tooltip(msg)
			}
		})
		ui.Row(c.Key("actions")).Shrink(0).Gap(4).AlignItems(ui.Center).Children(func() {
			if tr.localFile != "" && tr.state == "done" {
				iconButton(c, "open-file", iconOpenOut, "打开下载文件", "用系统程序打开："+tr.localFile, false, func() { a.openTransferFile(tr) }).Size(30, 30)
			}
			if tr.localDir != "" {
				iconButton(c, "open-folder", iconFolder, "打开下载所在目录", "打开下载所在目录："+tr.localDir, !tr.folderReady, func() {
					a.openPath(tr.localDir)
				}).Size(30, 30)
			}
			if tr.running() && tr.cancel != nil {
				iconButton(c, "cancel", iconStop, "取消", "取消任务", false, tr.cancel).Size(30, 30)
			}
			removeTip := "删除任务记录，保留下载文件"
			if tr.running() {
				removeTip = "请先取消任务，再删除记录"
			}
			iconButton(c, "remove", iconTrash, "删除任务记录", removeTip, tr.running(), func() {
				a.removeTransfer(tr)
			}).Size(30, 30)
		})
	})
}

func transferIcon(kind string) *ui.SVG {
	switch kind {
	case "上传":
		return iconUpload
	case "下载":
		return iconSave
	case "下载并打开":
		return iconSaveOpen
	case "打开":
		return iconOpenOut
	case "复制":
		return iconCopy
	case "移动":
		return iconMove
	case "解压":
		return iconArchive
	case "删除", "彻底删除":
		return iconTrash
	case "恢复":
		return iconRetry
	case "扫描", "查重":
		return iconSearch
	case "校验":
		return iconCheck
	default:
		return iconTasks
	}
}

// removeTransfer removes a finished record, keeping its downloaded files and
// leaving active tasks tracked for cancellation, progress and quit handling.
func (a *app) removeTransfer(tr *transfer) {
	if tr.running() {
		return
	}
	for i, item := range a.transfers {
		if item == tr {
			copy(a.transfers[i:], a.transfers[i+1:])
			a.transfers[len(a.transfers)-1] = nil
			a.transfers = a.transfers[:len(a.transfers)-1]
			a.saveTransferHistory()
			return
		}
	}
}

func (a *app) clearTransfers() {
	kept := a.transfers[:0]
	for _, tr := range a.transfers {
		if tr.running() {
			kept = append(kept, tr)
		}
	}
	for i := len(kept); i < len(a.transfers); i++ {
		a.transfers[i] = nil
	}
	a.transfers = kept
	a.saveTransferHistory()
}

// watch follows the events of a signed-in server while the app runs: the
// progress of its tasks, and changes of the files shown.
func (a *app) watch(sv *config.Server) {
	if a.watching[sv.ID] != nil || sv.Token == "" || a.noEvents {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.watching[sv.ID] = cancel
	c := fileindex.New(sv.Addr, sv.Token)
	id := sv.ID
	go func() {
		for delay := time.Second; ctx.Err() == nil; delay = min(delay*2, time.Minute) {
			start := time.Now()
			err := c.Events(ctx, func(e fileindex.Event) {
				a.post(func() {
					if !a.closed && ctx.Err() == nil {
						a.onEvent(id, e)
					}
				})
			})
			if errors.Is(err, fileindex.ErrAuth) {
				a.post(func() {
					if sv.Token == c.Token && sv.Addr == c.Addr {
						a.signedOut(sv)
					}
				})
				return
			}
			if time.Since(start) > time.Minute {
				delay = time.Second // it ran a while: the server is back
			}
			select {
			case <-ctx.Done():
			case <-time.After(delay):
			}
		}
	}()
}

func (a *app) unwatch(serverID string) {
	if cancel := a.watching[serverID]; cancel != nil {
		cancel()
		delete(a.watching, serverID)
	}
}

func (a *app) onEvent(server string, e fileindex.Event) {
	switch e.Type {
	case "job":
		a.applyScan(server, e.Job, e.Current)
	case "dupes":
		a.adminRefreshSoon(server)
	case "task":
		if m := a.admin; m != nil && m.server == server && e.Task != nil && e.Task.Kind == "dupes" {
			found := false
			for i, t := range m.tasks {
				if t.ID == e.Task.ID {
					m.tasks[i] = *e.Task
					found = true
					break
				}
			}
			if !found {
				m.tasks = append(m.tasks, *e.Task)
			}
			if e.Task.Finished() {
				a.adminRefreshSoon(server)
			}
		}
		if e.Task == nil {
			return
		}
		for _, tr := range a.transfers {
			if tr.server != server || tr.task != e.Task.ID || !tr.running() {
				continue
			}
			a.applyTask(tr, e.Task)
		}
	case "trash":
		a.trashRefreshSoon(server)
	case "fs":
		a.adminRefreshSoon(server)
		a.invalidateTree(server, e.Root)
		if b := a.br; b != nil && b.server == server && b.root.ID == e.Root {
			a.refreshSoon()
		}
	}
}

// hooks report the progress of an upload to a transfer.
func hooks(a *app, tr *transfer) engine.Hooks {
	return engine.Hooks{Progress: func(p engine.Progress) {
		a.post(func() {
			tr.done, tr.total, tr.current = p.DoneBytes, p.TotalBytes, p.Current
			tr.files = p.Done
		})
	}}
}

func (a *app) recordOpenedFile(server, name, file string) {
	tr := a.addTransfer("打开", name)
	tr.server = server
	tr.localFile = file
	tr.localDir = filepath.Dir(file)
	a.finishTransfer(tr, nil, nil)
}
func (a *app) openTransferFile(tr *transfer) {
	file, name, server := tr.localFile, tr.title, tr.server
	go func() {
		info, err := os.Stat(file)
		a.post(func() {
			if a.closed {
				return
			}
			if err != nil {
				a.errorMsg = "无法打开下载文件：" + err.Error()
				return
			}
			if !info.Mode().IsRegular() {
				a.errorMsg = "下载文件已不存在或不是普通文件"
				return
			}
			a.recordOpenedFile(server, name, file)
			a.openPath(file)
		})
	}()
}
