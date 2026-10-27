package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"filesync/fileindex"
	"filesync/localindex"
)

// The properties of a file or folder, of a server or of this computer.

// fileStatMore is what os.FileInfo leaves to the platform; zero when it
// does not tell.
type fileStatMore struct {
	atime, ctime, btime time.Time
	owner               string
}

// propsDialog shows the properties of an entry.
type propsDialog struct {
	name    string
	loading bool
	note    string // why less is shown
	err     string
	cancel  context.CancelFunc
	rows    func() [][2]string
	// counting says the size of a folder of this computer is counted.
	counting bool
}

// propRows are label/value pairs, those without a value left out.
type propRows [][2]string

func (r *propRows) add(label, value string) {
	if value != "" {
		*r = append(*r, [2]string{label, value})
	}
}

// showServerProps shows the properties of an entry of a server, read from
// it: the entry as on its disk and, of a folder, what its index holds.
func (a *app) showServerProps(server string, e fileindex.Entry) {
	a.closeProps()
	if d := a.serverProps(server, e); d != nil {
		a.props = d
	}
}

// serverProps reads the properties of an entry of a server, nil for a
// server gone: the entry's own until they come.
func (a *app) serverProps(server string, e fileindex.Entry) *propsDialog {
	c, ok := a.serverClient(server)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	d := &propsDialog{name: e.Name, loading: true, cancel: cancel}
	basic := func() [][2]string {
		var r propRows
		r.add("类型", kindOf(e.IsDir, e.Link, e.Name))
		r.add("位置", path.Join(e.RootName, e.Parent))
		if !e.IsDir {
			r.add("大小", sizeWithBytes(e.Size))
		}
		r.add("修改时间", fullTime(e.MTime))
		return r
	}
	d.rows = basic
	go func() {
		defer cancel()
		p, err := c.Props(ctx, e.Root, e.Path)
		a.post(func() {
			d.loading = false
			var se *fileindex.StatusError
			switch {
			case err == nil:
				d.rows = func() [][2]string { return serverPropRows(p) }
			case errors.Is(err, context.Canceled):
			case errors.As(err, &se) && se.Code == 404 && se.Msg == "not found":
				d.note = "服务器版本较旧，只显示基本信息"
			default:
				d.err = err.Error()
				if errors.Is(err, fileindex.ErrAuth) {
					if sv := a.store.Server(server); sv != nil {
						a.signedOut(sv)
					}
				}
			}
		})
	}()
	return d
}

func serverPropRows(p *fileindex.Props) [][2]string {
	var r propRows
	r.add("类型", kindOf(p.IsDir, p.Link, p.Name))
	if p.Path == "" {
		r.add("位置", "索引目录")
	} else {
		r.add("位置", path.Join(p.RootName, parentDir(p.Path)))
	}
	r.add("服务器路径", p.Abs)
	if p.Link {
		r.add("链接到", p.LinkTarget)
	}
	if p.IsDir {
		switch ix := p.Index; {
		case ix == nil:
			r.add("大小", "该目录尚未建立索引")
		case ix.Missing:
			r.add("大小", "该文件夹还没有进入索引")
		default:
			r.add("大小", sizeWithBytes(ix.Size))
			r.add("包含", fmt.Sprintf("%d 个文件，%d 个文件夹", ix.Files, ix.Dirs))
		}
		if p.Children >= 0 {
			r.add("直接包含", fmt.Sprintf("%d 项", p.Children))
		}
		if ix := p.Index; ix != nil && ix.ScannedAt > 0 {
			r.add("统计来自", "索引（更新于 "+fullTime(ix.ScannedAt)+"）")
		}
	} else {
		r.add("大小", sizeWithBytes(p.Size))
	}
	r.add("修改时间", fullTime(p.MTime))
	r.add("访问时间", fullTime(p.ATime))
	r.add("状态更改", fullTime(p.CTime))
	if p.Mode != "" {
		r.add("权限", p.Mode+"（"+p.Perm+"）")
	}
	owner, group := p.Owner, p.Group
	if owner == "" {
		owner = fmt.Sprint(p.UID)
	}
	if group == "" {
		group = fmt.Sprint(p.GID)
	}
	r.add("所有者", fmt.Sprintf("%s:%s（%d:%d）", owner, group, p.UID, p.GID))
	if p.ReadOnly {
		r.add("只读", "所在索引目录为只读")
	}
	return r
}

