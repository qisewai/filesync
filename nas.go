package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path"
	"strings"
	"time"

	"filesync/config"
	"filesync/fileindex"

	"github.com/egoist/mygo/ui"
)

// nasScreen is what the server dialog shows.
type nasScreen int

const (
	screenServers nasScreen = iota // the servers, to manage or choose one
	screenEdit                     // a server's name and address
	screenLogin                    // a server's password
	screenRoots                    // a server's indexed directories
	screenDirs                     // a directory of one
)

// nasBrowser is the dialog of the fileindex servers: it manages them,
// signs in to them, and chooses a directory of one, one level at a time.
type nasBrowser struct {
	open   bool
	screen nasScreen
	// pick receives the directory chosen; nil when the dialog manages the
	// servers, or only signs in, after which afterLogin runs.
	pick       func(server string, root int64, rootName, path string)
	afterLogin func()
	// sameServer, when set, is the only server the dialog offers.
	sameServer string

	server *config.Server // the one in use
	client *fileindex.Client
	busy   string // what it waits for, or ""
	err    string
	gen    int // drops the answers of requests a newer one replaced

	// screenEdit
	editing      *config.Server // nil for a new server
	name, addr   string
	confirmDelID string

	password string // screenLogin

	roots []fileindex.Root
	root  *fileindex.Root
	path  string // in root, with slashes
	dirs  []string
	crumb int
	// where to open once signed in
	startServer string
	startRoot   int64
	startPath   string

	// sel is the row chosen in the list of roots or of directories, -1
	// for none: a click chooses a row, a double click or Enter opens it.
	sel       int
	rootsList ui.ListState
	dirsList  ui.ListState

	naming  bool
	newName string

	// probes are the connections tried to the servers, as the dialog
	// choosing a target lists them.
	probes map[string]*serverProbe
}

// probeState is how a server answered.
type probeState int

const (
	probeChecking    probeState = iota
	probeOK                     // signed in, its directories read
	probeNeedLogin              // reachable, but without a session
	probeUnreachable            // no answer, or an error
)

// probeTimeout is how long a server may take to answer the dialog: a
// NAS on the network answers at once.
const probeTimeout = 6 * time.Second

type serverProbe struct {
	state probeState
	err   string
	roots []fileindex.Root
}

// openServers opens the dialog to manage the servers.
func (a *app) openServers() {
	a.nas = nasBrowser{open: true}
}

// openPicker opens the dialog to choose a target directory, at the one
// chosen before if any.
//
// The servers are listed first, each connected to at once; only those
// that answer can be entered.
func (a *app) openPicker(server string, root int64, p string, pick func(server string, root int64, rootName, path string)) {
	a.nas = nasBrowser{open: true, pick: pick, probes: map[string]*serverProbe{}}
	if a.store.Server(server) != nil {
		a.nas.startServer, a.nas.startRoot, a.nas.startPath = server, root, p
	}
	for _, sv := range a.store.Servers {
		a.probe(sv)
	}
}

// openPickerIn opens the dialog to choose a directory of the server in
// use, at once: copies and moves stay on it.
func (a *app) openPickerIn(server string, root int64, p string, pick func(server string, root int64, rootName, path string)) {
	sv := a.store.Server(server)
	if sv == nil {
		return
	}
	a.nas = nasBrowser{open: true, pick: pick, sameServer: server, probes: map[string]*serverProbe{}}
	a.nas.startServer, a.nas.startRoot, a.nas.startPath = server, root, p
	a.nasUse(sv)
}

