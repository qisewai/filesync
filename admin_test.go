package main

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filesync/config"
	"filesync/fileindex"
	"filesync/fileindex/fitest"
	"github.com/egoist/mygo/ui"
)

func (h *harness) replaceField(label, value string) {
	h.t.Helper()
	h.click(label)
	h.tt.Key(ui.Ctrl, ui.KeyA)
	h.tt.Type(value)
	h.tt.Frame()
	h.settle()
}
func (h *harness) captureAdmin(name string) {
	h.t.Helper()
	dir := os.Getenv("FILESYNC_ADMIN_SCREENSHOT_DIR")
	if dir == "" {
		return
	}
	file, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		h.t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, h.tt.Image()); err != nil {
		h.t.Fatal(err)
	}
}
func TestNativeRootManagementPickerEditAndRemove(t *testing.T) {
	f := fitest.New(t)
	os.WriteFile(filepath.Join(f.ManageData, "new", "kept.txt"), []byte("keep"), 0644)
	h := browserHarness(t, f)
	h.click("目录管理")
	h.want("索引目录管理列表")
	h.click("添加索引目录…")
	h.click("浏览服务器目录…")
	h.click("📁 new")
	h.click("使用当前目录")
	if h.a.admin.draft.path != "/data/new" {
		t.Fatal("NAS picker didn't retain path")
	}
	h.replaceField("目录名称", "测试目录")
	h.click("自动更新频率")
	h.click("每天")
	h.replaceField("更新时间", "03:15")
	h.replaceField("索引排除规则", "*.tmp\n@eaDir")
	h.captureAdmin("directory-editor")
	h.click("保存目录设置")
	h.want("测试目录")
	h.want("每天 03:15 自动更新")
	h.captureAdmin("directory-management")
	roots, err := fileindex.New(f.Addr(), "tok").Roots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var created fileindex.Root
	for _, r := range roots {
		if r.Name == "测试目录" {
			created = r
		}
	}
	if created.ID == 0 || created.Schedule != "daily:03:15" || len(created.Excludes) != 2 {
		t.Fatalf("created %+v", created)
	}
	h.click("测试目录")
	h.click("编辑目录…")
	h.replaceField("目录名称", "改名目录")
	h.click("只读目录")
	h.click("保存目录设置")
	h.want("改名目录")
	h.click("改名目录")
	h.click("打开目录")
	h.settle()
	if h.a.br == nil || !h.a.br.root.ReadOnly {
		t.Fatal("created readonly root not browsable")
	} // reselect because save resets the root cursor
}
func TestNativeRootRemovalLeavesFilesAndUpdatesBrowser(t *testing.T) {
	f := fitest.New(t)
	os.WriteFile(filepath.Join(f.Root, "kept.txt"), []byte("original"), 0644)
	h := browserHarness(t, f)
	h.click("目录管理")
	h.click("nas")
	h.click("编辑目录…")
	h.click("只读目录")
	h.click("保存目录设置")
	if !h.a.br.root.ReadOnly {
		t.Fatal("browser kept stale write permission")
	}
	h.click("nas")
	h.click("移除索引…")
	h.click("取消")
	if len(h.a.roots[h.a.admin.server]) != 2 {
		t.Fatal("cancel removed root")
	}
	h.click("移除索引…")
	h.click("确认移除索引")
	if h.a.br != nil {
		t.Fatal("removed root still browsable")
	}
	data, err := os.ReadFile(filepath.Join(f.Root, "kept.txt"))
	if err != nil || string(data) != "original" {
		t.Fatal("root removal deleted files")
	}
}
func TestNativeScanEventsHistoryRebuildAndCancel(t *testing.T) {
	f := fitest.New(t)
	h := browserHarness(t, f)
	h.a.noEvents = false
	h.a.watch(h.a.store.Servers[0])
	h.click("目录管理")
	h.click("nas")
	h.click("更新索引")
	h.waitTransfers()
	if h.a.transfers[0].files != 7 || h.a.transfers[0].scanJob == 0 {
		t.Fatal("scan progress missing")
	}
	// Reconcile directory metadata after the debounced completion event.
	h.a.reloadAdmin()
	h.settle()
	h.click("扫描记录")
	h.want("扫描 7 · 新增 3 · 更新 0 · 删除 0 · 错误 0")
	h.click("关闭扫描记录")
	h.click("完整校验…")
	h.click("开始完整校验")
	h.waitTransfers()
	jobs, err := fileindex.New(f.Addr(), "tok").ScanJobs(context.Background(), 1)
	if err != nil || jobs[0].Mode != "rebuild" {
		t.Fatalf("history %+v %v", jobs, err)
	}
	h.a.reloadAdmin()
	h.settle()
	f.HoldScans.Store(true)
	h.click("更新索引")
	// Wait only for the POST response, then immediately request cancellation.
	for h.a.transfers[0].scanJob == 0 {
		fn := <-h.queue
		fn()
		h.tt.Frame()
	}
	tr := h.a.transfers[0]
	tr.cancel()
	deadline := time.After(5 * time.Second)
	for tr.running() {
		select {
		case fn := <-h.queue:
			fn()
			h.tt.Frame()
		case <-deadline:
			t.Fatal("scan cancellation not observed")
		}
	}
	if tr.state != "canceled" {
		t.Fatalf("cancel %s %s", tr.state, tr.err)
	}
	h.settle()
}
func TestScanSnapshotRecoversMissingEventsAndKeepsProgress(t *testing.T) {
	f := fitest.New(t)
	f.DropTaskEvents.Store(true)
	h := browserHarness(t, f)
	h.click("目录管理")
	h.click("nas")
	h.click("更新索引")
	h.waitTransfers()
	tr := h.a.transfers[0]
	if tr.files != 7 || tr.state != "done" {
		t.Fatal("missing event recovery")
	}
	server := h.a.admin.server
	h.a.applyScan(server, &fileindex.ScanJob{ID: 999, RootID: 1, Status: "running", Scanned: 99}, "folder/file")
	h.a.applyScan(server, &fileindex.ScanJob{ID: 999, RootID: 1, Status: "running", Scanned: 0}, "")
	for _, r := range h.a.roots[server] {
		if r.ID == 1 && (r.Active.Job.Scanned != 99 || r.Active.Current != "folder/file") {
			t.Fatal("snapshot overwrote live progress")
		}
	}
}
func TestNativeDuplicatesProtectLastCopyAndReadOnly(t *testing.T) {
	f := fitest.New(t)
	for _, name := range []string{"a.txt", "b.txt"} {
		os.WriteFile(filepath.Join(f.Root, name), []byte("identical"), 0644)
	}
	os.WriteFile(filepath.Join(f.ReadOnly, "c.txt"), []byte("identical"), 0644)
	h := browserHarness(t, f)
	h.click("查重")
	m := h.a.admin
	m.minSize = "全部非空文件"
	h.a.reloadAdmin()
	h.settle()
	if m.dupes.Summary.Groups != 1 || len(m.dupes.Groups[0].Files) != 3 {
		t.Fatalf("dupes %+v", m.dupes)
	}
	h.captureAdmin("duplicates")
	h.click("本页保留最早")
	entries, err := h.a.duplicateSelection(m)
	if err != nil || len(entries) != 1 {
		t.Fatalf("selection %+v %v", entries, err)
	}
	// A forged selection must also be rejected at execution, independent of UI.
	for _, e := range m.dupes.Groups[0].Files {
		m.selected[e.ID] = true
	}
	if _, err = h.a.duplicateSelection(m); err == nil {
		t.Fatal("accepted deletion of all copies / readonly file")
	}
	h.click("清除副本勾选")
	h.click("本页保留最新")
	entries, err = h.a.duplicateSelection(m)
	if err != nil || len(entries) != 2 {
		t.Fatalf("selection %+v %v", entries, err)
	}
	h.click("删除勾选副本… (2)")
	h.click("确认移到回收站")
	if m.dupes.Summary.Groups != 0 {
		t.Fatal("dupes not refreshed after trash")
	}
	items, err := fileindex.New(f.Addr(), "tok").ListTrash(context.Background())
	if err != nil || len(items) != 2 {
		t.Fatal("copies not in trash")
	}
	if _, err := os.Stat(filepath.Join(f.ReadOnly, "c.txt")); err != nil {
		t.Fatal("read-only original missing")
	}
}
func TestNativeStatisticsDrillLocateAndSourceLifecycle(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "目录", "子目录"), 0755)
	os.WriteFile(filepath.Join(f.Root, "目录", "子目录", "large.txt"), []byte(strings.Repeat("x", 20)), 0644)
	os.WriteFile(filepath.Join(f.Root, "loose.txt"), []byte("loose"), 0644)
	h := browserHarness(t, f)
	h.click("空间统计")
	h.want("总大小")
	if h.a.admin.overview.Size != 25 {
		t.Fatal("overview size")
	}
	if out := os.Getenv("FILESYNC_ADMIN_SCREENSHOT"); out != "" {
		file, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(file, h.tt.Image())
		file.Close()
	}
	// This nas button is in the stats card, while the sidebar uses 📁 nas.
	h.click("nas")
	h.click("目录")
	if h.a.admin.statsDir != "目录" {
		t.Fatal("root usage drill")
	}
	h.click("子目录")
	if h.a.admin.statsDir != "目录/子目录" {
		t.Fatal("nested usage drill")
	}
	h.click("统计上一级")
	if h.a.admin.statsDir != "目录" {
		t.Fatal("directory up")
	}
	h.click("统计目录顶层")
	if h.a.admin.statsDir != "" {
		t.Fatal("directory top")
	}
	e := h.a.admin.largest[0]
	h.a.locateManagedEntry(h.a.admin.server, e)
	h.settle()
	h.want("large.txt")
	server := h.a.admin.server
	h.a.openAdmin(server, "stats")
	h.a.openAdmin(server, "roots")
	h.settle()
	if h.a.admin.tab != "roots" {
		t.Fatal("late stats revived old page")
	}
	f.Expired.Store(true)
	h.a.reloadAdmin()
	h.settle()
	if h.a.admin != nil || h.a.store.Server(server).Token != "" {
		t.Fatal("auth expiry not cleared")
	}
}
func TestScheduleValidationAndRoundTrip(t *testing.T) {
	for _, spec := range []string{"", "hourly:24", "daily:02:30", "weekly:6:04:00"} {
		d := newRootDraft(&fileindex.Root{Schedule: spec})
		got, err := d.schedule()
		if err != nil || got != spec {
			t.Fatalf("%q -> %q %v", spec, got, err)
		}
	}
	d := newRootDraft(nil)
	d.scheduleKind = "每隔几小时"
	d.hours = "0"
	if _, err := d.schedule(); err == nil {
		t.Fatal("invalid interval accepted")
	}
	d.scheduleKind = "每天"
	d.clock = "25:00"
	if _, err := d.schedule(); err == nil {
		t.Fatal("invalid time accepted")
	}
	if remoteParentAllowed([]string{"/data"}, "/data") || !remoteParentAllowed([]string{"/data"}, "/data/nas") {
		t.Fatal("NAS picker boundary")
	}
}
func TestRootEditKeepsReferencedBackupSettings(t *testing.T) {
	f := fitest.New(t)
	st := signedIn(t, f)
	st.Add(&config.Task{Name: "backup", Server: st.Servers[0].ID, Root: 1, RootName: "nas"})
	h := newHarness(t, st)
	h.settle()
	h.click("目录管理")
	h.click("nas")
	h.click("移除索引…")
	h.want("当前备份任务仍引用此目录")
	h.click("取消")
	if st.Tasks[0].Root != 1 {
		t.Fatal("management changed backup settings")
	}
}

