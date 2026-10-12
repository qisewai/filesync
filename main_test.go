package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filesync/config"
	"filesync/engine"
	"filesync/fileindex/fitest"

	"github.com/egoist/mygo/ui"
)

// harness runs the app's view without a window. Work that background
// goroutines post waits in a queue until the test runs it, on its own
// goroutine as the main thread would.
type harness struct {
	t     *testing.T
	a     *app
	tt    *ui.Tester
	queue chan func()
}

func newHarness(t *testing.T, store *config.Store) *harness {
	rollNumbers = false
	h := &harness{t: t, a: newApp(store), queue: make(chan func(), 1024)}
	h.a.post = func(fn func()) { h.queue <- fn }
	h.a.noEvents = true
	h.a.localRoots = []localRoot{} // none of the computer's own
	t.Cleanup(h.a.stopBackground)
	h.tt = ui.NewTester(h.a.view, 980, 680)
	return h
}

// settle runs posted work until no scan, backup or request of the server
// dialog is under way.
func (h *harness) settle() {
	h.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case fn := <-h.queue:
			fn()
			h.tt.Frame()
		case <-deadline:
			h.t.Fatal("the work did not finish")
		default:
			if !h.a.busy() && (h.a.local == nil || !h.a.local.loading) && (h.a.localIdx == nil || !h.a.localIdx.running && !h.a.localIdx.loading && !h.a.localIdx.saving && (h.a.localIdx.draft == nil || !h.a.localIdx.draft.picking)) && h.a.nas.busy == "" && !h.probing() && len(h.a.rootsLoading) == 0 && !h.treeLoading() && (h.a.br == nil || !h.a.br.loading && !h.a.br.operating) && (h.a.gsearch == nil || !h.a.gsearch.busy()) && (h.a.preview == nil || !h.a.preview.loading) && (h.a.recycle == nil || !h.a.recycle.loading && !h.a.recycle.operating) && (h.a.admin == nil || !h.a.admin.loading && !h.a.admin.busy && !h.a.admin.historyLoading && (h.a.admin.draft == nil || h.a.admin.draft.picker == nil || !h.a.admin.draft.picker.loading)) && len(h.queue) == 0 {
				h.tt.Frame()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func (h *harness) probing() bool {
	for _, pr := range h.a.nas.probes {
		if pr.state == probeChecking {
			return true
		}
	}
	return false
}

func (h *harness) treeLoading() bool {
	for _, n := range h.a.tree {
		if n.loading {
			return true
		}
	}
	return false
}

func (h *harness) click(label string) {
	h.t.Helper()
	if err := h.tt.Click(label); err != nil {
		h.t.Fatalf("%v; texts %q", err, h.tt.Texts())
	}
	h.settle()
}

// clickPage clicks the element of the page showing label, rather than a
// directory or an item of the sidebar named so, which builds first: the
// servers in the sidebar fold while it clicks.
func (h *harness) clickPage(label string) {
	h.t.Helper()
	was := map[string]bool{}
	for _, sv := range h.a.store.Servers {
		open := h.a.sectionOpen("server/" + sv.ID)
		was[sv.ID] = *open
		*open = false
	}
	h.tt.Frame()
	err := h.tt.Click(label)
	for id, open := range was {
		*h.a.sectionOpen("server/" + id) = open
	}
	if err != nil {
		h.t.Fatalf("%v; texts %q", err, h.tt.Texts())
	}
	h.settle()
}

// taskMenu chooses an item of the menu of the card of a task: the one
// chosen last, else the first.
func (h *harness) taskMenu(item string) {
	h.t.Helper()
	task := h.a.task()
	if task == nil {
		task = h.a.store.Tasks[0]
	}
	if err := h.tt.Click("任务菜单 " + task.Name); err != nil {
		h.t.Fatalf("%v; texts %q", err, h.tt.Texts())
	}
	if err := h.tt.ChooseMenuItem(item); err != nil {
		h.t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.settle()
}

// more chooses an item of the menu of the less used commands of a page.
func (h *harness) more(item ...string) {
	h.t.Helper()
	if err := h.tt.Click("更多"); err != nil {
		h.t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem(item...); err != nil {
		h.t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.settle()
}

func (h *harness) want(text string) {
	h.t.Helper()
	if !h.tt.HasText(text) {
		h.t.Fatalf("no %q in %q", text, h.tt.Texts())
	}
}

func (h *harness) typeInto(label, text string) {
	h.t.Helper()
	h.click(label)
	h.tt.Type(text)
}

// newTask opens the local directory dir, chooses its entries names, and
// starts a task backing them up, or dir itself without names.
func (h *harness) newTask(dir string, names ...string) {
	h.t.Helper()
	h.a.openLocal(dir)
	h.settle()
	for i, name := range names {
		r, ok := h.tt.Find(name)
		if !ok {
			h.t.Fatalf("no %q in %q", name, h.tt.Texts())
		}
		if i == 0 {
			h.tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
		} else {
			h.tt.ClickAtWith(ui.Ctrl, r.X+r.W/2, r.Y+r.H/2)
		}
		h.settle()
	}
	if len(names) == 0 {
		h.localMenu("备份当前文件夹…")
	} else {
		h.localMenu("创建备份任务…")
	}
	h.want("新建备份任务")
}

// localMenu chooses an item of the menu of the files of this computer,
// opened under them, which keeps those chosen.
func (h *harness) localMenu(item string) {
	h.t.Helper()
	h.tt.Frame()
	r, ok := h.tt.Find("本机文件")
	if !ok {
		h.t.Fatalf("no files in %q", h.tt.Texts())
	}
	h.tt.RightClickAt(r.X+r.W/2, r.Y+r.H-8)
	if err := h.tt.ChooseMenuItem(item); err != nil {
		h.t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.settle()
}

// signedIn is a store with the server, signed in.
func signedIn(t *testing.T, f *fitest.Server) *config.Store {
	store := &config.Store{Dir: t.TempDir()}
	store.AddServer(&config.Server{Name: "家里", Addr: f.Addr(), Token: "tok"})
	return store
}

func TestAddServerChooseDirectoryAndBackUp(t *testing.T) {
	f := fitest.New(t)
	src := filepath.Join(t.TempDir(), "xiaomai")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "photo.jpg"), []byte("jpeg"), 0o644)
	os.MkdirAll(filepath.Join(src, "docs"), 0o755)
	os.WriteFile(filepath.Join(src, "docs", "report.txt"), []byte("report"), 0o644)
	os.WriteFile(filepath.Join(src, "Thumbs.db"), []byte("excluded by default"), 0o644)
	os.MkdirAll(filepath.Join(f.Root, "已有"), 0o755)

	store := &config.Store{Dir: t.TempDir()}
	h := newHarness(t, store)
	h.want("欢迎使用 FileSync")
	h.click("添加服务器…")
	h.typeInto("名称", "家里")
	h.typeInto("地址", f.Addr())
	h.click("保存")
	h.want("家里")
	h.want("未登录")
	h.click("完成")

	h.newTask(filepath.Dir(src), "xiaomai")
	h.click("选择…")
	// The servers are listed, and each connected to: this one answers but
	// has no session.
	h.want("● 需要登录")
	h.click("登录并浏览")
	h.want("请输入 fileindex 的管理员密码。这里只保存登录状态，不保存密码。")
	h.typeInto("密码", "wrong")
	h.click("登录")
	h.want("密码错误")
	h.typeInto("密码", fitest.Password)
	h.click("登录")
	h.want("只读")
	// A click chooses a root, and opening it shows its directories.
	h.click("/data/nas")
	h.want("选择“nas”")
	h.click("打开")
	h.want("已有")
	h.want("选择当前目录“nas”")
	// A new folder is chosen, not opened.
	h.click("新建文件夹")
	h.tt.Type("备份")
	h.click("创建")
	h.want("已有")
	h.click("选择“备份”")
	h.want("家里 · nas/备份")
	// The source directory itself is backed up, by default.
	h.want("在目标中创建“xiaomai”文件夹")
	h.want("“xiaomai”会备份为 家里 · nas/备份/xiaomai")
	h.click("保存")

	if len(store.Tasks) != 1 || store.Servers[0].Token == "" {
		t.Fatalf("tasks %+v, servers %+v", store.Tasks, store.Servers)
	}
	if b, err := os.ReadFile(filepath.Join(store.Dir, "tasks.json")); err != nil || len(b) == 0 {
		t.Fatal("not saved:", err)
	}
	// Saved, the task shows.
	h.want("备份 xiaomai")
	h.want("家里 · nas/备份/xiaomai")

	h.taskMenu("预览并备份…")
	h.want("预览：备份 xiaomai")
	h.want("photo.jpg")
	h.want("docs/report.txt")
	if h.tt.HasText("Thumbs.db") {
		t.Error("an excluded file is planned")
	}
	h.click("开始备份")
	h.want("备份完成")
	if b, err := os.ReadFile(filepath.Join(f.Root, "备份", "xiaomai", "docs", "report.txt")); err != nil || string(b) != "report" {
		t.Fatalf("report.txt on the server: %q, %v", b, err)
	}
	run := store.Tasks[0].History
	if len(run) != 1 || run[0].Status != config.StatusOK || run[0].Copied != 2 || run[0].Log == "" {
		t.Fatalf("history %+v", run)
	}

	h.click("完成")
	h.want("成功")
	h.taskMenu("预览并备份…")
	h.want("目标已是最新")
}

// The dialog opens where the task's directory is, chosen; a directory can
// be chosen as it is, or opened and the directory shown chosen.
func TestPickerChoices(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "王宁", "项目", "深处"), 0o755)
	os.MkdirAll(filepath.Join(f.Root, "照片"), 0o755)
	store := signedIn(t, f)
	h := newHarness(t, store)
	h.newTask(t.TempDir())

	h.click("选择…")
	h.click("浏览")
	h.click("/data/nas")
	h.click("选择“nas”")
	h.want("家里 · nas")
	if d := h.a.draft; d.root != 1 || d.path != "" {
		t.Fatalf("draft %+v", d)
	}

	h.click("选择…")
	h.click("浏览")
	// The root of the task is chosen in its list.
	h.want("选择“nas”")
	h.click("打开")
	h.click("王宁")
	h.click("打开")
	h.click("项目")
	h.click("选择“项目”")
	h.want("家里 · nas/王宁/项目")

	h.click("选择…")
	h.click("浏览")
	// The directory of the task is chosen in its parent.
	h.want("选择“项目”")
	h.want("王宁")
	h.click("打开")
	h.want("深处")
	h.click("选择当前目录“项目”")
	h.want("家里 · nas/王宁/项目")
	// A double click opens a directory too.
	h.click("选择…")
	h.click("浏览")
	r, ok := h.tt.Find("项目")
	if !ok {
		t.Fatal("no 项目")
	}
	h.tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	h.tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	h.settle()
	h.want("选择当前目录“项目”")
	h.want("深处")
	h.click("上一级")
	h.want("选择“项目”")
	h.click("上一级")
	h.want("选择“王宁”")
	h.click("取消")
	if h.a.draft.path != "王宁/项目" {
		t.Errorf("canceled, but the path is %q", h.a.draft.path)
	}
}

// A read-only root says why it cannot be chosen, and where to change it.
func TestReadOnlyRoot(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.ReadOnly, "资料"), 0o755)
	h := newHarness(t, signedIn(t, f))
	var opened []string
	h.a.openURL = func(u string) { opened = append(opened, u) }
	h.newTask(t.TempDir())
	h.click("选择…")
	h.click("浏览")
	h.click("/data/ro")
	h.want("“只读”在 fileindex 中设为只读，不能备份到这里。请在 fileindex 的“索引目录”页面编辑它，关闭“只读”，再回来选择。")
	h.click("打开")
	h.click("资料")
	h.click("选择“资料”")
	if h.a.draft.server != "" {
		t.Fatal("chose a directory of a read-only root")
	}
	h.click("打开 fileindex 设置")
	if len(opened) != 1 || opened[0] != "http://"+f.Addr()+"/roots" {
		t.Errorf("opened %q", opened)
	}
	// Made writable on the server, it can be chosen once its list is read
	// again.
	f.Writable.Store(true)
	h.click("上一级")
	h.want("选择“只读”")
	if h.tt.HasText("打开 fileindex 设置") {
		t.Error("still said read-only")
	}
	f.Writable.Store(false)

	// A root that can be written says nothing.
	h.click("服务器")
	h.click("浏览")
	h.click("/data/nas")
	if h.tt.HasText("打开 fileindex 设置") {
		t.Error("a writable root is said read-only")
	}
}

