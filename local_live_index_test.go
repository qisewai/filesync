package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"filesync/config"
	"filesync/localindex"
)

func TestLocalOperationsRefreshActiveSearchWithoutManualScan(t *testing.T) {
	root := t.TempDir()
	old := localWrite(t, root, "report.txt", 8)
	h := localSearchHarness(t, root)
	h.a.localCachePath = filepath.Join(t.TempDir(), "index.bin")
	h.indexMenu("更新索引")
	if g := h.find("report"); g.total != 1 {
		t.Fatal("baseline search")
	}
	h.a.localRename(old)
	if err := h.a.naming.submit("report-renamed.txt"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	g := h.a.gsearch.group(localPlace)
	if g == nil || g.total != 1 || g.hits[0].e.Path != filepath.Join(root, "report-renamed.txt") {
		t.Fatalf("search not refreshed: %+v", g)
	}
	h.a.localMkdir(root)
	if err := h.a.naming.submit("report-folder"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if g = h.a.gsearch.group(localPlace); g.total != 2 {
		t.Fatalf("mkdir missing: %+v", g)
	}
	h.a.localDelete([]string{filepath.Join(root, "report-renamed.txt")}, true)
	h.a.asking.action()
	h.settle()
	if g = h.a.gsearch.group(localPlace); g.total != 1 || g.hits[0].e.Name != "report-folder" {
		t.Fatalf("delete still visible: %+v", g)
	}
	if h.a.localIdx.status.Count != 1 || !h.a.localIdx.status.Delta {
		t.Fatalf("status: %+v", h.a.localIdx.status)
	}
}

func TestStartupReplaysUnfinishedMoveAndCheckpointMismatch(t *testing.T) {
	root := t.TempDir()
	old := localWrite(t, root, "old.txt", 2)
	opts := localindex.ScanOptions{Roots: []string{root}}
	cache := filepath.Join(t.TempDir(), "index.bin")
	x := localindex.New()
	if _, err := x.Scan(context.Background(), opts, nil); err != nil {
		t.Fatal(err)
	}
	if err := x.SaveSnapshot(context.Background(), cache, opts, localindex.SnapshotMeta{}); err != nil {
		t.Fatal(err)
	}
	log, err := localindex.OpenOperationLog(cache + ".operations")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "new.txt")
	if _, err = log.Begin("move", old, target); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(old, target); err != nil {
		t.Fatal(err)
	}
	log.Close()
	h := newHarness(t, &config.Store{Dir: t.TempDir(), LocalIndex: &config.LocalIndexSettings{Roots: []string{root}}})
	h.a.localCachePath = cache
	h.a.initializeOperationLog()
	h.settle()
	result, err := h.a.localIdx.index.Search(context.Background(), localindex.Query{Hidden: true})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := result.Page(context.Background(), 0, 10)
	if err != nil || len(entries) != 1 || entries[0].Path != target {
		t.Fatalf("replay %+v %v", entries, err)
	}
	h.a.rebuildLocalIndex()
	h.settle()
	if h.a.localIdx.status.Delta || h.a.localIdx.err != "" {
		t.Fatalf("checkpoint: %+v", h.a.localIdx)
	}
}
