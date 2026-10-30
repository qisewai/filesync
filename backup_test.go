package main

import (
	"os"
	"path/filepath"
	"testing"

	"filesync/config"
	"filemanager/engine"
	"filemanager/remoteclient/fitest"
)

// A task's card backs it up at once: it compares, copies, and tells how
// it ended, in the card; again, there is nothing to do.
func TestBackUpAtOnce(t *testing.T) {
	f := fitest.New(t)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644)
	store := signedIn(t, f)
	store.Add(&config.Task{Name: "文档", Source: src, Server: store.Servers[0].ID, Root: 1, RootName: "nas", Path: "备份", Mode: engine.Incremental})
	h := newHarness(t, store)
	h.click("备份任务")
	h.want("还没有运行过")
	h.click("立即备份")
	h.want("备份完成")
	h.want("新增 1 · 更新 0 · 删除 0 · 1 B · 用时 0 秒")
	if _, err := os.Stat(filepath.Join(f.Root, "备份", "a.txt")); err != nil {
		t.Fatal(err)
	}
	// The result in full, then back to the cards.
	h.click("详情")
	h.want("打开日志")
	h.click("返回任务列表")
	if h.a.job != nil {
		t.Fatal("the result stayed")
	}
	h.want("上次")
	h.click("立即备份")
	h.want("目标已是最新：1 个文件都没有变化")
	h.click("关闭")
	if h.a.job != nil {
		t.Fatal("not closed")
	}

	// The details and the history show in place of the cards, a place of
	// the history of the window.
	h.taskMenu("详情与运行记录")
	h.want("运行记录")
	h.want("成功")
	if h.a.location() != "/backup?task="+store.Tasks[0].ID {
		t.Fatalf("location %q", h.a.location())
	}
	h.click("返回任务列表")
	h.want("1 个备份任务")
}

// What deletes on the NAS waits for a confirmation, in the preview.
func TestBackUpAtOnceAsksBeforeDeleting(t *testing.T) {
	f := fitest.New(t)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "keep.txt"), []byte("1"), 0o644)
	dst := filepath.Join(f.Root, "m")
	os.MkdirAll(dst, 0o755)
	os.WriteFile(filepath.Join(dst, "old.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dst, "keep.txt"), []byte("1"), 0o644)
	store := signedIn(t, f)
	store.Add(&config.Task{Name: "镜像", Source: src, Server: store.Servers[0].ID, Root: 1, RootName: "nas", Path: "m", Mode: engine.Mirror})
	h := newHarness(t, store)
	h.click("备份任务")
	h.click("立即备份")
	h.want("预览：镜像")
	if _, err := os.Stat(filepath.Join(dst, "old.txt")); err != nil {
		t.Fatal("deleted before the confirmation")
	}
	h.click("开始备份")
	h.want("备份完成")
	if _, err := os.Stat(filepath.Join(dst, "old.txt")); err == nil {
		t.Fatal("not deleted")
	}
}
