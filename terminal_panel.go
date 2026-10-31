package main

import (
	"fmt"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
	"sync"
)

var iconTerminal = icon(`<rect x="3" y="4" width="18" height="16" rx="2"/><path d="m7 8 4 4-4 4m6 0h4"/>`)

type terminalLaunch struct {
	sync.Mutex
	canceled bool
	term     *terminal.Terminal
}

func (l *terminalLaunch) close() {
	l.Lock()
	defer l.Unlock()
	l.canceled = true
	if l.term != nil {
		l.term.Close()
	}
}

type terminalPanelState struct {
	launch                    *terminalLaunch
	term                      *terminal.Terminal
	shown, starting, disposed bool
	height                    float32
	generation                uint64
	err                       string
	focus                     ui.Handle
}

func (a *app) toggleTerminal() {
	s := &a.terminalPanel
	s.shown = !s.shown
	if s.shown {
		s.focus.Focus()
		if s.term == nil && !s.starting {
			a.startTerminal()
		}
	} else {
		s.focus.CancelFocus()
	}
}

func (a *app) startTerminal() {
	s := &a.terminalPanel
	if s.disposed || s.starting {
		return
	}
	s.starting, s.err = true, ""
	s.generation++
	generation := s.generation
	dir := ""
	if a.onLocal() && a.local != nil {
		dir = a.local.dir
	}
	launch := &terminalLaunch{}
	s.launch = launch
	post := a.post
	go func() {
		term, err := terminal.New(terminal.Options{Dir: dir, Scrollback: 4 << 20})
		launch.Lock()
		if launch.canceled {
			if term != nil {
				term.Close()
			}
			launch.Unlock()
			return
		}
		launch.term = term
		launch.Unlock()
		post(func() {
			if s.disposed || s.generation != generation {
				if term != nil {
					term.Close()
				}
				return
			}
			s.starting = false
			if err != nil {
				s.err = fmt.Sprintf("无法启动终端：%v", err)
				return
			}
			s.term = term
		})
	}()
}

func (a *app) closeTerminal() {
	s := &a.terminalPanel
	s.generation++
	if s.launch != nil {
		s.launch.close()
		s.launch = nil
	}
	s.starting = false
	s.focus.CancelFocus()
	if s.term != nil {
		s.term.Close()
		s.term = nil
	}
}

func (a *app) terminalView(c *ui.Context) {
	s := &a.terminalPanel
	if s.height == 0 {
		s.height = 240
	}
	box := ui.Column(c.Key("terminal-panel")).Shrink(0).MinHeight(0).Clip()
	target := float32(0)
	if s.shown {
		target = 1
	}
	progress := box.Animate("visibility", target, panelMotion)
	box.Height(s.height * progress)
	if progress <= 0 {
		return
	}
	box.Children(func() {
		grip := ui.Box(c.Key("terminal-resize")).Height(5).Shrink(0).Background(c.Theme().Border).Cursor(ui.CursorResizeNS).Focusable().Label("调整终端高度")
		change := func(d float32) { s.height = min(max(s.height+d, 120), 480) }
		_, dy, held := grip.Dragged()
		if held {
			change(-dy)
		}
		grip.OnShortcut(0, ui.KeyUp, func() { change(10) }).OnShortcut(0, ui.KeyDown, func() { change(-10) })
		ui.Row(c).Height(32).Shrink(0).Padding(0, 8).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconTerminal).FontSize(16)
			ui.Text(c, "本地终端").FontSize(12)
			ui.Spacer(c)
			iconButton(c, "restart-terminal", iconTerminal, "重启终端", "结束当前会话并重新启动", s.starting, func() { a.closeTerminal(); a.startTerminal(); s.focus.Focus() })
			iconButton(c, "stop-terminal", iconStop, "结束终端", "结束会话并关闭面板", false, func() { a.closeTerminal(); s.shown = false })
			iconButton(c, "hide-terminal", iconClose, "隐藏终端", "隐藏终端（保留会话）", false, a.toggleTerminal)
		})
		switch {
		case s.err != "":
			ui.Text(c, s.err).TextColor(c.Theme().Danger)
		case s.term != nil:
			terminal.View(c.Key("terminal-view"), s.term).Fill().Bind(&s.focus).Disabled(!s.shown)
		default:
			ui.Text(c, "正在启动终端…")
		}
	})
}
