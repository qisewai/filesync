package main

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"filesync/config"
)

func TestTransferRecordActionsKeepFilesAndTrackActiveTasks(t *testing.T) {
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	dir := t.TempDir()
	file := filepath.Join(dir, "报告.pdf")
	if err := os.WriteFile(file, []byte("downloaded file"), 0600); err != nil {
		t.Fatal(err)
	}
	h.a.openLocal(dir)
	h.settle()
	download := h.a.addTransfer("下载并打开", "报告.pdf")
	download.localDir = dir
	h.a.finishTransfer(download, nil, nil)
	active := h.a.addTransfer("上传", "正在上传的资料.zip")
	active.done, active.total = 40, 100
	cancelCalls := 0
	active.cancel = func() { cancelCalls++; h.a.finishTransfer(active, context.Canceled, nil) }
	opened := ""
	h.a.openPath = func(p string) { opened = p }
	h.tt.Frame()
	h.click("传输任务")
	h.click("打开下载所在目录")
	if opened != dir {
		t.Fatalf("opened %q, want %q", opened, dir)
	}
	// The first remove button belongs to the running task and must be disabled.
	h.click("删除任务记录")
	if len(h.a.transfers) != 2 || cancelCalls != 0 {
		t.Fatal("removed/canceled active task")
	}
	h.click("取消")
	if cancelCalls != 1 || active.state != "canceled" {
		t.Fatalf("cancel %d %s", cancelCalls, active.state)
	}
	h.click("删除任务记录")
	if len(h.a.transfers) != 1 || h.a.transfers[0] != download {
		t.Fatal("removed the wrong record")
	}
	h.tt.Frame()
	h.tt.Frame()
	h.click("打开下载所在目录")
	h.click("删除任务记录")
	h.tt.Frame()
	h.tt.Frame()
	if len(h.a.transfers) != 0 || !h.a.showTransfers || !h.tt.HasText("暂无传输记录") {
		t.Fatal("empty transfer history unavailable")
	}
	body, err := os.ReadFile(file)
	if err != nil || string(body) != "downloaded file" {
		t.Fatalf("record removal changed file %q %v", body, err)
	}
}

func TestTransferFolderWaitsForCacheAndFailedStatus(t *testing.T) {
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.openLocal(t.TempDir())
	h.settle()
	tr := h.a.addTransfer("下载并打开", "待缓存的文件.docx")
	tr.localDir = filepath.Join(t.TempDir(), "cache")
	opened := ""
	h.a.openPath = func(p string) { opened = p }
	h.tt.Frame()
	h.click("传输任务")
	h.click("打开下载所在目录")
	if opened != "" {
		t.Fatal("opened cache before it existed")
	}
	if err := os.MkdirAll(tr.localDir, 0700); err != nil {
		t.Fatal(err)
	}
	h.a.finishTransfer(tr, nil, nil)
	h.tt.Frame()
	h.click("打开下载所在目录")
	if opened != tr.localDir {
		t.Fatal("completed cache folder unavailable")
	}
	h.a.showTransfers = false
	failed := h.a.addTransfer("下载", "连接失败的文件.zip")
	h.a.finishTransfer(failed, os.ErrNotExist, nil)
	h.tt.Frame()
	h.want("1 个传输失败")
}

func TestTransferPanelIconsRenderAcrossWidths(t *testing.T) {
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.openLocal(t.TempDir())
	h.settle()
	download := h.a.addTransfer("下载并打开", "季度汇总报告.docx")
	download.localDir = "/tmp/filesync/open/季度汇总报告"
	h.a.finishTransfer(download, nil, nil)
	archive := h.a.addTransfer("解压", "照片备份.7z → 图片")
	h.a.finishTransfer(archive, nil, nil)
	upload := h.a.addTransfer("上传", "项目资料 → NAS")
	upload.done, upload.total = 40<<20, 100<<20
	upload.cancel = func() {}
	h.tt.Frame()
	h.click("传输任务")
	h.tt.Frame()
	h.tt.Frame()
	if out := os.Getenv("FILESYNC_TRANSFERS_SNAPSHOT"); out != "" {
		file, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(file, h.tt.Image())
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Rebuild the same model in a narrower window, then remove finished rows.
	h.tt.SetSize(640, 480)
	h.tt.Frame()
	h.a.removeTransfer(download)
	h.a.removeTransfer(archive)
	h.tt.Frame()
	h.tt.Frame()
	if h.a.activeTransfers() != 1 {
		t.Fatal("active task lost during rebuilding")
	}
}
