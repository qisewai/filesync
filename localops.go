package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filemanager/api"
	fmfs "filemanager/fs"

	"github.com/egoist/mygo/ui"
)

// The operations on the files of this computer, as a file manager has
// them: a new folder, renaming, deleting to the trash, and copying and
// moving among its folders (the clipboard pastes with them, clip.go).

// nameAsk asks for a name, as of a new folder, or the new name of a file.
type nameAsk struct {
	title, ok string
	name      string
	submit    func(name string) error
	err       string
}

// askName asks for a name, then runs submit with it.
func (a *app) askName(title, ok, name string, submit func(name string) error) {
	a.naming = &nameAsk{title: title, ok: ok, name: name, submit: submit}
}

func (a *app) nameDialog(c *ui.Context) {
	d := a.naming
	if d == nil {
		return
	}
	open := true
	ui.Modal(c.Key("name-dialog"), &open, func() {
		ui.Text(c, d.title).Bold()
		done := ui.TextInput(c.Key("name-input"), &d.name).Placeholder("名称").AutoFocus().Width(320).Submitted()
		if d.err != "" {
			ui.Text(c, d.err).FontSize(12).TextColor(c.Theme().Danger).MaxWidth(320)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Clicked() {
				open = false
			}
			name := strings.TrimSpace(d.name)
			if ui.PrimaryButton(c, d.ok).Disabled(name == "").Clicked() || done && name != "" {
				if err := d.submit(name); err != nil {
					d.err = err.Error()
				} else {
					open = false
				}
			}
		})
	})
	if !open && a.naming == d {
		a.naming = nil
	}
}