// probe connects to a server for the dialog choosing a target: it reads
// its directories when signed in, or checks it answers otherwise.
func (a *app) probe(sv *config.Server) {
	b := &a.nas
	pr := &serverProbe{}
	b.probes[sv.ID] = pr
	c := fileindex.New(sv.Addr, sv.Token)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		var roots []fileindex.Root
		var err error
		if c.Token != "" {
			roots, err = c.Roots(ctx)
		} else {
			_, err = c.Authed(ctx)
		}
		a.post(func() {
			if a.closed || !b.open || b.probes[sv.ID] != pr {
				return
			}
			switch {
			case errors.Is(err, fileindex.ErrAuth) || err == nil && c.Token == "":
				pr.state = probeNeedLogin
			case err != nil:
				pr.state, pr.err = probeUnreachable, connectError(err)
			default:
				pr.state, pr.roots = probeOK, roots
			}
		})
	}()
}

// connectError says why a server could not be reached, shortly.
func connectError(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout():
		return "连接超时，请检查 NAS 是否开机、地址是否正确"
	case strings.Contains(err.Error(), "connection refused"):
		return "连接被拒绝，fileindex 可能没有运行，或端口不对"
	case strings.Contains(err.Error(), "no route to host") || strings.Contains(err.Error(), "network is unreachable"):
		return "网络不通，请检查是否和 NAS 在同一网络"
	case strings.Contains(err.Error(), "no such host"):
		return "找不到这个主机名"
	}
	return err.Error()
}

// openLogin opens the dialog to sign in to a server again, then runs after.
func (a *app) openLogin(server string, after func()) {
	sv := a.store.Server(server)
	if sv == nil {
		return
	}
	a.nas = nasBrowser{open: true, afterLogin: after, server: sv, client: fileindex.New(sv.Addr, ""), screen: screenLogin}
}

// nasDo runs fn off the main thread, showing what as it waits, then done
// on the main thread with its error, unless a newer request replaced it.
// A request the server refuses for want of a session asks the password.
func (a *app) nasDo(what string, fn func(ctx context.Context) error, done func(err error)) {
	b := &a.nas
	a.nasGen++
	b.gen = a.nasGen
	gen := b.gen
	b.busy, b.err = what, ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		err := fn(ctx)
		a.post(func() {
			if a.closed || !b.open || b.gen != gen {
				return
			}
			b.busy = ""
			if errors.Is(err, fileindex.ErrAuth) {
				b.screen, b.password = screenLogin, ""
				return
			}
			if err != nil {
				b.err = err.Error()
			}
			done(err)
		})
	}()
}

// nasUse goes to a server: its directories, once signed in.
func (a *app) nasUse(sv *config.Server) {
	b := &a.nas
	b.server = sv
	b.client = fileindex.New(sv.Addr, sv.Token)
	b.root, b.roots = nil, nil
	if sv.Token == "" {
		b.screen, b.password = screenLogin, ""
		return
	}
	a.nasSignedIn()
}

func (a *app) nasLogin() {
	b := &a.nas
	c := b.client
	pw := b.password
	a.nasDo("正在登录 "+b.server.Title()+" …", func(ctx context.Context) error {
		return c.Login(ctx, pw)
	}, func(err error) {
		b.password = ""
		if err != nil {
			return
		}
		b.server.Token = c.Token
		a.save()
		a.serverChanged(b.server.ID)
		a.nasSignedIn()
	})
}

func (a *app) nasLogout(sv *config.Server) {
	c := fileindex.New(sv.Addr, sv.Token)
	sv.Token = ""
	a.save()
	a.serverChanged(sv.ID)
	go c.Logout(context.Background()) // the server may be away: the session is gone here anyway
}

