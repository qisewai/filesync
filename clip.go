package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"strings"

	"filemanager/remoteclient"

	mt "github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// The clipboard of files: copied or cut where they are, pasted in the
// folder shown, of this computer or of any server, as a file manager
// does. What it takes to get them there is the paste's: a task of the
// server within one server, its export passed on to another, a download,
// an upload, or a copy among the folders of this computer.
//
// Files of this computer go on the system's clipboard too, as file
// managers put them, which they paste; files their clipboard holds, as
// copied or cut in one, paste here.

// fileClip is what was copied or cut.
type fileClip struct {
	signature string
	busy      bool
	move      bool
	// local: files of this computer, es their paths; else files of a
	// server, or of an archive, which files reads.
	local bool
	files *nasFiles
	es    []fileindex.Entry
	// from is where they are, as shown; dirs the folders holding them.
	from string
}

// title names what the clipboard holds, as a transfer does.
func (cl *fileClip) title() string {
	if len(cl.es) == 1 {
		return cl.es[0].Name
	}
	return fmt.Sprintf("%s 等 %d 项", cl.es[0].Name, len(cl.es))
}

// archive reports whether the files are of an archive, which only copies.
func (cl *fileClip) archive() bool {
	return cl.files != nil && cl.files.client != nil && cl.files.client.InArchive()
}

// pasteTarget is a folder to paste into: of this computer (local), or a
// folder of a root of a server.
type pasteTarget struct {
	local    string
	server   string
	root     int64
	rootName string
	dir      string
}

func (t pasteTarget) where(a *app) string {
	if t.local != "" {
		return t.local
	}
	return a.target(t.server, t.rootName, t.dir)
}

// clipSource is what the page shown has chosen, to copy, or with move to
// cut; or why it cannot be.
func (a *app) clipSource(move bool) (*fileClip, string) {
	switch {
	case a.onRoot():
		b := a.br
		es := b.chosen()
		if len(es) == 0 || b.loading {
			return nil, ""
		}
		if move && b.root.ReadOnly {
			return nil, "“" + b.root.Name + "”是只读目录，只能复制"
		}
		return &fileClip{move: move, files: a.brFiles(), es: es, from: a.nasWhere(b.server, b.root.ID, b.path)}, ""
	case a.onLocal():
		b := a.local
		es := b.chosen()
		if len(es) == 0 || b.loading {
			return nil, ""
		}
		return &fileClip{move: move, local: true, es: es, from: b.dir}, ""
	case a.onSearch():
		s := a.gsearch
		var chosen []searchHit
		var es []fileindex.Entry
		for _, h := range s.hits {
			if s.sel.Has(h.key()) {
				chosen = append(chosen, h)
				es = append(es, h.e)
			}
		}
		if len(chosen) == 0 {
			return nil, ""
		}
		acts := a.hitActions(chosen)
		switch {
		case acts.local:
			return &fileClip{move: move, local: true, es: es, from: "搜索结果"}, ""
		case acts.files == nil:
			return nil, "选中的结果来自不同位置，请分别复制"
		case move && !acts.writable:
			return nil, "选中的结果在只读目录中，只能复制"
		}
		return &fileClip{move: move, files: acts.files, es: es, from: "搜索结果"}, ""
	case a.onArchive():
		b := a.arc
		es := b.chosen()
		if len(es) == 0 || b.loading {
			return nil, ""
		}
		if move {
			return nil, "压缩包内容只读，只能复制"
		}
		return &fileClip{files: a.archiveFiles(), es: es, from: "压缩包 " + pathBase(b.file)}, ""
	}
	return nil, ""
}

// copyChosen puts what is chosen on the clipboard: cut with move.
func (a *app) copyChosen(c *ui.Context, move bool) {
	cl, why := a.clipSource(move)
	if why != "" {
		a.errorMsg = why
		return
	}
	if cl == nil {
		return
	}
	a.setClip(c.Services(), cl)
}

