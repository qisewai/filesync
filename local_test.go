package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"filesync/config"
	"filesync/fileindex"
	"filesync/fileindex/fitest"
)

// The sidebar shows the computer's places, their directories read as they
// open, and the page of one lists it.
func TestLocalFiles(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "文档", "合同"), 0o755)
	os.WriteFile(filepath.Join(home, "文档", "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(home, ".隐藏"), []byte("h"), 0o644)
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.localRoots = []localRoot{{"主目录", home}}
	h.tt.Frame()

	h.click("本机文件夹 主目录")
	if h.a.place != localPlace || h.a.local.dir != home {
		t.Fatalf("place %q", h.a.place)
	}
	h.want("文档")
	if h.tt.HasText(".隐藏") {
		t.Error("a hidden file shows")
	}
	h.more("显示隐藏文件")
	h.want(".隐藏")

	// The tree opens a directory chosen in it.
	h.click("本机文件夹 主目录") // already shown: nothing changes
	h.a.localNode(home, home).open = true
	h.tt.Frame()
	h.settle()
	h.click("本机文件夹 文档")
	if h.a.local.dir != filepath.Join(home, "文档") {
		t.Fatalf("dir %q", h.a.local.dir)
	}
	h.want("a.txt")
	h.want("合同")
	h.click("上级文件夹")
	if h.a.local.dir != home || h.a.local.sel.Len() != 1 || !h.a.local.sel.Has(filepath.Join(home, "文档")) {
		t.Fatalf("up to %q, chose %d", h.a.local.dir, h.a.local.sel.Len())
	}

	// Without a choice, the directory shown is backed up, as itself.
	h.a.local.sel.Clear()
	h.tt.Frame()
	h.localMenu("备份当前文件夹…")
	if d := h.a.draft; d.source != home || d.items != nil || !d.withDir {
		t.Fatalf("draft %+v", d)
	}
	h.click("取消")
}

func TestDiskImageKind(t *testing.T) {
	for _, name := range []string{"win10.vmdk", "系统.GHO", "ubuntu.iso", "vm.qcow2", "backup.tib"} {
		if k := fileKind(fileindex.Entry{Ext: extOf(name)}); k != "disk" {
			t.Errorf("%s is %q", name, k)
		}
	}
	if categoryLabel("disk") != "磁盘镜像" {
		t.Error("no label")
	}
}

// The home directory shows under the file system's root too: both open,
// level after level, each as it is opened.
func TestLocalTreeNestedPlaces(t *testing.T) {
	top := t.TempDir()
	home := filepath.Join(top, "home", "ning")
	os.MkdirAll(filepath.Join(home, "一层", "二层", "三层"), 0o755)
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.localRoots = []localRoot{{"主目录", home}, {"文件系统", top}}
	h.tt.Frame()
	h.settle()
	open := func(place, dir string) {
		h.a.localNode(place, dir).open = true
		h.tt.Frame()
		h.settle()
	}
	// Down the file system, through the home directory and below it.
	dir := top
	for _, name := range []string{"home", "ning", "一层", "二层", "三层"} {
		open(top, dir)
		if !h.tt.HasText("本机文件夹 " + name) {
			t.Fatalf("%s under the file system does not show %s", dir, name)
		}
		dir = filepath.Join(dir, name)
	}
	// The home directory's own place opens apart.
	if h.a.localNode(home, home).open {
		t.Fatal("opening under the file system opened the home directory's place")
	}
	open(home, home)
	open(home, filepath.Join(home, "一层"))
	if n := h.a.localNode(home, filepath.Join(home, "一层")); !n.loaded || len(n.dirs) != 1 {
		t.Fatalf("under the home directory %+v", n)
	}
	// Chosen, the directory shows chosen under its nearest place.
	h.a.openLocal(filepath.Join(home, "一层"))
	h.settle()
	if h.a.localTop(filepath.Join(home, "一层")) != home {
		t.Fatal("nearest place")
	}
}

// A directory with none inside is a leaf, known before it is opened: no
// arrow opens it to nothing. Shares on the network are not read for it.
func TestLocalTreeLeaves(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "有子目录", "子"), 0o755)
	os.MkdirAll(filepath.Join(home, "空的"), 0o755)
	os.WriteFile(filepath.Join(home, "空的", "只有文件.txt"), nil, 0o644)
	os.MkdirAll(filepath.Join(home, "隐藏的", ".git"), 0o755)
	os.MkdirAll(filepath.Join(home, "网络盘", "目录"), 0o755)
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.localRoots = []localRoot{{"主目录", home}}
	h.a.netMounts = []string{filepath.Join(home, "网络盘")}
	h.a.localNode(home, home).open = true
	h.tt.Frame()
	h.settle()
	node := func(name string) *directoryNode { return h.a.localNode(home, filepath.Join(home, name)) }
	if !node("空的").leaf() || !node("隐藏的").leaf() {
		t.Fatal("a directory without directories (but hidden ones) is not a leaf")
	}
	if node("有子目录").leaf() || node("有子目录").loaded {
		t.Fatal("a directory with directories is a leaf, or was read before opening")
	}
	if node("网络盘").loaded {
		t.Fatal("a share on the network was read")
	}
	// Shown with hidden files, a hidden directory is one to open.
	h.a.openLocal(home)
	h.settle()
	h.more("显示隐藏文件")
	h.settle()
	if node("隐藏的").leaf() {
		t.Fatal("with hidden files shown, .git makes no directory")
	}
}

