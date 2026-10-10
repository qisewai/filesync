package main

import (
	"context"
	"errors"
	"path"
	"strings"

	"filesync/fileindex"

	"github.com/egoist/mygo/ui"
)

// nasBrowser is the dialog that signs in to the fileindex server of the
// NAS and chooses a directory there, one level at a time.
type nasBrowser struct {
	open bool
	// pick receives the directory chosen, with its name for people; nil
	// when the dialog only signs in, and afterLogin runs once it has.
	pick       func(url, name string)
	afterLogin func()

	addr     string
	password string
	needPass bool
	busy     string // what it waits for, or ""
	err      string

	client *fileindex.Client
	roots  []fileindex.Root
	root   *fileindex.Root // nil while choosing a root
	path   string          // in root, with slashes
	dirs   []string
	gen    int // drops the answers of requests a newer one replaced

	crumb    int
	naming   bool
	newName  string
	startAt  *fileindex.Location // where to go once connected
	dirsList ui.ListState
}

// openNAS opens the dialog. start, a URL of a directory of the NAS, is
// where it opens, if not empty.
func (a *app) openNAS(start string, pick func(url, name string), afterLogin func()) {
	b := &a.nas
	*b = nasBrowser{open: true, pick: pick, afterLogin: afterLogin, addr: a.store.LastServer}
	if loc, err := fileindex.Parse(start); err == nil {
		b.addr = loc.Addr
		b.startAt = &loc
	}
	if b.addr != "" {
		a.nasConnect()
	}
}

// nasDo runs fn off the main thread, showing what as it waits, and then
// done on the main thread with its error, unless a newer request replaced
// it.
func (a *app) nasDo(what string, fn func(ctx context.Context) error, done func(err error)) {
	b := &a.nas
	b.gen++
	gen := b.gen
	b.busy, b.err = what, ""
	go func() {
		err := fn(context.Background())
		a.post(func() {
			if b.gen != gen {
				return
			}
			b.busy = ""
			if errors.Is(err, fileindex.ErrAuth) {
				b.needPass = true
				return
			}
			if err != nil {
				b.err = err.Error()
			}
			done(err)
		})
	}()
}

func (a *app) nasConnect() {
	b := &a.nas
	b.addr = strings.TrimSpace(strings.TrimPrefix(b.addr, "http://"))
	b.addr = strings.TrimRight(b.addr, "/")
	if b.addr == "" {
		b.err = "请输入 NAS 的地址。"
		return
	}
	if !strings.Contains(b.addr, ":") {
		b.addr += ":8090" // fileindex's port
	}
	b.client = fileindex.New(b.addr, a.store.Sessions[b.addr])
	b.needPass, b.root, b.roots = false, nil, nil
	var authed bool
	a.nasDo("正在连接 "+b.addr+" …", func(ctx context.Context) (err error) {
		authed, err = b.client.Authed(ctx)
		return err
	}, func(err error) {
		if err != nil {
			return
		}
		if !authed {
			b.needPass = true
			return
		}
		a.nasSignedIn()
	})
}

func (a *app) nasLogin() {
	b := &a.nas
	pw := b.password
	a.nasDo("正在登录…", func(ctx context.Context) error {
		return b.client.Login(ctx, pw)
	}, func(err error) {
		if err != nil {
			return
		}
		b.password, b.needPass = "", false
		a.store.SetSession(b.addr, b.client.Token)
		a.save()
		a.nasSignedIn()
	})
}

// nasSignedIn goes on once the server knows the user.
func (a *app) nasSignedIn() {
	b := &a.nas
	if b.pick == nil {
		b.open = false
		if b.afterLogin != nil {
			b.afterLogin()
		}
		return
	}
	var roots []fileindex.Root
	a.nasDo("正在读取目录…", func(ctx context.Context) (err error) {
		roots, err = b.client.Roots(ctx)
		return err
	}, func(err error) {
		if err != nil {
			return
		}
		b.roots = roots
		if start := b.startAt; start != nil && start.Addr == b.addr {
			b.startAt = nil
			for i := range roots {
				if roots[i].ID == start.Root {
					a.nasGo(&roots[i], start.Path)
				}
			}
		}
	})
}

// nasGo lists the directory p of a root.
func (a *app) nasGo(root *fileindex.Root, p string) {
	b := &a.nas
	var dirs []string
	b.naming = false
	a.nasDo("正在读取目录…", func(ctx context.Context) (err error) {
		dirs, err = b.client.Dirs(ctx, root.ID, p)
		return err
	}, func(err error) {
		if err != nil {
			return
		}
		b.root, b.path, b.dirs = root, p, dirs
	})
}

