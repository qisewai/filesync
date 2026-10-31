package main

import (
	"cmp"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"filemanager/remoteclient"
	"github.com/egoist/mygo/ui"
)

// Grouping is a projection of the source rows. File operations and search
// pagination always use source indices and identities, never header indices.
type fileGrouping struct {
	by              string
	descending      bool
	collapsed       map[string]bool
	table, grid     ui.ListState
	selected        int
	width           float32
	anchor          string
	revision        uint64
	cacheBy         string
	cacheDescending bool
	day             string
	count, columns  int
	groups          []fileGroup
	rows            []groupRow
	order, rowOf    []int
}

type fileGroup struct {
	name  string
	order int
	items []int
}

type groupRow struct {
	group int
	items []int // nil for a heading
}

// groupMenu is shared by the file-area context menus, leaving toolbar space
// for navigation and icon actions.
func groupMenu(m *ui.Menu, g *fileGrouping) {
	m.Submenu("分组依据", func(m *ui.Menu) {
		for _, by := range []string{"", colName, "类型", colTime, colSize} {
			name := by
			if name == "" {
				name = "无分组"
			}
			if m.Item(name).Checked(g.by == by).Chosen() {
				g.by = by
				g.collapsed = nil
				g.rows = nil
				g.table.ScrollTo(0, ui.Start)
				g.grid.ScrollTo(0, ui.Start)
			}
		}
		m.Separator()
		if m.Item("分组升序").Checked(!g.descending).Disabled(g.by == "").Chosen() {
			g.descending = false
		}
		if m.Item("分组降序").Checked(g.descending).Disabled(g.by == "").Chosen() {
			g.descending = true
		}
		m.Separator()
		if m.Item("展开所有组").Disabled(g.by == "").Chosen() {
			g.collapsed = nil
			g.rows = nil
		}
		if m.Item("折叠所有组").Disabled(g.by == "").Chosen() {
			g.collapsed = make(map[string]bool)
			for _, group := range g.groups {
				g.collapsed[group.name] = true
			}
			g.rows = nil
		}
	})
	m.Separator()
}

func entryGroup(e fileindex.Entry, by string, now time.Time) (string, int) {
	switch by {
	case "类型":
		if e.IsDir {
			return "文件夹", 0
		}
		ext := strings.ToLower(path.Ext(e.Name))
		if ext == "" {
			return "无扩展名文件", 1
		}
		return strings.ToUpper(strings.TrimPrefix(ext, ".")) + " 文件", 2
	case colSize:
		if e.IsDir {
			return "文件夹", 0
		}
		switch {
		case e.Size == 0:
			return "空文件", 1
		case e.Size < 1<<20:
			return "小于 1 MB", 2
		case e.Size < 100<<20:
			return "1–100 MB", 3
		case e.Size < 1<<30:
			return "100 MB–1 GB", 4
		default:
			return "1 GB 及以上", 5
		}
	case colTime:
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		at := e.ModTime().In(now.Location())
		switch {
		case !at.Before(today.AddDate(0, 0, 1)):
			return "未来", 0
		case !at.Before(today):
			return "今天", 1
		case !at.Before(today.AddDate(0, 0, -1)):
			return "昨天", 2
		case !at.Before(today.AddDate(0, 0, -7)):
			return "最近 7 天", 3
		case !at.Before(today.AddDate(0, 0, -30)):
			return "最近 30 天", 4
		default:
			return "更早", 5
		}
	default:
		r, _ := utf8.DecodeRuneInString(e.Name)
		switch {
		case r >= '0' && r <= '9':
			return "0–9", 0
		case unicode.Is(unicode.Han, r):
			return "中文", 2
		case unicode.IsLetter(r):
			return string(unicode.ToUpper(r)), 1
		default:
			return "其他", 3
		}
	}
}

func buildFileGroups(items []fileindex.Entry, by string, descending bool, now time.Time) []fileGroup {
	groups := make([]fileGroup, 0)
	positions := make(map[string]int)
	for i, e := range items {
		name, order := entryGroup(e, by, now)
		at, ok := positions[name]
		if !ok {
			at = len(groups)
			positions[name] = at
			groups = append(groups, fileGroup{name: name, order: order})
		}
		groups[at].items = append(groups[at].items, i)
	}
	slices.SortFunc(groups, func(a, b fileGroup) int {
		n := cmp.Compare(a.order, b.order)
		if n == 0 {
			n = naturalCompare(a.name, b.name)
		}
		if descending {
			n = -n
		}
		return n
	})
	return groups
}

func groupedRows(groups []fileGroup, collapsed map[string]bool, columns int) []groupRow {
	var rows []groupRow
	for i, g := range groups {
		rows = append(rows, groupRow{group: i})
		if collapsed[g.name] {
			continue
		}
		for start := 0; start < len(g.items); start += columns {
			rows = append(rows, groupRow{group: i, items: g.items[start:min(start+columns, len(g.items))]})
		}
	}
	return rows
}

