package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"filesync/config"
	"filemanager/remoteclient"
	"filemanager/remoteclient/fitest"

	"github.com/egoist/mygo/ui"
)

func (h *harness) menu(target string, item ...string) {
	h.t.Helper()
	if err := h.tt.RightClick(target); err != nil {
		h.t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem(item...); err != nil {
		h.t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.settle()
}

// Deleting asks to move to the trash, or to delete for good, which runs
// as a task of the server, shown with the transfers.
func TestDeleteForever(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "旧项目", "src"), 0o755)
	os.WriteFile(filepath.Join(f.Root, "旧项目", "src", "a.go"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(f.Root, "临时.txt"), []byte("t"), 0o644)
	h := browserHarness(t, f)
	h.click("文件夹 nas")
	h.clickPage("旧项目")
	h.tt.Key(0, ui.KeyDelete)
	h.settle()
	h.want("删除“旧项目”？")
	h.click("彻底删除…")
	h.want("文件夹里的所有内容也会一起删除。彻底删除后无法恢复：不会进入回收站。")
	h.click("彻底删除")
	h.waitTransfers()
	if _, err := os.Stat(filepath.Join(f.Root, "旧项目")); !os.IsNotExist(err) {
		t.Fatalf("still there: %v", err)
	}
	if ents, _ := os.ReadDir(f.Trash); len(ents) != 0 {
		t.Fatal("went to the trash")
	}
	if len(h.a.transfers) != 1 || h.a.transfers[0].kind != "彻底删除" || h.a.transfers[0].state != "done" {
		t.Fatalf("transfers %+v", h.a.transfers)
	}
	h.settle()
	if slices.ContainsFunc(h.a.br.items, func(e fileindex.Entry) bool { return e.Name == "旧项目" }) {
		t.Error("the listing still shows it")
	}

	// Shift+Delete, with the files focused, asks to delete for good.
	h.clickPage("临时.txt")
	h.tt.Key(ui.Shift, ui.KeyDelete)
	h.want("删除“临时.txt”？")
	h.want("彻底删除后无法恢复：不会进入回收站。")
	// Enter there is Cancel's: nothing is deleted by a key pressed on.
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if h.a.deleting != nil {
		t.Fatal("still asking")
	}
	if _, err := os.Stat(filepath.Join(f.Root, "临时.txt")); err != nil {
		t.Fatal("Enter deleted it:", err)
	}
	// Typing the search, Delete deletes text, not files; the search shows
	// on its page, which back leaves.
	h.click("搜索文件")
	h.tt.Type("临")
	h.settle()
	h.tt.Key(0, ui.KeyDelete)
	h.settle()
	if h.a.deleting != nil || !h.a.onSearch() {
		t.Fatal("Delete in the search asked to delete files")
	}
	h.a.nav.Back()
	h.settle()
	// Delete asks to move it to the trash, which the status tells.
	h.clickPage("临时.txt")
	h.tt.Key(0, ui.KeyDelete)
	h.want("1 项会移到 fileindex 的回收站，可以在左侧的回收站中恢复。")
	if err := h.tt.Click("移到回收站"); err != nil {
		t.Fatal(err)
	}
	h.want("正在把“临时.txt”移到回收站…")
	h.settle()
	if _, err := os.Stat(filepath.Join(f.Trash, "临时.txt")); err != nil {
		t.Fatal("not in the trash:", err)
	}
}

// The directories of the sidebar have the menu of the files page.
func TestDirectoryMenu(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "照片", "2024"), 0o755)
	os.MkdirAll(filepath.Join(f.Root, "文档"), 0o755)
	h := browserHarness(t, f)
	h.a.treeNode(h.a.store.Servers[0].ID, 1, "").open = true
	h.tt.Frame()
	h.settle()

	h.menu("文件夹 照片", "重命名…")
	if h.a.br.path != "" || h.a.renaming == nil || h.a.renaming.e.Path != "照片" {
		t.Fatalf("rename %q %+v", h.a.br.path, h.a.renaming)
	}
	h.a.renaming.to = "相册"
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter) // in the name, which has the focus
	h.settle()
	if _, err := os.Stat(filepath.Join(f.Root, "相册", "2024")); err != nil {
		t.Fatal(err)
	}
	h.want("文件夹 相册")

	// A folder made in a directory of the tree, which the page then shows.
	h.menu("文件夹 文档", "新建文件夹…")
	if h.a.br.path != "文档" || !h.a.br.naming {
		t.Fatalf("mkdir in %q", h.a.br.path)
	}
	h.tt.Type("合同")
	h.click("创建")
	if _, err := os.Stat(filepath.Join(f.Root, "文档", "合同")); err != nil {
		t.Fatal(err)
	}

	h.menu("文件夹 相册", "删除…")
	h.want("删除“相册”？")
	if err := h.tt.Click("移到回收站"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if _, err := os.Stat(filepath.Join(f.Root, "相册")); !os.IsNotExist(err) {
		t.Fatal("not deleted")
	}

	// A root is renamed and removed with the roots, not here.
	if err := h.tt.RightClick("文件夹 nas"); err != nil {
		t.Fatal(err)
	}
	if items := h.tt.Menu(); slices.Contains(items, "重命名…") || slices.Contains(items, "删除…") || !slices.Contains(items, "目录管理…") {
		t.Fatalf("menu of a root %q", items)
	}
	h.tt.CloseMenu()
}

func TestLocalDirectoryMenu(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "项目"), 0o755)
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.localRoots = []localRoot{{"主目录", home}}
	h.a.localNode(home, home).open = true
	h.tt.Frame()
	h.settle()
	h.menu("本机文件夹 项目", "创建备份任务…")
	if !h.a.editing || h.a.draft.source != filepath.Join(home, "项目") || !h.a.draft.withDir {
		t.Fatalf("draft %+v", h.a.draft)
	}
}