// validName reports why a name cannot be a file's, nil when it can.
func validName(name string) error {
	switch {
	case name == "" || name == "." || name == "..":
		return errors.New("名称无效")
	case strings.ContainsAny(name, `/\`+"\x00"):
		return errors.New("名称不能包含 / 或 \\")
	}
	return nil
}

// localMkdir asks the name of a new folder in dir, and makes it.
func (a *app) localMkdir(dir string) {
	a.askName("新建文件夹", "创建", "新建文件夹", func(name string) error {
		if err := validName(name); err != nil {
			return err
		}
		p := filepath.Join(dir, name)
		if _, err := os.Lstat(p); err == nil {
			return fmt.Errorf("“%s”已经存在", name)
		}
		log, err := a.operationLog()
		if err != nil {
			return err
		}
		opErr, logErr := loggedLocalOperation(log, "mkdir", "", p, func() error { return a.localMutation("mkdir", "", p) })
		if logErr != nil {
			a.errorMsg = logErr.Error()
		}
		if opErr != nil {
			return opErr
		}
		a.localChanged(dir, name)
		return nil
	})
}

// localRename asks the new name of a file or folder of this computer.
func (a *app) localRename(p string) {
	old := filepath.Base(p)
	a.askName("重命名", "重命名", old, func(name string) error {
		if name == old {
			return nil
		}
		if err := validName(name); err != nil {
			return err
		}
		to := filepath.Join(filepath.Dir(p), name)
		if _, err := os.Lstat(to); err == nil && !strings.EqualFold(name, old) {
			return fmt.Errorf("“%s”已经存在", name)
		}
		log, err := a.operationLog()
		if err != nil {
			return err
		}
		opErr, logErr := loggedLocalOperation(log, "rename", p, to, func() error { return a.localMutation("rename", p, to) })
		if logErr != nil {
			a.errorMsg = logErr.Error()
		}
		if opErr != nil {
			return opErr
		}
		a.localChanged(filepath.Dir(p), name)
		return nil
	})
}

// localDelete asks before moving files of this computer to the trash, or,
// with forever, deleting them for good.
func (a *app) localDelete(paths []string, forever bool) {
	if len(paths) == 0 {
		return
	}
	what := fmt.Sprintf("所选的 %d 项", len(paths))
	if len(paths) == 1 {
		what = "“" + filepath.Base(paths[0]) + "”"
	}
	title, msg, ok := "删除"+what+"？", "会移到系统的回收站，可以从回收站恢复。", "移到回收站"
	if forever {
		title, msg, ok = "彻底删除"+what+"？", "彻底删除后无法恢复：不会进入回收站。", "彻底删除"
	}
	dir := filepath.Dir(paths[0])
	a.ask(title, msg, ok, func() {
		log, err := a.operationLog()
		if err != nil {
			a.errorMsg = err.Error()
			return
		}
		var failed []string
		for _, p := range paths {
			action := "trash"
			if forever {
				action = "delete"
			}
			opErr, logErr := loggedLocalOperation(log, action, p, "", func() error {
				if forever {
					return a.localMutation("delete", p, "")
				}
				return a.localMutation("trash", p, "")
			})
			if err := errors.Join(opErr, logErr); err != nil {
				failed = append(failed, filepath.Base(p)+"："+err.Error())
			}
		}
		if len(failed) > 0 {
			a.errorMsg = "删除失败：" + strings.Join(failed, "；")
		}
		if a.local != nil {
			a.local.sel.Clear()
		}
		a.localChanged(dir)
	})
}

// localTransfer copies, or moves, files of this computer into dir: into
// their own folder, a copy takes a numbered name, a move does nothing;
// names taken elsewhere are asked about, files then replaced and folders
// merged.
func (a *app) localTransfer(files []string, move bool, dir string) bool {
	if len(files) == 0 {
		return false
	}
	kind := "复制"
	if move {
		kind = "移动"
	}
	type item struct{ from, to string }
	var items []item
	var taken []string
	for _, f := range files {
		if within(dir, f) {
			a.errorMsg = fmt.Sprintf("不能把“%s”%s到它自己里面", filepath.Base(f), kind)
			return false
		}
		to := filepath.Join(dir, filepath.Base(f))
		switch {
		case filepath.Dir(f) == filepath.Clean(dir) && move:
			continue // already there
		case filepath.Dir(f) == filepath.Clean(dir):
			to = numbered(dir, filepath.Base(f))
		default:
			if _, err := os.Lstat(to); err == nil {
				taken = append(taken, filepath.Base(f))
			}
		}
		items = append(items, item{f, to})
	}
	if len(items) == 0 {
		a.errorMsg = "所选项目已在当前文件夹中，无需移动"
		return false
	}
	start := func() bool {
		log, err := a.operationLog()
		if err != nil {
			a.errorMsg = err.Error()
			return false
		}
		title := filepath.Base(items[0].from)
		if len(items) > 1 {
			title = fmt.Sprintf("%s 等 %d 项", title, len(items))
		}
		tr := a.addTransfer(kind, title+" → "+dir)
		tr.localDir = dir
		ctx, cancel := context.WithCancel(context.Background())
		tr.cancel = cancel
		go func() {
			var total int64
			for _, it := range items {
				total += treeSize(it.from)
			}
			a.post(func() { tr.total = total })
			var failed []string
			var done int64
			progress := func(n int64, current string) {
				done += n
				d := done
				a.post(func() { tr.done, tr.current = d, current })
			}
			var err error
			for _, it := range items {
				if err = ctx.Err(); err != nil {
					break
				}
				action := "copy"
				if move {
					action = "move"
				}
				opErr, logErr := loggedLocalOperation(log, action, it.from, it.to, func() error {
					if move {
						return moveTreeChecked(ctx, it.from, it.to, progress, log.CheckWritePath)
					}
					return copyTreeChecked(ctx, it.from, it.to, progress, log.CheckWritePath)
				})
				e := errors.Join(opErr, logErr)
				if errors.Is(e, context.Canceled) {
					err = e
					break
				}
				if e != nil {
					failed = append(failed, filepath.Base(it.from)+"："+e.Error())
				}
			}
			a.post(func() {
				a.finishTransfer(tr, err, failed)
				a.localChanged(dir)
				if move {
					a.localChanged(filepath.Dir(items[0].from))
				}
			})
		}()
		return true
	}
	if len(taken) == 0 {
		return start()
	}
	a.ask("合并同名目录并覆盖文件？", fmt.Sprintf("%s 中已经有 %s 等 %d 项与%s的同名。同名文件夹将合并，目标中独有的文件会保留；同名文件将被源文件覆盖，不再逐个询问。", dir, strings.Join(taken[:min(3, len(taken))], "、"), len(taken), kind), "合并并覆盖", func() { start() })
	return true
}

// numbered is a name for a copy of name in dir that nothing there has:
// “报告 (2).txt”, as file managers name them.
func numbered(dir, name string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if ext == name { // a name of a dot, as .bashrc
		base, ext = name, ""
	}
	for n := 2; ; n++ {
		p := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, n, ext))
		if _, err := os.Lstat(p); err != nil {
			return p
		}
	}
}

// Compatibility wrappers keep UI orchestration separate from the shared core.
func treeSize(p string) int64 { return fmfs.TreeSize(p) }
func moveTree(ctx context.Context, from, to string, progress func(int64, string)) error {
	return fmfs.MoveTree(ctx, from, to, progress)
}
func moveTreeChecked(ctx context.Context, from, to string, progress func(int64, string), guard func(string) error) error {
	return fmfs.MoveTreeChecked(ctx, from, to, progress, guard)
}
func copyTree(ctx context.Context, from, to string, progress func(int64, string)) error {
	return fmfs.CopyTree(ctx, from, to, progress)
}
func copyTreeChecked(ctx context.Context, from, to string, progress func(int64, string), guard func(string) error) error {
	return fmfs.CopyTreeChecked(ctx, from, to, progress, guard)
}

// The foreground journal remains a temporary bridge to live index replay.
// Filesystem execution and its independent durable evidence belong to backend.
func (a *app) localMutation(action, source, target string) error {
	if a.backend == nil {
		if a.backendErr != nil {
			return a.backendErr
		}
		return errors.New("本机后台不可用")
	}
	location := func(p string) api.Location {
		if p == "" {
			return api.Location{}
		}
		return api.Location{Root: "local", Path: filepath.ToSlash(strings.TrimPrefix(filepath.Clean(p), string(filepath.Separator)))}
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	_, err := a.backend.Mutate(ctx, api.MutationRequest{Action: action, Source: location(source), Target: location(target)})
	return err
}
