package main

import (
	"fmt"
	"path/filepath"

	"filesync/fileindex"

	"github.com/egoist/mygo/ui"
)

// The details panel, right of the pages: what is chosen, or the folder
// shown when nothing is, with its picture and its properties.

// detailSubject is what the details show.
type detailSubject struct {
	// source is the server of the entries, localPlace for this computer,
	// "" for what has no properties to read (as a member of an archive).
	source string
	es     []fileindex.Entry
	// folder says the entry is the folder shown, nothing being chosen.
	folder bool
}

// key names the subject, to read its properties again as it changes.
func (s detailSubject) key() string {
	if len(s.es) != 1 {
		return ""
	}
	e := s.es[0]
	return fmt.Sprintf("%s\x00%d\x00%s\x00%d\x00%d", s.source, e.Root, e.Path, e.MTime, e.Size)
}

// detailSubject is what the page shown has chosen, or the folder it shows.
func (a *app) detailSubject() detailSubject {
	switch {
	case a.onRoot():
		b := a.br
		if es := b.chosen(); len(es) > 0 {
			return detailSubject{source: b.server, es: es}
		}
		name := pathBase(b.path)
		if b.path == "" {
			name = b.root.Name
		}
		e := fileindex.Entry{Root: b.root.ID, RootName: b.root.Name, Name: name, Path: b.path, Parent: parentDir(b.path), IsDir: true}
		return detailSubject{source: b.server, es: []fileindex.Entry{e}, folder: true}
	case a.onLocal():
		b := a.local
		if es := b.chosen(); len(es) > 0 {
			return detailSubject{source: localPlace, es: es}
		}
		e := fileindex.Entry{Name: filepath.Base(b.dir), Path: b.dir, IsDir: true}
		return detailSubject{source: localPlace, es: []fileindex.Entry{e}, folder: true}
	case a.onSearch():
		s := a.gsearch
		var out detailSubject
		for _, h := range s.hits {
			if s.sel.Has(h.key()) {
				if len(out.es) > 0 && out.source != h.source {
					out.source = "" // of several places
				} else if len(out.es) == 0 {
					out.source = h.source
				}
				out.es = append(out.es, h.e)
			}
		}
		return out
	case a.onArchive():
		return detailSubject{es: a.arc.chosen()}
	}
	return detailSubject{}
}

// detailsPane is the details panel, with its head.
func (a *app) detailsPane(c *ui.Context) {
	t := c.Theme()
	ui.Column(c.Key("details")).Fill().MinWidth(0).Children(func() {
		right := float32(8)
		if !a.ownControls {
			right += c.TitleBar().Right
		}
		a.headBar(c, "details-head").Padding(0, right, 0, 16).Children(func() {
			ui.Text(c, "详情").FontSize(13).Bold().TextColor(t.TextMuted).SingleLine()
			ui.Spacer(c)
			flatIcon(c, "toggle-details", iconDetails, "隐藏详情", "隐藏详情面板（Ctrl+I）", false, a.toggleDetails)
			if a.ownControls {
				a.windowControls(c)
			}
		})
		ui.Scroll(c.Key("details-scroll")).Grow(1).MinHeight(0).Padding(16).Gap(14).Children(func() {
			a.detailsBody(c, a.detailSubject())
		})
	})
}

func (a *app) detailsBody(c *ui.Context, s detailSubject) {
	t := c.Theme()
	switch len(s.es) {
	case 0:
		a.closeDetail()
		msg := "选择文件或文件夹查看详情"
		if !a.onRoot() && !a.onLocal() && !a.onSearch() && !a.onArchive() {
			msg = "打开文件夹后，这里显示所选项目的详情"
		}
		ui.Column(c).Grow(1).Center().Padding(40, 8).Gap(8).Children(func() {
			ui.Icon(c, iconDetails).FontSize(28).TextColor(t.TextMuted.Alpha(0.5))
			ui.Text(c, msg).FontSize(12).TextColor(t.TextMuted).TextAlign(ui.Center)
		})
		return
	case 1:
	default:
		a.closeDetail()
		a.detailsMany(c, s)
		return
	}
	e := s.es[0]
	// The picture, or the icon of its kind.
	ui.Column(c.Key("details-picture")).Height(150).Center().Radius(t.Radius * 2).Background(t.Surface).Clip().Children(func() {
		var bmp *ui.Bitmap
		switch {
		case e.IsDir:
		case s.source == localPlace:
			bmp = a.localThumb(e)
		case s.source != "":
			bmp = a.serverThumb(s.source, e)
		}
		if bmp != nil {
			ui.Image(c, bmp).Size(240, 140).Fit(ui.Contain).Radius(4)
		} else {
			fileIcon(c, e, 56)
		}
	})
	ui.Column(c).Gap(2).Children(func() {
		ui.Text(c, breakable(e.Name)).FontSize(15).Bold().MaxLines(3).Selectable().Label(e.Name)
		kind := kindOf(e.IsDir, e.Link, e.Name)
		if s.folder {
			kind = "当前文件夹"
		}
		ui.Text(c, kind).FontSize(12).TextColor(t.TextMuted)
	})
	a.detailActions(c, s)
	ui.Divider(c)
	d := a.detailFor(s)
	if d == nil {
		return
	}
	if d.note != "" {
		ui.Text(c, d.note).FontSize(12).TextColor(t.TextMuted)
	}
	if d.err != "" {
		ui.Text(c, d.err).FontSize(12).TextColor(t.Danger).Selectable()
	}
	ui.Column(c.Key("details-rows")).Gap(8).Children(func() {
		// A folder of this computer: what it holds is counted when asked,
		// which may take long.
		if s.source == localPlace && e.IsDir && !d.counting {
			ui.Column(c.Key("detail-size")).Gap(1).Children(func() {
				ui.Text(c, "大小").FontSize(11).TextColor(t.TextMuted)
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "未统计").FontSize(13).TextColor(t.TextMuted)
					flatIcon(c, "details-count", iconCount, "统计大小", "统计文件夹大小和文件数（在本机索引范围内时用索引）", false, func() {
						key := a.detailKey
						a.closeDetail()
						a.detailProps, a.detailKey = a.localProps(e.Path, true), key
					})
				})
			})
		}
		for i, row := range d.rows() {
			if row[0] == "类型" {
				continue // under the name
			}
			ui.Column(c.Key(fmt.Sprint("detail-", i))).Gap(1).Children(func() {
				ui.Text(c, row[0]).FontSize(11).TextColor(t.TextMuted)
				ui.Text(c, row[1]).FontSize(13).Selectable()
			})
		}
	})
	if d.loading {
		ui.Spinner(c).FontSize(12).Label("正在读取")
	}
}

