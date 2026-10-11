package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filesync/config"
	"filesync/localindex"
)

func restoreLocalHarness(t *testing.T, store *config.Store, cache, root string) *harness {
	t.Helper()
	h := newHarness(t, store)
	h.a.localCachePath = cache
	h.a.localRoots = []localRoot{{"测试文件夹", root}}
	h.a.openLocal(root)
	h.settle()
	return h
}

func TestLocalSnapshotRestartAndManualUpdate(t *testing.T) {
	root := t.TempDir()
	path := localWrite(t, root, "hello.txt", 1)
	h := localSearchHarness(t, root)
	cache := filepath.Join(t.TempDir(), "cache", "index.bin")
	h.a.localCachePath = cache
	if err := h.a.store.Save(); err != nil {
		t.Fatal(err)
	}
	h.indexMenu("更新索引")
	if h.a.localIdx.savedVersion != h.a.localIdx.status.Version {
		t.Fatal("successful scan not saved")
	}
	localWrite(t, root, "hello.txt", 8)
	localWrite(t, root, "new.txt", 4)
	store, err := config.Load(h.a.store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	h2 := restoreLocalHarness(t, store, cache, root)
	m := h2.a.localIdx
	if m.status.Count != 1 || m.running || m.progress.Scanned != 0 {
		t.Fatal("startup scanned the filesystem")
	}
	h2.replaceField("本机搜索", "hello")
	if h2.a.local.total != 1 || h2.a.local.items[0].Size != 1 || h2.a.local.items[0].Path != path {
		t.Fatal("snapshot was not restored")
	}
	h2.captureLocal("snapshot-loaded")
	h2.tt.SetSize(900, 480)
	h2.captureLocal("snapshot-minimum")
	h2.tt.SetSize(980, 680)
	h2.indexMenu("更新索引")
	if m.status.Count != 2 || h2.a.local.items[0].Size != 8 || m.savedVersion != m.status.Version {
		t.Fatal("manual update failed")
	}
	if m.progress.ReadDirs == 0 {
		t.Fatal("changed directory name list was reused")
	}
	h2.indexMenu("全部重建")
	if m.progress.ReusedDirs != 0 || m.progress.ReadDirs != 1 {
		t.Fatalf("full rebuild reused cached names: %+v", m.progress)
	}
}

func TestLocalSnapshotCorruptionAndScopeChange(t *testing.T) {
	root := t.TempDir()
	localWrite(t, root, "hello.txt", 1)
	store := &config.Store{Dir: t.TempDir(), LocalIndex: &config.LocalIndexSettings{Roots: []string{root}}}
	cache := filepath.Join(t.TempDir(), "index.bin")
	os.WriteFile(cache, []byte("damaged"), 0o600)
	h := restoreLocalHarness(t, store, cache, root)
	if h.a.localIdx.status.Count != 0 || !strings.Contains(h.a.localIdx.err, "全部重建") {
		t.Fatalf("corruption: %+v", h.a.localIdx)
	}
	h.indexMenu("全部重建")
	if h.a.localIdx.err != "" || h.a.localIdx.status.Count != 1 {
		t.Fatal("rebuild did not repair damaged cache")
	}
	h.indexMenu("索引范围…")
	h.replaceField("本机索引排除规则", "*.txt")
	h.click("保存索引设置")
	loaded, err := config.Load(store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	h2 := restoreLocalHarness(t, loaded, cache, root)
	if h2.a.localIdx.status.Count != 0 || h2.a.localIdx.running || h2.a.localIdx.err != "" {
		t.Fatal("old scope restored after settings change")
	}
	h2.indexMenu("更新索引")
	if h2.a.localIdx.status.Count != 0 {
		t.Fatal("new exclusion ignored")
	}
	h3 := restoreLocalHarness(t, loaded, cache, root)
	if h3.a.localIdx.savedVersion == 0 || h3.a.localIdx.status.Count != 0 {
		t.Fatal("empty snapshot failed to restore")
	}
}

func TestLocalSnapshotLoadingAndSaveFailure(t *testing.T) {
	root := t.TempDir()
	localWrite(t, root, "hello.txt", 1)
	h := localSearchHarness(t, root)
	// The normal manual request stays searchable even if the cache is unwritable.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	os.WriteFile(blocked, []byte("file"), 0o600)
	h.a.localCachePath = filepath.Join(blocked, "index.bin")
	h.indexMenu("更新索引")
	if !strings.Contains(h.a.localIdx.err, "快照保存失败") || h.a.localIdx.status.Count != 1 || h.a.localIdx.savedVersion != 0 {
		t.Fatal("write failure lost search data or claimed durability")
	}
	cache := filepath.Join(t.TempDir(), "index.bin")
	h.a.localCachePath = cache
	h.indexMenu("更新索引")
	if h.a.localIdx.err != "" || h.a.localIdx.savedVersion == 0 {
		t.Fatal("could not recover from write failure")
	}
	h.a.localIdx.index = localindex.New()
	h.a.localIdx.status = localindex.Status{}
	h.a.loadLocalSnapshot()
	h.tt.Frame()
	h.want("正在加载索引…")
	h.captureLocal("snapshot-loading")
	// Searches started while the loader publishes its version are refreshed
	// through the same UI queue as the normal startup path.
	h.a.local.query = "hello"
	h.a.queueLocalSearch(0)
	h.settle()
	if h.a.localIdx.loading || h.a.local.total != 1 {
		t.Fatal("loaded version did not refresh the query")
	}
	if _, err := h.a.localIdx.index.Search(context.Background(), localindex.Query{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalSnapshotRetainsUnreadableRecordsAndWarnings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions")
	}
	root := t.TempDir()
	localWrite(t, root, "locked/keep.txt", 1)
	localWrite(t, root, "other.txt", 1)
	h := localSearchHarness(t, root)
	cache := filepath.Join(t.TempDir(), "index.bin")
	h.a.localCachePath = cache
	h.indexMenu("更新索引")
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	if f, err := os.Open(locked); err == nil {
		f.Close()
		t.Skip("permissions bypassed")
	}
	localWrite(t, root, "other.txt", 8)
	h.indexMenu("更新索引")
	if h.a.localIdx.progress.Errors == 0 || h.a.localIdx.status.Count != 3 {
		t.Fatal("unreadable records were discarded")
	}
	h2 := restoreLocalHarness(t, h.a.store, cache, root)
	if h2.a.localIdx.progress.Errors == 0 || !strings.Contains(strings.Join(h2.a.localIdx.progress.Messages, "\n"), locked) {
		t.Fatal("unreadable-path warning did not survive restart")
	}
	h2.replaceField("本机搜索", "keep")
	if h2.a.local.total != 1 {
		t.Fatal("unreadable record did not survive restart")
	}
}
