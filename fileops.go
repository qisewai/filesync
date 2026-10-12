package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"filesync/fileindex"

	"github.com/egoist/mygo/ui"
)

// The operations on files of a server: from the page of a folder, or from
// the page of the search, which tell where they run, and what follows.

// nasFiles is where the operations on files of a server run from.
type nasFiles struct {
	server string
	// root and dir are where the pickers of a target start.
	root int64
	dir  string
	// running reports an operation under way, which busy shows, ""
	// once it ended; fail tells why one failed.
	running func() bool
	busy    func(label string)
	fail    func(msg string)
	// changed follows a change of the files, choosing the entry named so,
	// if any; clear lets go of the files chosen.
	changed func(choose string)
	clear   func()
}

// serverClient is a client of a server, nil for a server removed.
func (a *app) serverClient(server string) (*fileindex.Client, bool) {
	sv := a.store.Server(server)
	if sv == nil {
		return nil, false
	}
	return fileindex.New(sv.Addr, sv.Token), true
}

// brFiles are the files of the folder of a server shown.
func (a *app) brFiles() *nasFiles {
	b := a.br
	return &nasFiles{
		server:  b.server,
		root:    b.root.ID,
		dir:     b.path,
		running: func() bool { return b.operating },
		busy:    func(label string) { b.operating, b.opLabel = label != "", label },
		fail: func(msg string) {
			if a.br == b {
				b.err = msg
			}
		},
		changed: func(choose string) {
			if a.br != b {
				return
			}
			b.err = ""
			if choose != "" {
				a.browse(b.path, choose)
			} else {
				a.reloadKeeping()
			}
		},
		clear: func() { b.sel.Clear() },
	}
}

// opIn runs a quick operation of the server, then tells the change.
func (a *app) opIn(f *nasFiles, label string, fn func(ctx context.Context, c *fileindex.Client) error, choose string) {
	if f.running() {
		return
	}
	c, ok := a.serverClient(f.server)
	if !ok {
		return
	}
	f.busy(label)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		err := fn(ctx, c)
		a.post(func() {
			f.busy("")
			if err != nil {
				f.fail(err.Error())
				if errors.Is(err, fileindex.ErrAuth) {
					if sv := a.store.Server(f.server); sv != nil {
						a.signedOut(sv)
					}
				}
				return
			}
			f.changed(choose)
		})
	}()
}

// downloadIn asks where, then downloads the entries there.
func (a *app) downloadIn(f *nasFiles, es []fileindex.Entry) { a.downloadTo(f, false, es, "下载到") }

// downloadTo asks for a directory of this computer, then downloads the
// entries there, asking first to replace what has their names; with move,
// the entries then go to the trash of the server.
func (a *app) downloadTo(f *nasFiles, move bool, es []fileindex.Entry, title string) {
	c, ok := a.serverClient(f.server)
	if !ok || len(es) == 0 {
		return
	}
	go func() {
		dest, err := a.pickDir(title, "")
		a.post(func() {
			if a.closed {
				return
			}
			if err != nil {
				f.fail(err.Error())
				return
			}
			if dest == "" {
				return
			}
			var taken []string
			for _, e := range es {
				if _, err := os.Lstat(filepath.Join(dest, e.Name)); err == nil {
					taken = append(taken, e.Name)
				}
			}
			start := func() { a.startDownloadFrom(c, f, es, dest, move) }
			if len(taken) == 0 {
				start()
				return
			}
			a.ask("替换本机同名的项？", fmt.Sprintf("%s 中已经有 %s 等 %d 项与下载的同名。文件会被替换，文件夹会合并。", dest, strings.Join(taken[:min(3, len(taken))], "、"), len(taken)), "替换", start)
		})
	}()
}

