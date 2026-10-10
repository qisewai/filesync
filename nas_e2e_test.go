package main

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filesync/config"
)

// TestNASE2E goes through the NAS dialog against a real fileindex server:
//
//	FILESYNC_E2E_ADDR=127.0.0.1:18090 FILESYNC_E2E_PASSWORD_FILE=… [FILESYNC_SHOTS=dir] go test -run NASE2E .
func TestNASE2E(t *testing.T) {
	addr := os.Getenv("FILESYNC_E2E_ADDR")
	if addr == "" {
		t.Skip("set FILESYNC_E2E_ADDR")
	}
	pw, _ := os.ReadFile(os.Getenv("FILESYNC_E2E_PASSWORD_FILE"))
	shots := os.Getenv("FILESYNC_SHOTS")
	shot := func(h *harness, name string) {
		if shots == "" {
			return
		}
		h.tt.Frame()
		f, _ := os.Create(filepath.Join(shots, name+".png"))
		png.Encode(f, h.tt.Image())
		f.Close()
	}
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "笔记.txt"), []byte("hello nas"), 0o644)
	store := &config.Store{Dir: t.TempDir()}
	h := newHarness(t, store)
	waitNAS := func() {
		deadline := time.Now().Add(10 * time.Second)
		for h.a.nas.busy != "" && time.Now().Before(deadline) {
			select {
			case fn := <-h.queue:
				fn()
			default:
				time.Sleep(time.Millisecond)
			}
		}
		h.tt.Frame()
	}

	h.click("新建任务")
	h.click("源目录")
	h.tt.Type(src)
	h.click("NAS…")
	h.click("NAS 地址")
	h.tt.Type(addr)
	h.click("连接")
	waitNAS()
	h.want("请输入 fileindex 的管理员密码。这里只保存登录状态，不保存密码。")
	shot(h, "nas-1-login")
	h.click("密码")
	h.tt.Type(strings.TrimSpace(string(pw)))
	h.click("登录")
	waitNAS()
	h.want("fileindex 的索引目录")
	shot(h, "nas-2-roots")
	h.click("share")
	waitNAS()
	shot(h, "nas-3-dirs")
	h.click("新建文件夹")
	h.tt.Type("ui测试")
	h.click("创建")
	waitNAS()
	h.want("没有子目录。可以选择这个目录，或新建一个。")
	shot(h, "nas-4-new-folder")
	h.click("选择此目录")
	h.tt.Frame()
	if !strings.HasPrefix(h.a.draft.target, "fileindex://"+addr+"/1/ui测试") {
		t.Fatalf("target %q", h.a.draft.target)
	}
	shot(h, "nas-5-editor")
	h.click("保存")
	h.want("NAS " + addr + " · share/ui测试")
	if store.Sessions[addr] == "" {
		t.Fatal("session not kept")
	}
	shot(h, "nas-6-detail")
	h.click("预览并备份…")
	h.settle()
	h.want("笔记.txt")
	h.click("开始备份")
	h.settle()
	h.want("备份完成")
	shot(h, "nas-7-done")
}
