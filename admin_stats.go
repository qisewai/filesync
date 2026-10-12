package main

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"filesync/fileindex"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"
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
			a.statsSummary(c, m, ov)
			// Rows of cards as high as the higher.
			ui.Row(c).Gap(14).Wrap().AlignItems(ui.Stretch).Children(func() {
				statsCard(c, "文件夹占用", 3, func() { a.statsFolders(c, m, ov) })
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
			ui.Row(c).Children(func() {
				statsCard(c, "按修改年份", 1, func() { yearChart(c, m, ov.Years) })
			})
			a.largestCard(c, m)
		})
	})
}

// statsSummary is the total, and how the kinds of files share it, in a
// donut and its legend: pointing at a kind in either shows it in both.
func (a *app) statsSummary(c *ui.Context, m *adminPage, ov *fileindex.Overview) {
	t := c.Theme()
	var cats []fileindex.Bucket
	for _, b := range ov.Categories {
		if b.Size > 0 {
			cats = append(cats, b)
		}
	}
	if m.catHover >= len(cats) {
		m.catHover = -1
	}
	hover := -1
	ui.Row(c).Padding(18, 24).Gap(32).AlignItems(ui.Center).Wrap().Radius(t.Radius*2).Border(1, t.Border).Background(t.Background).Children(func() {
		if len(cats) > 0 {
			if i := categoryDonut(c, m, cats, ov.Size); i >= 0 {
				hover = i
			}
		}
		ui.Column(c).Grow(1).Basis(0).MinWidth(260).Gap(16).Children(func() {
			ui.Row(c).Gap(32).AlignItems(ui.End).Wrap().Children(func() {
				figure := func(label, value string, size float32) {
					ui.Column(c).Gap(2).Children(func() {
						ui.Text(c, label).FontSize(12).TextColor(t.TextMuted)
						ui.Text(c, value).FontSize(size).Bold().FontFeatures("tnum")
					})
				}
				figure("总大小", formatBytes(ov.Size), 28)
				figure("文件", groupDigits(ov.Files), 20)
				figure("文件夹", groupDigits(ov.Dirs), 20)
			})
			// The legend, two columns of the kinds.
			ui.Row(c).GapX(20).GapY(4).Wrap().Children(func() {
				for i, b := range cats {
					item := ui.Row(c.Key("cat/"+b.Key)).Gap(8).AlignItems(ui.Center).Padding(4, 8).Radius(t.Radius).
						Width(236).Label(categoryLabel(b.Key)).
						Tooltip(fmt.Sprintf("%s 个文件", groupDigits(b.Count)))
					if item.Hovered() {
						hover = i
					}
					if i == m.catHover {
						item.Background(t.SurfaceHover)
					}
					item.Children(func() {
						ui.Column(c).Size(10, 10).Radius(3).Background(categoryColor(b.Key))
						ui.Text(c, categoryLabel(b.Key)).FontSize(13).Grow(1)
						ui.Text(c, formatBytes(b.Size)).FontSize(12).FontFeatures("tnum")
						ui.Text(c, fmt.Sprintf("%.1f%%", percentOf(b.Size, ov.Size))).FontSize(12).TextColor(t.TextMuted).Width(46).TextAlign(ui.End).FontFeatures("tnum")
					})
				}
			})
		})
	})
	if hover != m.catHover {
		// The donut and the legend built before the pointer was known
		// show it in the next frame.
		m.catHover = hover
		c.After(0)
	}
}

// The donut of the summary: its size, and the radii of its ring.
const (
	donutSize  = 156
	donutInner = 52
	donutOuter = 72
)

// categoryDonut shows the kinds of files as a ring, the one pointed at
// standing out, and how many kinds, or the share of the one pointed at,
// in its middle. It returns the kind under the pointer, -1 for none.
func categoryDonut(c *ui.Context, m *adminPage, cats []fileindex.Bucket, total int64) int {
	t := c.Theme()
	values := make([]float64, len(cats))
	for i, b := range cats {
		values[i] = float64(b.Size)
	}
	slices := chart.Pie(values, 0.025)
	const mid = donutSize / 2
	ring := ui.Box(c.Key("category-donut")).Size(donutSize, donutSize).Center().Label("文件类型占比").
		Description(categoryShares(cats, total))
	under := -1
	if x, y, over := ring.PointerPosition(); over {
		if i, ok := chart.SliceAt(slices, x-mid, y-mid, donutInner, donutOuter+4); ok {
			under = i
		}
	}
	hover := m.catHover
	ring.Draw(func(p *ui.Painter, r ui.Rect) {
		for i, s := range slices {
			outer, color := float32(donutOuter), categoryColor(cats[i].Key)
			if hover >= 0 && i != hover {
				color = color.Alpha(0.35)
			}
			if i == hover {
				outer += 4
			}
			p.FillPath(chart.Sector(r.X+mid, r.Y+mid, donutInner, outer, s), color)
		}
	})
	ring.Children(func() {
		ui.Column(c).Width(2*donutInner - 8).Gap(1).AlignItems(ui.Center).Children(func() {
			label, value := "文件类型", fmt.Sprintf("%d 类", len(cats))
			if hover >= 0 {
				b := cats[hover]
				label, value = categoryLabel(b.Key), fmt.Sprintf("%.1f%%", percentOf(b.Size, total))
			}
			ui.Text(c, label).FontSize(11).TextColor(t.TextMuted).SingleLine()
			ui.Text(c, value).FontSize(15).Bold().SingleLine().FontFeatures("tnum")
		})
	})
	return under
}