// detailsMany sums up several entries chosen.
func (a *app) detailsMany(c *ui.Context, s detailSubject) {
	t := c.Theme()
	files, dirs := 0, 0
	var size int64
	for _, e := range s.es {
		if e.IsDir {
			dirs++
		} else {
			files++
			size += e.Size
		}
	}
	ui.Column(c).Height(110).Center().Radius(t.Radius * 2).Background(t.Surface).Children(func() {
		ui.Icon(c, iconCopy).FontSize(44).TextColor(t.TextMuted)
	})
	ui.Text(c, fmt.Sprintf("已选择 %d 项", len(s.es))).FontSize(15).Bold()
	ui.Divider(c)
	row := func(label, value string) {
		ui.Column(c).Gap(1).Children(func() {
			ui.Text(c, label).FontSize(11).TextColor(t.TextMuted)
			ui.Text(c, value).FontSize(13)
		})
	}
	row("包含", fmt.Sprintf("%d 个文件，%d 个文件夹", files, dirs))
	if files > 0 {
		label := "文件大小"
		if dirs > 0 {
			label = "文件大小（不含文件夹）"
		}
		row(label, sizeWithBytes(size))
	}
}

// detailActions are the buttons under the name: opening it, and its
// properties in a dialog of their own.
func (a *app) detailActions(c *ui.Context, s detailSubject) {
	e := s.es[0]
	if s.folder {
		return
	}
	ui.Row(c.Key("details-actions")).Gap(6).Wrap().Children(func() {
		ui.Button(c.Key("details-open"), "打开").OnClick(func() {
			switch {
			case a.onRoot():
				a.openEntry(e)
			case a.onLocal():
				a.openLocalEntry(e)
			case a.onSearch():
				if h, ok := a.hitOf(e); ok {
					a.openHit(h, false)
				}
			case a.onArchive():
				a.archiveOpen(e)
			}
		})
		if a.onSearch() {
			if h, ok := a.hitOf(e); ok {
				ui.Button(c.Key("details-reveal"), "打开所在位置").OnClick(func() { a.openHit(h, true) })
			}
		}
	})
}

// detailFor reads the properties of the subject, once for each: again
// only as it changes.
func (a *app) detailFor(s detailSubject) *propsDialog {
	key := s.key()
	if a.detailProps != nil && a.detailKey == key {
		return a.detailProps
	}
	a.closeDetail()
	e := s.es[0]
	switch {
	case s.source == "":
		d := &propsDialog{name: e.Name}
		d.rows = func() [][2]string {
			var r propRows
			if !e.IsDir {
				r.add("大小", sizeWithBytes(e.Size))
			}
			r.add("修改时间", fullTime(e.MTime))
			r.add("位置", "压缩包内 · "+pathOr(e.Parent, "/"))
			return r
		}
		a.detailProps = d
	case s.source == localPlace:
		a.detailProps = a.localProps(e.Path, false)
	default:
		a.detailProps = a.serverProps(s.source, e)
	}
	a.detailKey = key
	return a.detailProps
}

// closeDetail stops reading the properties shown in the details.
func (a *app) closeDetail() {
	if a.detailProps != nil && a.detailProps.cancel != nil {
		a.detailProps.cancel()
	}
	a.detailProps, a.detailKey = nil, ""
}

func pathOr(p, or string) string {
	if p == "" {
		return or
	}
	return p
}