// Unchecked, the contents of the source go straight into the target.
func TestBackUpContentsOnly(t *testing.T) {
	f := fitest.New(t)
	src := filepath.Join(t.TempDir(), "xiaomai")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644)
	store := signedIn(t, f)
	h := newHarness(t, store)
	h.newTask(filepath.Dir(src), "xiaomai")
	h.click("选择…")
	h.click("浏览")
	h.click("/data/nas")
	h.click("选择“nas”")
	h.click("在目标中创建“xiaomai”文件夹")
	h.want("“xiaomai”里的内容会直接放在 家里 · nas 中")
	h.click("保存")
	if store.Tasks[0].WithDir {
		t.Fatal("still with its directory")
	}
	h.taskMenu("预览并备份…")
	h.click("开始备份")
	h.want("备份完成")
	if _, err := os.Stat(filepath.Join(f.Root, "a.txt")); err != nil {
		t.Error("not in the target itself:", err)
	}
}

func TestArchiveMode(t *testing.T) {
	f := fitest.New(t)
	src := filepath.Join(t.TempDir(), "xiaomai")
	os.MkdirAll(filepath.Join(src, "docs"), 0o755)
	os.WriteFile(filepath.Join(src, "docs", "a.txt"), []byte("a"), 0o644)
	store := signedIn(t, f)
	h := newHarness(t, store)
	h.newTask(filepath.Dir(src), "xiaomai")
	h.click("选择…")
	h.click("浏览")
	h.click("/data/nas")
	h.click("选择“nas”")
	h.click("tar 归档")
	h.want("归档里包含“xiaomai”文件夹")
	h.want("gzip（.tar.gz，推荐）")
	h.click("保存")
	if store.Tasks[0].Compress != "gzip" {
		t.Fatalf("compress %q", store.Tasks[0].Compress)
	}
	if store.Tasks[0].Mode != engine.Archive {
		t.Fatalf("mode %v", store.Tasks[0].Mode)
	}
	h.want("tar 归档")
	h.taskMenu("预览并备份…")
	h.want("docs/a.txt")
	h.click("开始备份")
	h.want("备份完成")
	ents, _ := os.ReadDir(f.Root)
	// Compressed with gzip, by default.
	if len(ents) != 1 || !strings.HasPrefix(ents[0].Name(), "xiaomai-") || !strings.HasSuffix(ents[0].Name(), ".tar.gz") {
		t.Fatalf("server has %v", ents)
	}
	h.want("已保存为 家里 · nas/" + ents[0].Name() + "（fileindex 中路径）")
}

