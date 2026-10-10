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
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		a.scopePicker(c, m)
		ui.Text(c, "数据来自索引，不读取硬盘文件内容。").FontSize(12).TextColor(c.Theme().TextMuted)
	})
	if m.overview == nil {
		ui.Spacer(c)
		return
	}
	ov := m.overview
	ui.Scroll(c).Grow(1).MinHeight(0).Gap(14).Children(func() {
		ui.Row(c).Gap(12).Wrap().Children(func() {
			for _, v := range []struct{ label, value string }{{"总大小", formatBytes(ov.Size)}, {"文件", fmt.Sprint(ov.Files)}, {"文件夹", fmt.Sprint(ov.Dirs)}} {
				ui.Column(c).Grow(1).MinWidth(150).Padding(14).Border(1, c.Theme().Border).Radius(8).Gap(6).Children(func() { ui.Text(c, v.label).TextColor(c.Theme().TextMuted); ui.Text(c, v.value).FontSize(24).Bold() })
			}
		})
		ui.Row(c).Gap(12).Wrap().AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Grow(1).MinWidth(250).Padding(14).Border(1, c.Theme().Border).Radius(8).Gap(8).Children(func() {
				ui.Text(c, "文件夹占用").Bold()
				scope := a.adminScope(m)
				if scope == 0 {
					roots := append([]fileindex.Root(nil), a.roots[m.server]...)
					sort.Slice(roots, func(i, j int) bool { return roots[i].TotalSize > roots[j].TotalSize })
					ui.Scroll(c.Key("stats-roots")).Height(240).Gap(8).Children(func() {
						for _, root := range roots {
							a.usageBar(c, root.Name, root.TotalSize, ov.Size, root.FileCount, func() { m.scope = rootOption(root); m.scopeID = root.ID; m.statsDir = ""; a.reloadAdmin() })
						}
					})
				} else {
					ui.Row(c).Gap(8).Children(func() {
						ui.Button(c, "统计目录顶层").Disabled(m.statsDir == "" || m.loading).OnClick(func() { m.statsDir = ""; a.reloadAdmin() })
						ui.Button(c, "统计上一级").Disabled(m.statsDir == "" || m.loading).OnClick(func() {
							m.statsDir = path.Dir(m.statsDir)
							if m.statsDir == "." {
								m.statsDir = ""
							}
							a.reloadAdmin()
						})
					})
					ui.Text(c, "/"+m.statsDir).SingleLine().FontSize(12).TextColor(c.Theme().TextMuted)
					var total int64
					for _, d := range m.dirs {
						total += d.Size
					}
					ui.List(c.Key("stats-dirs"), &m.dirList, len(m.dirs)).Height(200).Label("文件夹占用列表").Rows(func(row ui.ListRow) {
						dir := m.dirs[row.Index]
						label := dir.Name
						var action func()
						if dir.IsDir {
							action = func() { m.statsDir = dir.Path; a.reloadAdmin() }
						} else {
							label = "本层文件"
						}
						a.usageBar(c, label, dir.Size, total, dir.Files, action)
					})
				}
			})
			ui.Column(c).Grow(1).MinWidth(250).Padding(14).Border(1, c.Theme().Border).Radius(8).Gap(8).Children(func() {
				ui.Text(c, "文件类型").Bold()
				for _, b := range ov.Categories {
					a.usageBar(c, categoryLabel(b.Key), b.Size, ov.Size, b.Count, nil)
				}
			})
		})
		ui.Row(c).Gap(12).Wrap().AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Grow(1).MinWidth(250).Padding(14).Border(1, c.Theme().Border).Radius(8).Gap(8).Children(func() {
				ui.Text(c, "扩展名 Top 15").Bold()
				for _, b := range ov.Exts {
					a.usageBar(c, b.Key, b.Size, ov.Size, b.Count, nil)
				}
			})
			ui.Column(c).Grow(1).MinWidth(250).Padding(14).Border(1, c.Theme().Border).Radius(8).Gap(8).Children(func() {
				ui.Text(c, "按修改年份").Bold()
				for _, b := range ov.Years {
					a.usageBar(c, b.Key, b.Size, ov.Size, b.Count, nil)
				}
			})
		})
		ui.Text(c, "最大的 50 个文件（双击预览或用系统程序打开）").Bold()
		ui.Table(c.Key("largest-files"), &m.largestList, []ui.TableColumn{{Title: "名称"}, {Title: "位置", Width: 250}, {Title: "大小", Width: 100}}, len(m.largest), func(row, col int) {
			e := m.largest[row]
			switch col {
			case 0:
				ui.Text(c, e.Name).SingleLine()
			case 1:
				ui.Button(c.Key(fmt.Sprintf("largest-location/%d/%s", e.Root, e.Path)), e.RootName+"/"+e.Parent).OnClick(func() { a.locateManagedEntry(m.server, e) })
			case 2:
				ui.Text(c, formatBytes(e.Size)).SingleLine()
			}
		}).Height(360).Label("最大文件列表").OnSubmit(func() {
			i := m.largestCursor
			if i >= 0 && i < len(m.largest) {
				a.openManagedEntry(m.server, m.largest[i], m.largest)
			}
		})
	})
}
func (a *app) usageBar(c *ui.Context, label string, size, total, count int64, action func()) {
	ui.Column(c).Gap(4).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			if action != nil {
				ui.Button(c.Key(label), label).Grow(1).Shrink(1).MinWidth(0).OnClick(action)
			} else {
				ui.Text(c, label).Grow(1).Shrink(1).MinWidth(0).SingleLine()
			}
			ui.Text(c, formatBytes(size)).FontSize(12)
		})
		fraction := float64(0)
		if total > 0 {
			fraction = min(float64(size)/float64(total), 1)
		}
		ui.Progress(c, fraction).Label(label + "占用")
		ui.Text(c, fmt.Sprintf("%d 个文件 · %.1f%%", count, fraction*100)).FontSize(11).TextColor(c.Theme().TextMuted)
	})
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
	case "code":
		return "代码"
	case "other":
		return "其他"
	}
	return strings.TrimSpace(key)
}
