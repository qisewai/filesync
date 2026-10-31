package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	archiveio "filemanager/archive"
	"filemanager/remoteclient"
	"github.com/egoist/mygo/ui"
)

type archiveBrowser struct {
	browser
	client            *fileindex.Client
	file, version     string
	local, sequential bool
	extract           bool
	extractLocal      bool
	extractTarget     func([]fileindex.Entry)
}

func (a *app) onArchive() bool            { return a.place == "archive" && a.arc != nil }
func archiveEntry(e fileindex.Entry) bool { return !e.IsDir && !e.Link && archiveio.Supported(e.Name) }
func (a *app) openArchive(server string, c *fileindex.Client, e fileindex.Entry, dir string) {
	if c != nil && fileindex.IsPeer(c.Addr) {
		a.errorMsg = "设备共享暂不支持在线浏览压缩包，请先下载到本机后打开"
		return
	}

	if a.arc != nil && a.arc.cancel != nil {
		a.arc.cancel()
	}
	b := &archiveBrowser{client: c, file: e.Path, local: server == localPlace, browser: browser{server: server, root: fileindex.Root{ID: e.Root, ReadOnly: true}, cursor: -1}}
	if b.local {
		b.root.Name = "本机"
		b.client = fileindex.NewLocalArchive(e.Path)
	} else {
		for _, r := range a.roots[server] {
			if r.ID == e.Root {
				b.root = r
				break
			}
		}
	}
	b.list.Key = func(i int) any { return b.items[i].Path }
	b.list.Selection = &b.sel
	b.list.Selected = &b.cursor
	b.sort.by = ui.SortOrder{Column: colName}
	b.list.Sort = &b.sort.by
	b.gridState.Key = b.list.Key
	b.gridState.Selection = &b.sel
	b.gridState.Selected = &b.cursor
	a.arc = b
	a.place = "archive"
	a.archiveBrowse(dir)
}
func (a *app) archiveBrowse(dir string, choose ...string) {
	b := a.arc
	p, err := archiveio.Clean(dir)
	if err != nil {
		b.err = err.Error()
		return
	}
	if b.cancel != nil {
		b.cancel()
	}
	if b.path != p {
		b.filter = filenameFilter{}
	}
	b.path = p
	b.items = nil
	b.sel.Clear()
	b.cursor = -1
	b.err = ""
	b.loading = true
	b.opLabel = "正在读取压缩包目录；顺序压缩格式首次读取可能较慢…"
	b.gen++
	gen := b.gen
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	c := b.client
	root, file := b.root.ID, b.file
	go func() {
		defer cancel()
		items, version, seq, err := c.ArchiveListProgress(ctx, root, file, p, func(n, total int64) {
			a.post(func() {
				if a.arc == b && b.gen == gen && b.loading {
					b.opLabel = "正在读取压缩包目录…"
					if total > 0 {
						b.opLabel = fmt.Sprintf("正在读取压缩包目录：%s / %s", formatBytes(n), formatBytes(total))
					}
				}
			})
		})
		a.post(func() {
			if a.closed || a.arc != b || b.gen != gen || !a.onArchive() || !a.sourceCurrent(b.server, c) {
				return
			}
			b.loading = false
			if err != nil {
				b.err = err.Error()
				if errors.Is(err, fileindex.ErrAuth) {
					a.signedOut(a.store.Server(b.server))
				}
				return
			}
			b.version, b.sequential = version, seq
			if b.local {
				out := *c
				s := *c.ArchiveSource
				s.Version = version
				out.ArchiveSource = &s
				b.client = &out
			} else {
				b.client = c.Archive(root, file, version)
			}
			sortEntries(&b.sort, items, nil)
			b.items = items
			b.filter.source(items)
			for i, e := range items {
				if len(choose) > 0 && e.Name == choose[0] {
					b.sel.Add(e.Path)
					b.cursor = i
					b.list.ScrollIntoView(i)
				}
			}
			if b.extract {
				b.extract = false
				a.archiveExtract(items)
			}
			if b.extractLocal {
				b.extractLocal = false
				a.downloadTo(a.archiveFiles(), false, items, "解压到本机")
			}
			if fn := b.extractTarget; fn != nil {
				b.extractTarget = nil
				fn(items)
			}
		})
	}()
}
func (a *app) archiveOpen(e fileindex.Entry) {
	b := a.arc
	if e.Link {
		b.err = "压缩包内符号链接只显示，不能打开"
		return
	}
	if e.Encrypted {
		b.err = "文件已加密，暂不支持打开"
		return
	}
	if e.IsDir {
		a.archiveBrowse(e.Path)
		return
	}
	if previewKind(e) != "" {
		var group []fileindex.Entry
		for _, it := range b.items {
			if !it.Link && !it.Encrypted && previewKind(it) != "" {
				group = append(group, it)
			}
		}
		a.showPreview(b.server, b.client, e, group)
	} else {
		a.systemOpenSource(b.server, b.client, e, true)
	}
}
func (a *app) archiveUp() {
	b := a.arc
	if b.path != "" {
		a.archiveBrowse(parentDir(b.path), path.Base(b.path))
		return
	}
	if b.local {
		a.openLocal(filepath.Dir(b.file), filepath.Base(b.file))
	} else {
		a.showDirectory(b.server, b.root, parentDir(b.file), path.Base(b.file))
	}
}
func (a *app) archivePath(c *ui.Context) {
	b := a.arc
	names := []string{b.root.Name}
	targets := []string{""}
	if b.local {
		ns, ds := localCrumbs(filepath.Dir(b.file))
		names = ns
		targets = ds
	} else {
		for _, s := range strings.Split(parentDir(b.file), "/") {
			if s != "" {
				names = append(names, s)
				targets = append(targets, path.Join(targets[len(targets)-1], s))
			}
		}
	}
	fileAt := len(names)
	names = append(names, path.Base(filepath.ToSlash(b.file)))
	if b.path != "" {
		names = append(names, strings.Split(b.path, "/")...)
	}
	chosen := len(names) - 1
	if ui.Breadcrumbs(c, names, &chosen).Label("压缩包位置").Shrink(1).MinWidth(0).Changed() {
		if chosen < fileAt {
			if b.local {
				a.openLocal(targets[chosen])
			} else {
				a.showDirectory(b.server, b.root, targets[chosen])
			}
		} else {
			a.archiveBrowse(strings.Join(names[fileAt+1:chosen+1], "/"))
		}
	}
	ui.Badge(c, "压缩包 · 只读")
}
func (a *app) archiveView(c *ui.Context) {
	b := a.arc
	if c.Shortcut(0, ui.KeyF5) {
		a.archiveBrowse(b.path)
	}
	if !a.searchTyping && c.Shortcut(0, ui.KeyEscape) {
		b.sel.Clear()
	}
	if b.err != "" {
		notice(c, c.Theme().Danger, b.err)
	}
	if b.loading {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c)
			ui.Text(c, b.opLabel)
			ui.Button(c, "取消读取").OnClick(func() { b.cancel(); b.gen++; b.loading = false; b.err = "读取已取消，可刷新重试" })
		})
	}
	if b.sequential {
		notice(c, c.Theme().TextMuted, "此格式按顺序读取，靠后的文件打开较慢。音视频需下载后打开；搜索不包含压缩包内文件。")
	}
	zone := a.fileList(c, &fileView{filter: &b.filter, key: "archive-files", label: "压缩包文件", items: b.items, sel: &b.sel, cursor: &b.cursor, list: &b.list, gridSt: &b.gridState, grid: b.grid, sort: &b.sort, loading: b.loading, thumb: func(fileindex.Entry) *ui.Bitmap { return nil }, open: a.archiveOpen, resort: func() { sortEntries(&b.sort, b.items, &b.cursor) }, gridLast: &b.gridLast, empty: func() {
		if !b.loading && b.err == "" {
			emptyFolder(c, "")
		}
	}})
	zone.ContextMenu(func(m *ui.Menu) {
		groupMenu(m, &b.sort.group)
		chosen := b.chosen()
		if b.loading {
			return
		}
		if len(chosen) == 1 {
			e := chosen[0]
			if m.Item("打开").Disabled(e.Link || e.Encrypted).Chosen() {
				a.archiveOpen(e)
			}
			if !e.IsDir && m.Item("系统程序打开").Disabled(e.Link || e.Encrypted).Chosen() {
				a.systemOpenSource(b.server, b.client, e, true)
			}
		}
		a.archiveMenu(c, m, chosen)
	})
}
func (a *app) archiveStatus(c *ui.Context) {
	b := a.arc
	status := fmt.Sprintf("压缩包内 %d 项", len(b.items))
	if n := len(b.chosen()); n > 0 {
		status += fmt.Sprintf(" · 已选 %d 项", n)
	}
	statusText(c, status)
}
func (a *app) archiveFiles() *nasFiles {
	b := a.arc
	return &nasFiles{server: b.server, client: b.client, root: b.root.ID, dir: parentDir(b.file), fail: func(msg string) {
		if a.arc == b {
			b.err = msg
		}
	}, changed: func(string) {}, clear: func() { b.sel.Clear() }, running: func() bool { return false }, busy: func(string) {}}
}
func (a *app) archiveExtract(es []fileindex.Entry) {
	if len(es) == 0 {
		return
	}
	f := a.archiveFiles()
	pick := func(sv string, root int64, rootName, dir string) {
		a.relayIn(f, false, es, sv, root, dir, "解压", es[0].Name+" → "+a.target(sv, rootName, dir))
	}
	if f.client.ArchiveSource.Local {
		a.openPicker("", 0, "", pick)
	} else {
		a.openPickerIn(f.server, f.root, f.dir, pick)
	}
	a.nas.title = "解压到"
	a.nas.local = func() { a.downloadTo(f, false, es, "解压到本机") }
	a.nas.localDir = func(dir string) { a.downloadInto(f, false, es, dir) }
}
func (a *app) extractArchive(server string, c *fileindex.Client, e fileindex.Entry) {
	if c != nil && !c.InArchive() {
		f := &nasFiles{server: server, root: e.Root, dir: parentDir(e.Path), fail: func(msg string) { a.errorMsg = msg }, changed: func(string) {
			if a.onRoot() && a.br.server == server {
				a.reloadKeeping()
			}
		}}
		a.openPickerIn(server, e.Root, parentDir(e.Path), func(to string, root int64, rootName, dir string) {
			if to == server {
				a.startServerExtract(f, c, e.Root, e.Path, "", nil, root, dir, e.Name+" → "+a.target(to, rootName, dir))
				return
			}
			// Another server still uses the archive browser's relay selection.
			a.openArchive(server, c, e, "")
			a.arc.extract = false
			a.arc.extractTarget = func(items []fileindex.Entry) {
				a.relayIn(a.archiveFiles(), false, items, to, root, dir, "解压", e.Name+" → "+a.target(to, rootName, dir))
			}
		})
		a.nas.title = "解压到（同名项保留两份）"
		a.nas.local = func() { a.openArchive(server, c, e, ""); a.arc.extractLocal = true }
		return
	}
	a.openArchive(server, c, e, "")
	a.arc.extract = true
}

