package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"filemanager/remoteclient"
	"github.com/egoist/mygo/ui"
)

const textPreviewLimit = 256 << 10
const imagePreviewLimit = 32 << 20

type preview struct {
	open, loading, actual bool
	server                string
	client                *fileindex.Client
	entry                 fileindex.Entry
	group                 []fileindex.Entry
	text, err, note       string
	bitmap                *ui.Bitmap
	cancel                context.CancelFunc
	zoom                  float32
	// placeholder is the picture shown before, faded until this loads.
	placeholder *ui.Bitmap
	// pan is where the picture's center is from the stage's, which a drag
	// moves and throws; the stage's size is the last frame's.
	pan            ui.Point
	stageW, stageH float32
	// swipe is the step a swipe of the picture fitting asks, 0 for none.
	swipe       int
	stripScroll float32 // the wheel turned over the strip, to a step
	// doc is the URL the document shows at in the web view, "" for none;
	// source shows a web page's text instead.
	doc    string
	source bool
	// sibs is what siblings found, made once: the group does not change.
	sibs   []fileindex.Entry
	sibsAt int
	sibsOK bool
}

func previewKind(e fileindex.Entry) string {
	if e.IsDir {
		return ""
	}
	ext := strings.ToLower(path.Ext(e.Name))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp":
		return "image"
	case ".pdf":
		return "pdf"
	case ".html", ".htm", ".xhtml":
		return "html"
	case ".txt", ".md", ".log", ".csv", ".tsv", ".json", ".jsonl", ".yaml", ".yml", ".toml", ".ini", ".conf", ".cfg", ".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".vue", ".css", ".xml", ".svg", ".sh", ".sql", ".rs", ".c", ".h", ".cpp", ".java", ".srt", ".vtt":
		return "text"
	}
	if !strings.Contains(e.Name, ".") || strings.HasPrefix(e.Name, ".") && strings.Count(e.Name, ".") == 1 {
		return "text"
	}
	return ""
}
func mediaFile(e fileindex.Entry) bool {
	switch strings.ToLower(path.Ext(e.Name)) {
	case ".mp4", ".mkv", ".avi", ".mov", ".webm", ".m4v", ".mpeg", ".mpg", ".mts", ".m2ts", ".wmv", ".flv", ".mp3", ".flac", ".wav", ".ogg", ".opus", ".m4a", ".aac", ".wma", ".aiff":
		return true
	}
	return false
}
func (a *app) sourceCurrent(server string, c *fileindex.Client) bool {
	if c != nil && c.InArchive() && c.ArchiveSource.Local {
		return !a.closed && server == localPlace
	}
	sv := a.store.Server(server)
	return !a.closed && sv != nil && sv.Addr == c.Addr && sv.Token == c.Token && sv.Token != ""
}
func (a *app) closePreview() {
	if p := a.preview; p != nil {
		p.cancel()
		if p.doc != "" {
			a.hideDoc()
		}
		a.preview = nil
	}
}
func (a *app) showPreview(server string, c *fileindex.Client, e fileindex.Entry, group []fileindex.Entry) {
	a.closePreview()
	ctx, cancel := context.WithCancel(context.Background())
	if !c.InArchive() {
		cancel()
		ctx, cancel = context.WithTimeout(context.Background(), requestTimeout)
	}
	p := &preview{open: true, loading: true, server: server, client: c, entry: e, group: group, cancel: cancel, zoom: 1}
	a.preview = p
	kind := previewKind(e)
	go func() {
		defer cancel()
		var bitmap *ui.Bitmap
		var text, note string
		var err error
		limit, tooLarge := int64(textPreviewLimit), ""
		switch kind {
		case "image":
			limit, tooLarge = imagePreviewLimit, "图片超过 32 MB，请用系统程序打开"
		case "pdf":
			limit, tooLarge = imagePreviewLimit, "PDF 超过 32 MB，请用系统程序打开"
		case "html":
			limit, tooLarge = htmlPreviewLimit, "网页超过 4 MB，请用系统程序打开"
		}
		var data []byte
		var truncated bool
		if tooLarge != "" && e.Size > limit {
			err = errors.New(tooLarge)
		} else {
			data, truncated, err = c.ReadPreview(ctx, e.Root, e.Path, limit)
		}
		if err == nil && truncated && tooLarge != "" {
			err = errors.New(tooLarge)
		}
		if err == nil {
			switch kind {
			case "pdf":
				note = "PDF · " + formatBytes(e.Size)
			case "html":
				text, err = decodePreviewText(data, false)
				data = []byte(text)
				note = "网页 · 不运行脚本，不加载外部资源"
			case "image":
				var cfg image.Config
				cfg, _, err = image.DecodeConfig(bytes.NewReader(data))
				if err == nil && (cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 16_000_000) {
					err = fmt.Errorf("图片超过 1600 万像素，请用系统程序打开")
				}
				if err == nil {
					bitmap, err = ui.DecodeBitmap(data)
				}
				if err == nil {
					w, h := bitmap.Size()
					note = fmt.Sprintf("%d × %d · %s", w, h, formatBytes(e.Size))
					if strings.EqualFold(path.Ext(e.Name), ".gif") {
						note += " · GIF 显示首帧"
					}
				}
			default:
				text, err = decodePreviewText(data, truncated)
				note = "UTF-8 / UTF-16 · 只读，可选择复制"
				if truncated {
					note += " · 仅显示前 256 KiB，完整内容请用系统程序打开"
				}
			}
		}
		a.post(func() {
			if a.preview != p || !p.open || !a.sourceCurrent(server, c) {
				return
			}
			p.loading = false
			p.bitmap = bitmap
			p.text = text
			p.note = note
			if err == nil && (kind == "pdf" || kind == "html") {
				p.doc = a.docs.put(kind, data)
				a.showDoc(p.doc)
			}
			if err != nil {
				p.err = err.Error()
				if err == fileindex.ErrAuth {
					a.signedOut(a.store.Server(server))
				}
			}
		})
	}()
}
func decodePreviewText(data []byte, truncated bool) (string, error) {
	if len(data) >= 2 && (bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff})) {
		var order binary.ByteOrder = binary.LittleEndian
		if data[0] == 0xfe {
			order = binary.BigEndian
		}
		data = data[2:]
		if len(data)%2 != 0 && !truncated {
			return "", fmt.Errorf("UTF-16 文本不完整")
		}
		units := make([]uint16, len(data)/2)
		for i := range units {
			units[i] = order.Uint16(data[2*i:])
		}
		if truncated && len(units) > 0 && units[len(units)-1] >= 0xd800 && units[len(units)-1] <= 0xdbff {
			units = units[:len(units)-1]
		}
		return string(utf16.Decode(units)), nil
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if truncated {
		for i := 0; i < 3 && len(data) > 0 && !utf8.Valid(data); i++ {
			data = data[:len(data)-1]
		}
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return "", fmt.Errorf("这不是 UTF-8/UTF-16 文本，请用系统程序打开")
	}
	return string(data), nil
}

