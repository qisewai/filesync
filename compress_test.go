package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filesync/config"
	"filesync/fileindex"
	"filesync/fileindex/fitest"
)

func TestCompressionDialogSubmissionResultAndClose(t *testing.T) {
	f := fitest.New(t)
	f.CompressionBodies = make(chan []byte, 1)
	os.MkdirAll(filepath.Join(f.Root, "照片"), 0755)
	h := browserHarness(t, f)
	entry := fileindex.Entry{Root: 1, RootName: "nas", Name: "照片", Path: "照片", IsDir: true}
	h.a.compressIn(h.a.brFiles(), []fileindex.Entry{entry})
	h.tt.Frame()
	h.want("压缩到服务器")
	h.click("tar.zst")
	h.click("较小体积")
	h.click("覆盖")
	if h.a.compressing.dstRoot != 1 {
		t.Fatal("default destination missing")
	}
	h.click("开始压缩")
	var req fileindex.CompressRequest
	select {
	case b := <-f.CompressionBodies:
		if err := json.Unmarshal(b, &req); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("not submitted")
	}
	if req.Format != "tar.zst" || req.Level != "compact" || req.Conflict != "overwrite" || req.Name != "照片.tar.zst" || len(req.Paths) != 1 {
		t.Fatalf("request %+v", req)
	}
	tr := h.a.transfers[0]
	deadline := time.After(6 * time.Second)
	for tr.running() {
		select {
		case fn := <-h.queue:
			fn()
			h.tt.Frame()
		case <-deadline:
			t.Fatal("server task not followed")
		}
	}
	if tr.state != "done" || tr.archiveResult == nil || tr.archiveResult.Path != "照片.tar.zst" {
		t.Fatalf("result %+v", tr)
	}
	h.a.openTransfers()
	h.tt.Frame()
	h.want("打开压缩包所在位置")
	h.click("打开压缩包所在位置")
	if h.a.br.root.ID != 1 || h.a.br.path != "" {
		t.Fatal("not located")
	}
	// Closing only detaches; it must not invoke a server compression cancel.
	tr.state = "running"
	tr.cancel = func() { t.Error("closing canceled server compression") }
	if !h.a.requestClose() {
		t.Fatal("compression blocked close")
	}
}

func TestCompressionHistoryRecoveryAndUnknownResult(t *testing.T) {
	dir := t.TempDir()
	f := fitest.New(t)
	store := &config.Store{Dir: dir, Servers: []*config.Server{{ID: "nas", Addr: f.Addr(), Token: "tok"}}}
	h := newHarness(t, store)
	tr := h.a.addTransfer("压缩", "测试压缩")
	tr.server, tr.task, tr.taskCreated = "nas", 4, 123
	h.a.applyTask(tr, &fileindex.Task{ID: 4, CreatedAt: 123, Kind: "compress", Status: "done", Result: &fileindex.ArchiveResult{Root: 1, Path: "备份.zip", Size: 100}})
	flushHistory(t, h.a)
	h.a.stopBackground()
	h.a.transferHistory.wait()
	restarted := newHarness(t, store)
	saved := restarted.a.transfers[0]
	if saved.archiveResult == nil || saved.taskCreated != 123 || saved.archiveResult.Path != "备份.zip" {
		t.Fatal("result not persisted")
	}
	bad := restarted.a.addTransfer("压缩", "重启后任务")
	bad.taskCreated = 123
	restarted.a.applyTask(bad, &fileindex.Task{ID: 4, CreatedAt: 456, Kind: "compress", Status: "done", Result: &fileindex.ArchiveResult{Root: 1, Path: "wrong.zip"}})
	if bad.state != "failed" || !strings.Contains(bad.err, "无法确认") {
		t.Fatal("reused task accepted")
	}
	bad = restarted.a.addTransfer("压缩", "无结果")
	restarted.a.applyTask(bad, &fileindex.Task{Kind: "compress", Status: "done"})
	if bad.state != "failed" {
		t.Fatal("missing result accepted")
	}
}

func TestCompressionClientOldServerAndTaskRecovery(t *testing.T) {
	f := fitest.New(t)
	c := fileindex.New(f.Addr(), "tok")
	f.OldServer.Store(true)
	_, err := c.CompressArchive(context.Background(), fileindex.CompressRequest{Root: 1, Paths: []string{"a"}, DstRoot: 1, Name: "a.zip", Format: "zip"})
	if err == nil || !strings.Contains(err.Error(), "更新 fileindex") {
		t.Fatal(err)
	}
	f.OldServer.Store(false)
	task, err := c.CompressArchive(context.Background(), fileindex.CompressRequest{Root: 1, Paths: []string{"a"}, DstRoot: 1, Name: "a.zip", Format: "zip"})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, &config.Store{Servers: []*config.Server{{ID: "nas", Addr: f.Addr(), Token: "tok"}}})
	h.a.refreshCompressionTasks("nas")
	deadline := time.After(4 * time.Second)
	for len(h.a.transfers) == 0 {
		select {
		case fn := <-h.queue:
			fn()
		case <-deadline:
			t.Fatal("not discovered")
		}
	}
	if h.a.transfers[0].task != task.ID {
		t.Fatal("wrong recovered task")
	}
}

func TestCompressionDismissalSurvivesRestart(t *testing.T) {
	f := fitest.New(t)
	c := fileindex.New(f.Addr(), "tok")
	task, err := c.CompressArchive(context.Background(), fileindex.CompressRequest{Root: 1, Paths: []string{"a"}, DstRoot: 1, Name: "a.zip", Format: "zip"})
	if err != nil {
		t.Fatal(err)
	}
	store := &config.Store{Dir: t.TempDir(), Servers: []*config.Server{{ID: "nas", Addr: f.Addr(), Token: "tok"}}}
	h := newHarness(t, store)
	tr := h.a.addTransfer("压缩", task.Title)
	tr.server, tr.task, tr.taskCreated, tr.state = "nas", task.ID, task.CreatedAt, "done"
	h.a.dismissCompression(tr)
	h.a.transfers = nil
	h.a.saveTransferHistory()
	flushHistory(t, h.a)
	h.a.stopBackground()
	h.a.transferHistory.wait()
	restarted := newHarness(t, store)
	if !restarted.a.dismissedCompression[compressionIdentity{"nas", task.ID, task.CreatedAt}] {
		t.Fatal("dismissed server task was not persisted")
	}
	restarted.a.refreshCompressionTasks("nas")
	deadline := time.After(150 * time.Millisecond)
	for {
		select {
		case fn := <-restarted.queue:
			fn()
		case <-deadline:
			if len(restarted.a.transfers) != 0 {
				t.Fatal("dismissed compression reappeared after restart")
			}
			return
		}
	}
}