// setClip puts cl on the clipboard, and on the system's what of it is of
// this computer, or the names of the rest.
const (
	clipOwner  mt.Format = "application/x-filesync-clipboard-owner"
	gnomeFiles mt.Format = "x-special/gnome-copied-files"
	kdeCut     mt.Format = "application/x-kde-cutselection"
	dropEffect mt.Format = "Preferred DropEffect"
)

var fileClipFormats = []mt.Format{clipOwner, mt.FileList, mt.URIList, gnomeFiles, kdeCut, dropEffect}

func (a *app) setClip(sv ui.Services, cl *fileClip) {
	var data mt.Data
	if cl.local {
		paths := make([]string, len(cl.es))
		for i, e := range cl.es {
			paths[i] = e.Path
		}
		var err error
		data, err = systemFiles(paths, cl.move)
		if err != nil {
			a.errorMsg = err.Error()
			return
		}
	} else {
		var names []string
		for _, e := range cl.es {
			names = append(names, e.Name)
		}
		data = mt.New(mt.NewItem(mt.Bytes(mt.Text, []byte(strings.Join(names, "\n")))))
	}
	token := rand.Text()
	data = ownedClipboard(data, token)
	sv.WriteClipboardData(data)
	a.clip, a.clipSys = cl, token
	cl.signature = clipboardSignature(data)
}

// FileList is needed for native Windows CF_HDROP and macOS file URLs;
// URIList and desktop-specific operation flags cover Linux file managers.
func systemFiles(paths []string, cut bool) (mt.Data, error) {
	files, err := mt.FileData(paths...)
	if err != nil {
		return mt.Data{}, err
	}
	list, err := files.Read(mt.URIList)
	if err != nil {
		return mt.Data{}, err
	}
	op, kde, effect := "copy", "0", uint32(1)
	if cut {
		op, kde, effect = "cut", "1", 2
	}
	win := make([]byte, 4)
	binary.LittleEndian.PutUint32(win, effect)
	return mt.New(mt.NewItem(
		mt.Bytes(mt.FileList, list), mt.Bytes(mt.URIList, list),
		mt.Bytes(gnomeFiles, []byte(op+"\n"+strings.ReplaceAll(string(list), "\r\n", "\n"))),
		mt.Bytes(kdeCut, []byte(kde)), mt.Bytes(dropEffect, win),
		mt.Bytes(mt.Text, []byte(strings.Join(paths, "\n"))),
	)), nil
}