func (a *app) startServerExtract(f *nasFiles, c *fileindex.Client, srcRoot int64, filename, version string, entries []string, dstRoot int64, dir, title string) {
	if c.InArchive() {
		base, ok := a.serverClient(f.server)
		if !ok {
			return
		}
		c = base
	}
	tr := a.addTransfer("解压", title)
	tr.server = f.server
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	tr.cancel = cancel
	go func() {
		defer cancel()
		task, err := c.ExtractArchive(ctx, srcRoot, filename, version, entries, dstRoot, dir)
		a.post(func() {
			if err != nil {
				a.finishTransfer(tr, err, nil)
				return
			}
			tr.task = task.ID
			tr.after = func() { f.changed("") }
			a.trackServerTask(c, tr, task)
		})
	}()
}
func (a *app) archiveLocation() string {
	b := a.arc
	if b.local {
		return "/local?" + url.Values{"dir": {filepath.Dir(b.file)}, "archive": {b.file}, "entry": {b.path}}.Encode()
	}
	p := "/nas/" + b.server + "/" + strconv.FormatInt(b.root.ID, 10) + "/" + b.file + "!"
	if b.path != "" {
		p += "/" + b.path
	}
	u := url.URL{Path: p, RawQuery: url.Values{"archive": {b.file}, "dir": {b.path}}.Encode()}
	return u.String()
}

// archiveMenu is the menu of the members of an archive chosen, or of the
// folder shown of it without any.
func (a *app) archiveMenu(c *ui.Context, m *ui.Menu, chosen []fileindex.Entry) {
	b := a.arc
	if len(chosen) == 0 {
		if m.Item("全部解压到…").Disabled(b.loading || len(b.items) == 0).Chosen() {
			a.archiveExtract(b.items)
		}
		if m.Item("刷新").Shortcut(0, ui.KeyF5).Disabled(b.loading).Chosen() {
			a.archiveBrowse(b.path)
		}
		return
	}
	m.Separator()
	if m.Item("复制").Shortcut(ui.Ctrl, ui.KeyC).Chosen() {
		a.copyChosen(c, false)
	}

	if m.Item("解压到…").Chosen() {
		a.archiveExtract(chosen)
	}
	if m.Item("下载到…").Chosen() {
		a.downloadTo(a.archiveFiles(), false, chosen, "解压到本机")
	}
}
