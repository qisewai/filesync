package main

import (
	"github.com/egoist/mygo/plugins/terminal"
	"os"
	"testing"
	"time"
)

func TestTerminalPanelVisibility(t *testing.T) {
	_, h := tabsFixture(t)
	s := &h.a.terminalPanel
	s.starting = true // Exercise layout without starting a host shell.
	h.a.toggleTerminal()
	h.settle()
	if !s.shown {
		t.Fatal("terminal not shown")
	}
	h.click("隐藏终端")
	h.settle()
	if s.shown || !s.starting {
		t.Fatal("hiding canceled the session")
	}
	h.a.toggleTerminal()
	h.settle()
	if path := os.Getenv("FILESYNC_TERMINAL_SNAPSHOT"); path != "" {
		saveResumeSnapshot(t, h, path)
	}
	h.click("结束终端")
	h.settle()
	if s.shown || s.starting {
		t.Fatal("stop did not close session")
	}
}

func TestTerminalSessionSurvivesHide(t *testing.T) {
	if os.Getenv("FILESYNC_TERMINAL_TEST") != "1" {
		t.Skip("requires native terminal runtime")
	}
	_, h := tabsFixture(t)
	term, err := terminal.New(terminal.Options{Command: []string{"/bin/sh", "-c", "printf 'FileSync terminal ready\\n'; sleep 30"}})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	s := &h.a.terminalPanel
	s.term = term
	h.a.toggleTerminal()
	h.settle()
	time.Sleep(100 * time.Millisecond)
	h.tt.Frame()
	if path := os.Getenv("FILESYNC_TERMINAL_SNAPSHOT"); path != "" {
		saveResumeSnapshot(t, h, path)
	}
	h.a.toggleTerminal()
	h.settle()
	select {
	case <-term.Done():
		t.Fatal("hide terminated shell")
	default:
	}
	h.a.toggleTerminal()
	h.settle()
	if s.term != term {
		t.Fatal("show replaced terminal")
	}
	h.a.closeTerminal()
	select {
	case <-term.Done():
	case <-time.After(4 * time.Second):
		t.Fatal("shell not closed")
	}
}