// nasSignedIn goes on once the server knows the user.
func (a *app) nasSignedIn() {
	b := &a.nas
	switch {
	case b.afterLogin != nil:
		b.open = false
		b.afterLogin()
		return
	case b.pick == nil:
		b.screen = screenServers
		return
	}
	var roots []fileindex.Root
	c := b.client
	show := func(err error) {
		if err != nil {
			b.screen = screenServers
			if pr := b.probes[b.server.ID]; pr != nil {
				pr.state, pr.err = probeUnreachable, connectError(err)
			}
			return
		}
		if pr := b.probes[b.server.ID]; pr != nil {
			pr.state, pr.err, pr.roots = probeOK, "", roots
		}
		b.roots, b.screen, b.sel = roots, screenRoots, -1
		if b.startServer != "" && b.startServer != b.server.ID {
			b.startRoot, b.startPath = 0, ""
		}
		if b.startRoot != 0 {
			start, p := b.startRoot, b.startPath
			b.startRoot = 0
			for i := range roots {
				if roots[i].ID == start {
					if p == "" {
						b.sel = i
					} else {
						a.nasGo(&roots[i], parentDir(p), path.Base(p))
					}
				}
			}
		}
	}
	// The directories the connection read show at once.
	if pr := b.probes[b.server.ID]; pr != nil && pr.state == probeOK && pr.roots != nil {
		roots = pr.roots
		show(nil)
		return
	}
	a.nasDo("正在读取 "+b.server.Title()+" 的目录…", func(ctx context.Context) (err error) {
		roots, err = c.Roots(ctx)
		return err
	}, show)
}

// nasGo lists the directory p of a root, choosing the subdirectory named
// choose if any.
func (a *app) nasGo(root *fileindex.Root, p string, choose ...string) {
	b := &a.nas
	c := b.client
	var dirs []string
	b.naming = false
	a.nasDo("正在读取目录…", func(ctx context.Context) (err error) {
		dirs, err = c.Dirs(ctx, root.ID, p)
		return err
	}, func(err error) {
		if err != nil {
			return
		}
		b.root, b.path, b.dirs, b.screen = root, p, dirs, screenDirs
		b.sel = -1
		for i, d := range dirs {
			if len(choose) > 0 && d == choose[0] {
				b.sel = i
				b.dirsList.ScrollIntoView(i)
			}
		}
	})
}

func (a *app) nasMkdir() {
	b := &a.nas
	c := b.client
	root, dir, name := b.root, b.path, strings.TrimSpace(b.newName)
	a.nasDo("正在新建文件夹…", func(ctx context.Context) error {
		return c.Mkdir(ctx, root.ID, dir, name)
	}, func(err error) {
		if err == nil {
			a.nasGo(root, dir, name)
		}
	})
}

// nasStartEdit shows the form of a server, or of a new one when sv is nil.
func (a *app) nasStartEdit(sv *config.Server) {
	b := &a.nas
	b.editing, b.err, b.screen = sv, "", screenEdit
	b.name, b.addr = "", ""
	if sv != nil {
		b.name, b.addr = sv.Name, sv.Addr
	}
}

// normalizeAddr makes an address the user typed host:port, fileindex's
// port 8090 by default.
func normalizeAddr(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "http://"), "https://")
	s = strings.TrimRight(s, "/")
	if s == "" {
		return "", errors.New("请输入地址。")
	}
	if _, _, err := net.SplitHostPort(s); err != nil {
		s = net.JoinHostPort(s, "8090")
	}
	if _, _, err := net.SplitHostPort(s); err != nil || strings.ContainsAny(s, " /") {
		return "", fmt.Errorf("地址 %q 无效，应为 主机:端口，如 192.168.0.235:8090。", s)
	}
	return s, nil
}

func (a *app) nasSaveServer() {
	b := &a.nas
	addr, err := normalizeAddr(b.addr)
	if err != nil {
		b.err = err.Error()
		return
	}
	if other := a.store.ServerByAddr(addr); other != nil && other != b.editing {
		b.err = "已经有这个地址的服务器：" + other.Title()
		return
	}
	sv := b.editing
	if sv == nil {
		sv = &config.Server{}
		a.store.AddServer(sv)
	}
	if sv.Addr != addr {
		sv.Token = "" // another server: the session was not its
	}
	sv.Name, sv.Addr = strings.TrimSpace(b.name), addr
	a.save()
	a.serverChanged(sv.ID)
	b.err = ""
	if b.pick != nil {
		// Back to the list, connecting to it at its address.
		b.screen = screenServers
		a.probe(sv)
		return
	}
	b.screen = screenServers
}

