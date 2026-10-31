package main

import (
	"github.com/egoist/mygo/ui"
	"time"
)

const panelMotion = 200 * time.Millisecond

// Keep pane identities stable while clipping a full-width sidebar. Only the
// surrounding layout changes width; its controls do not squeeze during motion.
func animatedPanel(c *ui.Context, key string, shown bool, width float32, content func()) {
	box := ui.Box(c.Key(key)).Fill().Shrink(0).Clip()
	target := float32(0)
	if shown {
		target = 1
	}
	progress := box.Animate("visibility", target, panelMotion)
	box.Width(width * progress)
	if progress > 0 {
		box.Children(func() {
			ui.Box(c.Key("content")).Fill().Width(width).Disabled(!shown).Children(content)
		})
	}
}

// A small hit target around a one-pixel separator keeps resizing available
// without leaving a splitter behind when a panel is hidden.
func (a *app) panelDivider(c *ui.Context, key string, width *float32, lo, hi float32, trailing bool) {
	divider := ui.Box(c.Key(key)).Width(1).Shrink(0).Fill().Width(1).
		Background(c.Theme().Border).Focusable().Role(ui.RoleSplitter).Label("Divider").Tooltip("调整"+key).
		Range(float64(lo), float64(hi), float64(*width))
	change := func(delta float32) {
		if trailing {
			delta = -delta
		}
		if c.Direction() == ui.RightToLeft {
			delta = -delta
		}
		*width = min(max(*width+delta, lo), hi)
		a.store.SidebarWidth, a.store.DetailsWidth = a.sidebarW, a.detailsW
		a.sidebarMoved = true
	}
	divider.OnShortcut(0, ui.KeyLeft, func() { change(-10) }).OnShortcut(0, ui.KeyRight, func() { change(10) })
	divider.Children(func() {
		grip := ui.Box(c).Absolute().Top(0).Bottom(0).Left(-3).Width(7).Cursor(ui.CursorResizeEW)
		dx, _, held := grip.Dragged()
		if held && dx != 0 {
			change(dx)
		}
	})
}