func TestScanTerminalSnapshotsAndSourceChanges(t *testing.T) {
	f := fitest.New(t)
	h := browserHarness(t, f)
	h.click("目录管理")
	server := h.a.admin.server
	done := &fileindex.ScanJob{ID: 999, RootID: 1, Status: "done", Scanned: 99}
	h.a.applyScan(server, done, "")
	h.a.applyScan(server, &fileindex.ScanJob{ID: 999, RootID: 1, Status: "running"}, "late")
	roots := append([]fileindex.Root(nil), h.a.roots[server]...)
	for i := range roots {
		if roots[i].ID == 1 {
			roots[i].LastJob = &fileindex.ScanJob{ID: 999, RootID: 1, Status: "queued"}
			roots[i].Active = &fileindex.ScanEvent{Job: roots[i].LastJob}
		}
	}
	h.a.applyRoots(server, roots)
	for _, root := range h.a.roots[server] {
		if root.ID == 1 && (root.Active != nil || root.LastJob.Status != "done") {
			t.Fatal("late snapshot revived completed scan")
		}
	}
	sv := h.a.store.Server(server)
	original := fileindex.New(sv.Addr, sv.Token)
	tr := h.a.addTransfer("扫描", "old source")
	tr.server, tr.scanRoot, tr.scanJob = server, 1, 1000
	sv.Addr = "changed.invalid:8090"
	h.a.trackedScan(original, tr, &fileindex.ScanJob{ID: 1000, RootID: 1, Status: "done"})
	if tr.state != "done" || h.a.roots[server][0].LastJob.ID != 999 {
		t.Fatal("old scan updated a different source")
	}
	sv.Addr = original.Addr
	h.tt.Frame()
}

