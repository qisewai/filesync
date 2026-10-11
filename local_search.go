package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"filesync/config"
	"filesync/fileindex"
	"filesync/localindex"
	"github.com/egoist/mygo/ui"
)

const localPageSize = 1000

type localIndexState struct {
	index           *localindex.Index
	settings        config.LocalIndexSettings
	running         bool
	loading, saving bool
	loadProgress    localindex.LoadProgress
	savedVersion    uint64
	cancel          context.CancelFunc
	progress        localindex.Progress
	status          localindex.Status
	note, err       string
	draft           *localIndexDraft
}
type localIndexDraft struct {
	open, picking bool
	roots         []string
	excludes, err string
}

func (a *app) ensureLocalIndex() {
	if a.localIdx != nil {
		return
	}
	m := &localIndexState{index: localindex.New()}
	if saved := a.store.LocalIndex; saved != nil {
		m.settings = config.LocalIndexSettings{Roots: slices.Clone(saved.Roots), Excludes: slices.Clone(saved.Excludes)}
	} else {
		if home, err := os.UserHomeDir(); err == nil {
			m.settings.Roots = []string{home}
		}
		m.settings.Excludes = []string{".cache/"}
	}
	a.localIdx = m
	if a.localCachePath != "" {
		a.loadLocalSnapshot()
	}
}