func (a *app) nasView(c *ui.Context) {
	b := &a.nas
	if !b.open {
		return
	}
	t := c.Theme()
	ui.DialogBase(c, &b.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.35))
		panel.Width(640).Height(520).Padding(20).Gap(12).Radius(t.Radius*3).Background(t.Background).Border(1, t.Border)
		title := "选择 NAS 上的目录"
		if b.pick == nil {
			title = "登录 NAS"
		}
		ui.Text(c, title).FontSize(18).Bold()
		ui.Row(c).Gap(8).Children(func() {
			if ui.TextInput(c.Key("nas-addr"), &b.addr).Placeholder("NAS 上 fileindex 的地址，例如 192.168.0.235:8090").Label("NAS 地址").Grow(1).Submitted() {
				a.nasConnect()
			}
			if ui.Button(c, "连接").Clicked() {
				a.nasConnect()
			}
		})
		if b.err != "" {
			notice(c, t.Danger, b.err)
		}
		switch {
		case b.busy != "":
			ui.Column(c).Grow(1).Center().Gap(10).Children(func() {
				ui.Spinner(c).FontSize(22).Label(b.busy)
				ui.Text(c, b.busy).TextColor(t.TextMuted)
			})
		case b.needPass:
			ui.Column(c).Grow(1).Gap(10).Children(func() {
				ui.Text(c, "请输入 fileindex 的管理员密码。这里只保存登录状态，不保存密码。").TextColor(t.TextMuted)
				if ui.TextInput(c.Key("nas-pw"), &b.password).Password().AutoFocus().Placeholder("密码").Label("密码").Submitted() {
					a.nasLogin()
				}
				ui.Row(c).Children(func() {
					if ui.PrimaryButton(c, "登录").Disabled(b.password == "").Clicked() {
						a.nasLogin()
					}
				})
			})
		case b.client == nil:
			ui.Column(c).Grow(1).Center().Children(func() {
				ui.Text(c, "输入地址后点“连接”。").TextColor(t.TextMuted)
			})
		case b.pick == nil:
			ui.Spacer(c)
		case b.root == nil:
			a.nasRoots(c)
		default:
			a.nasDirs(c)
		}
		ui.Row(c).Gap(8).Children(func() {
			if b.root != nil && b.pick != nil && b.busy == "" && !b.needPass {
				sel := ui.PrimaryButton(c, "选择此目录").Disabled(b.root.ReadOnly)
				if b.root.ReadOnly {
					sel.Tooltip("这个目录在 fileindex 中设为只读")
				}
				if sel.Clicked() {
					loc := fileindex.Location{Addr: b.addr, Root: b.root.ID, Path: b.path}
					name := "NAS " + b.addr + " · " + path.Join(b.root.Name, b.path)
					b.pick(loc.String(), name)
					b.open = false
				}
			}
			ui.Spacer(c)
			if ui.Button(c, "取消").Clicked() {
				b.open = false
				b.gen++
			}
		})
	})
}

func (a *app) nasRoots(c *ui.Context) {
	b := &a.nas
	t := c.Theme()
	ui.Text(c, "fileindex 的索引目录").Bold()
	ui.Scroll(c).Grow(1).Border(1, t.Border).Radius(t.Radius * 2).Children(func() {
		if len(b.roots) == 0 {
			ui.Text(c, "NAS 上还没有索引目录，请先在 fileindex 网页中添加。").TextColor(t.TextMuted).Padding(12)
		}
		for i := range b.roots {
			r := &b.roots[i]
			row := ui.Row(c.Key(r.ID)).Gap(10).Padding(8, 12).Cursor(ui.CursorPointer).Children(func() {
				ui.Text(c, r.Name).Bold()
				ui.Text(c, r.Path).FontSize(12).TextColor(t.TextMuted).Grow(1).Shrink(1).SingleLine()
				if r.ReadOnly {
					ui.Badge(c, "只读")
				}
			})
			if row.Hovered() {
				row.Background(t.SurfaceHover)
			}
			if row.Clicked() {
				a.nasGo(r, "")
			}
		}
	})
}

func (a *app) nasDirs(c *ui.Context) {
	b := &a.nas
	t := c.Theme()
	crumbs := []string{"索引目录", b.root.Name}
	if b.path != "" {
		crumbs = append(crumbs, strings.Split(b.path, "/")...)
	}
	b.crumb = len(crumbs) - 1
	if ui.Breadcrumbs(c, crumbs, &b.crumb).Label("位置").Changed() {
		if b.crumb == 0 {
			b.root = nil
			return
		}
		a.nasGo(b.root, strings.Join(crumbs[2:b.crumb+1], "/"))
		return
	}
	ui.List(c.Key("nas-dirs:"+b.path), &b.dirsList, len(b.dirs), func(i int) {
		name := b.dirs[i]
		row := ui.Row(c).Gap(8).Padding(7, 12).Cursor(ui.CursorPointer).Children(func() {
			ui.Text(c, "📁")
			ui.Text(c, name).Grow(1).Shrink(1).SingleLine()
			ui.Text(c, "›").TextColor(t.TextMuted)
		})
		if row.Hovered() {
			row.Background(t.SurfaceHover)
		}
		if row.Clicked() {
			a.nasGo(b.root, path.Join(b.path, name))
		}
	}).Grow(1).Border(1, t.Border).Radius(t.Radius * 2).Clip().Label("子目录").Children(func() {
		if len(b.dirs) == 0 {
			ui.Text(c, "没有子目录。可以选择这个目录，或新建一个。").TextColor(t.TextMuted).Padding(12)
		}
	})
	ui.Row(c).Gap(8).Children(func() {
		if b.path != "" && ui.Button(c, "上一级").Clicked() {
			a.nasGo(b.root, parentDir(b.path))
		}
		if b.naming {
			done := ui.TextInput(c.Key("nas-new"), &b.newName).Placeholder("新文件夹名称").AutoFocus().Width(220).Submitted()
			if ui.Button(c, "创建").Disabled(strings.TrimSpace(b.newName) == "").Clicked() || done && strings.TrimSpace(b.newName) != "" {
				a.nasMkdir()
			}
			if ui.Button(c, "取消").Clicked() {
				b.naming = false
			}
		} else if !b.root.ReadOnly && ui.Button(c, "新建文件夹").Clicked() {
			b.naming, b.newName = true, ""
		}
	})
}

func (a *app) nasMkdir() {
	b := &a.nas
	root, dir, name := b.root, b.path, strings.TrimSpace(b.newName)
	a.nasDo("正在新建文件夹…", func(ctx context.Context) error {
		return b.client.Mkdir(ctx, root.ID, dir, name)
	}, func(err error) {
		if err != nil {
			return
		}
		a.nasGo(root, path.Join(dir, name))
	})
}

func parentDir(p string) string {
	if d := path.Dir(p); d != "." {
		return d
	}
	return ""
}