// siblings are the entries of the group the arrows step through: those
// shown as the entry is, pictures among pictures.
func (p *preview) siblings() (list []fileindex.Entry, at int) {
	if p.sibsOK {
		return p.sibs, p.sibsAt
	}
	defer func() { p.sibs, p.sibsAt, p.sibsOK = list, at, true }()
	kind := previewKind(p.entry)
	at = -1
	for _, e := range p.group {
		if previewKind(e) != kind {
			continue
		}
		if e.Path == p.entry.Path && e.Root == p.entry.Root {
			at = len(list)
		}
		list = append(list, e)
	}
	return list, at
}

// previewStep shows the sibling delta away, keeping the picture shown
// until the next has loaded.
func (a *app) previewStep(delta int) {
	a.previewGo(func(list []fileindex.Entry, at int) int { return at + delta })
}

// previewGo shows the sibling to returns, if any.
func (a *app) previewGo(to func(list []fileindex.Entry, at int) int) {
	p := a.preview
	if p == nil {
		return
	}
	list, at := p.siblings()
	j := to(list, at)
	if at < 0 || j < 0 || j >= len(list) || j == at {
		return
	}
	old := p.bitmap
	if old == nil {
		old = p.placeholder
	}
	a.showPreview(p.server, p.client, list[j], p.group)
	a.preview.placeholder = old // the next fits the window, as pictures differ
}