func (a *app) nasView(c *ui.Context) {
	b := &a.nas
	if !b.open {
		return
	}
	t := c.Theme()
	ui.DialogBase(c, &b.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.35))
		panel.Width(660).Height(540).Padding(20).Gap(12).Radius(t.Radius*3).Background(t.Background).Border(1, t.Border)
		title := "fileindex 服务器"
		switch {
		case b.screen == screenEdit && b.editing == nil:
			title = "添加服务器"
		case b.screen == screenEdit:
			title = "编辑服务器"
		case b.screen == screenLogin:
			title = "登录 " + b.server.Title()
		case b.pick != nil:
			title = "选择 NAS 上备份到的目录"
		}
		ui.Text(c, title).FontSize(18).Bold()
		if b.err != "" {
			notice(c, t.Danger, b.err)
		}
		if b.busy != "" {
			ui.Column(c).Grow(1).Center().Gap(10).Children(func() {
				ui.Spinner(c).FontSize(22).Label(b.busy)
				ui.Text(c, b.busy).TextColor(t.TextMuted)
			})
		} else {
			switch b.screen {
			case screenServers:
				a.nasServers(c)
			case screenEdit:
				a.nasEdit(c)
			case screenLogin:
				a.nasLoginView(c)
			case screenRoots:
				a.nasRoots(c)
			case screenDirs:
				a.nasDirs(c)
			}
		}
		if root := a.nasRootInView(); b.pick != nil && b.busy == "" && root != nil && root.ReadOnly {
			ui.Row(c).Gap(10).Padding(10, 12).Radius(t.Radius*2).Background(t.Warning.Alpha(0.1)).Border(1, t.Warning.Alpha(0.4)).Children(func() {
				ui.Text(c, "“"+root.Name+"”在 fileindex 中设为只读，不能备份到这里。请在 fileindex 的“索引目录”页面编辑它，关闭“只读”，再回来选择。").Grow(1).Shrink(1)
				if ui.Button(c, "打开 fileindex 设置").Clicked() {
					a.openURL("http://" + b.server.Addr + "/roots")
				}
			})
		}
		ui.Row(c).Gap(8).Children(func() {
			if b.pick != nil && b.busy == "" {
				a.nasChooseButton(c)
			}
			ui.Spacer(c)
			label := "取消"
			if b.pick == nil && b.afterLogin == nil && b.screen == screenServers {
				label = "完成"
			}
			if ui.Button(c, label).Clicked() {
				b.open = false
				b.gen++
			}
		})
	})
}