// categoryShares says how the kinds share the space, for assistive
// technology.
func categoryShares(cats []fileindex.Bucket, total int64) string {
	parts := make([]string, len(cats))
	for i, b := range cats {
		parts[i] = fmt.Sprintf("%s %s（%.1f%%）", categoryLabel(b.Key), formatBytes(b.Size), percentOf(b.Size, total))
	}
	return strings.Join(parts, "，")
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
	crumbs := []string{"全部目录", rootName}
	if m.statsDir != "" {
		crumbs = append(crumbs, strings.Split(m.statsDir, "/")...)
	}
	chosen := len(crumbs) - 1
	if ui.Breadcrumbs(c.Key("stats-crumbs"), crumbs, &chosen).Label("统计位置").Changed() && !m.loading {
		if chosen == 0 {
			a.statsAll(m)
		} else {
			m.statsDir = strings.Join(crumbs[2:chosen+1], "/")
			a.reloadAdmin()
		}
	}
	var total int64
	for _, d := range m.dirs {
		total += d.Size
	}
	if len(m.dirs) == 0 {
		statsEmpty(c)
		return
	}
	// As high as its rows, up to a limit, and as the card where the card
	// is higher, as beside a longer one: scrolling then only past it.
	height := min(float32(len(m.dirs))*46+4, 330)
	ui.List(c.Key("stats-dirs"), &m.dirList, len(m.dirs)).Basis(height).Grow(1).MinHeight(height).Label("文件夹占用列表").Rows(func(row ui.ListRow) {
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
// as bars over an axis of sizes: the pointer, or the arrows, choose a
// year to tell its size and files.
func yearChart(c *ui.Context, m *adminPage, years []fileindex.Bucket) {
	t := c.Theme()
	if len(years) == 0 {
		statsEmpty(c)
		return
	}
	ys := append([]fileindex.Bucket(nil), years...)
	sort.Slice(ys, func(i, j int) bool { return ys[i].Key < ys[j].Key })
	labels := make([]string, len(ys))
	var top int64
	for i, y := range ys {
		labels[i] = y.Key
		top = max(top, y.Size)
	}
	x := chart.NewBand(labels...).Padding(0.3, 0.15)
	unit, div := byteUnit(top)
	y := chart.NewLinear(0, max(float64(top)/div, 1)).Nice(4)
	y.Format = func(v float64) string { return strconv.FormatFloat(v, 'g', 4, 64) + " " + unit }
	ticks := y.Ticks(4)
	// Narrow bands label some of the years, the latest always.
	bottom := x.Ticks()
	every := (len(ys) + 11) / 12
	for i := range bottom {
		if (len(ys)-1-i)%every != 0 {
			bottom[i].Label = ""
		}
	}
	xs := make([]float32, len(ys))
	for i := range ys {
		xs[i] = x.At(i)
	}
	describe := func(i int) string {
		return fmt.Sprintf("%s 年：%s，%s 个文件", ys[i].Key, formatBytes(ys[i].Size), groupDigits(ys[i].Count))
	}
	label := func(side chart.Side, tick chart.Tick) {
		ui.Text(c, tick.Label).NoWrap().FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum")
	}
	chart.Plot(c.Key("year-chart"), chart.Axes{Left: ticks, Bottom: bottom, Label: label}, func(area ui.Element) {
		area.Draw(func(p *ui.Painter, r ui.Rect) {
			for _, tick := range ticks {
				p.Line(r.X, chart.Y(r, tick.At), r.X+r.W, chart.Y(r, tick.At), 1, t.Border.Alpha(0.7))
			}
		})
		chart.Select(c, area, &m.yearPick, xs, describe)
		pick := m.yearPick
		for i, b := range ys {
			color := t.Accent.Alpha(0.85)
			if pick >= 0 && i != pick {
				color = t.Accent.Alpha(0.4)
			}
			chart.Rect(c, x.Band(i), y.Span(0, float64(b.Size)/div)).MinHeight(2).Radius(3, 3, 0, 0).Background(color)
		}
		if pick >= 0 && pick < len(ys) {
			// The label by the bar, inside the plot at its ends.
			at, h := xs[pick], ui.Center
			switch {
			case at < 0.2:
				h = ui.Start
			case at > 0.8:
				h = ui.End
			}
			chart.Anchor(c, at, y.At(float64(ys[pick].Size)/div), h, ui.End, func() {
				ui.Text(c, describe(pick)).NoWrap().FontSize(12).FontFeatures("tnum").Padding(4, 10).Margin(0, 0, 6, 0).
					Radius(t.Radius).Background(t.Background).Border(1, t.Border).Shadow(0, 2, 8, 0, ui.RGBA(0, 0, 0, 0.12))
			})
		}
	}).Height(240).Gap(8).Margin(8, 0, 0, 0).Label("按修改年份").Description("各年修改的文件大小，用左右方向键逐年查看")
}

// byteUnit is the unit sizes up to n read best in, and its bytes.
func byteUnit(n int64) (string, float64) {
	unit, div := "B", 1.0
	for _, u := range []string{"KB", "MB", "GB", "TB"} {
		if float64(n) < div*1024 {
			break
		}
		unit, div = u, div*1024
	}
	return unit, div
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

// statsAll shows the statistics of all the roots.
func (a *app) statsAll(m *adminPage) {
	m.scope, m.scopeID, m.statsDir, m.page = "全部目录", 0, "", 1
	m.selected = map[int64]bool{}
	a.reloadAdmin()
}

// statsUp goes from the folder the statistics show to the one holding
// it, from a root to all of them; nil at the top.
func (a *app) statsUp(m *adminPage) func() {
	switch {
	case m.loading:
		return nil
	case m.statsDir != "":
		return func() {
			m.statsDir = parentDir(m.statsDir)
			a.reloadAdmin()
		}
	case m.scopeID != 0:
		return func() { a.statsAll(m) }
	}
	return nil
}
