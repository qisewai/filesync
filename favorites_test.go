package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"filemanager/remoteclient/fitest"
	"filesync/config"
	"github.com/egoist/mygo/ui"
)

// dropdown chooses an item of the menu the button label opens.
func (h *harness) dropdown(label string, item ...string) {
	h.t.Helper()
	h.tt.Frame()
	if err := h.tt.Click(label); err != nil {
		h.t.Fatalf("%v; texts %q", err, h.tt.Texts())
	}
	if err := h.tt.ChooseMenuItem(item...); err != nil {
		h.t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.settle()
}

func TestFavorites(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "照片", "2026"), 0755)
	h := browserHarness(t, f)
	local := t.TempDir()
	os.MkdirAll(filepath.Join(local, "项目"), 0755)

	// The star beside the path pins the local folder shown.
	h.a.openLocal(filepath.Join(local, "项目"))
	h.settle()
	h.click("添加到收藏")
	h.want("收藏 项目")
	if len(h.a.store.Favorites) != 1 || h.a.store.Favorites[0].Local != filepath.Join(local, "项目") {
		t.Fatalf("favorites %+v", h.a.store.Favorites)
	}
	if again, _ := config.Load(h.a.store.Dir); len(again.Favorites) != 1 {
		t.Fatal("favorite not saved")
	}
	h.want("从收藏中移除") // the star, filled

	// A folder of the server, from the menu of the tree.
	h.menu("文件夹 照片", "添加到收藏")
	h.want("收藏 照片")

	// Each opens its folder.
	h.click("收藏 照片")
	if !h.a.onRoot() || h.a.br.path != "照片" {
		t.Fatalf("opened %q", h.a.place)
	}
	h.click("收藏 项目")
	if !h.a.onLocal() || h.a.local.dir != filepath.Join(local, "项目") {
		t.Fatalf("opened %q", h.a.place)
	}

	// Renamed, moved, removed from its menu.
	h.menu("收藏 照片", "重命名…")
	h.tt.Key(ui.Ctrl, ui.KeyA)
	h.tt.Type("家里的照片")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	h.want("收藏 家里的照片")
	h.menu("收藏 家里的照片", "上移")
	if h.a.store.Favorites[0].Name != "家里的照片" {
		t.Fatalf("order %+v", h.a.store.Favorites)
	}

	// A folder gone shows grayed, and says so when opened.
	if err := os.Remove(filepath.Join(local, "项目")); err != nil {
		t.Fatal(err)
	}
	h.a.recheckFavorites()
	deadline := time.Now().Add(5 * time.Second)
	for !h.a.favs.missing[filepath.Join(local, "项目")] && time.Now().Before(deadline) {
		h.tt.Frame()
		h.settle()
		time.Sleep(time.Millisecond)
	}
	if !h.a.favs.missing[filepath.Join(local, "项目")] {
		t.Fatal("missing folder not found")
	}
	h.click("收藏 项目")
	if h.a.errorMsg == "" {
		t.Fatal("opened a folder gone")
	}
	h.menu("收藏 项目", "从收藏中移除")
	if len(h.a.store.Favorites) != 1 {
		t.Fatalf("favorites %+v", h.a.store.Favorites)
	}
}