// viewerCovers tells whether the picture viewer covers the window.
func (a *app) viewerCovers() bool {
	p := a.preview
	return p != nil && p.open && previewKind(p.entry) == "image"
}

func (a *app) filePreviewView(c *ui.Context) {
	p := a.preview
	if p == nil {
		return
	}
	if previewKind(p.entry) == "image" {
		a.imageViewer(c, p)
	} else {
		a.textViewer(c, p)
	}
	if !p.open && a.preview == p {
		a.closePreview()
	}
}

// The look of the picture viewer, dark whatever the theme.
var (
	viewerText  = ui.RGBA(250, 250, 250, 1)
	viewerMuted = ui.RGBA(161, 161, 170, 1)
	viewerHover = ui.RGBA(255, 255, 255, 0.12)
)

// imageViewer shows a picture over the whole window, with the others of
// its folder below it.
func (a *app) imageViewer(c *ui.Context, p *preview) {
	list, at := p.siblings()
	ui.DialogBase(c.Key("native-preview"), &p.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(9, 9, 11, 1))
		w, h := c.Size()
		panel.Width(w).Height(h)
		a.viewerKeys(c, p)

		// The bar: what, then what can be done; it moves the window, as
		// its title bar, clear of the window controls.
		top := ui.Row(c).Padding(10, 12+c.TitleBar().Right, 10, 20+c.TitleBar().Left).MinHeight(c.TitleBar().Height).Gap(4).AlignItems(ui.Center)
		if a.hiddenTitleBar {
			top.DragWindow()
		}
		top.Children(func() {
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, p.entry.Name).FontSize(15).Bold().TextColor(viewerText).SingleLine().Selectable()
				meta := []string{}
				if len(list) > 1 && at >= 0 {
					meta = append(meta, fmt.Sprintf("%d / %d", at+1, len(list)))
				}
				if p.bitmap != nil {
					bw, bh := p.bitmap.Size()
					meta = append(meta, fmt.Sprintf("%d × %d", bw, bh))
				}
				meta = append(meta, formatBytes(p.entry.Size), p.entry.ModTime().Format("2006-01-02 15:04"))
				if strings.EqualFold(path.Ext(p.entry.Name), ".gif") {
					meta = append(meta, "GIF 显示首帧")
				}
				ui.Text(c, strings.Join(meta, " · ")).FontSize(12).TextColor(viewerMuted).SingleLine()
			})
			ready := p.bitmap != nil
			viewerButton(c, "zoom-out", iconZoomOut, "缩小", !ready || !p.actual, true, func() { p.zoomBy(1 / 1.25) })
			zoom := "适合"
			if p.actual {
				zoom = fmt.Sprintf("%.0f%%", p.zoom*100)
			}
			ui.Text(c, zoom).FontSize(12).TextColor(viewerMuted).Width(44).TextAlign(ui.Center).FontFeatures("tnum")
			viewerButton(c, "zoom-in", iconZoomIn, "放大", !ready, true, func() { p.zoomBy(1.25) })
			if p.actual {
				viewerButton(c, "fit", iconFit, "适合窗口", !ready, true, p.fit)
			} else {
				viewerButton(c, "actual", iconActual, "原始大小", !ready, true, p.actualSize)
			}
			ui.Column(c).Width(1).Height(20).Background(ui.RGBA(255, 255, 255, 0.16)).Margin(0, 6)
			viewerButton(c, "open-out", iconOpenOut, "系统程序打开", false, true, func() {
				a.closePreview()
				a.systemOpenSource(p.server, p.client, p.entry, false)
			})
			viewerButton(c, "close", iconClose, "关闭预览", false, true, a.closePreview)
		})

		// The picture, between the arrows.
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Width(76).Center().Children(func() {
				if len(list) > 1 {
					viewerArrow(c, "prev", iconPrev, "上一张", at <= 0, func() { a.previewStep(-1) })
				}
			})
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).MinHeight(0).Center().Children(func() {
				switch bmp := p.bitmap; {
				case p.err != "":
					ui.Column(c).Gap(12).Padding(24).MaxWidth(460).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, iconImage).FontSize(44).TextColor(viewerMuted)
						ui.Text(c, p.err).TextColor(viewerText).TextAlign(ui.Center).MaxLines(4)
						ui.Button(c.Key("viewer-open"), "用系统程序打开").OnClick(func() {
							a.closePreview()
							a.systemOpenSource(p.server, p.client, p.entry, false)
						})
					})
				case bmp == nil && p.placeholder != nil:
					// The last picture, faded, until this one loads.
					ui.Image(c, p.placeholder).Grow(1).Fill().MinHeight(0).Fit(ui.ScaleDown).Opacity(0.35)
				case bmp == nil:
					ui.Spinner(c).FontSize(26).TextColor(viewerMuted).Label("正在加载预览")
				default:
					a.imageStage(c, p, bmp)
				}
			})
			ui.Column(c).Width(76).Center().Children(func() {
				if len(list) > 1 {
					viewerArrow(c, "next", iconNext, "下一张", at < 0 || at >= len(list)-1, func() { a.previewStep(1) })
				}
			})
		})

		// The others of the folder, around the one shown.
		if len(list) > 1 {
			from := max(0, min(at-6, len(list)-13))
			to := min(len(list), from+13)
			strip := ui.Row(c.Key("film-strip")).Padding(8, 16, 16, 16).Gap(8).Justify(ui.Center)
			// The wheel over the strip steps through the pictures.
			strip.HandleInput(func(ev ui.InputEvent) bool {
				if ev.Kind != ui.InputScroll {
					return false
				}
				d := ev.DY
				if d == 0 {
					d = ev.DX
				}
				step := 0
				if !ev.Precise {
					step = int(math.Copysign(1, float64(d)))
				} else if p.stripScroll += d; math.Abs(float64(p.stripScroll)) >= 40 {
					step = int(math.Copysign(1, float64(p.stripScroll)))
					p.stripScroll = 0
				}
				if d != 0 && step != 0 {
					a.previewStep(step)
				}
				return true
			})
			strip.Children(func() {
				for i := from; i < to; i++ {
					e := list[i]
					a.filmFrame(c, p, e, i == at, func() { a.previewGo(func([]fileindex.Entry, int) int { return i }) })
				}
			})
		}
	})
}

