package main

import (
	"context"
	"path"
	"strings"
	"time"

	"filemanager/remoteclient"
	"github.com/egoist/mygo/ui"
)

type compressionDialog struct {
	f                       *nasFiles
	entries                 []fileindex.Entry
	name                    string
	format, level, conflict int
	dstRoot                 int64
	dstDir, dstLabel, err   string
}

var archiveFormats = []string{"zip", "tar.gz", "tar.zst"}
var archiveLevels = []string{"fast", "standard", "compact"}
var archiveConflicts = []string{"rename", "overwrite", "skip"}

type compressionIdentity struct {
	Server  string `json:"server"`
	Task    int64  `json:"task"`
	Created int64  `json:"created"`
}

func (a *app) dismissCompression(tr *transfer) {
	if tr.kind != "压缩" || tr.task == 0 {
		return
	}
	if a.dismissedCompression == nil {
		a.dismissedCompression = map[compressionIdentity]bool{}
	}
	a.dismissedCompression[compressionIdentity{tr.server, tr.task, tr.taskCreated}] = true
}

func (a *app) openTransfers() {
	a.showTransfers = true
	if !a.noEvents {
		for _, sv := range a.store.Servers {
			if sv.Token != "" {
				a.refreshCompressionTasks(sv.ID)
			}
		}
	}
}

func archiveBaseName(es []fileindex.Entry, rootName string) string {
	if len(es) == 1 {
		return es[0].Name
	}
	parent := parentDir(es[0].Path)
	for _, e := range es[1:] {
		for parent != "" && parentDir(e.Path) != parent && !strings.HasPrefix(parentDir(e.Path), parent+"/") {
			parent = parentDir(parent)
		}
	}
	if parent != "" {
		return path.Base(parent)
	}
	if rootName != "" {
		return rootName
	}
	return "压缩包"
}

func (a *app) compressIn(f *nasFiles, es []fileindex.Entry) {
	if len(es) == 0 || f.client != nil && f.client.InArchive() {
		return
	}
	for _, e := range es {
		if e.Root != es[0].Root {
			a.errorMsg = "请在同一个索引目录内选择要压缩的项"
			return
		}
	}
	rootName := es[0].RootName
	d := &compressionDialog{f: f, entries: append([]fileindex.Entry(nil), es...), level: 1}
	for _, r := range a.roots[f.server] {
		if r.ID == es[0].Root {
			rootName = r.Name
			if !r.ReadOnly {
				d.dstRoot = r.ID
				d.dstDir = parentDir(es[0].Path)
				d.dstLabel = path.Join(r.Name, d.dstDir)
			}
		}
	}
	d.name = archiveBaseName(es, rootName)
	a.compressing = d
}

func (a *app) compressionView(c *ui.Context) {
	d := a.compressing
	if d == nil || a.nas.open {
		return
	}
	open := true
	ui.Modal(c.Key("compress-dialog"), &open, func() {
		ui.Text(c, "压缩到服务器").Bold().FontSize(18)
		ui.Text(c, "保留源文件；关闭客户端后服务器继续压缩。").FontSize(12).TextColor(c.Theme().TextMuted)
		ui.Field(c, "包名", func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c.Key("archive-name"), &d.name).Label("压缩包名称").Width(330)
				ui.Text(c, "."+archiveFormats[d.format])
			})
		})
		ui.Field(c, "格式", func() {
			ui.Segmented(c.Key("archive-format"), &d.format, "ZIP", "tar.gz", "tar.zst").Label("压缩格式")
		})
		ui.Field(c, "压缩级别", func() {
			ui.Segmented(c.Key("archive-level"), &d.level, "快速", "标准", "较小体积").Label("压缩级别")
		})
		ui.Field(c, "同名时", func() {
			ui.Segmented(c.Key("archive-conflict"), &d.conflict, "保留两份", "覆盖", "跳过").Label("同名处理")
		})
		ui.Field(c, "保存位置", func() {
			label := d.dstLabel
			if label == "" {
				label = "请选择可写目录"
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, label).Width(330).SingleLine().Tooltip(label)
				if ui.Button(c, "选择目录…").Clicked() {
					a.pickCompressionDir(d)
				}
			})
		})
		if d.err != "" {
			notice(c, c.Theme().Danger, d.err)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Clicked() {
				open = false
			}
			name := strings.TrimSpace(d.name)
			valid := name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00") && d.dstRoot != 0
			if ui.PrimaryButton(c, "开始压缩").Disabled(!valid).Clicked() {
				a.submitCompression(d)
				open = false
			}
		})
	})
	if !open && a.compressing == d {
		a.compressing = nil
	}
}