func TestPathBar(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "照片", "2025"), 0755)
	os.MkdirAll(filepath.Join(f.Root, "照片", "2026"), 0755)
	os.WriteFile(filepath.Join(f.Root, "照片", "说明.txt"), []byte("x"), 0644)
	h := newHarness(t, signedIn(t, f))
	h.settle()
	h.a.goPlace(rootPlace(h.a.store.Servers[0].ID, 1))
	h.settle()
	h.a.browse("照片/2025")
	h.settle()

	// The arrow after a folder lists the folders in it.
	h.dropdown("“照片”中的文件夹", "2026")
	if h.a.br.path != "照片/2026" {
		t.Fatalf("went to %q", h.a.br.path)
	}
	h.click("nas")
	if h.a.br.path != "" {
		t.Fatalf("went to %q", h.a.br.path)
	}

	// Editing: the whole path is text, each part suggested as it is
	// typed.
	h.click("编辑路径")
	if h.a.pathEditing == nil || h.a.pathEditing.text != "家里/nas/" {
		t.Fatalf("editing %+v", h.a.pathEditing)
	}
	h.want("照片") // the folders in it, at once
	h.tt.Key(ui.Ctrl, ui.KeyA)
	h.tt.Type("家") // the servers, and the places of this computer
	h.want("家里")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if h.a.pathEditing.text != "家里/" {
		t.Fatalf("completed %q", h.a.pathEditing.text)
	}
	h.want("nas") // its roots
	h.want("只读")
	h.tt.Type("n")
	h.tt.Key(0, ui.KeyTab)
	h.settle()
	h.tt.Type("照")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if h.a.pathEditing.text != "家里/nas/照片/" {
		t.Fatalf("completed %q", h.a.pathEditing.text)
	}
	h.want("2025")
	h.tt.Type("2026")
	h.tt.Key(0, ui.KeyEnter) // takes 2026/
	h.settle()
	h.tt.Key(0, ui.KeyEnter) // nothing more to take: goes there
	h.settle()
	if h.a.pathEditing != nil || h.a.br.path != "照片/2026" || h.a.errorMsg != "" {
		t.Fatalf("went to %q, %s", h.a.br.path, h.a.errorMsg)
	}

	// An invalid address reports an error and keeps the current directory.
	h.click("编辑路径")
	h.tt.Key(ui.Ctrl, ui.KeyA)
	h.tt.Type("家里/nas/照片/没有")
	h.click("前往")
	if !h.a.onRoot() || h.a.br.path != "照片/2026" || h.a.errorMsg == "" {
		t.Fatalf("invalid path: %s, %q", h.a.location(), h.a.errorMsg)
	}
	h.a.errorMsg = ""
	h.a.showDirectory(h.a.store.Servers[0].ID, h.a.roots[h.a.store.Servers[0].ID][0], "照片/2026")
	h.settle()

	// Of this computer: its places are suggested first, as full paths.
	local := t.TempDir()
	os.MkdirAll(filepath.Join(local, "子目录"), 0755)
	os.MkdirAll(filepath.Join(local, ".隐藏"), 0755)
	os.WriteFile(filepath.Join(local, "a.txt"), []byte("a"), 0644)
	h.a.localRoots = []localRoot{{"临时", local}}
	h.click("编辑路径")
	h.tt.Key(ui.Ctrl, ui.KeyA)
	h.tt.Type("临")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if h.a.pathEditing.text != local+string(filepath.Separator) {
		t.Fatalf("completed %q", h.a.pathEditing.text)
	}
	h.want("子目录")
	if h.tt.HasText(".隐藏") {
		t.Error("hidden folder suggested")
	}
	h.tt.Type("子")
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if !h.a.onLocal() || h.a.local.dir != filepath.Join(local, "子目录") {
		t.Fatalf("went to %q %v", h.a.place, h.a.local)
	}

	// A file goes to its folder, chosen.
	h.click("编辑路径")
	h.tt.Key(ui.Ctrl, ui.KeyA)
	h.tt.Type(filepath.Join(local, "a.txt"))
	h.click("前往")
	if !h.a.onLocal() || h.a.local.dir != local || !h.a.local.sel.Has(filepath.Join(local, "a.txt")) {
		t.Fatalf("went to %q", h.a.place)
	}
	// Escape leaves the path as it was.
	h.click("编辑路径")
	h.tt.Type("elsewhere")
	h.tt.Key(0, ui.KeyEscape)
	h.settle()
	h.tt.Key(0, ui.KeyEscape)
	h.settle()
	if h.a.pathEditing != nil || h.a.local.dir != local {
		t.Fatal("escape did not cancel")
	}
}

// A favorite, of a server or of this computer, is a folder to paste into
// from the sidebar.
func TestPasteIntoFavorites(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "目标", "深处"), 0755)
	os.WriteFile(filepath.Join(f.Root, "笔记.txt"), []byte("notes"), 0644)
	store := signedIn(t, f)
	dest := t.TempDir()
	store.AddFavorite(&config.Favorite{Name: "深处", Server: store.Servers[0].ID, Root: 1, RootName: "nas", Path: "目标/深处"})
	store.AddFavorite(&config.Favorite{Name: "本机下载", Local: dest})
	h := newHarness(t, store)
	h.settle()
	h.a.goPlace(rootPlace(store.Servers[0].ID, 1))
	h.settle()
	h.clickPage("笔记.txt")
	h.tt.Key(ui.Ctrl, ui.KeyC)

	h.click("收藏 深处")
	h.settle()
	h.tt.Key(ui.Ctrl, ui.KeyV)
	h.waitStarted(1)
	h.waitTransfers()
	if b, _ := os.ReadFile(filepath.Join(f.Root, "目标", "深处", "笔记.txt")); string(b) != "notes" {
		t.Fatalf("copied %q", b)
	}
	h.click("收藏 本机下载")
	h.settle()
	h.tt.Key(ui.Ctrl, ui.KeyV)
	h.waitStarted(2)
	h.waitTransfers()
	if b, _ := os.ReadFile(filepath.Join(dest, "笔记.txt")); string(b) != "notes" {
		t.Fatalf("downloaded %q", b)
	}
}

