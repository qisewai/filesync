package main

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"filesync/fileindex"
	"github.com/egoist/mygo/ui"
)

func (a *app) statsView(c *ui.Context, m *adminPage) {
	t := c.Theme()
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		a.scopePicker(c, m)
		ui.Text(c, "数据来自索引，不读取硬盘文件内容。").FontSize(12).TextColor(t.TextMuted)
	})
	if m.overview == nil {
		ui.Spacer(c)
		return
	}
	ov := m.overview
	ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).Gap(14).Padding(0, 8, 4, 0).Children(func() {
			a.statsSummary(c, ov)
			// Rows of cards as high as the higher: the chart grows to the
			// folders beside it.
			ui.Row(c).Gap(14).Wrap().AlignItems(ui.Stretch).Children(func() {
				statsCard(c, "文件夹占用", 3, func() { a.statsFolders(c, m, ov) })
				statsCard(c, "按修改年份", 2, func() { yearChart(c, ov.Years) })
			})
			ui.Row(c).Gap(14).Wrap().AlignItems(ui.Stretch).Children(func() {
				statsCard(c, "文件类型", 3, func() {
					for _, b := range ov.Categories {
						usageRow(c, "", nil, categoryColor(b.Key), categoryLabel(b.Key), b.Size, ov.Size, b.Count, nil)
					}
					if len(ov.Categories) == 0 {
						statsEmpty(c)
					}
				})
				statsCard(c, "扩展名 Top 15", 2, func() {
					exts := append([]fileindex.Bucket(nil), ov.Exts...)
					sort.SliceStable(exts, func(i, j int) bool { return exts[i].Size > exts[j].Size })
					for _, b := range exts {
						name := b.Key
						if name == "" {
							name = "（无扩展名）"
						}
						usageRow(c, "", nil, extColor(b.Key), name, b.Size, ov.Size, b.Count, nil)
					}
					if len(ov.Exts) == 0 {
						statsEmpty(c)
					}
				})
			})
			a.largestCard(c, m)
		})
	})
}

// statsSummary is the total, and how the kinds of files share it.
func (a *app) statsSummary(c *ui.Context, ov *fileindex.Overview) {
	t := c.Theme()
	ui.Column(c).Padding(18).Gap(14).Radius(t.Radius*2).Border(1, t.Border).Background(t.Background).Children(func() {
		ui.Row(c).Gap(28).AlignItems(ui.End).Wrap().Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, "总大小").FontSize(12).TextColor(t.TextMuted)
				ui.Text(c, formatBytes(ov.Size)).FontSize(30).Bold().FontFeatures("tnum")
			})
			figure := func(label string, n int64) {
				ui.Column(c).Gap(2).Children(func() {
					ui.Text(c, label).FontSize(12).TextColor(t.TextMuted)
					ui.Text(c, groupDigits(n)).FontSize(20).Bold().FontFeatures("tnum")
				})
			}
			figure("文件", ov.Files)
			figure("文件夹", ov.Dirs)
		})
		if ov.Size <= 0 || len(ov.Categories) == 0 {
			return
		}
		// One bar, shared by the kinds of files.
		ui.Row(c).Height(12).Radius(6).Clip().Background(t.Border).Label("文件类型占比").Children(func() {
			for _, b := range ov.Categories {
				if b.Size <= 0 {
					continue
				}
				ui.Column(c).Height(12).WidthPercent(float32(float64(b.Size) / float64(ov.Size) * 100)).Background(categoryColor(b.Key)).
					Tooltip(fmt.Sprintf("%s %s（%.1f%%）", categoryLabel(b.Key), formatBytes(b.Size), percentOf(b.Size, ov.Size)))
			}
		})
		ui.Row(c).Gap(18).Wrap().Children(func() {
			for _, b := range ov.Categories {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Column(c).Size(10, 10).Radius(3).Background(categoryColor(b.Key))
					ui.Text(c, categoryLabel(b.Key)).FontSize(12)
					ui.Text(c, formatBytes(b.Size)).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum")
				})
			}
		})
	})
}

