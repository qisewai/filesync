package main

import (
	"filemanager/remoteclient/fitest"
	"filesync/config"
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
	h.tt.Key(ui.Ctrl, ui.KeyL)
	h.tt.Key(ui.Ctrl, ui.KeyA)
	h.tt.Type(text)
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
}

func TestFolderSearchKeepsFocusAfterClearing(t *testing.T) {
	for _, remote := range []bool{false, true} {
		for _, method := range []string{"backspace", "select-all", "escape", "clear-button"} {
			name := "local/" + method
			if remote {
				name = "remote/" + method
			}
			t.Run(name, func(t *testing.T) {
				var h *harness
				if remote {
					h = browserHarness(t, fitest.New(t))
				} else {
					h = localSearchHarness(t, t.TempDir())
				}
				origin := h.a.location()
				h.tt.Key(ui.Ctrl, ui.KeyF)
				h.tt.Type("abc")
				h.settle()
				switch method {
				case "backspace":
					for range 3 {
						h.tt.Key(0, ui.KeyBackspace)
					}
				case "select-all":
					h.tt.Key(ui.Ctrl, ui.KeyA)
					h.tt.Key(0, ui.KeyBackspace)
				case "escape":
					h.tt.Key(0, ui.KeyEscape)
				case "clear-button":
					if err := h.tt.Click("清除搜索"); err != nil {
						t.Fatal(err)
					}
				}
				// No click or focus shortcut between clearing and typing again.
				if h.a.location() != origin || !h.tt.Focused("搜索当前文件夹") {
					t.Fatalf("clear lost directory or focus: %s", h.a.location())
				}
				h.tt.Type("xyz")
				h.settle()
				if !h.a.onFolderSearch() || h.a.gsearch.query != "xyz" || h.a.gsearch.folder.location() != origin {
					t.Fatal("typing after clear did not start a new search in the same folder")
				}
			})
		}
	}
}

func TestSeparateAddressAndLocalFolderSearch(t *testing.T) {
	root := t.TempDir()
	localWrite(t, root, "inside/report.txt", 1)
	localWrite(t, root, "inside/deep/report2.txt", 1)
	localWrite(t, root, "outside/report.txt", 1)
	h := localSearchHarness(t, root)
	h.indexMenu("更新索引")
	dir := filepath.Join(root, "inside")
	h.submitAddress(dir)
	if !h.a.onLocal() || h.a.local.dir != dir {
		t.Fatal("address did not navigate")
	}
	h.submitAddress("report.txt")
	if !h.a.onLocal() || !h.a.local.sel.Has(filepath.Join(dir, "report.txt")) {
		t.Fatal("file was not revealed")
	}
	h.tt.Key(ui.Ctrl, ui.KeyF)
	h.tt.Type("report*")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if !h.a.onFolderSearch() || len(h.a.gsearch.items) != 2 || len(h.a.gsearch.groups) != 1 {
		t.Fatalf("folder search escaped scope: %+v", h.a.gsearch)
	}
	if _, ok := h.tt.Find("编辑路径"); !ok {
		t.Fatal("search lost address bar")
	}
	if h.tt.HasText("全局搜索") || h.tt.HasText("搜索范围：全部位置") {
		t.Fatal("folder search has global scope controls")
	}
	if out := os.Getenv("FILESYNC_SPLIT_SEARCH_SNAPSHOT"); out != "" {
		saveResumeSnapshot(t, h, out)
	}
	h.tt.Key(ui.Alt, ui.KeyLeft)
	h.settle()
	if !h.a.onLocal() || h.a.local.dir != dir {
		t.Fatal("back did not return to browsing")
	}
	h.tt.Key(ui.Alt, ui.KeyRight)
	h.settle()
	if !h.a.onFolderSearch() || h.a.gsearch.folder.dir != dir || len(h.a.gsearch.items) != 2 {
		t.Fatal("forward lost search origin")
	}
	// Visiting a different folder must not replace the origin saved in history.
	h.submitAddress(filepath.Join(root, "outside"))
	h.tt.Key(ui.Alt, ui.KeyLeft)
	h.settle()
	if !h.a.onFolderSearch() || h.a.gsearch.folder.dir != dir || len(h.a.gsearch.items) != 2 {
		t.Fatal("history reused the newer browser folder")
	}
	h.click("清除搜索")
	if !h.a.onLocal() || h.a.local.dir != dir {
		t.Fatal("clear opened global search")
	}
	h.submitAddress(filepath.Join(root, "missing"))
	if !h.a.onLocal() || h.a.errorMsg == "" {
		t.Fatal("invalid address became search")
	}
}

func TestFirstDirectoryDoesNotKeepSearchLanding(t *testing.T) {
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.settle()
	root := t.TempDir()
	h.a.openLocal(root)
	h.settle()
	if h.a.nav.CanGoBack() {
		t.Fatal("initial global search landing remained in file history")
	}
	os.Mkdir(filepath.Join(root, "child"), 0755)
	h.a.openLocal(filepath.Join(root, "child"))
	h.settle()
	h.tt.Key(ui.Alt, ui.KeyLeft)
	h.settle()
	if !h.a.onLocal() || h.a.local.dir != root || h.a.nav.CanGoBack() {
		t.Fatal("back did not stop at the first browsed directory")
	}
}

func TestSeparateAddressAndRemoteFolderSearch(t *testing.T) {
	h, f, _ := searchHarness(t)
	os.MkdirAll(filepath.Join(f.Root, "项目", "nested"), 0755)
	os.WriteFile(filepath.Join(f.Root, "项目", "nested", "报告.txt"), []byte("nested"), 0644)
	sv := h.a.store.Servers[0]
	h.a.goPlace(rootPlace(sv.ID, 1))
	h.settle()
	h.submitAddress(sv.Title() + "/nas/项目")
	if !h.a.onRoot() || h.a.br.path != "项目" {
		t.Fatal("remote address did not navigate")
	}
	h.tt.Key(ui.Ctrl, ui.KeyF)
	h.tt.Type("报告")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if !h.a.onFolderSearch() || len(h.a.gsearch.items) != 2 || len(h.a.gsearch.groups) != 1 {
		t.Fatal("remote search escaped folder or omitted descendants")
	}
	h.tt.Key(ui.Alt, ui.KeyLeft)
	h.settle()
	if !h.a.onRoot() || h.a.br.path != "项目" {
		t.Fatal("back did not return to remote browsing")
	}
	h.tt.Key(ui.Alt, ui.KeyRight)
	h.settle()
	if !h.a.onFolderSearch() || h.a.gsearch.folder.dir != "项目" {
		t.Fatal("forward lost remote origin")
	}
	h.submitAddress(sv.Title() + "/nas/不存在")
	if !h.a.onFolderSearch() || h.a.errorMsg == "" {
		t.Fatal("invalid remote address changed search")
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