func TestEditorValidates(t *testing.T) {
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.newTask(t.TempDir())
	h.click("备份任务名称")
	h.tt.Key(ui.Ctrl, ui.KeyA)
	h.tt.Key(0, ui.KeyBackspace)
	h.click("保存")
	h.want("请输入名称。")
	h.want("请选择 NAS 上备份到的目录。")
	if len(h.a.store.Tasks) != 0 {
		t.Error("an invalid task was saved")
	}
	h.click("取消")
	if h.a.editing {
		t.Fatal("still editing")
	}
	h.click("备份任务")
	h.want("还没有备份任务")
}

// Files and folders chosen together are backed up into the target, the
// others of their directory left out.
func TestBackUpChosenEntries(t *testing.T) {
	f := fitest.New(t)
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "照片"), 0o755)
	os.WriteFile(filepath.Join(src, "照片", "a.jpg"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(src, "报告.pdf"), []byte("r"), 0o644)
	os.WriteFile(filepath.Join(src, "不要.txt"), []byte("n"), 0o644)
	os.MkdirAll(filepath.Join(f.Root, "目标"), 0o755)
	os.WriteFile(filepath.Join(f.Root, "目标", "原有.txt"), []byte("o"), 0o644)
	store := signedIn(t, f)
	h := newHarness(t, store)
	h.newTask(src, "照片", "报告.pdf")
	h.want(src + " 中的 照片、报告.pdf")
	if h.tt.HasText("在目标中创建“" + filepath.Base(src) + "”文件夹") {
		t.Error("asks for the directory of entries chosen")
	}
	h.click("选择…")
	h.click("浏览")
	h.click("/data/nas")
	h.click("打开")
	h.click("目标")
	h.click("选择“目标”")
	h.want("选中的 2 项会备份到 家里 · nas/目标 中")
	h.click("完全一致")
	h.click("保存")
	task := store.Tasks[0]
	if task.Source != src || len(task.Items) != 2 || task.Name != "备份 照片 等 2 项" {
		t.Fatalf("task %+v", task)
	}
	h.taskMenu("预览并备份…")
	h.want("照片/a.jpg")
	if h.tt.HasText("不要.txt") || h.tt.HasText("原有.txt") {
		t.Fatal("plans entries not chosen")
	}
	h.click("开始备份")
	h.want("备份完成")
	for _, p := range []string{"照片/a.jpg", "报告.pdf", "原有.txt"} {
		if _, err := os.Stat(filepath.Join(f.Root, "目标", p)); err != nil {
			t.Error(err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.Root, "目标", "不要.txt")); err == nil {
		t.Error("backed up an entry not chosen")
	}

	// Edited, the task keeps what it backs up.
	h.click("完成")
	h.taskMenu("编辑…")
	h.want("编辑备份任务")
	h.want(src + " 中的 照片、报告.pdf")
	h.click("取消")
}

func TestMirrorAsksBeforeMassDelete(t *testing.T) {
	f := fitest.New(t)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "only"), []byte("1"), 0o644)
	dst := filepath.Join(f.Root, "m")
	os.MkdirAll(dst, 0o755)
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} {
		os.WriteFile(filepath.Join(dst, name), []byte("x"), 0o644)
	}
	store := signedIn(t, f)
	store.Add(&config.Task{Name: "镜像", Source: src, Server: store.Servers[0].ID, Root: 1, RootName: "nas", Path: "m", Mode: engine.Mirror})
	h := newHarness(t, store)
	h.click("备份任务")

	h.taskMenu("预览并备份…")
	h.click("开始备份")
	h.want("确定要删除这么多文件吗？")
	h.click("取消")
	if _, err := os.Stat(filepath.Join(dst, "a")); err != nil {
		t.Fatal("deleted although canceled")
	}

	h.click("开始备份")
	h.click("仍然备份")
	h.want("备份完成")
	entries, _ := os.ReadDir(dst)
	if len(entries) != 1 || entries[0].Name() != "only" {
		t.Errorf("target has %v", entries)
	}
	if _, err := os.Stat(filepath.Join(f.Trash, "m", "a")); err != nil {
		t.Error("not in the trash:", err)
	}
}