func (a *app) nasServers(c *ui.Context) {
	b := &a.nas
	t := c.Theme()
	if b.pick != nil {
		ui.Text(c, "选择一个服务器：").TextColor(t.TextMuted)
	}
	ui.Scroll(c).Grow(1).Border(1, t.Border).Radius(t.Radius * 2).Children(func() {
		if len(a.store.Servers) == 0 {
			ui.Text(c, "还没有服务器。点“添加服务器”，输入 NAS 上 fileindex 的地址。").TextColor(t.TextMuted).Padding(12)
		}
		for _, sv := range a.store.Servers {
			if b.sameServer != "" && sv.ID != b.sameServer {
				continue
			}
			row := ui.Row(c.Key(sv.ID)).Gap(10).Padding(10, 12).Children(func() {
				ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
					ui.Text(c, sv.Title()).Bold().SingleLine()
					sub := sv.Addr
					if n := len(a.store.TasksOf(sv.ID)); n > 0 {
						sub += fmt.Sprintf(" · %d 个任务", n)
					}
					ui.Text(c, sub).FontSize(12).TextColor(t.TextMuted).SingleLine()
					if pr := b.probes[sv.ID]; b.pick != nil && pr != nil && pr.state == probeUnreachable {
						ui.Text(c, pr.err).FontSize(12).TextColor(t.Danger).MaxLines(2)
					}
				})
				if b.pick != nil {
					a.probeStatus(c, sv)
					return
				}
				if sv.Token != "" {
					ui.Text(c, "已登录").FontSize(12).TextColor(t.Success)
				} else {
					ui.Text(c, "未登录").FontSize(12).TextColor(t.TextMuted)
				}
				if b.confirmDelID == sv.ID {
					if ui.Button(c.Key("del-yes"), "确定删除").Clicked() {
						if a.serverHasTransfers(sv.ID) {
							b.err = "还有传输任务正在使用这个服务器，请先等待完成或取消"
						} else if err := a.store.RemoveServer(sv.ID); err != nil {
							b.err = err.Error()
						} else {
							a.save()
							a.serverChanged(sv.ID)
						}
						b.confirmDelID = ""
					}
					if ui.Button(c.Key("del-no"), "不删除").Clicked() {
						b.confirmDelID = ""
					}
					return
				}
				if sv.Token != "" {
					if ui.Button(c.Key("logout"), "退出登录").Clicked() {
						a.nasLogout(sv)
					}
				} else if ui.Button(c.Key("login"), "登录").Clicked() {
					a.nasUse(sv)
				}
				if ui.Button(c.Key("edit"), "编辑").Clicked() {
					a.nasStartEdit(sv)
				}
				if ui.Button(c.Key("delete"), "删除").Clicked() {
					b.err, b.confirmDelID = "", sv.ID
				}
			})
			if pr := b.probes[sv.ID]; b.pick != nil && pr != nil && pr.state == probeOK && row.Hovered() {
				row.Background(t.SurfaceHover)
			}
		}
	})
	ui.Row(c).Children(func() {
		if ui.Button(c, "＋ 添加服务器").Clicked() {
			a.nasStartEdit(nil)
		}
	})
}

// probeStatus shows how a server answered, and what can be done with it.
func (a *app) probeStatus(c *ui.Context, sv *config.Server) {
	b := &a.nas
	t := c.Theme()
	pr := b.probes[sv.ID]
	if pr == nil {
		a.probe(sv)
		return
	}
	switch pr.state {
	case probeChecking:
		ui.Spinner(c).FontSize(14).Label("正在连接 " + sv.Title())
		ui.Text(c, "正在连接…").FontSize(12).TextColor(t.TextMuted)
		ui.PrimaryButton(c.Key("use"), "浏览").Disabled(true)
	case probeOK:
		ui.Text(c, "● 已连接").FontSize(12).TextColor(t.Success)
		if ui.PrimaryButton(c.Key("use"), "浏览").Clicked() {
			a.nasUse(sv)
		}
	case probeNeedLogin:
		ui.Text(c, "● 需要登录").FontSize(12).TextColor(t.Warning)
		if ui.PrimaryButton(c.Key("use"), "登录并浏览").Clicked() {
			a.nasUse(sv)
		}
	case probeUnreachable:
		ui.Text(c, "● 无法连接").FontSize(12).TextColor(t.Danger).Tooltip(pr.err)
		if ui.Button(c.Key("edit"), "编辑地址").Clicked() {
			a.nasStartEdit(sv)
		}
		if ui.Button(c.Key("retry"), "重试").Clicked() {
			a.probe(sv)
		}
	}
}

func (a *app) nasEdit(c *ui.Context) {
	b := &a.nas
	ui.Column(c).Grow(1).Gap(12).Children(func() {
		ui.Field(c, "名称", func() {
			ui.TextInput(c.Key("sv-name"), &b.name).Placeholder("例如 家里的 NAS（可不填）").AutoFocus()
		})
		ui.Field(c, "地址", func() {
			if ui.TextInput(c.Key("sv-addr"), &b.addr).Placeholder("例如 192.168.0.235:8090").Submitted() {
				a.nasSaveServer()
			}
		}).Description("NAS 上 fileindex 网页的地址；不写端口时用 8090。")
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, "保存").Clicked() {
				a.nasSaveServer()
			}
			if ui.Button(c, "返回").Clicked() {
				b.err, b.screen = "", screenServers
			}
		})
	})
}