// A notch of the wheel zooms by wheelZoom, about 7 doubling the size; a
// touchpad by touchpadZoom an e-fold a DIP it scrolls.
const (
	wheelZoom    = 1.1
	touchpadZoom = 0.005
)

// zoomMotion is about how long a zoom takes to show, and a thrown picture
// to come to rest.
var zoomMotion = 280 * time.Millisecond

// swipeStep is how far a swipe of the picture fitting goes, thrown, to
// show the next or the one before.
const swipeStep = 80

// imageStage shows the picture, fitting the stage and centered, or zoomed
// and moved where the wheel and drags put it. The picture is scaled, and
// its holder dragged and thrown, with one spring: zooming about a point
// keeps it under the pointer all the way. Fitting, a swipe steps through
// the pictures.
func (a *app) imageStage(c *ui.Context, p *preview, bmp *ui.Bitmap) {
	stage := ui.Column(c.Key("image-stage")).Grow(1).Fill().MinHeight(0).Clip().HandleInput(p.stageInput)
	resized := false
	if r := stage.Bounds(); r.W > 0 && (r.W != p.stageW || r.H != p.stageH) {
		resized = p.stageW != 0
		p.stageW, p.stageH = r.W, r.H
		p.clampPan() // the window resized
	}
	stage.Children(func() {
		if p.stageW <= 0 {
			// Before the stage has a size, as the picture first shows.
			ui.Image(c, bmp).Grow(1).Fill().MinHeight(0).Fit(ui.ScaleDown).Label("图片 " + p.entry.Name)
			return
		}
		anim := ui.Animation{Spring: ui.SpringOptions{Duration: zoomMotion}}
		bw, bh := bmp.Size()
		scale := p.scale()
		opts := ui.DraggableOptions{Animation: anim}
		if p.actual {
			// Within its edges, thrown, it glides on to rest.
			ex := max(0, (float32(bw)*scale-p.stageW)/2)
			ey := max(0, (float32(bh)*scale-p.stageH)/2)
			opts.Bounds = &ui.Rect{X: -ex, Y: -ey, W: 2 * ex, H: 2 * ey}
			opts.Snap = func(at ui.Point) ui.Point { return at }
		} else {
			opts.Axis = ui.Horizontal
			opts.Snap = func(at ui.Point) ui.Point {
				switch {
				case at.X < -swipeStep:
					p.swipe = 1
				case at.X > swipeStep:
					p.swipe = -1
				}
				return ui.Point{}
			}
		}
		// A picture, and its holder, of its own each: one starts where it
		// is meant to be.
		holder := ui.Box(c.Key("viewer-pan/"+p.entry.Path)).Absolute().Left(0).Top(0).Size(p.stageW, p.stageH).Draggable(&p.pan, opts)
		switch {
		case holder.Dragging():
			holder.Cursor(ui.CursorGrabbing)
		case p.actual:
			holder.Cursor(ui.CursorGrab)
		}
		holder.Children(func() {
			img := ui.Image(c.Key("viewer-image/"+p.entry.Path), bmp).Absolute().
				Left(p.stageW/2-float32(bw)/2).Top(p.stageH/2-float32(bh)/2).Size(float32(bw), float32(bh)).
				Label("图片 " + p.entry.Name)
			if resized {
				scale = img.Jump("scale", scale) // the window: at once
			} else {
				scale = img.AnimateTo("scale", scale, anim)
			}
			img.Scale(scale, scale)
		})
	})
	if step := p.swipe; step != 0 {
		p.swipe = 0
		a.previewStep(step)
	}
}

