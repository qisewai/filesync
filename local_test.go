package main

import (
	"filesync/fileindex"
	"os"
	"path/filepath"
	"testing"

	"filesync/config"
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
	h.click("显示隐藏文件")
	h.want(".隐藏")

	// The tree opens a directory chosen in it.
	h.click("本机文件夹 主目录") // already shown: nothing changes
	h.a.localNode(home).open = true
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
	h.click("创建备份任务…")
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