func (a *app) nasLoginView(c *ui.Context) {
	b := &a.nas
	t := c.Theme()
	ui.Column(c).Grow(1).Gap(10).Children(func() {
		ui.Text(c, b.server.Addr).TextColor(t.TextMuted)
		ui.Text(c, "请输入 fileindex 的管理员密码。这里只保存登录状态，不保存密码。").TextColor(t.TextMuted)
		if ui.TextInput(c.Key("nas-pw"), &b.password).Password().AutoFocus().Placeholder("密码").Label("密码").Submitted() && b.password != "" {
			a.nasLogin()
		}
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, "登录").Disabled(b.password == "").Clicked() {
				a.nasLogin()
			}
			if b.afterLogin == nil && ui.Button(c, "返回").Clicked() {
				b.err, b.screen = "", screenServers
			}
		})
	})
}

// nasRootInView is the root the choice would go to: the one chosen in the
// list of roots, or the one whose directories show.
func (a *app) nasRootInView() *fileindex.Root {
	b := &a.nas
	switch {
	case b.screen == screenRoots && b.sel >= 0 && b.sel < len(b.roots):
		return &b.roots[b.sel]
	case b.screen == screenDirs:
		return b.root
	}
	return nil
}

// nasChooseButton picks the row chosen, or else the directory shown.
func (a *app) nasChooseButton(c *ui.Context) {
	b := &a.nas
	var (
		root  *fileindex.Root
		p     string
		label string
	)
	switch {
	case b.screen == screenRoots && b.sel >= 0 && b.sel < len(b.roots):
		root = &b.roots[b.sel]
		label = "选择“" + root.Name + "”"
	case b.screen == screenDirs && b.sel >= 0 && b.sel < len(b.dirs):
		root, p = b.root, path.Join(b.path, b.dirs[b.sel])
		label = "选择“" + b.dirs[b.sel] + "”"
	case b.screen == screenDirs:
		root, p = b.root, b.path
		label = "选择当前目录“" + path.Base(path.Join(b.root.Name, b.path)) + "”"
	default:
		return
	}
	btn := ui.PrimaryButton(c, label).Disabled(root.ReadOnly)
	if root.ReadOnly {
		btn.Tooltip("“" + root.Name + "”在 fileindex 中设为只读，不能备份到这里")
	}
	if btn.Clicked() {
		b.pick(b.server.ID, root.ID, root.Name, p)
		b.open = false
	}
}

func (a *app) nasRoots(c *ui.Context) {
	b := &a.nas
	t := c.Theme()
	crumbs := []string{"服务器", b.server.Title()}
	b.crumb = 1
	if ui.Breadcrumbs(c, crumbs, &b.crumb).Label("位置").Changed() && b.crumb == 0 {
		b.screen = screenServers
		return
	}
	b.rootsList.Selected = &b.sel
	list := ui.List(c.Key("nas-roots"), &b.rootsList, len(b.roots)).Grow(1).Border(1, t.Border).Radius(t.Radius * 2).Clip().Label("索引目录").
		Rows(func(row ui.ListRow) {
			r := b.roots[row.Index]
			muted := t.TextMuted
			if row.Selected() {
				muted = t.AccentText.Alpha(0.85)
			}
			ui.Row(c).Gap(10).Padding(8, 12).Children(func() {
				ui.Text(c, "🗄")
				ui.Text(c, r.Name).Bold()
				ui.Text(c, r.Path).FontSize(12).TextColor(muted).Grow(1).Shrink(1).SingleLine()
				if r.ReadOnly {
					ui.Badge(c, "只读")
				}
			})
		})
	if list.Submitted() && b.sel >= 0 {
		a.nasGo(&b.roots[b.sel], "")
	}
	if len(b.roots) == 0 {
		ui.Text(c, "这个服务器上还没有索引目录，请先在 fileindex 网页中添加。").TextColor(t.TextMuted)
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		if ui.Button(c, "打开").Disabled(b.sel < 0).Clicked() {
			a.nasGo(&b.roots[b.sel], "")
		}
		ui.Text(c, "单击选中，双击打开").FontSize(12).TextColor(t.TextMuted)
	})
}

