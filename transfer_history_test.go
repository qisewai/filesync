package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filesync/config"
)

func flushHistory(t *testing.T, a *app) {
	t.Helper()
	if a.transferHistory == nil {
		t.Fatal("history writer unavailable")
	}
	if err := a.transferHistory.flush(); err != nil {
		t.Fatal(err)
	}
}
func readHistory(t *testing.T, dir string) transferHistoryFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "transfers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var data transferHistoryFile
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatal(err)
	}
	if data.Version != 1 {
		t.Fatal(data.Version)
	}
	return data
}

func TestTransferHistoryRestartOpenDeleteAndClear(t *testing.T) {
	dir := t.TempDir()
	dest := t.TempDir()
	file := filepath.Join(dest, "季度报告.txt")
	os.WriteFile(file, []byte("report"), 0600)
	h := newHarness(t, &config.Store{Dir: dir})
	tr := h.a.addTransfer("下载并打开", "季度报告.txt")
	tr.localDir, tr.localFile = dest, file
	tr.done, tr.total = 6, 6
	tr.server = "old-server"
	tr.errors = []string{}
	h.a.finishTransfer(tr, nil, nil)
	flushHistory(t, h.a)
	h.a.stopBackground()
	if err := h.a.transferHistory.wait(); err != nil {
		t.Fatal(err)
	}
	restarted := newHarness(t, &config.Store{Dir: dir})
	restarted.settle()
	if len(restarted.a.transfers) != 1 {
		t.Fatal("history lost after restart")
	}
	saved := restarted.a.transfers[0]
	if saved.localFile != file || saved.localDir != dest || saved.state != "done" || saved.started.IsZero() || saved.finished.IsZero() || saved.done != 6 {
		t.Fatalf("restored %+v", saved)
	}
	// This entry is available on the welcome page without choosing a directory.
	restarted.click("传输任务")
	restarted.want("季度报告.txt")
	opened := ""
	restarted.a.openPath = func(p string) { opened = p }
	restarted.click("打开下载所在目录")
	if opened != dest {
		t.Fatal(opened)
	}
	opened = ""
	restarted.click("打开下载文件")
	restarted.waitOpened(&opened)
	if opened != file {
		t.Fatal(opened)
	}
	if len(restarted.a.transfers) != 2 || restarted.a.transfers[0].kind != "打开" {
		t.Fatal("reopening was not recorded")
	}
	flushHistory(t, restarted.a)
	restarted.click("删除任务记录")
	flushHistory(t, restarted.a)
	if len(readHistory(t, dir).Items) != 1 {
		t.Fatal("deleted record returned on disk")
	}
	restarted.click("清除已完成")
	flushHistory(t, restarted.a)
	if len(readHistory(t, dir).Items) != 0 {
		t.Fatal("cleared history remained on disk")
	}
	restarted.want("暂无传输记录")
	restarted.a.stopBackground()
	restarted.a.transferHistory.wait()
	again := newHarness(t, &config.Store{Dir: dir})
	again.settle()
	again.click("传输任务")
	again.want("暂无传输记录")
	body, err := os.ReadFile(file)
	if err != nil || string(body) != "report" {
		t.Fatalf("files changed %q %v", body, err)
	}
}
func TestTransferHistoryRestoresUnfinishedWithoutRunningOrOpening(t *testing.T) {
	dir := t.TempDir()
	a := newApp(&config.Store{Dir: dir})
	tr := a.addTransfer("下载", "未完成的下载")
	tr.done, tr.total = 10, 100
	tr.localDir = t.TempDir()
	tr.localFile = filepath.Join(tr.localDir, "partial.txt")
	tr.after = func() { t.Error("restored completion callback invoked") }
	tr.cancel = func() { t.Error("restored cancellation invoked") }
	a.saveTransferHistory()
	flushHistory(t, a)
	a.stopBackground()
	a.transferHistory.wait()
	h := newHarness(t, &config.Store{Dir: dir})
	h.settle()
	if h.a.activeTransfers() != 0 || len(h.a.transfers) != 1 || h.a.transfers[0].state != "interrupted" {
		t.Fatal("unfinished history treated as running/completed")
	}
	restored := h.a.transfers[0]
	if restored.cancel != nil || restored.after != nil || restored.done != 10 {
		t.Fatalf("restored %+v", restored)
	}
	h.click("传输任务")
	h.want("上次未完成")
	if h.tt.HasText("打开下载文件") {
		t.Fatal("partial file can be opened as complete")
	}
	// New IDs remain unique when old rows survive another build.
	next := h.a.addTransfer("上传", "new")
	if next.id == restored.id {
		t.Fatal("reused row identity")
	}
	h.a.finishTransfer(next, context.Canceled, nil)
	h.tt.Frame()
	h.tt.Frame()
}
func TestTransferHistoryEmptyEntryAndMissingFile(t *testing.T) {
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.settle()
	h.click("传输任务")
	h.want("暂无传输记录")
	h.a.showTransfers = false
	tr := h.a.addTransfer("下载", "已被移走的文件")
	tr.localFile = filepath.Join(t.TempDir(), "missing")
	h.a.finishTransfer(tr, nil, nil)
	h.tt.Frame()
	h.click("传输任务")
	h.click("打开下载文件")
	deadline := time.After(3 * time.Second)
	for h.a.errorMsg == "" {
		select {
		case fn := <-h.queue:
			fn()
			h.tt.Frame()
		case <-deadline:
			t.Fatal("missing file not reported")
		}
	}
	if len(h.a.transfers) != 1 {
		t.Fatal("recorded a failed open as success")
	}
}
func TestTransferHistoryKeepsCredentialsOutAndUsesPrivateAtomicFile(t *testing.T) {
	dir := t.TempDir()
	a := newApp(&config.Store{Dir: dir, Servers: []*config.Server{{ID: "nas", Addr: "nas:8090", Token: "very-secret-session-token"}}})
	defer func() { a.stopBackground(); a.transferHistory.wait() }()
	tr := a.addTransfer("下载", "a.txt")
	tr.server = "nas"
	tr.errors = []string{"before"}
	a.saveTransferHistory()
	tr.errors[0] = "after"
	flushHistory(t, a)
	b, err := os.ReadFile(filepath.Join(dir, "transfers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("very-secret-session-token")) || bytes.Contains(b, []byte("after")) {
		t.Fatal("credentials or mutable data leaked into snapshot")
	}
	if !bytes.Contains(b, []byte("before")) {
		t.Fatal("error detail lost")
	}
	fi, _ := os.Stat(filepath.Join(dir, "transfers.json"))
	if fi.Mode().Perm()&0077 != 0 {
		t.Fatalf("history permissions %o", fi.Mode().Perm())
	}
	temps, _ := filepath.Glob(filepath.Join(dir, ".transfers-*.tmp"))
	if len(temps) != 0 {
		t.Fatal("temporary history files leaked")
	}
}
func TestTransferHistoryCoalescesUpdatesAndDrainsOnClose(t *testing.T) {
	dir := t.TempDir()
	w := newTransferHistoryWriter(filepath.Join(dir, "transfers.json"))
	for i := range 50 {
		w.enqueue([]transferRecord{{Kind: "下载", Title: "latest", State: "done", Done: int64(i)}}, nil)
	}
	if err := w.wait(); err != nil {
		t.Fatal(err)
	}
	if got := readHistory(t, dir).Items[0].Done; got != 49 {
		t.Fatalf("stale write won: %d", got)
	}
}
func TestTransferHistoryCorruptFileIsKeptAndSaveFailureVisible(t *testing.T) {
	dir := t.TempDir()
	broken := []byte("broken JSON")
	os.WriteFile(filepath.Join(dir, "transfers.json"), broken, 0600)
	a := newApp(&config.Store{Dir: dir})
	if a.errorMsg == "" {
		t.Fatal("bad history silently ignored")
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "transfers.json.broken-*"))
	if len(backups) != 1 {
		t.Fatal("corrupt file not preserved")
	}
	body, _ := os.ReadFile(backups[0])
	if !bytes.Equal(body, broken) {
		t.Fatal("corrupt backup changed")
	}
	tr := a.addTransfer("下载", "new")
	a.finishTransfer(tr, nil, nil)
	flushHistory(t, a)
	a.stopBackground()
	a.transferHistory.wait()
	blockedDir := t.TempDir()
	h := newHarness(t, &config.Store{Dir: blockedDir})
	blocked := filepath.Join(blockedDir, "transfers.json")
	os.Mkdir(blocked, 0700)
	marker := filepath.Join(blocked, "keep")
	os.WriteFile(marker, []byte("keep"), 0600)
	h.a.addTransfer("上传", "cannot save")
	if err := h.a.transferHistory.flush(); err == nil {
		t.Fatal("save unexpectedly succeeded")
	}
	deadline := time.After(3 * time.Second)
	for !strings.Contains(h.a.errorMsg, "无法保存传输记录") {
		select {
		case fn := <-h.queue:
			fn()
			h.tt.Frame()
		case <-deadline:
			t.Fatal("save failure not shown")
		}
	}
	body, _ = os.ReadFile(marker)
	if string(body) != "keep" {
		t.Fatal("unrelated file overwritten")
	}
}
func TestTransferHistoryRestoredPanelSnapshot(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(t.TempDir(), "报告.pdf")
	os.WriteFile(file, []byte("PDF"), 0600)
	records := []transferRecord{{Kind: "下载并打开", Title: "季度报告.pdf", State: "done", Started: time.Now().Add(-24 * time.Hour), Finished: time.Now().Add(-23 * time.Hour), Directory: filepath.Dir(file), File: file, FolderReady: true}, {Kind: "解压", Title: "照片备份.7z → 图片", State: "done", Started: time.Now().Add(-48 * time.Hour), Finished: time.Now().Add(-47 * time.Hour)}, {Kind: "下载", Title: "上次未完成的下载", State: "running", Started: time.Now().Add(-72 * time.Hour)}}
	if err := writeTransferHistory(filepath.Join(dir, "transfers.json"), records); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, &config.Store{Dir: dir})
	h.settle()
	h.click("传输任务")
	h.tt.Frame()
	if out := os.Getenv("FILESYNC_HISTORY_SNAPSHOT"); out != "" {
		f, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, h.tt.Image())
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}