// A directory of a server that does not tell whether it holds any,
// opened to nothing, becomes a leaf.
func TestDirectoryTreeLeaf(t *testing.T) {
	f := fitest.New(t)
	f.OldServer.Store(true)
	os.MkdirAll(filepath.Join(f.Root, "空目录"), 0o755)
	h := browserHarness(t, f)
	sv := h.a.store.Servers[0].ID
	h.a.treeNode(sv, 1, "").open = true
	h.tt.Frame()
	h.settle()
	n := h.a.treeNode(sv, 1, "空目录")
	if n.leaf() {
		t.Fatal("a leaf before it was read")
	}
	n.open = true
	h.tt.Frame()
	h.settle()
	h.tt.Frame()
	if !n.leaf() || n.open {
		t.Fatalf("read to nothing, not a leaf: %+v", n)
	}
	if h.tt.HasText("正在读取…") {
		t.Error("a quick read shows that it reads")
	}
}

// The server tells which directories hold none: leaves before they are
// opened. One that comes to hold some can be opened again.
func TestDirectoryTreeLeavesFromServer(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "空目录"), 0o755)
	os.MkdirAll(filepath.Join(f.Root, "有子目录", "子"), 0o755)
	h := browserHarness(t, f)
	sv := h.a.store.Servers[0].ID
	h.a.treeNode(sv, 1, "").open = true
	h.tt.Frame()
	h.settle()
	if !h.a.treeNode(sv, 1, "空目录").leaf() {
		t.Fatal("told empty, not a leaf")
	}
	if n := h.a.treeNode(sv, 1, "有子目录"); n.leaf() || n.loaded {
		t.Fatal("a directory holding one is a leaf, or was read")
	}
	os.MkdirAll(filepath.Join(f.Root, "空目录", "新"), 0o755)
	h.a.refreshDirectory(h.a.store.Servers[0], h.a.roots[sv][0], "")
	h.tt.Frame()
	h.settle()
	if h.a.treeNode(sv, 1, "空目录").leaf() {
		t.Fatal("holds one now, still a leaf")
	}

	// An older server tells nothing: opened to nothing, it is one then.
	f.OldServer.Store(true)
	os.MkdirAll(filepath.Join(f.Root, "旧服务器"), 0o755)
	h.a.refreshDirectory(h.a.store.Servers[0], h.a.roots[sv][0], "")
	h.tt.Frame()
	h.settle()
	if h.a.treeNode(sv, 1, "旧服务器").leaf() {
		t.Fatal("a leaf without the server telling")
	}
}

func TestUserDirs(t *testing.T) {
	home := t.TempDir()
	conf := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", conf)
	for _, d := range []string{"桌面", "下载", "媒体"} {
		os.MkdirAll(filepath.Join(home, d), 0o755)
	}
	os.WriteFile(filepath.Join(conf, "user-dirs.dirs"), []byte(`# written by xdg-user-dirs-update
XDG_DESKTOP_DIR="$HOME/桌面"
XDG_DOWNLOAD_DIR="$HOME/下载"
XDG_TEMPLATES_DIR="$HOME/"
XDG_DOCUMENTS_DIR="$HOME/"
XDG_MUSIC_DIR="$HOME/媒体"
XDG_VIDEOS_DIR="$HOME/媒体"
`), 0o644)
	got := userDirs(home)
	want := []localRoot{{"桌面", filepath.Join(home, "桌面")}, {"下载", filepath.Join(home, "下载")}, {"音乐", filepath.Join(home, "媒体")}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("user dirs %+v", got)
	}
}