func (a *app) loadLocalSnapshot() {
	m := a.localIdx
	if m == nil || m.running || m.loading {
		return
	}
	x, filename := m.index, a.localCachePath
	if filename == "" {
		return
	}
	opts := localindex.ScanOptions{Roots: slices.Clone(m.settings.Roots), Excludes: slices.Clone(m.settings.Excludes)}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel, m.loading = cancel, true
	go func() {
		defer cancel()
		meta, err := x.LoadSnapshot(ctx, filename, opts, func(p localindex.LoadProgress) {
			a.post(func() {
				if !a.closed && a.localIdx == m && m.index == x {
					m.loadProgress = p
				}
			})
		})
		status := x.Status()
		a.post(func() {
			if a.closed || a.localIdx != m || m.index != x {
				return
			}
			m.loading = false
			m.cancel = nil
			switch {
			case errors.Is(err, os.ErrNotExist), errors.Is(err, context.Canceled):
			case errors.Is(err, localindex.ErrSnapshotScope):
				m.note = "索引范围已改变，请更新索引"
			case err != nil:
				m.err = "无法加载索引快照，请手动全部重建：" + err.Error()
			default:
				m.status = status
				m.savedVersion = status.Version
				m.note = "已加载索引快照"
				m.progress.Errors = meta.Errors
				m.progress.Messages = meta.Messages
				if meta.Partial {
					m.note = fmt.Sprintf("已加载索引快照 · %d 处记录未能更新", meta.Errors)
				}
			}
			if b := a.local; b != nil && b.results {
				a.queueLocalSearch(0)
			}
		})
	}()
}
func indexSkipPaths(roots []string) []string {
	out := localindex.BlockedMounts()
	if runtime.GOOS == "linux" {
		out = append(out, "/proc", "/sys", "/dev")
		// Explicit local mounts below /run/media (and user-selected temporary
		// folders) are valid scopes. A broad filesystem scan skips /run and /tmp.
		for _, skip := range []string{"/run", "/tmp"} {
			if !slices.ContainsFunc(roots, func(r string) bool { return localindex.Within(r, skip) }) {
				out = append(out, skip)
			}
		}
	}
	return out
}
func (a *app) startLocalScan() {
	a.scanLocalIndex(false)
}
func (a *app) rebuildLocalIndex() {
	a.scanLocalIndex(true)
}
func (a *app) scanLocalIndex(full bool) {
	a.ensureLocalIndex()
	m := a.localIdx
	if m.running || m.loading {
		return
	}
	if len(m.settings.Roots) == 0 {
		m.err = "请先在索引范围中添加目录"
		return
	}
	opts := localindex.ScanOptions{Roots: slices.Clone(m.settings.Roots), Excludes: slices.Clone(m.settings.Excludes)}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.running = true
	m.err, m.note = "", ""
	m.progress = localindex.Progress{Count: m.status.Count}
	x, filename := m.index, a.localCachePath
	go func() {
		defer cancel()
		opts.Skip = indexSkipPaths(opts.Roots)
		progress := func(p localindex.Progress) {
			version := x.Status().Version
			a.post(func() {
				if !a.closed && a.localIdx == m && m.index == x {
					m.progress = p
					m.status.Count = p.Count
					// Only the explicitly requested scan changes the index.
					// Refresh an idle query as that scan publishes partial data.
					if b := a.local; m.running && b != nil && b.results && !b.loading && !b.queryPending && !b.more && !b.opening && b.sel.Len() == 0 && (b.matched == nil || b.matched.Version() != version) {
						a.queueLocalSearch(0)
					}
				}
			})
		}
		var p localindex.Progress
		var err error
		if full {
			p, err = x.Scan(ctx, opts, progress)
		} else {
			p, err = x.Update(ctx, opts, progress)
		}
		status := x.Status()
		var saveErr error
		if err == nil && filename != "" {
			a.post(func() {
				if !a.closed && a.localIdx == m && m.index == x {
					m.saving = true
				}
			})
			saveErr = x.SaveSnapshot(ctx, filename, opts, localindex.SnapshotMeta{Partial: p.Errors > 0, Errors: p.Errors, Messages: p.Messages})
		}
		a.post(func() {
			if a.closed || a.localIdx != m || m.index != x {
				return
			}
			m.running = false
			m.saving = false
			m.cancel = nil
			m.progress = p
			m.status = status
			switch {
			case errors.Is(err, context.Canceled):
				m.note = "扫描已取消，已建立的索引仍可搜索"
			case err != nil:
				m.err = err.Error()
			case p.Errors > 0:
				m.note = fmt.Sprintf("扫描结束 · %d 处未能读取，已有记录已保留", p.Errors)
			default:
				m.note = "本机索引已更新"
			}
			if err == nil && filename != "" {
				if errors.Is(saveErr, context.Canceled) {
					m.note = "索引已更新，快照保存已取消；下次启动使用已有快照"
				} else if saveErr != nil {
					m.err = "索引已更新，但快照保存失败：" + saveErr.Error()
				} else {
					m.savedVersion = status.Version
				}
			}
			if b := a.local; b != nil && b.results && (b.matched == nil || b.matched.Version() != status.Version) {
				a.queueLocalSearch(0)
			}
		})
	}()
}
func (a *app) cancelLocalSearch() {
	b := a.local
	if b == nil {
		return
	}
	if b.debounce != nil {
		b.debounce.Stop()
		b.debounce = nil
	}
	if b.searchCancel != nil {
		b.searchCancel()
		b.searchCancel = nil
	}
	b.queryPending, b.more, b.opening = false, false, false
	b.gen++
}
func (a *app) localQuery() localindex.Query {
	b := a.local
	q := localindex.Query{Text: b.query, Hidden: b.hidden, Desc: b.sort.by.Descending}
	if b.scope == "当前目录及子目录" {
		q.Dir = b.dir
	}
	q.Exts = strings.Fields(strings.NewReplacer(",", " ", "，", " ", ";", " ").Replace(b.extensions))
	q.Category = localCategoryKeys[b.category]
	switch b.sort.by.Column {
	case colSize:
		q.Sort = "size"
	case colTime:
		q.Sort = "mtime"
	case colWhere:
		q.Sort = "path"
	default:
		q.Sort = "name"
	}
	return q
}
func (a *app) queueLocalSearch(delay time.Duration) {
	b := a.local
	if b == nil {
		return
	}
	a.ensureLocalIndex()
	a.cancelLocalSearch()
	b.results, b.queryPending = true, true
	b.sort.applied = b.sort.by
	b.sel.Clear()
	b.cursor = -1
	b.matched = nil
	m, x, gen, q := a.localIdx, a.localIdx.index, b.gen, a.localQuery()
	start := func() {
		if a.closed || a.local != b || b.gen != gen || a.localIdx != m || m.index != x {
			return
		}
		b.debounce = nil
		b.queryPending = false
		b.loading = true
		b.err = ""
		ctx, cancel := context.WithCancel(context.Background())
		b.searchCancel = cancel
		go func() {
			defer cancel()
			matches, err := x.Search(ctx, q)
			var entries []localindex.Entry
			if err == nil {
				entries, err = matches.Page(ctx, 0, localPageSize)
			}
			a.post(func() {
				if a.closed || a.local != b || b.gen != gen || a.localIdx != m || m.index != x {
					return
				}
				b.loading = false
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						b.err = err.Error()
					}
					return
				}
				b.matched = matches
				b.total = matches.Total()
				b.items = localEntries(entries)
				b.awaitTop = true
				b.list.ScrollTo(0, ui.Start)
			})
		}()
	}
	if delay <= 0 {
		start()
	} else {
		b.debounce = time.AfterFunc(delay, func() { a.post(start) })
	}
}
func localEntries(es []localindex.Entry) []fileindex.Entry {
	out := make([]fileindex.Entry, len(es))
	for i, e := range es {
		out[i] = fileindex.Entry{Name: e.Name, Path: e.Path, Parent: e.Parent, Size: e.Size, MTime: e.MTime, IsDir: e.IsDir, Ext: e.Ext}
	}
	return out
}
func (a *app) loadMoreLocal() {
	b := a.local
	if b == nil || !b.results || b.loading || b.queryPending || b.more || b.matched == nil || len(b.items) >= b.total {
		return
	}
	gen, matches, offset := b.gen, b.matched, len(b.items)
	ctx, cancel := context.WithCancel(context.Background())
	b.searchCancel = cancel
	b.more = true
	go func() {
		defer cancel()
		es, err := matches.Page(ctx, offset, localPageSize)
		a.post(func() {
			if a.closed || a.local != b || b.gen != gen || b.matched != matches {
				return
			}
			b.more = false
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					b.err = err.Error()
				}
				return
			}
			b.items = append(b.items, localEntries(es)...)
		})
	}()
}

