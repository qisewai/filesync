package main

import (
	_ "embed"
	"fmt"
	"slices"

	"filesync/config"
)

// The icons of the tray: idle, and busy with a backup or a transfer.
var (
	//go:embed assets/tray.png
	trayIcon []byte
	//go:embed assets/tray-busy.png
	trayBusyIcon []byte
)

// trayModel is what the tray shows: the app's state, and the tasks its
// menu backs up.
type trayModel struct {
	// Status heads the menu, and Detail, with the progress, is the tip of
	// the icon: the menu, rebuilt only as the status changes, stays open.
	Status, Detail string
	Busy           bool
	Tasks          []trayTask
	// QuitOnClose is the setting the menu checks.
	QuitOnClose bool
}

type trayTask struct {
	ID, Name string
	Disabled bool
}

// sameMenu reports whether o's menu is m's.
func (m trayModel) sameMenu(o trayModel) bool {
	return m.Status == o.Status && m.QuitOnClose == o.QuitOnClose && slices.Equal(m.Tasks, o.Tasks)
}

func (m trayModel) equal(o trayModel) bool {
	return m.Status == o.Status && m.Detail == o.Detail && m.Busy == o.Busy && m.QuitOnClose == o.QuitOnClose && slices.Equal(m.Tasks, o.Tasks)
}

// trayState is the tray's model of the app as it is.
func (a *app) trayState() trayModel {
	m := trayModel{Status: "空闲", QuitOnClose: a.store.QuitOnClose}
	j := a.job
	switch {
	case j != nil && j.state == jobRunning:
		frac := 0.0
		if p := j.progress; p.TotalBytes > 0 {
			frac = float64(p.DoneBytes) / float64(p.TotalBytes)
		} else if p.Total > 0 {
			frac = float64(p.Done) / float64(p.Total)
		}
		m.Status = "正在备份：" + a.jobName()
		m.Detail = fmt.Sprintf("%s · %d%%", m.Status, int(min(frac, 1)*100))
		m.Busy = true
	case j != nil && j.state == jobScanning:
		m.Status = "正在扫描：" + a.jobName()
		m.Busy = true
	}
	if n := a.activeTransfers(); n > 0 {
		if m.Busy {
			m.Status += fmt.Sprintf("，%d 个传输", n)
		} else {
			m.Status = fmt.Sprintf("%d 个传输任务进行中", n)
		}
		m.Busy = true
	}
	if m.Detail == "" {
		m.Detail = m.Status
	}
	for _, t := range a.store.Tasks {
		m.Tasks = append(m.Tasks, trayTask{ID: t.ID, Name: t.Name, Disabled: a.busy()})
	}
	return m
}

func (a *app) jobName() string {
	if t := a.store.Task(a.job.taskID); t != nil {
		return t.Name
	}
	return "备份"
}

// syncTray shows the app's state in the tray, when it changed.
func (a *app) syncTray() {
	if a.tray == nil {
		return
	}
	m := a.trayState()
	if a.trayShown != nil && a.trayShown.equal(m) {
		return
	}
	a.trayShown = &m
	a.tray(m)
}

// trayBackup backs up a task chosen in the tray's menu, at once, as its
// card does.
func (a *app) trayBackup(id string) {
	i := slices.IndexFunc(a.store.Tasks, func(t *config.Task) bool { return t.ID == id })
	a.place = "backup"
	if i < 0 || a.busy() {
		return
	}
	a.editing = false
	a.quickBackup(a.store.Tasks[i])
}

// setQuitOnClose changes whether closing the window quits.
func (a *app) setQuitOnClose(quit bool) {
	a.store.QuitOnClose = quit
	a.save()
}