func TestPendingScanPreventsDuplicateStart(t *testing.T) {
	f := fitest.New(t)
	h := browserHarness(t, f)
	h.click("目录管理")
	m := h.a.admin
	r := h.a.roots[m.server][0]
	h.a.startScan(m, r, false)
	h.a.startScan(m, r, false)
	if len(h.a.transfers) != 1 {
		t.Fatal("created two pending scans")
	}
	h.waitTransfers()
	jobs, err := fileindex.New(f.Addr(), "tok").ScanJobs(context.Background(), r.ID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("started duplicate server scans: %+v %v", jobs, err)
	}
}

func TestPartialAdminMutationReconcilesResult(t *testing.T) {
	f := fitest.New(t)
	for _, name := range []string{"a.txt", "b.txt"} {
		os.WriteFile(filepath.Join(f.Root, name), []byte("identical"), 0644)
	}
	h := browserHarness(t, f)
	h.click("查重")
	m := h.a.admin
	m.minSize = "全部非空文件"
	h.a.reloadAdmin()
	h.settle()
	h.a.selectDuplicates(m, false)
	h.a.adminAction(m, func(ctx context.Context, c *fileindex.Client) error {
		if err := c.Trash(ctx, 1, []string{"b.txt"}); err != nil {
			return err
		}
		return fmt.Errorf("later root unavailable")
	}, "unreachable success", nil)
	h.settle()
	if len(m.selected) != 0 || m.dupes.Summary.Groups != 0 || m.err != "later root unavailable" {
		t.Fatal("partial deletion left stale rows or selection")
	}
}