var (
	localCategories   = []string{"全部类型", "文件夹", "文件", "图片", "视频", "音频", "文档", "压缩文件", "磁盘镜像", "代码", "其他"}
	localCategoryKeys = map[string]string{"全部类型": "", "文件夹": "dir", "文件": "file", "图片": "image", "视频": "video", "音频": "audio", "文档": "doc", "压缩文件": "archive", "磁盘镜像": "disk", "代码": "code", "其他": "other"}
	// localScopes are the values of localBrowser.scope, as the segments
	// show them.
	localScopes = []string{"全部索引", "当前目录及子目录"}
)

// filtered reports whether the search asks for some type or extension,
// which searches with no words too.
func (b *localBrowser) filtered() bool {
	return b.category != localCategories[0] || strings.TrimSpace(b.extensions) != ""
}

// localSearchField searches the index as one types; cleared, with its
// button or Escape, it goes back to the directory. Enter, without words,
// lists what the filters ask for.
func (a *app) localSearchField(c *ui.Context) {
	b, t := a.local, c.Theme()
	clear := func() {
		if b.results {
			a.openLocal(b.dir)
		}
		b.query = ""
	}
	var in ui.Element
	box := ui.Row(c.Key("local-search-box")).Width(280).Shrink(1).MinWidth(160).AlignItems(ui.Center).Gap(6).
		Padding(5, 6, 5, 10).Radius(t.Radius).Background(t.Surface).Border(1, t.Border).Cursor(ui.CursorText)
	box.Children(func() {
		ui.Icon(c, iconSearch).FontSize(14).TextColor(t.TextMuted)
		in = ui.TextInputBase(c.Key("local-search"), &b.query).Label("本机搜索").Placeholder("搜索本机文件").Grow(1).Shrink(1).MinWidth(0)
		if b.query != "" {
			ui.ButtonBase(c.Key("clear-local-search")).Size(18, 18).Radius(9).Background(t.TextMuted.Alpha(0.3)).
				Label("清除搜索").Tooltip("清除搜索（Esc）").OnClick(clear).Children(func() {
				ui.Icon(c, iconClose).FontSize(10).TextColor(t.Background)
			})
		} else {
			ui.Row(c).Size(18, 18)
		}
	})
	if in.Focused() {
		box.BorderColor(t.Accent)
	}
	if box.Clicked() {
		in.Focus()
	}
	// Each reports once.
	changed, submitted, escape := in.Changed(), in.Submitted(), in.Shortcut(0, ui.KeyEscape)
	b.searching = in.Focused()
	empty := strings.TrimSpace(b.query) == ""
	switch {
	case escape && b.query != "", changed && empty:
		clear()
	case changed:
		a.queueLocalSearch(100 * time.Millisecond)
	case submitted && (!empty || b.filtered()):
		a.queueLocalSearch(0)
	}
}

