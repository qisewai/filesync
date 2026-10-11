package main

import (
	"log"

	"github.com/egoist/mygo"
)

// setUpTray puts the app in the system tray, and reports whether it is
// there: a desktop without one keeps the app in its window. quitting is
// set as the menu quits.
func setUpTray(a *app, win *mygo.Window, quitting *bool) bool {
	tray, err := mygo.NewTray(mygo.TrayOptions{Icon: trayIcon, ToolTip: "FileSync"})
	if err != nil {
		log.Printf("没有系统托盘: %v", err)
		return false
	}
	show := func() {
		win.Show()
		win.Focus()
	}
	// Chosen in the menu, an action runs with the app's state, on the
	// main thread, then the tray shows what it changed.
	do := func(fn func()) {
		win.Update(func() {
			fn()
			a.syncTray()
		})
	}
	busy := false
	var shown *trayModel // the model of the menu shown
	a.tray = func(m trayModel) {
		tray.SetToolTip("FileSync · " + m.Detail)
		if m.Busy != busy {
			busy = m.Busy
			icon := trayIcon
			if busy {
				icon = trayBusyIcon
			}
			if err := tray.SetIcon(icon, false); err != nil {
				log.Printf("托盘图标: %v", err)
			}
		}
		if shown != nil && shown.sameMenu(m) {
			return
		}
		shown = &m
		tasks := []*mygo.MenuItem{}
		for _, t := range m.Tasks {
			id := t.ID
			tasks = append(tasks, &mygo.MenuItem{Label: t.Name + "…", Disabled: t.Disabled, Click: func(*mygo.MenuItem, *mygo.Window) {
				show()
				do(func() { a.trayBackup(id) })
			}})
		}
		if len(tasks) == 0 {
			tasks = append(tasks, &mygo.MenuItem{Label: "（还没有备份任务）", Disabled: true})
		}
		tray.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
			{Label: m.Status, Disabled: true},
			mygo.Separator(),
			{Label: "显示 FileSync", Click: func(*mygo.MenuItem, *mygo.Window) { show() }},
			{Label: "备份任务", Submenu: tasks},
			{Label: "传输任务", Click: func(*mygo.MenuItem, *mygo.Window) {
				show()
				do(func() { a.showTransfers = len(a.transfers) > 0 })
			}},
			mygo.Separator(),
			{Label: "关闭窗口时最小化到托盘", Type: mygo.MenuItemCheckbox, Checked: !m.QuitOnClose, Click: func(item *mygo.MenuItem, _ *mygo.Window) {
				keep := item.Checked
				do(func() { a.setQuitOnClose(!keep) })
			}},
			{Label: "退出 FileSync", Click: func(*mygo.MenuItem, *mygo.Window) {
				// Asked first, in the window, while work goes on.
				show()
				*quitting = true
				win.Close()
			}},
		}))
	}
	a.notify = func(title, body string) {
		if !win.IsVisible() || win.IsMinimized() {
			showNotice(win, title, body)
		}
	}
	a.syncTray()
	return true
}

// showNotice shows a desktop notification, whose click shows the window.
func showNotice(win *mygo.Window, title, body string) {
	n := mygo.NewNotification(mygo.NotificationOptions{Title: title, Body: body})
	n.OnClick(func() {
		win.Show()
		win.Focus()
	})
	if err := n.Show(); err != nil {
		log.Printf("通知: %v", err)
	}
}
