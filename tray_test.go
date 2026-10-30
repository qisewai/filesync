package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filesync/config"
	"filemanager/engine"
	"filemanager/remoteclient/fitest"
)

func TestTray(t *testing.T) {
	f := fitest.New(t)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644)
	store := signedIn(t, f)
	store.Add(&config.Task{Name: "文档", Source: src, Server: store.Servers[0].ID, Root: 1, RootName: "nas", Path: "备份"})
	h := newHarness(t, store)
	var shown []trayModel
	h.a.tray = func(m trayModel) { shown = append(shown, m) }
	h.settle()
	if len(shown) != 1 || shown[0].Status != "空闲" || shown[0].Busy || len(shown[0].Tasks) != 1 || shown[0].Tasks[0].Name != "文档" {
		t.Fatalf("tray %+v", shown)
	}
	// Frames that change nothing leave it.
	h.tt.Frame()
	h.tt.Frame()
	if len(shown) != 1 {
		t.Fatalf("shown again: %d", len(shown))
	}

	// A task chosen in the menu backs up at once, in its card.
	h.a.trayBackup(store.Tasks[0].ID)
	if h.a.place != "backup" || h.a.job == nil {
		t.Fatal("not scanning")
	}
	h.settle()
	h.want("备份完成")
	busy := false
	for _, m := range shown {
		if m.Busy && m.Status == "正在备份：文档" && m.Tasks[0].Disabled {
			busy = true
		}
	}
	if !busy || shown[len(shown)-1].Busy {
		t.Fatalf("tray while backing up %+v", shown)
	}

	// The setting of the menu is kept.
	h.a.setQuitOnClose(true)
	again, _ := config.Load(store.Dir)
	if !again.QuitOnClose {
		t.Fatal("not saved")
	}
	h.tt.Frame()
	if !shown[len(shown)-1].QuitOnClose {
		t.Fatal("menu not updated")
	}
}

func TestTrayTransfers(t *testing.T) {
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	var last trayModel
	h.a.tray = func(m trayModel) { last = m }
	tr := h.a.addTransfer("上传", "file")
	h.tt.Frame()
	if !last.Busy || last.Status != "1 个传输任务进行中" {
		t.Fatalf("tray %+v", last)
	}
	h.a.finishTransfer(tr, nil, nil)
	h.tt.Frame()
	if last.Busy || last.Status != "空闲" {
		t.Fatalf("after %+v", last)
	}
}

func TestBackupNotice(t *testing.T) {
	task := &config.Task{Name: "文档"}
	if title, body := backupNotice(task, &engine.Result{Created: 2, Bytes: 2048}); title != "备份完成：文档" || body != "上传 2 个文件（2.00 KB）" {
		t.Errorf("%q %q", title, body)
	}
	if title, _ := backupNotice(task, &engine.Result{Fatal: errors.New("x")}); title != "备份失败：文档" {
		t.Errorf("%q", title)
	}
}