// filmFrame is a picture of the strip under the viewer.
func (a *app) filmFrame(c *ui.Context, p *preview, e fileindex.Entry, current bool, choose func()) {
	t := c.Theme()
	frame := ui.ButtonBase(c.Key("film/"+e.Path)).Size(64, 48).Radius(6).Clip().Label("缩略图 " + e.Name).Tooltip(e.Name).Background(ui.RGBA(255, 255, 255, 0.06))
	var bmp *ui.Bitmap
	if !p.client.InArchive() && a.br != nil && a.br.server == p.server {
		bmp = a.thumb(e)
	}
	frame.Children(func() {
		if bmp != nil {
			ui.Image(c, bmp).Size(64, 48).Fit(ui.Cover)
		} else {
			ui.Column(c).Size(64, 48).Center().Children(func() { ui.Icon(c, iconImage).FontSize(18).TextColor(viewerMuted) })
		}
	})
	switch {
	case current:
		frame.Border(2, t.Accent)
	case frame.Hovered():
		frame.Border(2, ui.RGBA(255, 255, 255, 0.5))
	default:
		frame.Opacity(0.55)
	}
	frame.OnClick(choose)
}

// viewerKeys are the keys of a preview: the arrows step, + and - zoom, 0
// fits the window.
func (a *app) viewerKeys(c *ui.Context, p *preview) {
	if c.Shortcut(0, ui.KeyLeft) {
		a.previewStep(-1)
	}
	if c.Shortcut(0, ui.KeyRight) {
		a.previewStep(1)
	}
	if p.bitmap == nil {
		return
	}
	if c.Shortcut(0, ui.KeyEqual) || c.Shortcut(ui.Shift, ui.KeyEqual) {
		p.zoomBy(1.25)
	}
	if c.Shortcut(0, ui.KeyMinus) {
		p.zoomBy(1 / 1.25)
	}
	if c.Shortcut(0, ui.Key0) {
		p.fit()
	}
}

// fitScale is the scale the picture fits the stage at, at most its own
// size.
func (p *preview) fitScale() float32 {
	if p.bitmap == nil {
		return 1
	}
	w, h := p.bitmap.Size()
	if w <= 0 || h <= 0 || p.stageW <= 0 || p.stageH <= 0 {
		return 1
	}
	return min(1, p.stageW/float32(w), p.stageH/float32(h))
}

// scale is the picture's scale as shown.
func (p *preview) scale() float32 {
	if p.actual {
		return p.zoom
	}
	return p.fitScale()
}

// zoomBy zooms by a factor about the stage's center.
func (p *preview) zoomBy(f float32) { p.zoomAt(f, 0, 0) }