// localFilters narrow the results: where, of which type, which extensions.
func (a *app) localFilters(c *ui.Context) {
	b := a.local
	scope := max(slices.Index(localScopes, b.scope), 0)
	if ui.Segmented(c.Key("local-scope"), &scope, "全部索引", "当前目录").Label("本机搜索范围").Tooltip("当前目录：" + b.dir).Changed() {
		b.scope = localScopes[scope]
		a.queueLocalSearch(0)
	}
	if ui.Select(c.Key("local-category"), &b.category, localCategories).MinWidth(0).Width(106).Label("本机文件类型").Changed() {
		a.queueLocalSearch(0)
	}
	if ui.TextInput(c.Key("local-extensions"), &b.extensions).Width(72).Label("本机扩展名").Placeholder("扩展名").Tooltip("多个扩展名用空格分隔，例如 pdf docx").Changed() {
		a.queueLocalSearch(100 * time.Millisecond)
	}
}

// localSearchEmpty is what the results show without any: how to build the
// index first, or that nothing matched.
func (a *app) localSearchEmpty(c *ui.Context) {
	m, t := a.localIdx, c.Theme()
	ui.Column(c).Fill().Center().Gap(8).Padding(32).Children(func() {
		ui.Icon(c, iconSearch).FontSize(36).TextColor(t.TextMuted.Alpha(0.6))
		if m.status.Count == 0 && !m.running && !m.loading {
			ui.Text(c, "还没有建立本机索引").FontSize(16).Bold()
			ui.Text(c, "建立索引后可以即时搜索本机文件。只读取文件名、大小和修改时间，不读取文件内容。").FontSize(12).TextColor(t.TextMuted).TextAlign(ui.Center).MaxWidth(360)
			ui.Row(c).Gap(8).Margin(8, 0, 0, 0).Children(func() {
				ui.PrimaryButton(c, "建立索引").OnClick(a.startLocalScan)
				ui.Button(c, "索引范围…").OnClick(a.editLocalIndex)
			})
			return
		}
		ui.Text(c, "没有找到匹配的文件").FontSize(16).Bold()
		hint := "换个关键词，或者切换到“全部索引”试试。"
		switch {
		case m.running:
			hint = "索引还在建立，结果会陆续出现。"
		case !m.status.Updated.IsZero():
			hint = "索引更新于 " + m.status.Updated.Format("2006-01-02 15:04") + "，之后的文件变化需要更新索引才能搜到。"
		}
		ui.Text(c, hint).FontSize(12).TextColor(t.TextMuted).TextAlign(ui.Center).MaxWidth(360)
		if !m.running && !m.loading {
			ui.Button(c, "更新索引").Margin(8, 0, 0, 0).OnClick(a.startLocalScan)
		}
	})
}