func groupHeading(c *ui.Context, g *fileGrouping, group fileGroup) {
	prefix := "▾"
	if g.collapsed[group.name] {
		prefix = "▸"
	}
	ui.ButtonBase(c).Grow(1).Justify(ui.Start).AlignItems(ui.Center).Gap(8).Padding(5, 4).Label("分组：" + group.name).OnClick(func() {
		if g.collapsed == nil {
			g.collapsed = make(map[string]bool)
		}
		g.collapsed[group.name] = !g.collapsed[group.name]
		g.rows = nil
	}).Children(func() {
		ui.Text(c, prefix).FontSize(11).TextColor(c.Theme().TextMuted)
		color := ui.Hex("#2563a6")
		if c.Theme().Dark {
			color = ui.Hex("#8ab4e8")
		}
		ui.Text(c, fmt.Sprintf("%s（%d）", group.name, len(group.items))).FontSize(14).FontWeight(400).TextColor(color)
		ui.Divider(c).Height(1).Grow(1).AlignSelf(ui.Center).Background(c.Theme().Border)
	})
}

func (a *app) groupedFiles(c *ui.Context, v *fileView) {
	if v.sort.stale() {
		v.resort()
	}
	g := &v.sort.group
	// The toolkit uses Surface for sticky section rows. Match the file area's
	// background so headings remain plain text with a thin rule, in both themes.
	previous := c.Theme()
	theme := *previous
	theme.Surface = theme.Background
	c.SetTheme(&theme)
	defer c.SetTheme(previous)
	groups := g.project(v)
	if v.grid {
		g.table.Key, g.table.Header = nil, nil
		a.groupedGrid(c, v, g, groups)
		return
	}
	g.grid.Key, g.grid.Header = nil, nil
	rows := g.layout(groups, 1, len(v.items))
	g.selected = -1
	if *v.cursor >= 0 && *v.cursor < len(g.rowOf) {
		g.selected = g.rowOf[*v.cursor]
	}

	g.table.Key = func(i int) any {
		if i < 0 || i >= len(rows) {
			return fmt.Sprint("\x00gone", i)
		}
		row := rows[i]
		if len(row.items) == 0 {
			return "\x00group/" + groups[row.group].name
		}
		return v.list.Key(row.items[0])
	}
	g.table.Header = func(i int) bool { return len(rows[i].items) == 0 }
	g.table.Selected = &g.selected
	g.table.Selection = v.sel
	g.table.Sort = &v.sort.by
	cols := fileColumns()
	if v.results {
		cols = append(cols, ui.TableColumn{Title: colWhere, Width: 220, Sortable: true})
	}
	table := ui.Table(c.Key(v.key+"-grouped"), &g.table, cols, len(rows), func(row, col int) {
		r := rows[row]
		if len(r.items) == 0 {
			groupHeading(c, g, groups[r.group])
			return
		}
		a.fileTableCell(c, v, r.items[0], col)
	}).Grow(1).Label(v.label)
	bindFilenameFilter(table, v.filter)
	if g.selected >= 0 && g.selected < len(rows) && len(rows[g.selected].items) > 0 {
		*v.cursor = rows[g.selected].items[0]
		if table.Submitted() {
			v.open(v.items[*v.cursor])
		}
	}
	// Grouped display order differs from the backend's paging order. Request
	// the next page at the visual end. Folded groups must not cause all
	// remaining pages to be fetched automatically.
	if v.visible != nil {
		_, last := g.table.Visible()
		if last >= len(rows)-2 && len(rows[len(rows)-1].items) > 0 {
			v.visible(0, len(v.items)-1)
		}
	}
}