// zoomAt zooms by a factor about the point x, y from the stage's center,
// which stays where it is. Zoomed out below fitting, the picture fits.
func (p *preview) zoomAt(f, x, y float32) {
	if p.bitmap == nil {
		return
	}
	fit, from := p.fitScale(), p.scale()
	to := min(8, max(0.02, from*f))
	if f < 1 && to <= fit*1.001 {
		p.fit()
		return
	}
	if !p.actual {
		p.pan = ui.Point{}
	}
	// The point of the picture under x, y, from its center.
	u, v := (x-p.pan.X)/from, (y-p.pan.Y)/from
	p.actual, p.zoom = true, to
	p.pan = ui.Point{X: x - u*to, Y: y - v*to}
	p.clampPan()
}

// fit shows the picture fitting the stage.
func (p *preview) fit() { p.actual, p.pan = false, ui.Point{} }

// actualSize shows the picture at its own size, centered.
func (p *preview) actualSize() {
	p.actual, p.zoom, p.pan = true, 1, ui.Point{}
	p.clampPan()
}

// clampPan keeps the picture over the stage: centered across a side it
// does not fill, and not past its edges along one it does.
func (p *preview) clampPan() {
	if p.bitmap == nil {
		return
	}
	w, h := p.bitmap.Size()
	clamp := func(pan, size, room float32) float32 {
		if size <= room {
			return 0
		}
		edge := (size - room) / 2
		return min(edge, max(-edge, pan))
	}
	if !p.actual {
		p.pan = ui.Point{}
		return
	}
	p.pan.X = clamp(p.pan.X, float32(w)*p.zoom, p.stageW)
	p.pan.Y = clamp(p.pan.Y, float32(h)*p.zoom, p.stageH)
}

// stageInput zooms with the wheel about the pointer, and toggles fitting
// and the picture's own size with a double click; drags move the
// picture's holder.
func (p *preview) stageInput(ev ui.InputEvent) bool {
	if p.bitmap == nil {
		return false
	}
	x, y := ev.X-p.stageW/2, ev.Y-p.stageH/2
	switch ev.Kind {
	case ui.InputScroll:
		dy := ev.DY
		if dy == 0 {
			dy = ev.DX
		}
		if dy == 0 {
			return true
		}
		f := float32(wheelZoom)
		if ev.Precise {
			f = float32(math.Exp(float64(-dy) * touchpadZoom)) // smoothly
		} else if dy > 0 {
			f = 1 / f
		}
		p.zoomAt(f, x, y)
		return true
	case ui.InputPointerDown:
		if ev.Button != 0 {
			return false
		}
		if ev.Clicks == 2 {
			if p.actual {
				p.fit()
			} else if fit := p.fitScale(); fit < 1 {
				p.zoomAt(1/fit, x, y) // its own size, about the point
			} else {
				p.zoomAt(2, x, y)
			}
			return true
		}
		return false // a drag moves the picture's holder
	}
	return false
}

// viewerButton is a round button of an icon, labeled for the tooltip and
// for assistive technology; dark is for the picture viewer.
func viewerButton(c *ui.Context, key string, svg *ui.SVG, label string, disabled, dark bool, click func()) {
	t := c.Theme()
	color, hover := t.Text, t.SurfaceHover
	if dark {
		color, hover = viewerText, viewerHover
	}
	b := ui.ButtonBase(c.Key("viewer/"+key)).Size(34, 34).Radius(17).Label(label).Tooltip(label).Disabled(disabled)
	hovered := b.Hovered() && !disabled
	b.Children(func() {
		ic := ui.Icon(c, svg).FontSize(18).TextColor(color)
		if disabled {
			ic.Opacity(0.35)
		}
	})
	if hovered {
		b.Background(hover)
	}
	b.OnClick(click)
}

// viewerArrow is a large round button beside the picture.
func viewerArrow(c *ui.Context, key string, svg *ui.SVG, label string, disabled bool, click func()) {
	b := ui.ButtonBase(c.Key("viewer/"+key)).Size(48, 48).Radius(24).Label(label).Tooltip(label).Disabled(disabled).Background(ui.RGBA(255, 255, 255, 0.08))
	hovered := b.Hovered() && !disabled
	b.Children(func() {
		ic := ui.Icon(c, svg).FontSize(24).TextColor(viewerText)
		if disabled {
			ic.Opacity(0.25)
		}
	})
	if hovered {
		b.Background(ui.RGBA(255, 255, 255, 0.18))
	}
	b.OnClick(click)
}

