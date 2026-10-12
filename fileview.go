package main

import (
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strings"

	"filesync/fileindex"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// fileView is what a page of files shows them with: the files of this
// computer and of a server, listed or found, look and act alike. The page
// keeps the state, and says what its files do.
type fileView struct {
	key     string // of the table and the grid
	label   string // for assistive technology
	items   []fileindex.Entry
	sel     *ui.Selection[string]
	cursor  *int
	list    *ui.ListState
	gridSt  *ui.GridState
	grid    bool
	sort    *tableSort
	loading bool // reading or searching, with nothing to show yet
	results bool // search results, which show where each is
	// where is the folder of a result, as shown.
	where func(e fileindex.Entry) string
	// thumb is a picture of a file, nil while it comes or for none.
	thumb func(e fileindex.Entry) *ui.Bitmap
	open  func(e fileindex.Entry)
	// resort puts the items in the order the sort asks, as it changed.
	resort func()
	// visible tells the rows in view, to load more results; gridLast keeps
	// the last item the grid built, which is in view or near.
	visible  func(first, last int)
	gridLast *int
	// empty is what shows without items, once read.
	empty func()
}

// fileList shows the files of a page in a table or a grid, in a bordered
// zone, which it returns for the page's menu and the files dropped on it.
func (a *app) fileList(c *ui.Context, v *fileView) ui.Element {
	t := c.Theme()
	return ui.Column(c).Grow(1).MinHeight(0).Radius(t.Radius*2).Border(1, t.Border).Clip().Children(func() {
		if v.loading && len(v.items) == 0 {
			ui.Column(c).Grow(1).Center().Children(func() { ui.Spinner(c).FontSize(22).Label("正在读取") })
			return
		}
		if v.grid {
			a.fileGrid(c, v)
		} else {
			a.fileTable(c, v)
		}
	})
}

func (a *app) fileTable(c *ui.Context, v *fileView) {
	t := c.Theme()
	if v.sort.stale() {
		v.resort()
	}
	cols := fileColumns()
	if v.results {
		cols = append(cols, ui.TableColumn{Title: colWhere, Width: 220, Sortable: true})
	}
	table := ui.Table(c.Key(v.key), v.list, cols, len(v.items), func(row, col int) {
		e := v.items[row]
		muted := t.TextMuted
		if k, ok := v.list.Key(row).(string); ok && v.sel.Has(k) {
			muted = t.AccentText.Alpha(0.85)
		}
		switch col {
		case 0:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				fileIcon(c, e, 16)
				ui.Text(c, e.Name).SingleLine().Grow(1).Shrink(1)
				if e.Encrypted {
					ui.Badge(c, "已加密")
				} else if e.Link {
					ui.Badge(c, "符号链接")
				}
			})
		case 1:
			if !e.IsDir {
				ui.Text(c, formatBytes(e.Size)).FontFeatures("tnum").TextColor(muted)
			}
		case 2:
			ui.Text(c, e.ModTime().Format("2006-01-02 15:04")).FontFeatures("tnum").TextColor(muted)
		case 3:
			where := v.where(e)
			ui.Text(c, where).SingleLine().Tooltip(where).TextColor(muted)
		}
	}).Grow(1).Label(v.label)
	table.Children(func() {
		if !v.loading && len(v.items) == 0 {
			v.empty()
		}
	})
	if table.Submitted() && *v.cursor >= 0 && *v.cursor < len(v.items) {
		v.open(v.items[*v.cursor])
	}
	if v.visible != nil {
		v.visible(v.list.Visible())
	}
}

func (a *app) fileGrid(c *ui.Context, v *fileView) {
	last := -1
	grid := ui.GridView(c.Key(v.key+"-grid"), v.gridSt, len(v.items), gridCell, 132, func(i int) {
		e := v.items[i]
		last = max(last, i)
		ui.Column(c).Grow(1).Padding(6).Gap(4).AlignItems(ui.Center).Children(func() {
			if bmp := v.thumb(e); bmp != nil {
				ui.Image(c, bmp).Size(96, 84).Fit(ui.Contain).Radius(4)
			} else {
				ui.Column(c).Size(96, 84).Center().Children(func() { fileIcon(c, e, 48) })
			}
			ui.Text(c, breakable(e.Name)).FontSize(12).MaxLines(2).TextAlign(ui.Center).
				Width(gridCell - 12).MinWidth(0).Clip().Label(e.Name)
			if e.Encrypted {
				ui.Badge(c, "已加密")
			} else if e.Link {
				ui.Badge(c, "符号链接")
			}
		}).Tooltip(e.Name)
	}).Grow(1).Padding(8).Label(v.label)
	if grid.Submitted() && *v.cursor >= 0 && *v.cursor < len(v.items) {
		v.open(v.items[*v.cursor])
	}
	grid.Children(func() {
		if !v.loading && len(v.items) == 0 {
			v.empty()
		}
	})
	if last >= 0 && v.gridLast != nil {
		*v.gridLast = last
	}
	if v.visible != nil && v.gridLast != nil {
		v.visible(0, *v.gridLast)
	}
}

// filesSize is the size of the files among es.
func filesSize(es []fileindex.Entry) int64 {
	var size int64
	for _, e := range es {
		if !e.IsDir {
			size += e.Size
		}
	}
	return size
}

// gridCell is the width of a file in the grid.
const gridCell = 120