func (a *app) groupedGrid(c *ui.Context, v *fileView, g *fileGrouping, groups []fileGroup) {
	columns := max(1, int((g.width-16)/(gridCell+8)))
	rows := g.layout(groups, columns, len(v.items))
	order := g.order
	g.grid.Key = func(i int) any {
		if i < 0 || i >= len(rows) {
			return fmt.Sprint("\x00gone", i)
		}
		row := rows[i]
		if len(row.items) == 0 {
			return "\x00group/" + groups[row.group].name
		}
		return v.list.Key(row.items[0])
	}
	g.grid.Header = func(i int) bool { return len(rows[i].items) == 0 }
	list := ui.List(c.Key(v.key+"-grouped-grid"), &g.grid, len(rows), func(row int) {
		r := rows[row]
		if len(r.items) == 0 {
			ui.Row(c).Background(c.Theme().Surface).Children(func() { groupHeading(c, g, groups[r.group]) })
			return
		}
		ui.Row(c).Gap(8).Padding(0, 8).Height(140).Children(func() {
			for _, i := range r.items {
				key := v.list.Key(i).(string)
				cell := ui.Column(c.Key(key)).Width(gridCell).Height(132).Radius(4).Focusable().Label(v.items[i].Name)
				if v.sel.Has(key) {
					cell.Background(c.Theme().Accent.Alpha(0.18))
				}
				bindFilenameFilter(cell, v.filter, true)
				cell.Children(func() { a.fileGridCell(c, v, i) })
				if cell.Clicked() {
					chooseGroupedFile(v, g, order, i, cell.ClickModifiers())
				}
				if cell.RightClicked() && !v.sel.Has(key) {
					chooseGroupedFile(v, g, order, i, 0)
				}
				if cell.DoubleClicked() {
					v.open(v.items[i])
				}
			}
		})
	}).Grow(1).Label(v.label).Focusable()
	bindFilenameFilter(list, v.filter)
	if w := list.Bounds().W; w > 0 {
		if g.width != w {
			g.width = w
			c.Invalidate()
		}
	}
	for _, key := range []ui.Key{ui.KeyLeft, ui.KeyRight, ui.KeyUp, ui.KeyDown, ui.KeyHome, ui.KeyEnd, ui.KeyPageUp, ui.KeyPageDown} {
		for _, mods := range []ui.Modifiers{0, ui.Shift, ui.Ctrl, ui.Ctrl | ui.Shift} {
			if !list.Shortcut(mods, key) || len(order) == 0 {
				continue
			}
			at := slices.Index(order, *v.cursor)
			switch key {
			case ui.KeyLeft:
				at--
			case ui.KeyRight:
				at++
			case ui.KeyUp:
				at -= columns
			case ui.KeyDown:
				at += columns
			case ui.KeyPageUp:
				at -= columns * max(1, int(list.Bounds().H/140))
			case ui.KeyPageDown:
				at += columns * max(1, int(list.Bounds().H/140))
			case ui.KeyHome:
				at = 0
			case ui.KeyEnd:
				at = len(order) - 1
			}
			i := order[max(0, min(at, len(order)-1))]
			if mods == ui.Ctrl {
				*v.cursor = i
			} else {
				chooseGroupedFile(v, g, order, i, mods)
			}
			g.grid.ScrollIntoView(g.rowOf[i])
		}
	}
	if list.Shortcut(ui.Ctrl, ui.KeySpace) && slices.Contains(order, *v.cursor) {
		chooseGroupedFile(v, g, order, *v.cursor, ui.Ctrl)
	}
	if list.Shortcut(ui.Ctrl, ui.KeyA) {
		for _, i := range order {
			v.sel.Add(v.list.Key(i).(string))
		}
	}
	if list.Shortcut(0, ui.KeyEnter) && slices.Contains(order, *v.cursor) {
		v.open(v.items[*v.cursor])
	}
	if v.visible != nil {
		_, last := g.grid.Visible()
		if last >= len(rows)-2 && len(rows[len(rows)-1].items) > 0 {
			v.visible(0, len(v.items)-1)
		}
	}
}

func chooseGroupedFile(v *fileView, g *fileGrouping, order []int, i int, mods ui.Modifiers) {
	key := v.list.Key(i).(string)
	if mods&ui.Shift != 0 {
		start := -1
		for p, at := range order {
			if v.list.Key(at) == g.anchor {
				start = p
				break
			}
		}
		end := slices.Index(order, i)
		if start < 0 {
			start = end
		}
		if mods&ui.Ctrl == 0 {
			v.sel.Clear()
		}
		for p := min(start, end); p <= max(start, end); p++ {
			v.sel.Add(v.list.Key(order[p]).(string))
		}
	} else {
		if mods&ui.Ctrl == 0 {
			v.sel.Clear()
			v.sel.Add(key)
		} else if v.sel.Has(key) {
			v.sel.Remove(key)
		} else {
			v.sel.Add(key)
		}
		g.anchor = key
	}
	*v.cursor = i
}

// Cache only integer projections, not a second copy of file metadata. Refresh,
// sorting and search pagination increment revision; dates also expire daily.
func (g *fileGrouping) project(v *fileView) []fileGroup {
	now := time.Now()
	day := now.Format("2006-01-02")
	if g.groups == nil || g.revision != v.sort.revision || g.cacheBy != g.by || g.cacheDescending != g.descending || g.day != day || g.count != len(v.items) {
		g.groups = buildFileGroups(v.items, g.by, g.descending, now)
		g.revision, g.cacheBy, g.cacheDescending, g.day, g.count = v.sort.revision, g.by, g.descending, day, len(v.items)
		g.rows = nil
	}
	return g.groups
}

func (g *fileGrouping) layout(groups []fileGroup, columns, count int) []groupRow {
	if g.rows == nil || g.columns != columns {
		g.rows = groupedRows(groups, g.collapsed, columns)
		g.columns = columns
		g.order = make([]int, 0, count)
		g.rowOf = make([]int, count)
		for i := range g.rowOf {
			g.rowOf[i] = -1
		}
		for r, row := range g.rows {
			for _, i := range row.items {
				g.rowOf[i] = r
				g.order = append(g.order, i)
			}
		}
	}
	return g.rows
}

func (g *fileGrouping) releaseProjection() {
	g.groups, g.rows, g.order, g.rowOf = nil, nil, nil, nil
	g.table.Key, g.table.Header = nil, nil
	g.grid.Key, g.grid.Header = nil, nil
}
