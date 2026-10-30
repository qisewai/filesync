package main

import (
	"filemanager/remoteclient/fitest"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func (h *harness) submitAddress(text string) {
	h.t.Helper()
	h.tt.Key(ui.Ctrl, ui.KeyF)
	h.tt.Type(text)
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
}

func TestUnifiedAddressLocalNavigationAndFolderSearch(t *testing.T) {
	root := t.TempDir()
	localWrite(t, root, "inside/report.txt", 1)
	localWrite(t, root, "outside/report.txt", 1)
	h := localSearchHarness(t, root)
	h.indexMenu("更新索引")
	h.submitAddress(filepath.Join(root, "inside"))
	if !h.a.onLocal() || h.a.local.dir != filepath.Join(root, "inside") {
		t.Fatal("existing directory did not navigate")
	}
	h.submitAddress("report.txt")
	if !h.a.onLocal() || !h.a.local.sel.Has(filepath.Join(root, "inside/report.txt")) {
		t.Fatal("existing file was not revealed")
	}
	h.submitAddress("report*")
	if !h.a.onSearch() || h.a.gsearch.scope != "folder" || len(h.a.gsearch.items) != 1 {
		t.Fatalf("folder search: %+v", h.a.gsearch)
	}
	// The same field in search results also accepts paths.
	h.replaceField("全局搜索", root)
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if !h.a.onLocal() || h.a.local.dir != root {
		t.Fatal("search field did not accept a path")
	}
	h.submitAddress(filepath.Join(root, "missing"))
	if !h.a.onSearch() || h.a.gsearch.query != filepath.Join(root, "missing") || h.a.errorMsg != "" {
		t.Fatal("missing path did not become a search")
	}
}

func TestUnifiedAddressRemoteNavigationAndFolderSearch(t *testing.T) {
	h, f, _ := searchHarness(t)
	os.MkdirAll(filepath.Join(f.Root, "other"), 0755)
	os.WriteFile(filepath.Join(f.Root, "other", "报告.pdf"), []byte("other"), 0644)
	sv := h.a.store.Servers[0]
	h.a.goPlace(rootPlace(sv.ID, 1))
	h.settle()
	h.submitAddress(sv.Title() + "/nas/项目")
	if !h.a.onRoot() || h.a.br.path != "项目" {
		t.Fatalf("NAS path did not navigate: location=%s edit=%+v error=%s", h.a.location(), h.a.pathEditing, h.a.errorMsg)
	}
	h.submitAddress("年度报告.pdf")
	if !h.a.onRoot() || !h.a.br.sel.Has("项目/年度报告.pdf") {
		t.Fatalf("NAS file was not revealed: location=%s edit=%+v selected=%v error=%s", h.a.location(), h.a.pathEditing, h.a.br.sel, h.a.errorMsg)
	}
	h.submitAddress("报告")
	if !h.a.onSearch() || h.a.gsearch.scope != "folder" || len(h.a.gsearch.items) != 1 {
		t.Fatal("NAS search escaped current folder")
	}
	h.replaceField("全局搜索", sv.Title()+"/nas/不存在")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if !h.a.onSearch() || h.a.errorMsg != "" || h.a.gsearch.folder.dir != "项目" {
		t.Fatal("invalid NAS path did not retain search folder")
	}
}

func TestUnifiedAddressIgnoresOldRemoteReply(t *testing.T) {
	h := browserHarness(t, fitest.New(t))
	started, release := make(chan struct{}), make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer remote.Close()
	h.a.store.Servers[0].Addr = strings.TrimPrefix(remote.URL, "http://")
	h.a.submitLocation("not-a-path")
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("path lookup did not start")
	}
	dir := t.TempDir()
	h.a.submitLocation(dir)
	close(release)
	select {
	case fn := <-h.queue:
		fn()
	case <-time.After(time.Second):
		t.Fatal("lookup did not finish")
	}
	h.settle()
	if !h.a.onLocal() || h.a.local.dir != dir {
		t.Fatal("old lookup replaced newer navigation")
	}
}