func (a *app) nasDirs(c *ui.Context) {
	b := &a.nas
	t := c.Theme()
	crumbs := []string{"服务器", b.server.Title(), b.root.Name}
	if b.path != "" {
		crumbs = append(crumbs, strings.Split(b.path, "/")...)
	}
	b.crumb = len(crumbs) - 1
	if ui.Breadcrumbs(c, crumbs, &b.crumb).Label("位置").Changed() {
		switch b.crumb {
		case 0:
			b.screen = screenServers
		case 1:
			a.nasBackToRoots()
		default:
			a.nasGo(b.root, strings.Join(crumbs[3:b.crumb+1], "/"))
		}
		return
	}
	b.dirsList.Selected = &b.sel
	list := ui.List(c.Key("nas-dirs:"+b.path), &b.dirsList, len(b.dirs)).Grow(1).Border(1, t.Border).Radius(t.Radius * 2).Clip().Label("子目录").
		Rows(func(row ui.ListRow) {
			ui.Row(c).Gap(8).Padding(7, 12).Children(func() {
				ui.Icon(c, iconFolder).FontSize(16).TextColor(ui.Hex("#d99a2b"))
				ui.Text(c, b.dirs[row.Index]).Grow(1).Shrink(1).SingleLine()
			})
		})
	list.Children(func() {
		if len(b.dirs) == 0 {
			ui.Text(c, "没有子目录。可以选择当前目录，或新建一个。").TextColor(t.TextMuted).Padding(12)
		}
	})
	if list.Submitted() && b.sel >= 0 {
		a.nasGo(b.root, path.Join(b.path, b.dirs[b.sel]))
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		if ui.Button(c, "上一级").Clicked() {
			if b.path == "" {
				a.nasBackToRoots()
			} else {
				a.nasGo(b.root, parentDir(b.path), path.Base(b.path))
			}
		}
		if ui.Button(c, "打开").Disabled(b.sel < 0).Clicked() {
			a.nasGo(b.root, path.Join(b.path, b.dirs[b.sel]))
		}
		if b.naming {
			done := ui.TextInput(c.Key("nas-new"), &b.newName).Placeholder("新文件夹名称").AutoFocus().Width(200).Submitted()
			if ui.Button(c, "创建").Disabled(strings.TrimSpace(b.newName) == "").Clicked() || done && strings.TrimSpace(b.newName) != "" {
				a.nasMkdir()
			}
			if ui.Button(c, "不建了").Clicked() {
				b.naming = false
			}
		} else {
			if !b.root.ReadOnly && ui.Button(c, "新建文件夹").Clicked() {
				b.naming, b.newName = true, ""
			}
			ui.Text(c, "单击选中，双击打开").FontSize(12).TextColor(t.TextMuted)
		}
	})
}

// nasBackToRoots shows the roots again, read anew, as one may have been
// made writable since, with the one left chosen.
func (a *app) nasBackToRoots() {
	b := &a.nas
	left := int64(0)
	if b.root != nil {
		left = b.root.ID
	}
	var roots []fileindex.Root
	a.nasDo("正在读取 "+b.server.Title()+" 的目录…", func(ctx context.Context) (err error) {
		roots, err = b.client.Roots(ctx)
		return err
	}, func(err error) {
		if err != nil {
			return
		}
		b.roots, b.screen, b.sel = roots, screenRoots, -1
		for i := range roots {
			if roots[i].ID == left {
				b.sel = i
			}
		}
	})
}

func parentDir(p string) string {
	if d := path.Dir(p); d != "." {
		return d
	}
	return ""
}
