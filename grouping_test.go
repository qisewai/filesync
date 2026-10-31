package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"filemanager/remoteclient"
	"filesync/config"
	"github.com/egoist/mygo/ui"
)

func TestFileGroupBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	for _, tt := range []struct {
		e        fileindex.Entry
		by, want string
	}{
		{fileindex.Entry{Name: "x", IsDir: true}, "类型", "文件夹"},
		{fileindex.Entry{Name: "a.TXT"}, "类型", "TXT 文件"},
		{fileindex.Entry{Name: "README"}, "类型", "无扩展名文件"},
		{fileindex.Entry{Name: "目录", IsDir: true, Size: 999}, colSize, "文件夹"},
		{fileindex.Entry{Size: 0}, colSize, "空文件"},
		{fileindex.Entry{Size: 1 << 20}, colSize, "1–100 MB"},
		{fileindex.Entry{Size: 100 << 20}, colSize, "100 MB–1 GB"},
		{fileindex.Entry{Size: 1 << 30}, colSize, "1 GB 及以上"},
		{fileindex.Entry{Name: "apple"}, colName, "A"},
		{fileindex.Entry{Name: "文件"}, colName, "中文"},
		{fileindex.Entry{Name: "9file"}, colName, "0–9"},
		{fileindex.Entry{MTime: now.Unix()}, colTime, "今天"},
		{fileindex.Entry{MTime: now.AddDate(0, 0, -1).Unix()}, colTime, "昨天"},
		{fileindex.Entry{MTime: now.AddDate(0, 0, -6).Unix()}, colTime, "最近 7 天"},
		{fileindex.Entry{MTime: now.AddDate(0, 0, -29).Unix()}, colTime, "最近 30 天"},
		{fileindex.Entry{MTime: now.AddDate(0, 0, -31).Unix()}, colTime, "更早"},
	} {
		if got, _ := entryGroup(tt.e, tt.by, now); got != tt.want {
			t.Errorf("%+v %s: %q want %q", tt.e, tt.by, got, tt.want)
		}
	}
	es := []fileindex.Entry{{Name: "b.txt"}, {Name: "c.jpg"}, {Name: "a.txt"}}
	groups := buildFileGroups(es, "类型", false, now)
	if !reflect.DeepEqual(groups[1].items, []int{0, 2}) {
		t.Fatalf("lost source indices/order: %+v", groups)
	}
	rows := groupedRows(groups, map[string]bool{"TXT 文件": true}, 2)
	if len(rows) != 3 || len(rows[2].items) != 0 {
		t.Fatalf("folded rows: %+v", rows)
	}
}

func chooseGrouping(h *harness, name string) {
	h.t.Helper()
	if err := h.tt.RightClick("本机文件"); err != nil {
		h.t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("分组依据", name); err != nil {
		h.t.Fatal(err)
	}
	h.settle()
}

func TestGroupedFileViews(t *testing.T) {
	for _, grid := range []bool{false, true} {
		t.Run(map[bool]string{false: "list", true: "grid"}[grid], func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"a.txt", "b.jpg", "c.txt"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			h := newHarness(t, &config.Store{Dir: t.TempDir()})
			h.a.openLocal(dir)
			h.settle()
			if grid {
				h.click("网格视图")
			}
			chooseGrouping(h, "类型")
			h.want("TXT 文件（2）")
			if out := os.Getenv("FILESYNC_GROUP_SNAPSHOTS"); out != "" {
				name := "list.png"
				if grid {
					name = "grid.png"
				}
				h.tt.Advance(time.Second)
				saveResumeSnapshot(t, h, filepath.Join(out, name))
			}

			h.clickPage("c.txt")
			if !h.a.local.sel.Has(filepath.Join(dir, "c.txt")) || h.a.local.items[h.a.local.cursor].Name != "c.txt" {
				t.Fatal("grouped click mapped to wrong file")
			}
			h.click("分组：TXT 文件")
			h.want("TXT 文件（2）")
			if _, ok := h.tt.Find("c.txt"); ok {
				t.Fatal("collapsed file still shown")
			}
			h.click("分组：TXT 文件")
			h.clickPage("a.txt")
			h.tt.Key(ui.Shift, ui.KeyRight)
			if grid {
				h.settle()
				if h.a.local.sel.Len() != 2 || !h.a.local.sel.Has(filepath.Join(dir, "c.txt")) {
					t.Fatal("grid shift selection did not follow group order")
				}
			}
			h.tt.Key(ui.Ctrl, ui.KeyA)
			h.settle()
			if h.a.local.sel.Len() != 3 {
				t.Fatalf("select all: %d", h.a.local.sel.Len())
			}
			chooseGrouping(h, "无分组")
			if _, ok := h.tt.Find("分组：TXT 文件"); ok {
				t.Fatal("heading remains after disabling grouping")
			}
			if h.a.local.sel.Len() != 3 {
				t.Fatal("switch lost selection")
			}
		})
	}
}

func TestGroupedSearchKeepsResultIdentity(t *testing.T) {
	h, _, _ := searchHarness(t)
	h.a.openSearch("报告")
	h.settle()
	s := h.a.gsearch
	if err := h.tt.RightClick("搜索结果"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("分组依据", "类型"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.clickPage("年度报告.pdf")
	var chosen []searchHit
	for _, hit := range s.hits {
		if s.sel.Has(hit.key()) {
			chosen = append(chosen, hit)
		}
	}
	if len(chosen) != 1 || chosen[0].e.Name != "年度报告.pdf" || chosen[0].source == localPlace {
		t.Fatalf("wrong result: %+v", chosen)
	}
	h.menu("年度报告.pdf", "打开所在位置")
	if !h.a.onRoot() || h.a.br.path != "项目" {
		t.Fatal("opened incorrect result folder")
	}
}

func TestGroupedSearchPaginationAndCollapse(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 450; i++ {
		localWrite(t, dir, fmt.Sprintf("item%03d.txt", i), 1)
	}
	h := localSearchHarness(t, dir)
	h.indexMenu("更新索引")
	h.find("item")
	s := h.a.gsearch
	if err := h.tt.RightClick("搜索结果"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("分组依据", "类型"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	before := len(s.items)
	if before >= 450 {
		t.Fatalf("eagerly loaded all results: %d", before)
	}
	h.click("分组：TXT 文件")
	h.settle()
	if len(s.items) != before {
		t.Fatal("collapse loaded more results")
	}
	h.click("分组：TXT 文件")
	h.clickPage("item000.txt")
	h.tt.Key(0, ui.KeyEnd)
	h.settle()
	if len(s.items) <= before {
		t.Fatalf("end did not load next page: %d", len(s.items))
	}
}

func TestGroupedRefreshInvalidatesCache(t *testing.T) {
	dir := t.TempDir()
	localWrite(t, dir, "a.txt", 1)
	h := localSearchHarness(t, dir)
	chooseGrouping(h, "类型")
	if err := os.Rename(filepath.Join(dir, "a.txt"), filepath.Join(dir, "a.jpg")); err != nil {
		t.Fatal(err)
	}
	h.tt.Key(0, ui.KeyF5)
	h.settle()
	h.want("JPG 文件（1）")
	if h.tt.HasText("TXT 文件（1）") {
		t.Fatal("stale group after same-length refresh")
	}
}