func clipboardSignature(data mt.Data) string {
	h := sha256.New()
	for _, format := range fileClipFormats {
		if b, err := data.Read(format); err == nil {
			fmt.Fprintf(h, "%s:%d:", format, len(b))
			h.Write(b)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func decodeSystemClip(data mt.Data) *fileClip {
	files, _ := data.Files()
	op := ""
	if b, err := data.Read(gnomeFiles); err == nil {
		head, body, ok := strings.Cut(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
		if ok && (head == "copy" || head == "cut") {
			op = head
			if len(files) == 0 {
				files, _ = mt.New(mt.NewItem(mt.Bytes(mt.URIList, []byte(body)))).Files()
			}
		}
	}
	if len(files) == 0 {
		return nil
	}
	cl := &fileClip{local: true, from: filepath.Dir(files[0]), signature: clipboardSignature(data)}
	if b, err := data.Read(kdeCut); err == nil {
		cl.move = strings.TrimSpace(string(b)) == "1"
	}
	if op != "" {
		cl.move = op == "cut"
	}
	if b, err := data.Read(dropEffect); err == nil && len(b) == 4 {
		cl.move = binary.LittleEndian.Uint32(b) == 2
	}
	seen := map[string]bool{}
	for _, f := range files {
		f = filepath.Clean(f)
		if seen[f] {
			continue
		}
		seen[f] = true
		cl.es = append(cl.es, fileindex.Entry{Name: filepath.Base(f), Path: f})
	}
	return cl
}

// pasteTargetShown is the folder shown, to paste into; or why it cannot
// take what is pasted.
func (a *app) pasteTargetShown() (pasteTarget, string) {
	switch {
	case a.onRoot():
		b := a.br
		if b.root.ReadOnly {
			return pasteTarget{}, "“" + b.root.Name + "”是只读目录，不能粘贴"
		}
		return pasteTarget{server: b.server, root: b.root.ID, rootName: b.root.Name, dir: b.path}, ""
	case a.onLocal():
		return pasteTarget{local: a.local.dir}, ""
	case a.onArchive():
		return pasteTarget{}, "压缩包内容只读，不能粘贴"
	}
	return pasteTarget{}, "请先打开要粘贴到的文件夹"
}

// clipNow is what a paste pastes: what another app put on the system's
// clipboard since, else what the app copied or cut.
func (a *app) clipNow(sv ui.Services) *fileClip {
	data := sv.ReadClipboardData(fileClipFormats...)
	if token, err := data.Read(clipOwner); err == nil && a.clipSys != "" && string(token) == a.clipSys {
		return a.clip
	}
	external := decodeSystemClip(data)
	if external != nil && a.clip != nil && external.signature == a.clip.signature {
		return a.clip
	}
	a.clip, a.clipSys = external, ""
	return external
}

func (a *app) hasPaste(sv ui.Services) bool { return a.clipNow(sv) != nil }

func (a *app) clearClip(sv ui.Services, cl *fileClip) {
	if cl == nil || a.clipNow(sv) != cl {
		return
	}
	sv.WriteClipboardData(mt.Data{})
	a.clip, a.clipSys = nil, ""
}

// pasteShown pastes into the folder shown.
func (a *app) pasteShown(c *ui.Context) {
	t, why := a.pasteTargetShown()
	if why != "" {
		a.errorMsg = why
		return
	}
	a.paste(c.Services(), t)
}

// paste pastes what the clipboard holds into t. A cut, pasted, leaves
// the clipboard: its files are no longer where it says.
func (a *app) paste(sv ui.Services, t pasteTarget) {
	cl := a.clipNow(sv)
	if cl == nil || len(cl.es) == 0 || cl.busy {
		return
	}
	kind := "复制"
	if cl.move {
		kind = "移动"
	}
	title := cl.title() + " → " + t.where(a)
	switch {
	case cl.local && t.local != "":
		paths := make([]string, len(cl.es))
		for i, e := range cl.es {
			paths[i] = e.Path
		}
		a.localTransfer(paths, cl.move, t.local, localTransferCallbacks{
			started: func() { cl.busy = true },
			finished: func(ok bool) {
				cl.busy = false
				if ok && cl.move {
					a.clearClip(sv, cl)
				}
			},
		})
		return
	case cl.local:
		paths := make([]string, len(cl.es))
		for i, e := range cl.es {
			paths[i] = e.Path
		}
		a.uploadInto(paths, cl.move, t.server, t.root, t.rootName, t.dir, localTransferCallbacks{
			started: func() { cl.busy = true },
			finished: func(ok bool) {
				cl.busy = false
				if ok && cl.move {
					a.clearClip(sv, cl)
				}
			},
		})
		return
	case t.local != "":
		a.downloadInto(cl.files, cl.move, cl.es, t.local)
	case cl.archive() || t.server != cl.files.server:
		a.relayIn(cl.files, cl.move, cl.es, t.server, t.root, t.dir, kind, title)
	default:
		if !a.serverPaste(cl, t, kind, title) {
			return
		}
	}
	if cl.move {
		if cl == a.clip {
			a.clip = nil
		}
		a.clipSys = ""
	}
}

// serverPaste copies or moves files within one server, as a task of the
// server; a copy into their own folder takes numbered names. It reports
// whether it started.
func (a *app) serverPaste(cl *fileClip, t pasteTarget, kind, title string) bool {
	es := cl.es
	if cl.move && t.root == es[0].Root {
		here := 0
		for _, e := range es {
			if e.IsDir && (t.dir == e.Path || strings.HasPrefix(t.dir, e.Path+"/")) {
				a.errorMsg = fmt.Sprintf("不能把“%s”移动到它自己里面", e.Name)
				return false
			}
			if e.Parent == t.dir {
				here++
			}
		}
		if here == len(es) {
			a.errorMsg = "所选项目已在当前文件夹中，无需移动"
			return false
		}
	}
	c, ok := a.serverClient(cl.files.server)
	if !ok {
		return false
	}
	f := cl.files
	tr := a.addTransfer(kind, title)
	tr.server = f.server
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	tr.cancel = cancel
	srcRoot := es[0].Root
	go func() {
		defer cancel()
		task, err := c.Transfer(ctx, cl.move, srcRoot, paths(es), t.root, t.dir)
		a.post(func() {
			if err != nil {
				a.finishTransfer(tr, err, nil)
				return
			}
			tr.task = task.ID
			tr.after = func() {
				f.changed("")
				a.nasChanged(t.server, t.root, t.dir)
			}
			a.trackServerTask(c, tr, task)
		})
	}()
	return true
}

// nasChanged reads a folder of a server again, if the page shows it.
func (a *app) nasChanged(server string, root int64, dir string) {
	if a.onRoot() && a.br.server == server && a.br.root.ID == root && a.br.path == dir {
		a.reloadKeeping()
	}
}

// uploadInto uploads files of this computer into dir of a root of a
// server, asking first to replace what it has of the same names; with
// move, what landed whole then goes to the trash of this computer.
func (a *app) uploadInto(files []string, move bool, server string, root int64, rootName, dir string, callbacks ...localTransferCallbacks) {
	c, ok := a.serverClient(server)
	if !ok {
		return
	}
	kind := "复制"
	if move {
		kind = "移动"
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		there, err := c.List(ctx, root, dir)
		a.post(func() {
			if a.closed {
				return
			}
			if err != nil {
				a.errorMsg = "无法读取目标目录：" + err.Error()
				return
			}
			have := map[string]bool{}
			for _, e := range there {
				have[e.Name] = true
			}
			var taken []string
			for _, f := range files {
				if have[filepath.Base(f)] {
					taken = append(taken, filepath.Base(f))
				}
			}
			start := func() {
				a.startUploadTo(server, root, a.target(server, rootName, dir), dir, files, move, func() { a.nasChanged(server, root, dir) }, callbacks...)
			}
			if len(taken) == 0 {
				start()
				return
			}
			a.ask("合并同名目录并覆盖文件？", fmt.Sprintf("目标目录中已经有 %s 等 %d 项与%s的同名。同名文件夹将合并，目标中独有的文件会保留；同名文件将被源文件覆盖，不再逐个询问。", strings.Join(taken[:min(3, len(taken))], "、"), len(taken), kind), "合并并覆盖", start)
		})
	}()
}

// clipShortcuts are the keys of the clipboard on the pages of files:
// Ctrl+C copies what is chosen, Ctrl+X cuts it, Ctrl+V pastes into the
// folder shown; not while typing.
func (a *app) clipShortcuts(c *ui.Context) {
	if a.searchTyping || a.naming != nil || a.renaming != nil || a.deleting != nil || a.asking != nil {
		return
	}
	if !a.onRoot() && !a.onLocal() && !a.onSearch() && !a.onArchive() {
		return
	}
	switch {
	case c.Shortcut(ui.Ctrl, ui.KeyC):
		a.copyChosen(c, false)
	case c.Shortcut(ui.Ctrl, ui.KeyX):
		a.copyChosen(c, true)
	case c.Shortcut(ui.Ctrl, ui.KeyV):
		a.pasteShown(c)
	}
}

// clipItems are the clipboard's items of a menu: cut, copy, and paste
// into the folder shown, consistently with Ctrl+V.
func (a *app) clipItems(c *ui.Context, m *ui.Menu, es []fileindex.Entry) {
	if len(es) > 0 {
		_, whyCut := a.clipSource(true)
		if m.Item("剪切").Shortcut(ui.Ctrl, ui.KeyX).Disabled(whyCut != "").Chosen() {
			a.copyChosen(c, true)
		}
		if m.Item("复制").Shortcut(ui.Ctrl, ui.KeyC).Chosen() {
			a.copyChosen(c, false)
		}

	}
	paste := a.hasPaste(c.Services())
	_, why := a.pasteTargetShown()
	if why != "" && len(es) > 0 {
		return // nowhere to paste, as among results
	}
	if m.Item("粘贴").Shortcut(ui.Ctrl, ui.KeyV).Disabled(!paste || why != "").Chosen() {
		a.pasteShown(c)
	}
}

// clipCut reports whether the clipboard holds e of the page shown, cut:
// shown faded, as about to leave.
func (a *app) clipCut() func(e fileindex.Entry) bool {
	cl := a.clip
	if cl == nil || !cl.move {
		return nil
	}
	keys := map[string]bool{}
	for _, e := range cl.es {
		if cl.local {
			keys[e.Path] = true
		} else {
			keys[fmt.Sprintf("%s\x00%d\x00%s", cl.files.server, e.Root, e.Path)] = true
		}
	}
	switch {
	case a.onLocal() && cl.local:
		return func(e fileindex.Entry) bool { return keys[e.Path] }
	case a.onRoot() && !cl.local:
		server := a.br.server
		return func(e fileindex.Entry) bool { return keys[fmt.Sprintf("%s\x00%d\x00%s", server, e.Root, e.Path)] }
	}
	return nil
}

// clipIndicator shows, in the status bar, what the clipboard holds; a
// click lets go of it.
func (a *app) clipIndicator(c *ui.Context) {
	cl := a.clip
	if cl == nil {
		return
	}
	t := c.Theme()
	verb := "已复制"
	if cl.move {
		verb = "已剪切"
	}
	text := fmt.Sprintf("%s %d 项", verb, len(cl.es))

	chip := ui.Row(c.Key("clip-indicator")).Gap(6).AlignItems(ui.Center).Padding(2, 4, 2, 8).Radius(t.Radius).Background(t.Surface).
		Tooltip(fmt.Sprintf("%s：%s\n来自 %s\n在目标文件夹按 Ctrl+V 粘贴", verb, cl.title(), cl.from))
	chip.Children(func() {
		svg := iconCopy
		if cl.move {
			svg = iconMove
		}
		ui.Icon(c, svg).FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, text).FontSize(12).SingleLine().TextColor(t.TextMuted)
		ui.ButtonBase(c.Key("clip-clear")).Size(16, 16).Radius(4).Label("清空剪贴板").Tooltip("清空").OnClick(func() {
			a.clearClip(c.Services(), cl)
		}).Children(func() { ui.Icon(c, iconClose).FontSize(9).TextColor(t.TextMuted) })
	})
}

// nasFilesAt are the files of a folder of a server, as the sidebar
// chooses them, not of a page: a change reads the folder again where it
// shows.
func (a *app) nasFilesAt(server string, root fileindex.Root, dir string) *nasFiles {
	return &nasFiles{
		server:  server,
		root:    root.ID,
		dir:     dir,
		running: func() bool { return false },
		busy:    func(string) {},
		fail:    func(msg string) { a.errorMsg = msg },
		changed: func(string) { a.nasChanged(server, root.ID, dir) },
		clear:   func() {},
	}
}

func ownedClipboard(data mt.Data, token string) mt.Data {
	reps := []mt.Representation{mt.Bytes(clipOwner, []byte(token))}
	for _, format := range data.Formats() {
		if b, err := data.Read(format); err == nil {
			reps = append(reps, mt.Bytes(format, b))
		}
	}
	return mt.New(mt.NewItem(reps...))
}