// A path typed starts with a server's name, up to a slash: the name has
// none.
func TestServerNameWithoutSlash(t *testing.T) {
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.openServers()
	h.a.nasStartEdit(nil)
	h.a.nas.name, h.a.nas.addr = "家/里", "nas.local:8090"
	h.a.nasSaveServer()
	if h.a.nas.err == "" || len(h.a.store.Servers) != 0 {
		t.Fatalf("saved %q, %v", h.a.nas.err, h.a.store.Servers)
	}
}

// Sections toggle through their arrows; directory rows navigate without
// expanding, and local roots share the favorites section's level.
func TestSidebarArrowOnlyExpansion(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "照片", "2026"), 0755)
	store := signedIn(t, f)
	store.AddServer(&config.Server{Name: "公司", Addr: "127.0.0.1:1"}) // signed out
	h := newHarness(t, store)
	local := t.TempDir()
	os.MkdirAll(filepath.Join(local, "项目", "代码"), 0755)
	h.a.localRoots = []localRoot{{"临时", local}}
	h.settle()

	if _, ok := h.tt.Find("本机"); ok {
		t.Fatal("obsolete local grouping is visible")
	}
	favoriteRow, _ := h.tt.Find("收藏")
	localRow, _ := h.tt.Find("本机文件夹 临时")
	if favoriteRow.X != localRow.X {
		t.Fatal("favorites and local places are not peers")
	}
	h.click("收藏")
	h.click("家里")
	for _, key := range []string{"favorites", "server/" + store.Servers[0].ID} {
		if !*h.a.sectionOpen(key) {
			t.Fatalf("%s starts closed", key)
		}
	}
	h.treeArrow("收藏", 0)
	if *h.a.sectionOpen("favorites") {
		t.Fatal("收藏 did not close")
	}
	h.treeArrow("收藏", 0)
	if !*h.a.sectionOpen("favorites") {
		t.Fatal("收藏 did not open")
	}
	h.treeArrow("家里", 0)
	if *h.a.sectionOpen("server/" + store.Servers[0].ID) {
		t.Fatal("家里 did not close")
	}
	if h.tt.HasText("文件夹 照片") {
		t.Fatal("closed server still shows its folders")
	}
	h.treeArrow("家里", 0)
	h.want("文件夹 nas")
	// Signed-out servers stay in management, not the navigation tree.
	if h.tt.HasText("公司") {
		t.Fatal("signed-out server is visible")
	}
	h.a.openLogin(store.Servers[1].ID, nil)
	h.settle()
	if !h.a.nas.open || h.a.nas.screen != screenLogin {
		t.Fatalf("公司 opened %+v", h.a.nas.screen)
	}
	h.a.nas.open = false
	h.tt.Frame()

	// Clicking a row navigates without expanding it; only its arrow toggles.
	h.click("本机文件夹 临时")
	if h.a.localNode(local, local).open {
		t.Fatal("selection expanded local root")
	}
	h.treeArrow("本机文件夹 临时", 0)
	h.want("本机文件夹 项目")
	h.click("本机文件夹 项目")
	if h.a.local.dir != filepath.Join(local, "项目") {
		t.Fatalf("opened %q", h.a.local.dir)
	}
	if h.a.localNode(local, filepath.Join(local, "项目")).open {
		t.Fatal("selection expanded directory")
	}
	h.treeArrow("本机文件夹 项目", 1)
	h.want("本机文件夹 代码")
	if out := os.Getenv("FILESYNC_SIDEBAR_SNAPSHOT"); out != "" {
		saveResumeSnapshot(t, h, out)
	}
	// A server section closes through its arrow.
	h.treeArrow("家里", 0)
	if *h.a.sectionOpen("server/" + h.a.store.Servers[0].ID) || h.tt.HasText("文件夹 照片") {
		t.Fatal("the server's section did not close")
	}
}
