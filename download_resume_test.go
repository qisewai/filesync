package main

import (
	"bytes"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localindex "filemanager/index"
	"filesync/config"
	"filemanager/remoteclient"
)

func waitDownload(t *testing.T, h *harness) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for h.a.activeTransfers() > 0 {
		select {
		case fn := <-h.queue:
			fn()
		case <-deadline:
			t.Fatal("download did not finish")
		}
	}
	h.settle()
}

func TestDownloadResumeAfterRestartFromTransferPage(t *testing.T) {
	content := bytes.Repeat([]byte("NAS download resume\n"), 4096)
	cut := len(content) / 2
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/roots" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"roots":[]}`)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/download/") {
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(404)
			return
		}
		calls++
		w.Header().Set("ETag", `"demo-v1"`)
		if calls == 1 {
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			w.Write(content[:cut])
			return
		}
		cookie, _ := r.Cookie("fi_session")
		if cookie == nil || cookie.Value != "new-session" {
			t.Error("retry did not use current login")
		}
		if r.Header.Get("Range") != fmt.Sprintf("bytes=%d-", cut) {
			t.Errorf("not resumed: %v", r.Header)
		}
		http.ServeContent(w, r, "照片备份.bin", time.Unix(1700000000, 0), bytes.NewReader(content))
	}))
	defer srv.Close()
	dir, dest := t.TempDir(), t.TempDir()
	addr := strings.TrimPrefix(srv.URL, "http://")
	settings := func(token string) *config.Store {
		return &config.Store{Dir: dir, Servers: []*config.Server{{ID: "nas", Name: "NAS", Addr: addr, Token: token}}}
	}
	h := newHarness(t, settings("old-session"))
	cache := filepath.Join(t.TempDir(), "index.bin")
	h.a.localCachePath = cache
	h.a.startDownloadFrom(fileindex.New(addr, "old-session"), &nasFiles{server: "nas"}, []fileindex.Entry{{Root: 1, Path: "照片备份.bin", Name: "照片备份.bin", Size: int64(len(content))}}, dest, false)
	waitDownload(t, h)
	if len(h.a.transfers) != 1 || h.a.transfers[0].state != "failed" || h.a.transfers[0].done != int64(cut) {
		t.Fatalf("interrupted transfer %+v", h.a.transfers)
	}
	flushHistory(t, h.a)
	h.a.stopBackground()
	h.a.transferHistory.wait()
	restarted := newHarness(t, settings("new-session"))
	restarted.a.localCachePath = cache
	restarted.settle()
	restarted.click("传输任务")
	restored := restarted.a.transfers[0]
	if restored.download == nil || restored.download.Path != "照片备份.bin" {
		t.Fatal("retry parameters lost")
	}
	if !restarted.tt.HasText("继续下载") {
		t.Fatal("resume action missing")
	}
	if out := os.Getenv("FILESYNC_RESUME_BEFORE_SNAPSHOT"); out != "" {
		saveResumeSnapshot(t, restarted, out)
	}
	restarted.click("继续下载")
	waitDownload(t, restarted)
	if restored.state != "done" || len(restarted.a.transfers) != 1 {
		t.Fatalf("resume result %+v", restored)
	}
	b, err := os.ReadFile(filepath.Join(dest, "照片备份.bin"))
	if err != nil || !bytes.Equal(b, content) {
		t.Fatalf("download differs: %v", err)
	}
	var records []localindex.Operation
	if err := restarted.a.localOpLog.Replay(func(_ uint64, op localindex.Operation) error { records = append(records, op); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 || records[0].Action != "download" || records[1].Phase != "failed" || records[2].Action != "download" || records[3].Phase != "done" || records[3].TargetState.Kind != "file" {
		t.Fatalf("download operation log: %+v", records)
	}
	if out := os.Getenv("FILESYNC_RESUME_AFTER_SNAPSHOT"); out != "" {
		saveResumeSnapshot(t, restarted, out)
	}
}

func saveResumeSnapshot(t *testing.T, h *harness, filename string) {
	t.Helper()
	h.tt.Frame()
	f, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(f, h.tt.Image())
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
}

func TestDownloadResumeGuardsAndCleanup(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, &config.Store{Servers: []*config.Server{{ID: "nas", Addr: "new:8090", Token: "tok"}}})
	tr := h.a.addTransfer("下载", "file")
	tr.server, tr.localFile = "nas", filepath.Join(dir, "file")
	tr.download = &downloadResume{Addr: "old:8090", Root: 1, Path: "file", Partial: filepath.Join(dir, ".filesync-download-test.part")}
	h.a.finishTransfer(tr, fmt.Errorf("offline"), nil)
	h.a.resumeDownload(tr)
	if tr.running() || !strings.Contains(h.a.errorMsg, "地址已改变") {
		t.Fatal("retry sent to another server")
	}
	tr.download.Addr = "new:8090"
	h.a.store.Servers[0].Token = ""
	h.a.resumeDownload(tr)
	if tr.running() || !strings.Contains(h.a.errorMsg, "登录") {
		t.Fatal("retry started while logged out")
	}
	os.WriteFile(tr.localFile, []byte("keep"), 0600)
	os.WriteFile(tr.download.Partial, []byte("partial"), 0600)
	os.WriteFile(tr.download.Partial+".json", []byte("{}"), 0600)
	h.a.removeTransfer(tr)
	if b, _ := os.ReadFile(tr.localFile); string(b) != "keep" {
		t.Fatal("removing record deleted downloaded file")
	}
	if _, err := os.Stat(tr.download.Partial); !os.IsNotExist(err) {
		t.Fatal("removing record leaked partial")
	}
}
