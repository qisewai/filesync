package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"filesync/localtrash"

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
		if err := os.Mkdir(p, 0o755); err != nil {
			return err
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
		if err := os.Rename(p, to); err != nil {
			return err
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
		var failed []string
		for _, p := range paths {
			var err error
			if forever {
				err = os.RemoveAll(p)
			} else {
				err = localtrash.Move(p)
			}
			if err != nil {
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
func (a *app) localTransfer(files []string, move bool, dir string) {
	if len(files) == 0 {
		return
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
			return
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
		return
	}
	start := func() {
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
				var e error
				if move {
					e = moveTree(ctx, it.from, it.to, progress)
				} else {
					e = copyTree(ctx, it.from, it.to, progress)
				}
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
	}
	if len(taken) == 0 {
		start()
		return
	}
	a.ask("替换同名的项？", fmt.Sprintf("%s 中已经有 %s 等 %d 项与%s的同名。文件会被替换，文件夹会合并。", dir, strings.Join(taken[:min(3, len(taken))], "、"), len(taken), kind), "替换", start)
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

// treeSize is the size of the files below p, links not followed.
func treeSize(p string) int64 {
	var n int64
	filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// moveTree moves from to to: a rename where it can, else a copy, the
// source removed once all of it was copied.
func moveTree(ctx context.Context, from, to string, progress func(int64, string)) error {
	if _, err := os.Lstat(to); err != nil {
		if err := os.Rename(from, to); err == nil {
			progress(treeSize(to), filepath.Base(to))
			return nil
		} else if !isCrossDevice(err) {
			return err
		}
	} else if fi, err := os.Lstat(from); err == nil && !fi.IsDir() {
		// A file over one: renamed over it, as replacing it.
		if err := os.Rename(from, to); err == nil {
			progress(fi.Size(), filepath.Base(to))
			return nil
		}
	}
	if err := copyTree(ctx, from, to, progress); err != nil {
		return err
	}
	return os.RemoveAll(from)
}

func isCrossDevice(err error) bool {
	var le *os.LinkError
	return errors.As(err, &le) && errors.Is(le.Err, syscall.EXDEV)
}

// copyTree copies from to to, folders merged, files replaced, links as
// links, keeping times and permissions.
func copyTree(ctx context.Context, from, to string, progress func(int64, string)) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if err := os.MkdirAll(dst, info.Mode().Perm()|0o700); err != nil {
				return err
			}
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			os.Remove(dst)
			return os.Symlink(target, dst)
		case !d.Type().IsRegular():
			return nil // devices, pipes: left out
		}
		if err := copyFile(ctx, p, dst, info, func(n int64) { progress(n, d.Name()) }); err != nil {
			return err
		}
		return nil
	})
}

// copyFile copies a file, through a temporary file beside the copy, which
// replaces what was there once whole.
func copyFile(ctx context.Context, from, to string, info fs.FileInfo, progress func(int64)) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := to + ".filesync-part"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			out.Close()
			os.Remove(tmp)
			return err
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(tmp)
				return werr
			}
			progress(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(tmp)
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	os.Chtimes(tmp, info.ModTime(), info.ModTime())
	return os.Rename(tmp, to)
}