// statsFolders is how the folders shown share their space; a folder opens
// to its own.
func (a *app) statsFolders(c *ui.Context, m *adminPage, ov *fileindex.Overview) {
	t := c.Theme()
	scope := a.adminScope(m)
	if scope == 0 {
		roots := append([]fileindex.Root(nil), a.roots[m.server]...)
		sort.Slice(roots, func(i, j int) bool { return roots[i].TotalSize > roots[j].TotalSize })
		ui.Text(c, "各索引目录，点击查看其中的文件夹").FontSize(12).TextColor(t.TextMuted)
		for _, root := range roots {
			usageRow(c, "root/"+fmt.Sprint(root.ID), iconRoots, folderColor, root.Name, root.TotalSize, ov.Size, root.FileCount, func() {
				m.scope = rootOption(root)
				m.scopeID = root.ID
				m.statsDir = ""
				a.reloadAdmin()
			})
		}
		return
	}
	// Where: the root, then the folders down to the one shown.
	rootName := "目录"
	for _, r := range a.roots[m.server] {
		if r.ID == scope {
			rootName = r.Name
		}
	}
	crumbs := []string{rootName}
	if m.statsDir != "" {
		crumbs = append(crumbs, strings.Split(m.statsDir, "/")...)
	}
	chosen := len(crumbs) - 1
	if ui.Breadcrumbs(c.Key("stats-crumbs"), crumbs, &chosen).Label("统计位置").Changed() && !m.loading {
		m.statsDir = strings.Join(crumbs[1:chosen+1], "/")
		a.reloadAdmin()
	}
	var total int64
	for _, d := range m.dirs {
		total += d.Size
	}
	if len(m.dirs) == 0 {
		statsEmpty(c)
		return
	}
	height := min(float32(len(m.dirs))*46+4, 330)
	ui.List(c.Key("stats-dirs"), &m.dirList, len(m.dirs)).Height(height).Label("文件夹占用列表").Rows(func(row ui.ListRow) {
		dir := m.dirs[row.Index]
		if !dir.IsDir {
			usageRow(c, "", iconFile, ui.Hex("#8a94a6"), "本层文件", dir.Size, total, dir.Files, nil)
			return
		}
		usageRow(c, "", iconFolder, folderColor, dir.Name, dir.Size, total, dir.Files, func() {
			m.statsDir = dir.Path
			a.reloadAdmin()
		})
	})
}

func (a *app) largestCard(c *ui.Context, m *adminPage) {
	t := c.Theme()
	ui.Column(c).Padding(16).Gap(10).Radius(t.Radius*2).Border(1, t.Border).Background(t.Background).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "最大的 50 个文件").Bold().Grow(1)
			ui.Text(c, "双击预览或用系统程序打开，点位置跳到所在文件夹").FontSize(12).TextColor(t.TextMuted)
		})
		if m.largestSort.stale() {
			sortEntries(&m.largestSort, m.largest, &m.largestCursor)
		}
		cols := []ui.TableColumn{{Title: colName, Sortable: true}, {Title: colWhere, Width: 260, Sortable: true}, {Title: colSize, Width: 96, Align: ui.End, Sortable: true}}
		height := min(float32(len(m.largest))*34+38, 380)
		ui.Table(c.Key("largest-files"), &m.largestList, cols, len(m.largest), func(row, col int) {
			e := m.largest[row]
			switch col {
			case 0:
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					fileIcon(c, e, 16)
					ui.Text(c, e.Name).SingleLine().Grow(1).Shrink(1)
				})
			case 1:
				where := path.Join(e.RootName, e.Parent)
				link := ui.ButtonBase(c.Key(fmt.Sprintf("largest-location/%d/%s", e.Root, e.Path))).Grow(1).Shrink(1).MinWidth(0).Justify(ui.Start).Tooltip("打开 " + where).Children(func() {
					ui.Text(c, where).SingleLine().FontSize(12).TextColor(t.Accent)
				})
				link.OnClick(func() { a.locateManagedEntry(m.server, e) })
			case 2:
				ui.Text(c, formatBytes(e.Size)).SingleLine().FontFeatures("tnum")
			}
		}).Height(height).Label("最大文件列表").OnSubmit(func() {
			i := m.largestCursor
			if i >= 0 && i < len(m.largest) {
				a.openManagedEntry(m.server, m.largest[i], m.largest)
			}
		}).Children(func() {
			if len(m.largest) == 0 {
				ui.Text(c, "没有文件").TextColor(t.TextMuted).Padding(12)
			}
		})
	})
}

// statsCard is a box of the page, grow its share of the row.
func statsCard(c *ui.Context, title string, grow float32, fn func()) {
	t := c.Theme()
	ui.Column(c).Grow(grow).Basis(0).MinWidth(300).Padding(16).Gap(4).Radius(t.Radius*2).Border(1, t.Border).Background(t.Background).Children(func() {
		ui.Text(c, title).Bold().Margin(0, 0, 6, 0)
		fn()
	})
}

func statsEmpty(c *ui.Context) {
	ui.Text(c, "没有数据").FontSize(12).TextColor(c.Theme().TextMuted).Padding(8, 0)
}

var folderColor = ui.Hex("#d99a2b")