// transferIn asks for a directory, then copies or moves the entries
// there: on the same server as a task of the server, on another through
// this computer.
func (a *app) transferIn(f *nasFiles, move bool, es []fileindex.Entry) {
	server := f.server
	c, ok := a.serverClient(server)
	if !ok || len(es) == 0 {
		return
	}
	kind := "复制"
	if move {
		kind = "移动"
	}
	a.openPickerIn(server, f.root, f.dir, func(sv string, root int64, rootName, dir string) {
		title := es[0].Name
		if len(es) > 1 {
			title = fmt.Sprintf("%s 等 %d 项", title, len(es))
		}
		if sv != server {
			a.relayIn(f, move, es, sv, root, dir, kind, title+" → "+a.target(sv, rootName, dir))
			return
		}
		tr := a.addTransfer(kind, title+" → "+path.Join(rootName, dir))
		tr.server = server
		srcRoot := es[0].Root
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		tr.cancel = cancel
		go func() {
			defer cancel()
			task, err := c.Transfer(ctx, move, srcRoot, paths(es), root, dir)
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
	})
	a.nas.title = kind + "到"
	a.nas.local = func() { a.downloadTo(f, move, es, kind+"到本机") }
}

// relayIn copies or moves the entries into dir of a root of another
// server, asking first to replace what it has of the same names.
func (a *app) relayIn(f *nasFiles, move bool, es []fileindex.Entry, to string, root int64, dir, kind, title string) {
	src, ok := a.serverClient(f.server)
	dst, ok2 := a.serverClient(to)
	if !ok || !ok2 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		there, err := dst.List(ctx, root, dir, true)
		a.post(func() {
			if a.closed {
				return
			}
			if err != nil {
				f.fail("无法读取目标目录：" + err.Error())
				return
			}
			names := map[string]bool{}
			for _, e := range there {
				names[e.Name] = true
			}
			var taken []string
			for _, e := range es {
				if names[e.Name] {
					taken = append(taken, e.Name)
				}
			}
			start := func() { a.startRelay(f, src, dst, move, es, to, root, dir, kind, title) }
			if len(taken) == 0 {
				start()
				return
			}
			a.ask("替换目标中同名的项？", fmt.Sprintf("目标目录中已经有 %s 等 %d 项与%s的同名。文件会被替换，文件夹会合并。", strings.Join(taken[:min(3, len(taken))], "、"), len(taken), kind), "替换", start)
		})
	}()
}

// startRelay passes the export of the entries on to the other server, as
// a transfer of this computer; a move then trashes what landed whole.
func (a *app) startRelay(f *nasFiles, src, dst *fileindex.Client, move bool, es []fileindex.Entry, to string, root int64, dir, kind, title string) {
	tr := a.addTransfer(kind, title)
	tr.server, tr.peer = f.server, to
	ctx, cancel := context.WithCancel(context.Background())
	tr.cancel = cancel
	srcRoot := es[0].Root
	onProgress := func(p fileindex.Progress) {
		a.post(func() { tr.done, tr.current = p.Done, p.Current; tr.total = max(p.Total, tr.total) })
	}
	go func() {
		whole, errs, err := fileindex.Relay(ctx, src, srcRoot, paths(es), dst, root, dir, onProgress)
		var failed []string
		for _, e := range errs {
			failed = append(failed, e.Error())
		}
		var moved bool
		if move && err == nil {
			// Only what landed whole leaves; the export numbers names
			// chosen twice, which are then all kept.
			count := map[string]int{}
			for _, e := range es {
				count[e.Name]++
			}
			var gone []string
			for _, e := range es {
				if count[e.Name] == 1 && whole[e.Name] {
					gone = append(gone, e.Path)
				} else {
					failed = append(failed, e.Name+": 没有完整复制，保留在源服务器上")
				}
			}
			if len(gone) > 0 {
				if err = src.Trash(ctx, srcRoot, gone); err != nil {
					err = fmt.Errorf("已复制到目标，但源文件未能移入回收站：%w", err)
				} else {
					moved = true
				}
			}
		}
		a.post(func() {
			a.finishTransfer(tr, err, failed)
			if moved {
				f.changed("")
			}
		})
	}()
}

// trashIn moves es to the trash.
func (a *app) trashIn(f *nasFiles, es []fileindex.Entry) {
	label := fmt.Sprintf("正在把 %d 项移到回收站…", len(es))
	if len(es) == 1 {
		label = "正在把“" + es[0].Name + "”移到回收站…"
	}
	root, ps := es[0].Root, paths(es)
	a.opIn(f, label, func(ctx context.Context, c *fileindex.Client) error { return c.Trash(ctx, root, ps) }, "")
	f.clear()
}