// breakable lets a name wrap where it has no spaces: after the marks that
// part its words, and inside runs of letters too long for a line, with
// zero-width spaces, which do not show.
func breakable(name string) string {
	const run = 14 // runes without a break, about a line of the grid
	var b strings.Builder
	n := 0
	for _, r := range name {
		b.WriteRune(r)
		n++
		switch {
		case r == ' ' || r > 0x2E80: // a space, or CJK, which breaks anyway
			n = 0
		case r == '_' || r == '-' || r == '.' || r == ',' || r == '/' || n >= run:
			b.WriteRune('\u200b')
			n = 0
		}
	}
	return b.String()
}

// emptyFolder is the empty state of a folder.
func emptyFolder(c *ui.Context, hint string) {
	emptyState(c, iconFolder, "空文件夹", hint, nil)
}

// commandBar is the bar over the files: the page's commands, or, with
// files chosen, how many, their size, and what to do with them, nil for
// nothing but the menu of the files; then, at its end, the
// view's switch and menu. It keeps its height as it changes, so that the
// files stay in place.
func commandBar(c *ui.Context, n int, size int64, clear func(), commands, actions, end func()) {
	t := c.Theme()
	ui.Row(c).Gap(6).AlignItems(ui.Center).Height(38).Children(func() {
		if n > 0 {
			ui.Row(c.Key("selection-bar")).Gap(4).AlignItems(ui.Center).Height(38).Padding(0, 4, 0, 2).Radius(t.Radius*1.5).
				Background(t.Accent.Alpha(0.08)).Border(1, t.Accent.Alpha(0.25)).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				iconButton(c, "clear-selection", iconClose, "取消选择", "取消选择（Esc）", false, clear)
				text := fmt.Sprintf("已选 %d 项", n)
				if size > 0 {
					text += " · " + formatBytes(size)
				}
				ui.Text(c, text).FontSize(13).SingleLine().FontFeatures("tnum").Padding(0, 6, 0, 2)
				if actions != nil {
					ui.Divider(c).Height(18).Width(1)
					// What does not fit goes into the toolbar's menu.
					ui.Toolbar(c, actions).Grow(1).Shrink(1).MinWidth(0).Label("选中项的操作")
				} else {
					ui.Spacer(c)
				}
			})
		} else {
			commands()
			ui.Spacer(c)
		}
		end()
	})
}

// viewToggle switches the files between a table and a grid.
func viewToggle(c *ui.Context, key string, grid *bool) {
	if *grid {
		iconButton(c, key, iconList, "列表视图", "列表视图", false, func() { *grid = false })
	} else {
		iconButton(c, key, iconGrid, "网格视图", "网格视图（显示缩略图）", false, func() { *grid = true })
	}
}

// moreMenu is the button of the menu of the less used commands of a page.
func moreMenu(c *ui.Context, key string, build func(m *ui.Menu)) {
	iconButton(c, key, iconMore, "更多", "更多选项", false, func() {}).Menu(build)
}

// thumbCached returns the thumbnail under key, getting it with fetch, off
// the main thread, when it shows first: nil while it comes, and for none.
// The cache keeps the 256 used last.
func (a *app) thumbCached(key string, parent context.Context, fetch func(ctx context.Context) (*ui.Bitmap, error)) *ui.Bitmap {
	a.thumbSeq++
	if th := a.thumbs[key]; th != nil {
		th.used = a.thumbSeq
		return th.bmp
	}
	if len(a.thumbs) >= 256 {
		oldest := ""
		for k, v := range a.thumbs {
			if oldest == "" || v.used < a.thumbs[oldest].used {
				oldest = k
			}
		}
		if old := a.thumbs[oldest]; old != nil && old.cancel != nil {
			old.cancel()
		}
		delete(a.thumbs, oldest)
	}
	th := &thumbOf{loading: true, used: a.thumbSeq}
	a.thumbs[key] = th
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, requestTimeout)
	th.cancel = cancel
	go func() {
		defer cancel()
		select {
		case a.thumbSlots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-a.thumbSlots }()
		bmp, _ := fetch(ctx)
		a.post(func() {
			if !a.closed {
				th.bmp, th.loading = bmp, false
			}
		})
	}()
	return nil
}

// localThumbExts are the pictures of this computer shown as thumbnails.
var localThumbExts = map[string]bool{"jpg": true, "jpeg": true, "png": true, "gif": true, "webp": true}

// localThumbLimit is the largest picture decoded for a thumbnail.
const localThumbLimit = 64 << 20

// localThumb is the thumbnail of a picture of this computer.
func (a *app) localThumb(e fileindex.Entry) *ui.Bitmap {
	if e.IsDir || !localThumbExts[e.Ext] || e.Size > localThumbLimit {
		return nil
	}
	key := fmt.Sprintf("local/%s/%d/%d", e.Path, e.MTime, e.Size)
	var parent context.Context
	if a.local != nil {
		parent = a.local.thumbCtx
	}
	return a.thumbCached(key, parent, func(ctx context.Context) (*ui.Bitmap, error) {
		f, err := os.Open(e.Path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		img, _, err := image.Decode(f)
		if err != nil || ctx.Err() != nil {
			return nil, err
		}
		return ui.NewBitmap(scaleDown(img, 192, 168)), nil
	})
}

// scaleDown fits a picture in w×h, no larger than it is.
func scaleDown(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	if b.Dx() <= w && b.Dy() <= h {
		return img
	}
	s := min(float64(w)/float64(b.Dx()), float64(h)/float64(b.Dy()))
	dst := image.NewRGBA(image.Rect(0, 0, max(1, int(float64(b.Dx())*s)), max(1, int(float64(b.Dy())*s))))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}