func TestExpiredSessionAsksTheLogin(t *testing.T) {
	f := fitest.New(t)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644)
	store := signedIn(t, f)
	store.Add(&config.Task{Name: "t", Source: src, Server: store.Servers[0].ID, Root: 1, RootName: "nas", Path: "x"})
	f.Expired.Store(true)
	h := newHarness(t, store)
	h.click("备份任务")

	h.taskMenu("预览并备份…")
	h.want("需要登录 NAS 上的 fileindex，登录后会自动重新扫描。")
	h.click("登录 NAS…")
	h.typeInto("密码", fitest.Password)
	h.click("登录")
	// Signed in again, the scan runs again.
	h.want("预览：t")
	h.want("a.txt")
}

func TestServersInUseStay(t *testing.T) {
	f := fitest.New(t)
	store := signedIn(t, f)
	store.AddServer(&config.Server{Addr: "10.0.0.9:8090"})
	store.Add(&config.Task{Name: "t", Source: t.TempDir(), Server: store.Servers[0].ID, Root: 1, RootName: "nas"})
	h := newHarness(t, store)
	h.click("设置")
	h.click("服务器列表…")
	h.want("1 个任务")
	h.want("已登录")

	// The server of the task cannot go.
	h.a.nas.confirmDelID = store.Servers[0].ID
	h.tt.Frame()
	h.click("确定删除")
	h.want("还有任务备份到这个服务器，请先修改或删除这些任务")
	if len(store.Servers) != 2 {
		t.Fatal("a server in use was removed")
	}
	// The other can.
	h.a.nas.confirmDelID = store.Servers[1].ID
	h.tt.Frame()
	h.click("确定删除")
	if len(store.Servers) != 1 {
		t.Fatalf("servers %+v", store.Servers)
	}

	h.click("退出登录")
	h.want("未登录")
	if store.Servers[0].Token != "" {
		t.Error("signed out but the session stays")
	}
}