// localTally is what a folder of this computer holds, counted as the walk
// goes.
type localTally struct {
	files, dirs, size, unreadable int64
	done                          bool
}

// showLocalProps shows the properties of a file or folder of this
// computer; of a folder, what it holds is counted as it is shown.
func (a *app) showLocalProps(p string) {
	a.closeProps()
	a.props = a.localProps(p, true)
}

// localProps reads the properties of a file or folder of this computer;
// with count, what a folder holds is counted, as the walk goes.
func (a *app) localProps(p string, count bool) *propsDialog {
	d := &propsDialog{name: filepath.Base(p)}
	fi, err := os.Lstat(p)
	if err != nil {
		d.err = err.Error()
		d.rows = func() [][2]string { return nil }
		return d
	}
	more := statMore(fi)
	link := fi.Mode()&fs.ModeSymlink != 0
	var target string
	if link {
		target, _ = os.Readlink(p)
	}
	t := &localTally{}
	direct := -1
	// What a folder holds is counted only when asked, off the main thread:
	// from the index of this computer where it holds the folder, else by
	// walking it.
	var indexed *localindex.Subtree
	if fi.IsDir() && count {
		d.counting, d.loading = true, true
		ctx, cancel := context.WithCancel(context.Background())
		d.cancel = cancel
		var index *localindex.Index
		if a.localIdx != nil {
			index = a.localIdx.index
		}
		go func() {
			n := -1
			if ents, err := os.ReadDir(p); err == nil {
				n = len(ents)
			}
			if index != nil {
				if sub, ok := index.Subtree(p); ok {
					a.post(func() {
						indexed, direct, d.loading = &sub, n, false
					})
					return
				}
			}
			a.post(func() { direct = n })
			a.walkLocal(ctx, p, d, t)
		}()
	}
	d.rows = func() [][2]string {
		var r propRows
		r.add("类型", kindOf(fi.IsDir(), link, fi.Name()))
		r.add("位置", filepath.Dir(p))
		r.add("链接到", target)
		switch {
		case fi.IsDir() && indexed != nil:
			r.add("大小", sizeWithBytes(indexed.Size))
			r.add("包含", fmt.Sprintf("%d 个文件，%d 个文件夹", indexed.Files, indexed.Dirs))
			if direct >= 0 {
				r.add("直接包含", fmt.Sprintf("%d 项", direct))
			}
			if !indexed.Updated.IsZero() {
				r.add("统计来自", "本机索引（更新于 "+indexed.Updated.Format("2006-01-02 15:04")+"）")
			}
		case fi.IsDir() && !count:
		case fi.IsDir():
			ellipsis := ""
			if !t.done {
				ellipsis = "…"
			}
			r.add("大小", sizeWithBytes(t.size)+ellipsis)
			r.add("包含", fmt.Sprintf("%d 个文件，%d 个文件夹%s", t.files, t.dirs, ellipsis))
			if t.unreadable > 0 {
				r.add("无法读取", fmt.Sprintf("%d 个文件夹", t.unreadable))
			}
			if direct >= 0 {
				r.add("直接包含", fmt.Sprintf("%d 项", direct))
			}
		default:
			r.add("大小", sizeWithBytes(fi.Size()))
		}
		r.add("修改时间", fullTime(fi.ModTime().Unix()))
		if !more.btime.IsZero() {
			r.add("创建时间", fullTime(more.btime.Unix()))
		}
		if !more.atime.IsZero() {
			r.add("访问时间", fullTime(more.atime.Unix()))
		}
		if !more.ctime.IsZero() {
			r.add("状态更改", fullTime(more.ctime.Unix()))
		}
		r.add("权限", fmt.Sprintf("%s（%04o）", lsMode(fi.Mode()), fi.Mode().Perm()))
		r.add("所有者", more.owner)
		return r
	}
	return d
}