// textViewer shows a text file, or a document in the web view, in a card
// over the window: a document's larger.
func (a *app) textViewer(c *ui.Context, p *preview) {
	t := c.Theme()
	list, at := p.siblings()
	kind := previewKind(p.entry)
	doc := kind == "pdf" || kind == "html"
	ui.DialogBase(c.Key("native-preview"), &p.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.45))
		w, h := c.Size()
		// Clear of the window controls above it.
		inset := max(24, c.TitleBar().Height+8)
		if doc {
			panel.Width(min(1180, w-48)).Height(h - 2*inset)
		} else {
			panel.Width(min(920, w-48)).Height(min(680, h-2*inset))
		}
		panel.Radius(t.Radius*3).Background(t.Background).Border(1, t.Border).Clip().
			Shadow(0, 12, 40, 0, ui.RGBA(0, 0, 0, 0.25))
		a.viewerKeys(c, p)
		ui.Row(c).Padding(12, 12, 12, 18).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			fileIcon(c, p.entry, 22)
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, p.entry.Name).Bold().SingleLine().Selectable()
				meta := []string{}
				if len(list) > 1 && at >= 0 {
					meta = append(meta, fmt.Sprintf("%d / %d", at+1, len(list)))
				}
				meta = append(meta, formatBytes(p.entry.Size), p.entry.ModTime().Format("2006-01-02 15:04"), path.Join(p.entry.RootName, p.entry.Parent))
				ui.Text(c, strings.Join(meta, " · ")).FontSize(12).TextColor(t.TextMuted).SingleLine()
			})
			if kind == "html" && p.doc != "" {
				shown := 0
				if p.source {
					shown = 1
				}
				if ui.Segmented(c.Key("html-shown"), &shown, "网页", "源代码").Label("显示").Changed() {
					p.source = shown == 1
				}
			}
			if len(list) > 1 {
				viewerButton(c, "prev", iconPrev, "上一项", at <= 0, false, func() { a.previewStep(-1) })
				viewerButton(c, "next", iconNext, "下一项", at < 0 || at >= len(list)-1, false, func() { a.previewStep(1) })
			}
			viewerButton(c, "open-out", iconOpenOut, "系统程序打开", false, false, func() {
				a.closePreview()
				a.systemOpenSource(p.server, p.client, p.entry, false)
			})
			viewerButton(c, "close", iconClose, "关闭预览", false, false, a.closePreview)
		})
		body := ui.Column(c).Grow(1).MinHeight(0).Padding(12)
		webPage := doc && !p.loading && p.err == "" && !p.source
		if webPage && a.docView != nil {
			body.Padding(0) // the page to the card's edges
		}
		body.Children(func() {
			switch {
			case p.loading:
				ui.Column(c).Grow(1).Center().Children(func() { ui.Spinner(c).FontSize(22).Label("正在加载预览") })
			case p.err != "":
				notice(c, t.Danger, p.err)
				ui.Spacer(c)
			case webPage && a.docView != nil:
				ui.WebView(c, a.docView).Grow(1).Label(p.entry.Name)
			case webPage:
				ui.Column(c).Grow(1).Center().Gap(12).Children(func() {
					fileIcon(c, p.entry, 40)
					ui.Text(c, "无法在这里显示："+a.docErr).TextColor(t.TextMuted).TextAlign(ui.Center).MaxLines(3)
					ui.Button(c.Key("doc-open-out"), "用系统程序打开").OnClick(func() {
						a.closePreview()
						a.systemOpenSource(p.server, p.client, p.entry, false)
					})
				})
			default:
				ui.TextArea(c.Key("preview-text"), &p.text).ReadOnly(true).Font("monospace").Grow(1).MinHeight(0).Label("文本预览")
			}
		})
		if p.note != "" {
			ui.Text(c, p.note).FontSize(11).TextColor(t.TextMuted).SingleLine().Padding(0, 18, 10, 18)
		}
	})
}