// deleteForeverIn deletes es, not to the trash, as a task of the server
// that the transfers show.
func (a *app) deleteForeverIn(f *nasFiles, es []fileindex.Entry) {
	c, ok := a.serverClient(f.server)
	if !ok {
		return
	}
	title := es[0].Name
	if len(es) > 1 {
		title = fmt.Sprintf("%s 等 %d 项", title, len(es))
	}
	tr := a.addTransfer("彻底删除", title)
	tr.server = f.server
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	tr.cancel = cancel
	root, ps := es[0].Root, paths(es)
	f.clear()
	go func() {
		defer cancel()
		task, err := c.DeleteForever(ctx, root, ps)
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

// The dialogs of the operations, over any page.

// deleteAsk asks before deleting entries, to the trash or, with forever,
// not.
type deleteAsk struct {
	f       *nasFiles
	es      []fileindex.Entry
	forever bool
}

// renameAsk asks the new name of an entry.
type renameAsk struct {
	f  *nasFiles
	e  fileindex.Entry
	to string
}

// confirmAsk asks to confirm an action.
type confirmAsk struct {
	title, msg, ok string
	action         func()
}

// askDeleteIn asks before deleting es.
func (a *app) askDeleteIn(f *nasFiles, es []fileindex.Entry, forever bool) {
	if len(es) > 0 {
		a.deleting = &deleteAsk{f: f, es: slices.Clone(es), forever: forever}
	}
}

// renameIn asks the new name of e.
func (a *app) renameIn(f *nasFiles, e fileindex.Entry) {
	a.renaming = &renameAsk{f: f, e: e, to: e.Name}
}

// ask asks to confirm an action, which runs if confirmed.
func (a *app) ask(title, msg, ok string, action func()) {
	a.asking = &confirmAsk{title: title, msg: msg, ok: ok, action: action}
}

// fileDialogs are the dialogs of the operations on files, open.
func (a *app) fileDialogs(c *ui.Context) {
	if r := a.renaming; r != nil {
		open := true
		ui.Modal(c.Key("rename-dialog"), &open, func() {
			ui.Text(c, "重命名").Bold()
			done := ui.TextInput(c.Key("rename"), &r.to).AutoFocus().Width(320).Submitted()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "取消").Clicked() {
					open = false
				}
				name := strings.TrimSpace(r.to)
				ok := name != "" && name != r.e.Name
				if ui.PrimaryButton(c, "重命名").Disabled(!ok).Clicked() || done && ok {
					open = false
					root, p := r.e.Root, r.e.Path
					a.opIn(r.f, "正在重命名为“"+name+"”…", func(ctx context.Context, c *fileindex.Client) error { return c.Rename(ctx, root, p, name) }, name)
				}
			})
		})
		if !open && a.renaming == r {
			a.renaming = nil
		}
	}
	if d := a.deleting; d != nil {
		a.deleteDialog(c, d)
	}
	if q := a.asking; q != nil {
		open := true
		if ui.AlertDialog(c, &open, q.title, q.msg, "取消", q.ok) == 1 && q.action != nil {
			q.action()
		}
		if !open && a.asking == q {
			a.asking = nil
		}
	}
}

func (a *app) deleteDialog(c *ui.Context, d *deleteAsk) {
	es := d.es
	open := true
	title := fmt.Sprintf("删除所选的 %d 项？", len(es))
	if len(es) == 1 {
		title = "删除“" + es[0].Name + "”？"
	}
	if d.forever {
		msg := "彻底删除后无法恢复：不会进入回收站。"
		if len(es) > 1 || es[0].IsDir {
			msg = "文件夹里的所有内容也会一起删除。" + msg
		}
		// In red, the focus on Cancel: Enter deletes nothing.
		t := c.Theme()
		ui.Modal(c.Key("delete-forever"), &open, func() {
			ui.Column(c).Width(340).Gap(10).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, iconAlert).FontSize(18).TextColor(t.Danger)
					ui.Text(c, title).Bold().Grow(1).Shrink(1).MaxLines(2)
				})
				ui.Text(c, msg).TextColor(t.TextMuted)
				ui.Row(c).Gap(8).Justify(ui.End).Margin(6, 0, 0, 0).Children(func() {
					if ui.Button(c, "取消").AutoFocus().Clicked() {
						open = false
					}
					del := ui.PrimaryButton(c, "彻底删除").Background(t.Danger)
					if del.Hovered() {
						del.Background(t.Danger.Mix(ui.RGBA(0, 0, 0, 1), 0.12))
					}
					if del.Clicked() {
						open = false
						a.deleteForeverIn(d.f, es)
					}
				})
			})
		})
	} else {
		msg := fmt.Sprintf("%d 项会移到 fileindex 的回收站，可以在左侧的回收站中恢复。", len(es))
		switch ui.AlertDialog(c, &open, title, msg, "取消", "彻底删除…", "移到回收站") {
		case 1:
			d.forever = true
			open = true
		case 2:
			a.trashIn(d.f, es)
		}
	}
	if !open && a.deleting == d {
		a.deleting = nil
	}
}