// usageRow is a share of the space in one row: what, how many files, how
// big, and a bar. With action, it opens, the key naming it.
func usageRow(c *ui.Context, key string, svg *ui.SVG, color ui.Color, label string, size, total, count int64, action func()) {
	t := c.Theme()
	pct := percentOf(size, total)
	body := func() {
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(5).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if svg != nil {
					ui.Icon(c, svg).FontSize(15).TextColor(color)
				} else {
					ui.Column(c).Size(9, 9).Radius(3).Background(color)
				}
				name := ui.Text(c, label).SingleLine().Grow(1).Shrink(1).MinWidth(0)
				if action != nil {
					name.TextColor(t.Accent)
				}
				ui.Text(c, groupDigits(count)+" 个文件").FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum")
				ui.Text(c, formatBytes(size)).FontSize(12).Bold().Width(76).TextAlign(ui.End).FontFeatures("tnum")
				ui.Text(c, fmt.Sprintf("%.1f%%", pct)).FontSize(11).TextColor(t.TextMuted).Width(44).TextAlign(ui.End).FontFeatures("tnum")
			})
			ui.Row(c).Height(5).Radius(3).Clip().Background(t.Border.Alpha(0.6)).Children(func() {
				if pct > 0 {
					ui.Column(c).Height(5).WidthPercent(float32(max(pct, 0.6))).Background(color).Radius(3)
				}
			})
		})
	}
	if action == nil {
		ui.Row(c).Padding(6, 8).Children(body).Label(label)
		return
	}
	k := c
	if key != "" {
		k = c.Key(key)
	}
	row := ui.ButtonBase(k).Padding(6, 8).Radius(t.Radius).Justify(ui.Start).Tooltip("查看“" + label + "”里面的占用").Children(body)
	if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.OnClick(action)
}

// yearChart shows the space of the files by the year they were changed,
// as columns.
func yearChart(c *ui.Context, years []fileindex.Bucket) {
	t := c.Theme()
	if len(years) == 0 {
		statsEmpty(c)
		return
	}
	ys := append([]fileindex.Bucket(nil), years...)
	sort.Slice(ys, func(i, j int) bool { return ys[i].Key < ys[j].Key })
	var top int64
	for _, y := range ys {
		top = max(top, y.Size)
	}
	// Narrow columns show some of the years, and their sizes on hover.
	every := (len(ys) + 6) / 7
	sizes := len(ys) <= 6
	// The columns grow with the card, as high as the cards beside it.
	ui.Row(c).Gap(6).AlignItems(ui.Stretch).MinHeight(170).Grow(1).Padding(8, 0, 0, 0).Children(func() {
		for i, y := range ys {
			frac := float32(0)
			if top > 0 {
				frac = float32(float64(y.Size) / float64(top))
			}
			label := y.Key
			if (len(ys)-1-i)%every != 0 {
				label = " "
			}
			ui.Column(c).Grow(1).Basis(0).MinWidth(0).MaxWidth(56).Gap(4).AlignItems(ui.Center).Tooltip(fmt.Sprintf("%s 年：%s，%s 个文件", y.Key, formatBytes(y.Size), groupDigits(y.Count))).Children(func() {
				// The plot: the column at its foot, its size over it.
				ui.Column(c).Grow(1).MinHeight(0).WidthPercent(100).Justify(ui.End).AlignItems(ui.Center).Gap(3).Children(func() {
					if sizes {
						ui.Text(c, formatBytes(y.Size)).FontSize(10).TextColor(t.TextMuted).SingleLine().FontFeatures("tnum")
					}
					ui.Column(c).HeightPercent(max(frac*86, 1)).MinHeight(2).WidthPercent(100).MaxWidth(36).Radius(4).Background(t.Accent.Alpha(0.85))
				})
				ui.Text(c, label).FontSize(11).FontFeatures("tnum")
			})
		}
	}).Label("按修改年份")
}

func percentOf(size, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return min(float64(size)/float64(total)*100, 100)
}

// groupDigits writes n with thousands separators, as 3,912,044.
func groupDigits(n int64) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// categoryColor is the color of a kind of files, as their icons have.
func categoryColor(key string) ui.Color {
	switch key {
	case "image":
		return ui.Hex("#2f9e6b")
	case "video":
		return ui.Hex("#8a5cd6")
	case "audio":
		return ui.Hex("#d6588f")
	case "doc":
		return ui.Hex("#3b7ddd")
	case "archive":
		return ui.Hex("#a8743a")
	case "disk":
		return ui.Hex("#475569")
	case "code":
		return ui.Hex("#1f9aa8")
	}
	return ui.Hex("#8a94a6")
}

// extColor is the color of the kind of files an extension is.
func extColor(ext string) ui.Color {
	kind := fileKind(fileindex.Entry{Ext: ext})
	if kind == "file" {
		kind = "other"
	}
	return categoryColor(kind)
}

func categoryLabel(key string) string {
	switch key {
	case "image":
		return "图片"
	case "video":
		return "视频"
	case "audio":
		return "音频"
	case "doc":
		return "文档"
	case "archive":
		return "压缩文件"
	case "disk":
		return "磁盘镜像"
	case "code":
		return "代码"
	case "other":
		return "其他"
	}
	return strings.TrimSpace(key)
}