// Cache identities include the server address and remote version; filenames
// never contain NAS directory components or Windows-reserved characters.
func openCachePath(c *fileindex.Client, e fileindex.Entry) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	identity := fmt.Sprintf("%s\x00%d\x00%s\x00%d\x00%d", c.Addr, e.Root, e.Path, e.Size, e.MTime)
	if c.InArchive() {
		identity += "\x00" + c.ArchiveSource.Path + "\x00" + c.ArchiveSource.Version
	}
	key := sha256.Sum256([]byte(identity))
	name := strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, path.Base(e.Name))
	if len(name) > 235 {
		ext := path.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		stem := strings.TrimSuffix(name, ext)
		stem = stem[:235-len(ext)]
		for !utf8.ValidString(stem) {
			stem = stem[:len(stem)-1]
		}
		name = stem + ext
	}
	name = strings.TrimRight(name, ". ")
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	// Prefix avoids CON, NUL and the other Windows device names.
	return filepath.Join(cache, "filesync", "open", fmt.Sprintf("%x", key), "file-"+name), nil
}
func (a *app) systemOpenSource(server string, c *fileindex.Client, e fileindex.Entry, download bool) {
	if c.InArchive() {
		download = true
	}
	if e.Link || e.Encrypted {
		a.errorMsg = "符号链接或加密文件不能打开"
		return
	}
	if !a.sourceCurrent(server, c) {
		return
	}
	dest, err := openCachePath(c, e)
	if err != nil {
		a.errorMsg = err.Error()
		return
	}
	// The directory identifies the NAS version. Reuse a complete cached file
	// even when an Office application edited it locally; opening it again must
	// not silently overwrite those edits. A new NAS version gets a new directory.
	if !mediaFile(e) || download {
		if fi, err := os.Stat(dest); err == nil && fi.Mode().IsRegular() {
			a.recordOpenedFile(server, e.Name, dest)
			a.openPath(dest)
			return
		}
	}
	if a.proxy == nil {
		a.proxy, err = fileindex.NewLocalProxy(func(server string, c *fileindex.Client) {
			a.post(func() {
				if a.sourceCurrent(server, c) {
					a.errorMsg = "登录会话已过期，请重新登录 NAS"
					a.signedOut(a.store.Server(server))
				}
			})
		})
		if err != nil {
			a.errorMsg = err.Error()
			return
		}
	}
	address, err := a.proxy.Register(server, c, e.Root, e.Path)
	if err != nil {
		a.errorMsg = err.Error()
		return
	}
	if mediaFile(e) && !download {
		// Played as it streams: nothing transfers but what the player
		// reads, so no transfer shows. A playlist is a local file, so the
		// OS chooses a media application instead of treating the proxy's
		// HTTP URL as an ordinary browser link.
		go func() {
			list := dest + ".m3u"
			err := os.MkdirAll(filepath.Dir(list), 0700)
			if err == nil {
				err = os.WriteFile(list, []byte("#EXTM3U\n"+address+"\n"), 0600)
			}
			a.post(func() {
				switch {
				case err != nil:
					a.errorMsg = "无法播放 " + e.Name + ": " + err.Error()
				case a.sourceCurrent(server, c):
					a.openPath(list)
				}
			})
		}()
		return
	}
	tr := a.addTransfer("下载并打开", e.Name)
	tr.server = server
	tr.total = e.Size
	tr.localDir = filepath.Dir(dest)
	tr.localFile = dest
	ctx, cancel := context.WithCancel(context.Background())
	tr.cancel = cancel
	a.saveTransferHistory()
	go func() {
		err := fileindex.DownloadProxyFile(ctx, address, dest, func(p fileindex.Progress) {
			a.post(func() { tr.done, tr.total, tr.folderReady = p.Done, p.Total, true })
		})
		a.post(func() {
			defer cancel()
			if err == nil && ctx.Err() != nil {
				err = ctx.Err()
			}
			if err == nil && a.sourceCurrent(server, c) && ctx.Err() == nil {
				tr.after = func() { a.openPath(dest) }
			}
			a.finishTransfer(tr, err, nil)
		})
	}()
}