func (a *app) pickCompressionDir(d *compressionDialog) {
	sv := a.store.Server(d.f.server)
	if sv == nil {
		return
	}
	a.nas = nasBrowser{open: true, pick: func(server string, root int64, rootName, dir string) {
		if server == d.f.server {
			d.dstRoot, d.dstDir, d.dstLabel = root, dir, path.Join(rootName, dir)
		}
	}, probes: map[string]*serverProbe{}, lockServer: sv.ID, title: "压缩包保存位置"}
	a.nas.startServer, a.nas.startRoot, a.nas.startPath = sv.ID, d.dstRoot, d.dstDir
	a.probe(sv)
	a.nasUse(sv)
}

func (a *app) submitCompression(d *compressionDialog) {
	c, ok := a.serverClient(d.f.server)
	if !ok {
		return
	}
	req := fileindex.CompressRequest{Root: d.entries[0].Root, Paths: paths(d.entries), DstRoot: d.dstRoot, DstDir: d.dstDir, Name: strings.TrimSpace(d.name) + "." + archiveFormats[d.format], Format: archiveFormats[d.format], Level: archiveLevels[d.level], Conflict: archiveConflicts[d.conflict]}
	tr := a.addTransfer("压缩", "压缩为「"+req.Name+"」")
	tr.server = d.f.server
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	tr.cancel = cancel
	go func() {
		defer cancel()
		task, err := c.CompressArchive(ctx, req)
		a.post(func() {
			if a.closed {
				return
			}
			if err != nil {
				a.finishTransfer(tr, err, nil)
				return
			}
			tr.task, tr.taskCreated = task.ID, task.CreatedAt
			a.trackServerTask(c, tr, task)
		})
	}()
}

func (a *app) locateCompressed(tr *transfer) {
	r := tr.archiveResult
	if r == nil {
		return
	}
	for _, root := range a.roots[tr.server] {
		if root.ID == r.Root {
			a.goPlace(rootPlace(tr.server, r.Root))
			a.browse(parentDir(r.Path), path.Base(r.Path))
			a.showTransfers = false
			return
		}
	}
	a.errorMsg = "压缩包所在索引目录不可用，请先连接来源服务器"
}

// Recover server-owned tasks independently of the client's process lifetime.
func (a *app) refreshCompressionTasks(server string) {
	c, ok := a.serverClient(server)
	if !ok || c.Token == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		tasks, err := c.Tasks(ctx)
		a.post(func() {
			if err != nil || a.closed {
				return
			}
			sv := a.store.Server(server)
			if sv == nil || sv.Addr != c.Addr || sv.Token != c.Token {
				return
			}
			for i := range tasks {
				task := &tasks[i]
				if task.Kind != "compress" {
					continue
				}
				if a.dismissedCompression[compressionIdentity{server, task.ID, task.CreatedAt}] {
					continue
				}
				var found *transfer
				pending := false
				for _, tr := range a.transfers {
					if tr.server == server && tr.kind == "压缩" {
						if tr.task == 0 && tr.running() {
							pending = true
						}
						if tr.task == task.ID && (tr.taskCreated == 0 || tr.taskCreated == task.CreatedAt) {
							found = tr
							break
						}
					}
				}
				if found == nil && pending {
					continue
				}
				if found == nil {
					found = a.addTransfer("压缩", task.Title)
					found.server, found.task, found.taskCreated = server, task.ID, task.CreatedAt
					if task.CreatedAt > 0 {
						found.started = time.Unix(task.CreatedAt, 0)
					}
				} else if found.state != "interrupted" {
					continue
				}
				found.state, found.err = "running", ""
				a.trackServerTask(c, found, task)
			}
			for _, tr := range a.transfers {
				if tr.server == server && tr.kind == "压缩" && tr.state == "interrupted" {
					tr.err = "服务器任务记录已失效，无法确认压缩结果，请检查目标目录"
				}
			}
			a.saveTransferHistory()
		})
	}()
}