// walkLocal counts what is below dir, links not followed, showing the
// count as it goes.
func (a *app) walkLocal(ctx context.Context, dir string, d *propsDialog, t *localTally) {
	var now localTally
	shown := time.Now()
	filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if e != nil && e.IsDir() && p != dir {
				now.unreadable++
			}
			return nil // WalkDir skips what it cannot read
		}
		if p == dir {
			return nil
		}
		if e.IsDir() {
			now.dirs++
		} else {
			now.files++
			if e.Type().IsRegular() {
				if fi, err := e.Info(); err == nil {
					now.size += fi.Size()
				}
			}
		}
		if time.Since(shown) > 200*time.Millisecond {
			shown = time.Now()
			snap := now
			a.post(func() { *t = snap })
		}
		return nil
	})
	if ctx.Err() != nil {
		return
	}
	now.done = true
	a.post(func() {
		*t = now
		d.loading = false
	})
}

func (a *app) closeProps() {
	if d := a.props; d != nil {
		if d.cancel != nil {
			d.cancel()
		}
		a.props = nil
	}
}

// propsView shows the properties dialog, when open.
func (a *app) propsView(c *ui.Context) {
	d := a.props
	if d == nil {
		return
	}
	t := c.Theme()
	open := true
	ui.Modal(c.Key("props-dialog"), &open, func() {
		ui.Column(c).Width(460).Gap(8).Children(func() {
			ui.Text(c, d.name+" 的属性").Bold().FontSize(16).MaxLines(2)
			if d.note != "" {
				ui.Text(c, d.note).FontSize(12).TextColor(t.TextMuted)
			}
			if d.err != "" {
				notice(c, t.Danger, d.err)
			}
			for i, row := range d.rows() {
				ui.Row(c.Key(fmt.Sprint("prop-", i))).Gap(12).Children(func() {
					ui.Text(c, row[0]).Width(80).TextColor(t.TextMuted)
					ui.Text(c, row[1]).Grow(1).Shrink(1).Selectable()
				})
			}
			if d.loading {
				ui.Spinner(c).FontSize(12).Label("正在读取")
			}
			ui.Row(c).Justify(ui.End).Margin(6, 0, 0, 0).Children(func() {
				if ui.Button(c, "关闭").AutoFocus().Clicked() {
					open = false
				}
			})
		})
	})
	if !open && a.props == d {
		a.closeProps()
	}
}

func kindOf(isDir, link bool, name string) string {
	switch {
	case link:
		return "符号链接"
	case isDir:
		return "文件夹"
	}
	if ext := strings.TrimPrefix(path.Ext(name), "."); ext != "" && ext != name[1:] {
		return strings.ToUpper(ext) + " 文件"
	}
	return "文件"
}

func sizeWithBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d 字节", n)
	}
	return fmt.Sprintf("%s（%s 字节）", formatBytes(n), groupDigits(n))
}

func fullTime(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).Format("2006-01-02 15:04:05")
}

// lsMode writes a mode as ls does: lrwxrwxrwx, drwxr-sr-t (Go's own
// String writes types its way, as L for a link, and setuid apart).
func lsMode(m fs.FileMode) string {
	b := []byte("-rwxrwxrwx")
	switch {
	case m.IsDir():
		b[0] = 'd'
	case m&fs.ModeSymlink != 0:
		b[0] = 'l'
	case m&fs.ModeNamedPipe != 0:
		b[0] = 'p'
	case m&fs.ModeSocket != 0:
		b[0] = 's'
	case m&fs.ModeCharDevice != 0:
		b[0] = 'c'
	case m&fs.ModeDevice != 0:
		b[0] = 'b'
	}
	for i := 0; i < 9; i++ {
		if m&(1<<uint(8-i)) == 0 {
			b[i+1] = '-'
		}
	}
	special := func(i int, set bool, c byte) {
		if set {
			if b[i] == '-' {
				c -= 'a' - 'A'
			}
			b[i] = c
		}
	}
	special(3, m&fs.ModeSetuid != 0, 's')
	special(6, m&fs.ModeSetgid != 0, 's')
	special(9, m&fs.ModeSticky != 0, 't')
	return string(b)
}
