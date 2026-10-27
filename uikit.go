package main

import (
	"github.com/egoist/mygo/ui"
)

// The parts the pages share, in the theme's look: the bars, buttons of
// icons, notices and empty states.

// iconButton is a square button showing an icon, named label, its tip
// tip.
func iconButton(c *ui.Context, key string, svg *ui.SVG, label, tip string, disabled bool, click func()) ui.Element {
	return flatIcon(c, key, svg, label, tip, disabled, click)
}

// toolButton is a button of a toolbar: an icon, then a short label.
func toolButton(c *ui.Context, key string, svg *ui.SVG, label, tip string, disabled bool, click func()) ui.Element {
	b := ui.Button(c.Key(key), "").Label(label).Disabled(disabled).OnClick(click).Children(func() {
		ui.Icon(c, svg).FontSize(16)
		ui.Text(c, label).SingleLine()
	})
	if tip != "" {
		b.Tooltip(tip)
	}
	return b
}

// card is a bordered box.
func card(c *ui.Context, fn func()) {
	t := c.Theme()
	ui.Column(c).Padding(16).Gap(10).Radius(t.Radius*2).Border(1, t.Border).Background(t.Background).Children(fn)
}

// notice is a colored message.
func notice(c *ui.Context, color ui.Color, msg string) {
	ui.Row(c).Padding(10, 12).Radius(c.Theme().Radius*2).Background(color.Alpha(0.1)).Border(1, color.Alpha(0.4)).Children(func() {
		ui.Text(c, msg).Grow(1).Shrink(1).Selectable()
	})
}

// closableNotice is a colored message with a button closing it.
func closableNotice(c *ui.Context, color ui.Color, msg string, close func()) {
	ui.Row(c).Padding(6, 6, 6, 12).Gap(8).AlignItems(ui.Center).Radius(c.Theme().Radius*2).Background(color.Alpha(0.1)).Border(1, color.Alpha(0.4)).Children(func() {
		ui.Text(c, msg).Grow(1).Shrink(1).Selectable()
		ui.ButtonBase(c.Key("close-notice")).Size(26, 26).Radius(c.Theme().Radius).Label("关闭提示").Tooltip("关闭").OnClick(close).Children(func() {
			ui.Icon(c, iconClose).FontSize(14).TextColor(c.Theme().TextMuted)
		})
	})
}

// emptyState fills what holds it with an icon, a title, what to do, and
// the buttons actions builds, if any.
func emptyState(c *ui.Context, svg *ui.SVG, title, body string, actions func()) {
	t := c.Theme()
	ui.Column(c).Fill().Center().Gap(8).Padding(32).Children(func() {
		ui.Icon(c, svg).FontSize(36).TextColor(t.TextMuted.Alpha(0.6))
		ui.Text(c, title).FontSize(16).Bold()
		if body != "" {
			ui.Text(c, body).FontSize(12).TextColor(t.TextMuted).TextAlign(ui.Center).MaxWidth(380)
		}
		if actions != nil {
			ui.Row(c).Gap(8).Margin(8, 0, 0, 0).Children(actions)
		}
	})
}

// statusDot is a small round mark of a state, in color.
func statusDot(c *ui.Context, color ui.Color) {
	ui.Row(c).Width(7).Height(7).Radius(4).Background(color)
}

// statusText is the text of the status bar, whose numbers roll as they
// change.
func statusText(c *ui.Context, s string) ui.Element {
	return rolling(ui.Text(c, s).FontSize(12).SingleLine().Shrink(1).FontFeatures("tnum").TextColor(c.Theme().TextMuted))
}

// rolling makes the characters of a text that change roll into place, as
// the digits of a counter, unless rollNumbers is off.
func rolling(e ui.Element) ui.Element {
	if !rollNumbers {
		return e
	}
	return e.ContentTransition(ui.ContentTransition{Numeric: true})
}

// rollNumbers is whether numbers roll as they change: tests, which draw a
// frame after another while anything moves, show them at once.
var rollNumbers = true