// localIndexStatus is the index's state, in the status bar, with its menu:
// the index is updated only when asked.
func (a *app) localIndexStatus(c *ui.Context) {
	m := a.localIdx
	if m == nil {
		return
	}
	t := c.Theme()
	busy := m.loading || m.running
	text, tip := "", ""
	dot := t.Success
	switch {
	case m.loading:
		text = "正在加载索引…"
		if m.loadProgress.Total > 0 {
			text += fmt.Sprintf(" %d%%", m.loadProgress.Read*100/m.loadProgress.Total)
		}
	case m.saving:
		text = "正在保存索引…"
	case m.running:
		text = fmt.Sprintf("正在建立索引 · 已核对 %s 项", groupDigits(int64(m.progress.Scanned)))
		tip = m.progress.Current
	case m.status.Count == 0 && m.status.Updated.IsZero():
		text, dot = "尚未建立索引", t.TextMuted
	default:
		text = "已索引 " + groupDigits(int64(m.status.Count)) + " 项"
		if !m.status.Updated.IsZero() {
			text += " · 更新于 " + m.status.Updated.Format("01-02 15:04")
		}
		if a.localCachePath != "" && m.status.Version > 0 && m.savedVersion != m.status.Version {
			tip, dot = "当前索引尚未保存，下次启动需要重新建立", t.Warning
		}
	}
	ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(1).MinWidth(0).Children(func() {
		if m.note != "" {
			ui.Text(c, m.note).FontSize(12).SingleLine().Shrink(1).TextColor(t.TextMuted).Tooltip(strings.Join(m.progress.Messages, "\n"))
			ui.Text(c, "·").FontSize(12).TextColor(t.TextMuted)
		}
		if busy {
			ui.Spinner(c).FontSize(12).Label("正在处理本机索引")
		} else {
			ui.Row(c).Width(7).Height(7).Radius(4).Background(dot)
		}
		ui.Text(c, text).FontSize(12).SingleLine().FontFeatures("tnum").TextColor(t.TextMuted).Tooltip(tip)
		if m.running {
			label := "取消扫描"
			if m.saving {
				label = "取消保存"
			}
			ui.Button(c.Key("cancel-local-index"), label).OnClick(func() {
				if m.cancel != nil {
					m.cancel()
				}
			})
			return
		}
		ui.MenuButton(c.Key("local-index-menu"), "索引", func(menu *ui.Menu) {
			if menu.Item("更新索引").Chosen() {
				a.startLocalScan()
			}
			if menu.Item("全部重建").Chosen() {
				a.rebuildLocalIndex()
			}
			menu.Separator()
			if menu.Item("索引范围…").Chosen() {
				a.editLocalIndex()
			}
		}).Disabled(m.loading).Label("本机索引").Tooltip("更新索引只重读有变化的目录")
	})
}

// editLocalIndex opens the settings of what the index holds.
func (a *app) editLocalIndex() {
	m := a.localIdx
	if m == nil || m.running || m.loading {
		return
	}
	m.draft = &localIndexDraft{open: true, roots: slices.Clone(m.settings.Roots), excludes: strings.Join(m.settings.Excludes, "\n")}
}

func (a *app) localIndexDialog(c *ui.Context) {
	m := a.localIdx
	if m == nil || m.draft == nil {
		return
	}
	d := m.draft
	t := c.Theme()
	w, h := c.Size()
	addRoot := func(dir string) {
		if dir != "" && !slices.Contains(d.roots, dir) {
			d.roots = append(d.roots, dir)
		}
	}
	ui.DialogBase(c.Key("local-index-settings"), &d.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.35))
		panel.Width(min(600, w-32)).MaxHeight(h-32).Padding(24).Gap(16).Radius(t.Radius*3).Background(t.Background).Border(1, t.Border)
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, "本机索引范围").FontSize(18).Bold()
			ui.Text(c, "保存后在状态栏的“索引”菜单中更新索引。扫描只读取文件名和元数据。").FontSize(12).TextColor(t.TextMuted)
		})
		if d.err != "" {
			notice(c, t.Danger, d.err)
		}
		ui.Scroll(c).Grow(1).Shrink(1).MinHeight(0).Gap(16).Children(func() {
			ui.Column(c).Gap(8).Children(func() {
				ui.Text(c, "索引目录").Bold()
				ui.Column(c).Radius(t.Radius*2).Border(1, t.Border).Clip().Children(func() {
					if len(d.roots) == 0 {
						ui.Text(c, "还没有索引目录").FontSize(12).TextColor(t.TextMuted).Padding(12)
					}
					for i, root := range d.roots {
						ui.Row(c.Key("root/"+root)).Gap(8).Padding(6, 6, 6, 12).AlignItems(ui.Center).BorderWidth(float32(min(i, 1)), 0, 0, 0).BorderColor(t.Border).Children(func() {
							ui.Icon(c, iconFolder).FontSize(15).TextColor(ui.Hex("#d99a2b"))
							ui.Text(c, homeTilde(root)).SingleLine().Grow(1).Shrink(1).MinWidth(0).Tooltip(root)
							ui.ButtonBase(c.Key("remove/"+root)).Size(26, 26).Radius(t.Radius).Label("移除 " + root).Tooltip("移除").OnClick(func() {
								d.roots = slices.DeleteFunc(d.roots, func(r string) bool { return r == root })
							}).Children(func() {
								ui.Icon(c, iconClose).FontSize(14).TextColor(t.TextMuted)
							})
						})
					}
				})
				ui.Row(c).Gap(8).Children(func() {
					ui.Button(c, "添加文件夹…").Disabled(d.picking).OnClick(func() {
						d.picking = true
						start := a.local.dir
						go func() {
							dir, err := a.pickDir("选择本机索引目录", start)
							a.post(func() {
								if a.closed || a.localIdx != m || m.draft != d {
									return
								}
								d.picking = false
								if err != nil {
									d.err = err.Error()
								} else {
									addRoot(dir)
								}
							})
						}()
					})
					ui.Button(c, "添加当前目录").Disabled(slices.Contains(d.roots, a.local.dir)).Tooltip(a.local.dir).OnClick(func() { addRoot(a.local.dir) })
				})
			})
			ui.Column(c).Gap(8).Children(func() {
				ui.Text(c, "排除规则").Bold()
				ui.TextArea(c.Key("local-index-excludes"), &d.excludes).Height(96).Label("本机索引排除规则").Placeholder(".cache/\nnode_modules/\n.git/\n*.tmp")
				ui.Text(c, "每行一条；以 / 结尾的规则排除整个文件夹。网络挂载和系统虚拟目录会自动跳过，符号链接不递归，重叠的目录只扫描一次。").FontSize(12).TextColor(t.TextMuted)
			})
		})
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			ui.Button(c, "取消").Label("取消索引设置").OnClick(func() { d.open = false })
			ui.PrimaryButton(c, "保存").Label("保存索引设置").Disabled(d.picking).OnClick(func() {
				excludes := nonemptyLines(d.excludes)
				normalized, err := localindex.Validate(d.roots, excludes)
				if err != nil {
					d.err = err.Error()
					return
				}
				changed := !slices.Equal(m.settings.Roots, normalized) || !slices.Equal(m.settings.Excludes, excludes)
				saved := a.store.LocalIndex
				a.store.LocalIndex = &config.LocalIndexSettings{Roots: normalized, Excludes: excludes}
				if err := a.store.Save(); err != nil {
					a.store.LocalIndex = saved
					d.err = err.Error()
					return
				}
				m.settings = *a.store.LocalIndex
				m.draft = nil
				if !changed {
					return
				}
				m.index = localindex.New()
				m.status = localindex.Status{}
				m.savedVersion = 0
				m.progress = localindex.Progress{}
				m.note = "索引范围已改变，请更新索引"
				m.err = ""
				if a.local.results {
					a.queueLocalSearch(0)
				}
			})
		})
	})
	if !d.open {
		m.draft = nil
	}
}

