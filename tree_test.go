package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"filemanager/remoteclient"
	"filemanager/remoteclient/fitest"
)

// As files are uploaded, the server tells of many changes: the tree reads
// its directories again, once for many, and shows them as they were
// meanwhile, its leaves too, rather than empty, then filled again.
func TestDirectoryTreeKeepsShowingAsTheServerChanges(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "相册", "2024"), 0o755)
	os.MkdirAll(filepath.Join(f.Root, "空目录"), 0o755)
	h := browserHarness(t, f)
	sv := h.a.store.Servers[0].ID
	h.a.treeNode(sv, 1, "").open = true
	h.tt.Frame()
	h.settle()
	h.a.treeNode(sv, 1, "相册").open = true
	h.tt.Frame()
	h.settle()
	album, empty := h.a.treeNode(sv, 1, "相册"), h.a.treeNode(sv, 1, "空目录")
	if len(album.dirs) != 1 || !empty.leaf() || !h.tt.HasText("2024") {
		t.Fatal("not read", album.dirs, empty.leaf())
	}

	os.MkdirAll(filepath.Join(f.Root, "相册", "2025"), 0o755)
	for range 20 {
		h.a.onEvent(sv, fileindex.Event{Type: "fs", Root: 1})
	}
	reads := 0
	deadline := time.After(10 * time.Second)
	for len(album.dirs) != 2 {
		select {
		case fn := <-h.queue:
			fn()
			h.tt.Frame()
			if album.loading {
				reads++
			}
			if !h.tt.HasText("2024") || !empty.leaf() || h.tt.HasText("正在读取…") {
				t.Fatal("the tree changed as it was read again")
			}
		case <-deadline:
			t.Fatal("not read again")
		}
	}
	h.settle()
	if !h.tt.HasText("2025") {
		t.Fatal("the new directory not shown")
	}
	if reads > 2 {
		t.Fatalf("read %d times for one burst of changes", reads)
	}
}

// The file list too: read again as the server changes, it stays as it was
// meanwhile, not loading, its choice kept, then shows the new files.
func TestFileListKeepsShowingAsTheServerChanges(t *testing.T) {
	f := fitest.New(t)
	os.WriteFile(filepath.Join(f.Root, "a.jpg"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(f.Root, "b.jpg"), []byte("b"), 0o644)
	h := browserHarness(t, f)
	b := h.a.br
	if b == nil || len(b.items) != 2 {
		t.Fatal("not listed")
	}
	b.sel.Add(b.items[1].Path)
	b.cursor = 1
	chosen := b.items[1].Path

	os.WriteFile(filepath.Join(f.Root, "c.jpg"), []byte("c"), 0o644)
	sv := h.a.store.Servers[0].ID
	for range 20 {
		h.a.onEvent(sv, fileindex.Event{Type: "fs", Root: 1})
	}
	deadline := time.After(10 * time.Second)
	for len(b.items) != 3 {
		select {
		case fn := <-h.queue:
			fn()
			h.tt.Frame()
			if b.loading || !b.sel.Has(chosen) || h.tt.HasText("正在读取…") {
				t.Fatal("the list changed as it was read again")
			}
		case <-deadline:
			t.Fatal("not read again")
		}
	}
	h.settle()
	if !b.sel.Has(chosen) || b.cursor < 0 || b.items[b.cursor].Path != chosen {
		t.Fatal("the choice or the cursor not kept")
	}
}

func TestRemoteTreeSelectionDoesNotExpand(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "相册", "2025", "一月"), 0755)
	h := browserHarness(t, f)
	server := h.a.store.Servers[0].ID
	h.click("文件夹 相册")
	n := h.a.treeNode(server, 1, "相册")
	if n.open || h.a.br.path != "相册" {
		t.Fatal("row should navigate without expanding")
	}
	h.treeArrow("文件夹 相册", 2)
	if !n.open || h.a.br.path != "相册" {
		t.Fatal("arrow should expand without navigating")
	}
	h.click("文件夹 2025")
	if h.a.treeNode(server, 1, "相册/2025").open {
		t.Fatal("child selection expanded its branch")
	}
	h.treeArrow("文件夹 相册", 2)
	if n.open || h.a.br.path != "相册/2025" {
		t.Fatal("collapse changed selected directory")
	}
	h.click("文件夹 相册")
	if n.open {
		t.Fatal("selecting collapsed ancestor reopened it")
	}
}