func TestRemoveTaskKeepsFiles(t *testing.T) {
	f := fitest.New(t)
	src := t.TempDir()
	store := signedIn(t, f)
	store.Add(&config.Task{Name: "要删除的任务", Source: src, Server: store.Servers[0].ID, Root: 1})
	h := newHarness(t, store)
	h.click("备份任务")
	h.taskMenu("删除任务…")
	h.want("删除任务“要删除的任务”？")
	h.click("删除")
	if len(store.Tasks) != 0 {
		t.Fatal("task kept")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("source gone")
	}
	h.want("还没有备份任务")
}

func TestNormalizeAddr(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.0.235":             "192.168.0.235:8090",
		" http://192.168.0.235:80/": "192.168.0.235:80",
		"nas.local:9000":            "nas.local:9000",
		"fe80::1":                   "[fe80::1]:8090",
	} {
		if got, err := normalizeAddr(in); err != nil || got != want {
			t.Errorf("%q: %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "a b", "host/path"} {
		if _, err := normalizeAddr(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

// The dialog choosing a target lists the servers, connecting to each at
// once; one that does not answer cannot be entered.
func TestPickerConnectsToServers(t *testing.T) {
	f := fitest.New(t)
	store := signedIn(t, f)
	// A port nothing listens on.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := l.Addr().String()
	l.Close()
	store.AddServer(&config.Server{Name: "旧 NAS", Addr: dead, Token: "old"})
	h := newHarness(t, store)
	h.newTask(t.TempDir())
	h.click("选择…")
	h.want("● 已连接")
	h.want("● 无法连接")
	h.want("连接被拒绝，fileindex 可能没有运行，或端口不对")
	if n := strings.Count(strings.Join(h.tt.Texts(), "\n"), "\n浏览\n"); n != 1 {
		t.Errorf("%d servers can be entered", n)
	}
	h.click("重试")
	h.want("● 无法连接")
	// Its new address is connected to once saved.
	f2 := fitest.New(t)
	h.click("编辑地址")
	h.a.nas.addr = f2.Addr()
	h.click("保存")
	h.want("选择一个服务器：")
	if h.tt.HasText("● 无法连接") {
		t.Fatal("still unreachable at its new address")
	}
	h.want("● 需要登录") // the session was of the old address

	// A session the server refused asks the password.
	f.Expired.Store(true)
	h.a.probe(store.Servers[0])
	h.settle()
	h.want("● 需要登录")
	h.click("登录并浏览")
	h.typeInto("密码", fitest.Password)
	h.click("登录")
	h.want("/data/nas")
}

// A server that cannot be reached is marked in the sidebar; the mark and
// its menu connect again, which reads its directories.
func TestSidebarReconnects(t *testing.T) {
	f := fitest.New(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := l.Addr().String()
	l.Close()
	store := &config.Store{Dir: t.TempDir()}
	store.AddServer(&config.Server{Name: "家里", Addr: dead, Token: "tok"})
	h := newHarness(t, store)
	h.settle()
	h.want("无法连接")
	h.want("连接被拒绝，fileindex 可能没有运行，或端口不对")
	if h.a.errorMsg != "" {
		t.Errorf("the page shows %q", h.a.errorMsg)
	}
	h.click("重新连接 家里")
	h.want("无法连接")

	// Back at an address that answers, the mark connects again.
	store.Servers[0].Addr = f.Addr()
	h.click("重新连接 家里")
	h.want("文件夹 nas")
	if h.tt.HasText("无法连接") || h.tt.HasText("重新连接 家里") {
		t.Error("still marked")
	}

	// The menu of the server reconnects too.
	if err := h.tt.RightClick("家里"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("重新连接"); err != nil {
		t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.settle()
	h.want("文件夹 nas")
}

// The error over the page can be closed.
func TestErrorCloses(t *testing.T) {
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.openLocal(t.TempDir())
	h.a.errorMsg = "出了点问题"
	h.tt.Frame()
	h.want("出了点问题")
	h.click("关闭提示")
	if h.a.errorMsg != "" || h.tt.HasText("出了点问题") {
		t.Fatal("not closed")
	}
}

// The sidebar drags wider and narrower, within bounds, and keeps its
// width.
func TestSidebarDrags(t *testing.T) {
	store := &config.Store{Dir: t.TempDir()}
	h := newHarness(t, store)
	drag := func(to float32) {
		r, ok := h.tt.Find("Divider")
		if !ok {
			t.Fatalf("no divider in %q", h.tt.Texts())
		}
		x, y := r.X+r.W/2, r.Y+r.H/2
		h.tt.Press(x, y)
		h.tt.Move(to, y)
		h.tt.Release(to, y)
		h.tt.Frame()
	}
	drag(330)
	if w := h.a.sidebarW; w < 320 || w > 340 {
		t.Fatalf("dragged to %v", w)
	}
	drag(900)
	if h.a.sidebarW != maxSidebar {
		t.Fatalf("wider than allowed: %v", h.a.sidebarW)
	}
	drag(20)
	if h.a.sidebarW != minSidebar {
		t.Fatalf("narrower than allowed: %v", h.a.sidebarW)
	}
	drag(300)
	h.a.stopBackground()
	again, err := config.Load(store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.SidebarWidth != h.a.sidebarW || newApp(again).sidebarW != h.a.sidebarW {
		t.Fatalf("saved %v, dragged to %v", again.SidebarWidth, h.a.sidebarW)
	}
}
