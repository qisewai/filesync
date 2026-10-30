package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filesync/config"
	"filemanager/remoteclient"
	"github.com/egoist/mygo/ui"
)

// Optional real-server UI integration. The fixture must be a disposable local
// fileindex instance with root 1 containing 资料/说明.txt.
func TestCompressionRealServerUI(t *testing.T) {
	filename := os.Getenv("FILESYNC_COMPRESSION_FIXTURE")
	if filename == "" {
		t.Skip("set FILESYNC_COMPRESSION_FIXTURE")
	}
	b, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var connection struct{ Addr, Token string }
	if err := json.Unmarshal(b, &connection); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(connection.Addr, "127.0.0.1:") {
		t.Fatal("use an isolated local fixture, never a NAS")
	}
	c := fileindex.New(connection.Addr, connection.Token)
	roots, err := c.Roots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var root fileindex.Root
	for _, r := range roots {
		if r.ID == 1 {
			root = r
		}
	}
	h := newHarness(t, &config.Store{Dir: t.TempDir(), Servers: []*config.Server{{ID: "fixture", Name: "压缩测试 NAS", Addr: connection.Addr, Token: connection.Token}}})
	h.a.roots["fixture"] = roots
	h.a.openRoot("fixture", root)
	h.a.goPlace(rootPlace("fixture", root.ID))
	h.settle()
	h.clickPage("资料")
	h.more("压缩到…")
	h.want("压缩到服务器")
	h.click("压缩包名称")
	h.tt.Key(ui.Ctrl, ui.KeyA)
	h.tt.Type("桌面验证")
	h.click("tar.gz")
	h.click("较小体积")
	artifacts := os.Getenv("FILESYNC_COMPRESSION_ARTIFACTS")
	if artifacts != "" {
		saveResumeSnapshot(t, h, filepath.Join(artifacts, "desktop_compression_settings.png"))
	}
	h.click("开始压缩")
	if len(h.a.transfers) != 1 {
		t.Fatal("task not tracked")
	}
	tr := h.a.transfers[0]
	deadline := time.After(12 * time.Second)
	for tr.running() {
		select {
		case fn := <-h.queue:
			fn()
			h.tt.Frame()
		case <-deadline:
			t.Fatal("task not completed")
		}
	}
	if tr.state != "done" || tr.archiveResult == nil {
		t.Fatalf("result %+v", tr)
	}
	archiveFile := filepath.Join(t.TempDir(), "result.tar.gz")
	if err := c.DownloadFile(context.Background(), tr.archiveResult.Root, tr.archiveResult.Path, archiveFile, nil); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(archiveFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	tarReader := tar.NewReader(g)
	found := false
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "资料/说明.txt" {
			found = true
			if string(body) != strings.Repeat("服务器压缩联调内容\n", 2000) {
				t.Fatal("archive contents differ")
			}
		}
	}
	if !found {
		t.Fatal("source file missing from archive")
	}
	h.a.openTransfers()
	h.settle()
	if artifacts != "" {
		saveResumeSnapshot(t, h, filepath.Join(artifacts, "desktop_compression_complete.png"))
	}
	h.click("打开压缩包所在位置")
	if h.a.br.path != "" {
		t.Fatal("wrong result folder")
	}
}