// homeTilde shows a path in the home directory from ~.
func homeTilde(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == string(filepath.Separator) {
		return p
	}
	if p == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return p
}

func nonemptyLines(text string) []string {
	var out []string
	for _, s := range strings.Split(text, "\n") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (a *app) openIndexedLocal(e fileindex.Entry, systemFolder, nativeFolder bool) {
	b := a.local
	if b.opening {
		return
	}
	b.opening = true
	gen := b.gen
	go func() {
		_, err := os.Lstat(e.Path)
		a.post(func() {
			if a.closed || a.local != b || b.gen != gen {
				return
			}
			b.opening = false
			if err != nil {
				b.err = "索引中的文件已移动、删除或无法访问，请手动更新索引：" + err.Error()
				return
			}
			if systemFolder {
				a.openPath(e.Parent)
			} else if nativeFolder {
				a.openLocal(e.Parent, e.Name)
			} else {
				a.openLocalEntry(e)
			}
		})
	}()
}
func (a *app) backupLocalSelection(es []fileindex.Entry) {
	if !a.local.results || len(es) == 0 {
		a.newTaskFrom(a.local.dir, es)
		return
	}
	groups := map[string][]fileindex.Entry{}
	var parents []string
	for _, e := range es {
		if _, ok := groups[e.Parent]; !ok {
			parents = append(parents, e.Parent)
		}
		groups[e.Parent] = append(groups[e.Parent], e)
	}
	slices.Sort(parents)
	queue := make([][]fileindex.Entry, 0, len(parents))
	for _, p := range parents {
		queue = append(queue, groups[p])
	}
	a.localBackups = queue
	a.nextLocalBackup()
}
func (a *app) nextLocalBackup() {
	if len(a.localBackups) == 0 {
		return
	}
	es, rest := a.localBackups[0], a.localBackups[1:]
	a.newTaskFrom(filepath.Dir(es[0].Path), es)
	a.localBackups = rest
}
