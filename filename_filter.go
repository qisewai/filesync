package main

import (
	"fmt"
	"strings"
	"unicode"

	"filemanager/remoteclient"
	"github.com/egoist/mygo/ui"
	"github.com/mozillazg/go-pinyin"
)

// Keep the current directory listing, never another search index or remote
// query. Only the visible slice participates in selection and file operations.
type filenameFilter struct {
	input                           ui.Handle
	all                             []fileindex.Entry
	query, applied                  string
	dirty                           bool
	focusInput, focusFiles, focused bool
}

func (f *filenameFilter) source(items []fileindex.Entry) { f.all = items; f.dirty = true }

func filenameMatches(name, query string) bool {
	name, query = strings.ToLower(name), strings.ToLower(query)
	if strings.HasPrefix(name, query) {
		return true
	}
	if query == "" {
		return true
	}
	return pinyinPrefix(name, query, false) || pinyinPrefix(name, query, true)
}

// A small prefix automaton supports alternate readings without generating the
// Cartesian product of all pronunciations of a filename.
func pinyinPrefix(name, query string, initials bool) bool {
	positions := []int{0}
	args := pinyin.NewArgs()
	args.Heteronym = true
	for _, r := range name {
		readings := []string{string(r)}
		if unicode.Is(unicode.Han, r) {
			if py := pinyin.SinglePinyin(r, args); len(py) > 0 {
				readings = py
			}
		}
		next := make([]int, 0, len(positions))
		for _, at := range positions {
			for _, reading := range readings {
				if initials && unicode.Is(unicode.Han, r) {
					reading = reading[:1]
				}
				rest := query[at:]
				if strings.HasPrefix(reading, rest) {
					return true
				}
				if strings.HasPrefix(rest, reading) {
					n := at + len(reading)
					exists := false
					for _, p := range next {
						if p == n {
							exists = true
							break
						}
					}
					if !exists {
						next = append(next, n)
					}
				}
			}
		}
		if len(next) == 0 {
			return false
		}
		positions = next
	}
	return false
}

func (f *filenameFilter) apply(items *[]fileindex.Entry, sel *ui.Selection[string], cursor *int, sort *tableSort) {
	if !f.dirty && f.query == f.applied {
		return
	}
	at := ""
	if *cursor >= 0 && *cursor < len(*items) {
		at = (*items)[*cursor].Path
	}
	shown := f.all
	if f.query != "" {
		shown = make([]fileindex.Entry, 0)
		for _, e := range f.all {
			if filenameMatches(e.Name, f.query) {
				shown = append(shown, e)
			}
		}
	}
	*items = shown
	*cursor = -1
	visible := make(map[string]bool, len(shown))
	for i, e := range shown {
		visible[e.Path] = true
		if e.Path == at {
			*cursor = i
		}
	}
	for key := range sel.All() {
		if !visible[key] {
			sel.Remove(key)
		}
	}
	sortEntries(sort, shown, cursor)
	sort.group.releaseProjection()
	sort.group.table.ScrollTo(0, ui.Start)
	sort.group.grid.ScrollTo(0, ui.Start)
	f.applied = f.query
	f.dirty = false
}

func (a *app) activeFilenameFilter() (*filenameFilter, *[]fileindex.Entry, *ui.Selection[string], *int, *tableSort) {
	switch {
	case a.onArchive():
		b := a.arc
		return &b.filter, &b.items, &b.sel, &b.cursor, &b.sort
	case a.onLocal():
		b := a.local
		return &b.filter, &b.items, &b.sel, &b.cursor, &b.sort
	case a.onRoot():
		b := a.br
		return &b.filter, &b.items, &b.sel, &b.cursor, &b.sort
	}
	return nil, nil, nil, nil, nil
}

func (a *app) applyFilenameFilter() {
	f, items, sel, cursor, sort := a.activeFilenameFilter()
	if f != nil {
		changed := f.query != f.applied
		f.apply(items, sel, cursor, sort)
		if changed {
			switch {
			case a.onArchive():
				a.arc.list.ScrollTo(0, ui.Start)
				a.arc.gridState.ScrollTo(0, ui.Start)
			case a.onLocal():
				a.local.list.ScrollTo(0, ui.Start)
				a.local.gridSt.ScrollTo(0, ui.Start)
			case a.onRoot():
				a.br.list.ScrollTo(0, ui.Start)
				a.br.gridState.ScrollTo(0, ui.Start)
			}
		}
	}
}

// Attach to the actual focusable list/grid, not its surrounding layout.
func bindFilenameFilter(e ui.Element, f *filenameFilter, cell ...bool) {
	if f == nil {
		return
	}
	if f.query != "" {
		e.OnShortcut(0, ui.KeyEscape, func() { f.query = ""; f.focusFiles = true })
	}
	e.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind != ui.InputText || ev.Text == "" {
			return false
		}
		text := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, ev.Text)
		if text == "" {
			return false
		}
		f.query += text
		f.focusInput = true
		return true
	}).TextCaret(ui.Rect{X: 8, Y: 8, H: 20}).InputMethod(false)
	if f.focusFiles && len(cell) == 0 {
		e.Focus()
		f.focusFiles = false
	}
}

func (a *app) filenameFilterStatus(c *ui.Context) {
	f, items, _, _, _ := a.activeFilenameFilter()
	if f == nil || f.query == "" {
		if f != nil {
			f.focused = false
		}
		return
	}
	ui.Row(c.Key("filename-filter-status")).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, fmt.Sprintf("筛选 %d / %d", len(*items), len(f.all))).FontSize(12).TextColor(c.Theme().TextMuted)
		in := ui.TextInput(c.Key("filename-filter"), &f.query).Bind(&f.input).Width(180).Shrink(1).MinWidth(90).Label("当前目录文件名过滤").Placeholder("英文 / 拼音前缀").OnTextKey(func(k ui.TextKey) bool {
			if k.Key == ui.KeyEscape && k.Mods == 0 {
				k.SetText("")
				f.query = ""
				f.focusFiles = true
				return true
			}
			if (k.Key == ui.KeyDown || k.Key == ui.KeyEnter) && k.Mods == 0 {
				f.focusFiles = true
				return true
			}
			return false
		})
		if f.focusInput {
			in.Focus()
			n := len([]rune(f.query))
			in.SetTextSelection(n, n)
			f.focusInput = false
		}
		f.focused = in.Focused()
		a.searchTyping = a.searchTyping || f.focused
		if in.Changed() {
			if f.query == "" {
				f.focusFiles = true
			}
			c.Invalidate()
		}
		iconButton(c, "clear-filename-filter", iconClose, "清除文件名过滤", "清除过滤（Esc）", false, func() { f.query = ""; f.focusFiles = true })
	})
}

func (a *app) filenameFilterFocused(c *ui.Context) bool {
	f, _, _, _, _ := a.activeFilenameFilter()
	return f != nil && f.input.Focused(c)
}
