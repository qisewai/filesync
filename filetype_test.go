package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"filemanager/remoteclient"
	"filemanager/remoteclient/fitest"
	"filesync/config"
	"github.com/egoist/mygo/ui"
)

func TestFileTypeLabel(t *testing.T) {
	for _, tc := range []struct {
		entry fileindex.Entry
		want  string
	}{
		{fileindex.Entry{Name: "dir.txt", IsDir: true}, "文件夹"},
		{fileindex.Entry{Name: "report.TxT"}, "TXT 文件"},
		{fileindex.Entry{Name: "archive.tar.gz"}, "GZ 文件"},
		{fileindex.Entry{Name: "README"}, "文件"},
		{fileindex.Entry{Name: ".gitignore"}, "GITIGNORE 文件"},
	} {
		if got := fileTypeLabel(tc.entry); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.entry.Name, got, tc.want)
		}
	}
}

func TestFileTypeColumnLocalAndGrouped(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "目录"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"b.TXT", "a.jpg", "plain"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.openLocal(dir)
	h.settle()
	h.want("修改日期")
	h.want("类型")
	h.want("文件夹")
	h.want("JPG 文件")
	h.want("TXT 文件")
	h.clickPage("b.TXT")
	h.clickPage(colType)
	if got := names(h.a.local.items); !slices.Equal(got, []string{"目录", "plain", "a.jpg", "b.TXT"}) {
		t.Fatalf("ascending: %v", got)
	}
	h.clickPage(colType)
	if got := names(h.a.local.items); !slices.Equal(got, []string{"目录", "b.TXT", "a.jpg", "plain"}) {
		t.Fatalf("descending: %v", got)
	}
	if h.a.local.items[h.a.local.cursor].Name != "b.TXT" {
		t.Fatal("sorting lost selection")
	}
	if out := os.Getenv("FILESYNC_TYPE_PLAIN_SNAPSHOT"); out != "" {
		h.tt.Advance(time.Second)
		saveResumeSnapshot(t, h, out)
	}
	chooseGrouping(h, "类型")
	h.want("TXT 文件（1）")
	h.want("JPG 文件（1）")
	if out := os.Getenv("FILESYNC_TYPE_SNAPSHOT"); out != "" {
		h.tt.Advance(time.Second)
		saveResumeSnapshot(t, h, out)
	}
}

func TestFileTypeColumnSearchPagination(t *testing.T) {
	f := fitest.New(t)
	for i := range 120 {
		for _, ext := range []string{"jpg", "txt"} {
			if err := os.WriteFile(filepath.Join(f.Root, fmt.Sprintf("match-%03d.%s", i, ext)), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	h := browserHarness(t, f)
	h.click("文件夹 nas")
	h.find("match")
	h.clickPage(colType)
	s := h.a.gsearch
	if len(s.items) != searchPageSize || s.items[0].Ext != "jpg" || s.items[119].Ext != "jpg" || s.items[120].Ext != "txt" {
		t.Fatalf("wrong first sorted page: %d", len(s.items))
	}
	s.list.ScrollTo(len(s.items)-1, ui.End)
	h.tt.Frame()
	h.settle()
	if len(s.items) != 240 || s.items[239].Ext != "txt" {
		t.Fatalf("wrong sorted later page: %d", len(s.items))
	}
	h.clickPage(colType)
	if len(s.items) != searchPageSize || s.items[0].Ext != "txt" {
		t.Fatal("descending type sort did not reach server")
	}
}
