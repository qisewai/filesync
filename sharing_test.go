package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filemanager/api"
	"filemanager/sharing"
	"filesync/config"
	"github.com/egoist/mygo/ui"
)

func TestPeerInvitationBrowseAndDownload(t *testing.T) {
	root := t.TempDir()
	localWrite(t, root, "共享资料/中文.txt", 12)
	cfg, err := sharing.NewConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Name = "另一台电脑"
	cfg.Listen = "127.0.0.1:0"
	cfg.Roots = []api.Root{{ID: "1", Name: "共享磁盘", Path: root}}
	configPath := filepath.Join(t.TempDir(), "sharing.json")
	if err := sharing.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { err := sharing.Run(ctx, configPath, pw); pw.CloseWithError(err); done <- err }()
	var ready sharing.Ready
	if err := json.NewDecoder(pr).Decode(&ready); err != nil {
		cancel()
		t.Fatal(err)
	}
	pr.Close()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("peer did not stop")
		}
	})
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.ensureSharing()
	h.a.sharing.invitation, err = cfg.Invitation(ready.Address)
	if err != nil {
		t.Fatal(err)
	}
	h.a.connectPeer()
	deadline := time.After(10 * time.Second)
	for h.a.sharing.connecting {
		select {
		case fn := <-h.queue:
			fn()
			h.tt.Frame()
		case <-deadline:
			t.Fatal("pairing did not finish")
		}
	}
	h.settle()
	if len(h.a.store.Servers) != 1 {
		t.Fatal("peer not saved", h.a.sharing.err)
	}
	sv := h.a.store.Servers[0]
	if !h.a.serverConnected(sv.ID) {
		t.Fatal("peer not connected", h.a.serverErr[sv.ID])
	}
	h.a.goPlace(rootPlace(sv.ID, 1))
	h.settle()
	h.want("共享资料")
	h.treeArrow("文件夹 共享磁盘", 1)
	h.click("文件夹 共享资料")
	h.want("中文.txt")
	h.clickPage("中文.txt")
	h.tt.Key(ui.Ctrl, ui.KeyC)
	h.settle()
	dst := t.TempDir()
	h.a.openLocal(dst)
	h.settle()
	h.tt.Key(ui.Ctrl, ui.KeyV)
	h.waitStarted(1)
	h.waitTransfers()
	if b, err := os.ReadFile(filepath.Join(dst, "中文.txt")); err != nil || len(b) != 12 {
		t.Fatal("peer paste download", err)
	}
	// A peer must not display NAS-only administration controls.
	h.a.goPlace(rootPlace(sv.ID, 1))
	h.settle()
	for _, text := range h.tt.Texts() {
		if text == "管理 "+sv.Title() {
			t.Fatal("NAS management shown for peer")
		}
	}
	h.a.place = "settings"
	h.settle()
	h.want("设备共享")
	if out := os.Getenv("FILESYNC_SHARING_SNAPSHOT"); out != "" {
		h.tt.SetSize(1100, 1500)
		h.settle()
		h.tt.Advance(2 * time.Second)
		saveResumeSnapshot(t, h, out)
	}
}