func TestFilesystemEventRefreshesManagementStatistics(t *testing.T) {
	f := fitest.New(t)
	h := browserHarness(t, f)
	h.click("空间统计")
	m := h.a.admin
	os.WriteFile(filepath.Join(f.Root, "new.txt"), []byte("new"), 0644)
	h.a.onEvent(m.server, fileindex.Event{Type: "fs", Root: 1})
	deadline := time.After(3 * time.Second)
	for m.overview.Files != 1 {
		select {
		case fn := <-h.queue:
			fn()
			h.tt.Frame()
		case <-deadline:
			t.Fatal("filesystem event did not refresh statistics")
		}
	}
	h.settle()
}

func TestLargeDirectoryUsageListCanNavigateLastRow(t *testing.T) {
	f := fitest.New(t)
	h := browserHarness(t, f)
	h.click("空间统计")
	h.click("nas")
	m := h.a.admin
	for i := range 1200 {
		name := fmt.Sprintf("usage-folder-%04d", i)
		m.dirs = append(m.dirs, fileindex.DirUsage{Name: name, Path: name, IsDir: true, Files: 1, Size: 10})
	}
	h.tt.Frame()
	visible := 0
	for _, text := range h.tt.Texts() {
		if strings.HasPrefix(text, "usage-folder-") {
			visible++
		}
	}
	if visible == 0 || visible > 20 {
		t.Fatalf("unexpected visible directory count: %d", visible)
	}
	m.dirList.ScrollTo(1199, ui.End)
	h.tt.Frame()
	h.click("usage-folder-1199")
	if m.statsDir != "usage-folder-1199" {
		t.Fatal("last row did not navigate")
	}
	for range 4 {
		h.tt.Frame()
	}
}
