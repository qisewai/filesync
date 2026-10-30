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

	"filesync/config"
	"filesync/fileindex"
	"filesync/localindex"
	"github.com/egoist/mygo/ui"
)

type localIndexState struct {
	liveRunning, liveReplay bool
	livePaths               []string
	liveCancel              context.CancelFunc
	index                   *localindex.Index
	settings                config.LocalIndexSettings
	running                 bool
	loading, saving         bool
	loadProgress            localindex.LoadProgress
	savedVersion            uint64
	cancel                  context.CancelFunc
	progress                localindex.Progress
	status                  localindex.Status
	note, err               string
	draft                   *localIndexDraft
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
			a.startLiveIndex()
			a.indexChanged()
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
	var operationLog *localindex.OperationLog
	if full && a.localCachePath != "" {
		if a.localOpLogLoading {
			m.err = "正在校验文件操作日志，请稍后重试"
			return
		}
		operationLog = a.localOpLog
		// Reserve ownership while a missing/damaged log is opened off-thread.
		if operationLog == nil {
			a.localOpLogLoading = true
		}
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
		var checkpointErr error
		checkpointReady := false
		if full && filename != "" {
			if operationLog == nil {
				operationLog, checkpointErr = localindex.OpenRecoverableOperationLog(filename + ".operations")
				opened, openErr := operationLog, checkpointErr
				a.post(func() {
					a.localOpLogLoading = false
					if a.closed {
						if opened != nil {
							opened.Close()
						}
						return
					}
					a.localOpLog, a.localOpLogErr = opened, openErr
					if opened != nil {
						a.connectLocalOperationLog(opened)
					}
				})
			}
			if checkpointErr == nil {
				checkpointErr = operationLog.StartCheckpoint()
				checkpointReady = checkpointErr == nil
				if checkpointReady {
					defer operationLog.EndCheckpoint()
				}
			}
		}

		opts.Skip = indexSkipPaths(opts.Roots)
		progress := func(p localindex.Progress) {
			a.post(func() {
				if !a.closed && a.localIdx == m && m.index == x {
					m.progress = p
					m.status.Count = p.Count
				}
			})
		}
		var p localindex.Progress
		var err error
		if checkpointErr != nil {
			err = checkpointErr
		} else if full {
			p, err = x.Scan(ctx, opts, progress)
		} else {
			p, err = x.Update(ctx, opts, progress)
		}
		status := x.Status()
		var saveErr, compactErr error
		if err == nil && filename != "" {
			a.post(func() {
				if !a.closed && a.localIdx == m && m.index == x {
					m.saving = true
				}
			})
			saveErr = x.SaveSnapshot(ctx, filename, opts, localindex.SnapshotMeta{Partial: p.Errors > 0, Errors: p.Errors, Messages: p.Messages})
			if saveErr == nil {
				_, compactErr = x.LoadSnapshot(ctx, filename, opts, nil)
				status = x.Status()
			}
		}
		if checkpointReady && err == nil && saveErr == nil && compactErr == nil && p.Errors == 0 {
			checkpointErr = operationLog.CommitCheckpoint(ctx, filename)
			if checkpointErr == nil {
				x.ClearDeltas()
				status = x.Status()
			}
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
					if compactErr != nil {
						m.err = "索引已保存，但切换磁盘读取失败：" + compactErr.Error()
					}
				}
			}
			if checkpointErr != nil {
				m.err = "索引日志未能建立检查点：" + checkpointErr.Error()
			}
			m.liveReplay = true
			a.startLiveIndex()
			a.indexChanged()
		})
	}()
}

// indexChanged searches again, as the index of this computer changed, on
// the page of the search.
func (a *app) indexChanged() {
	if a.onSearch() && len(a.gsearch.groups) > 0 {
		a.runSearch(0)
	}
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
		if m.status.Approximate {
			text = "索引项数正在核对"
		}
		if m.status.Delta {
			text += " · 含实时变更"
		}
		if !m.status.Updated.IsZero() {
			text += " · 更新于 " + m.status.Updated.Format("01-02 15:04")
		}
		if a.localCachePath != "" && m.status.Version > 0 && m.savedVersion != m.status.Version && !m.status.Delta {
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
		rolling(ui.Text(c, text).FontSize(12).SingleLine().FontFeatures("tnum").TextColor(t.TextMuted).Tooltip(tip))
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
			ui.Text(c, "保存后点“更新索引”（本机页状态栏的“索引”菜单或设置页）。扫描只读取文件名和元数据。").FontSize(12).TextColor(t.TextMuted)
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
							ui.Icon(c, iconFolder).FontSize(15).TextColor(folderColor)
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
					// The folder shown, if this computer's files were.
					here := ""
					if a.local != nil {
						here = a.local.dir
					}
					ui.Button(c, "添加文件夹…").Disabled(d.picking).OnClick(func() {
						d.picking = true
						start := here
						if start == "" {
							start = a.startLocal()
						}
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
					if here != "" {
						ui.Button(c, "添加当前目录").Disabled(slices.Contains(d.roots, here)).Tooltip(here).OnClick(func() { addRoot(here) })
					}
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
				a.indexChanged()
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

// backupLocal starts a task backing up es of this computer, of the
// directory dir, or dir itself without any; entries of different folders,
// as found by a search, make a task for each folder, set up one after the
// other.
func (a *app) backupLocal(dir string, es []fileindex.Entry) {
	if len(es) == 0 {
		a.newTaskFrom(dir, nil)
		return
	}
	groups := map[string][]fileindex.Entry{}
	var parents []string
	for _, e := range es {
		parent := filepath.Dir(e.Path)
		if _, ok := groups[parent]; !ok {
			parents = append(parents, parent)
		}
		groups[parent] = append(groups[parent], e)
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
