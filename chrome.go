package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// The window's frame: the sidebar's head, the tabs over the pages, the
// toolbar under them and the details on the right, in the look of a
// source list: quiet grays, the accent kept for what calls for it.

// folderColor is the color of folders, in the sidebar and the files.
var folderColor = ui.Hex("#3b82f6")

// appTheme is the theme of the window: the system's, lighter lines.
func appTheme(base *ui.Theme) *ui.Theme {
	t := *base
	if !t.Dark {
		t.Background = ui.Hex("#ffffff")
		t.Surface = ui.Hex("#f5f5f6")
		t.SurfaceHover = ui.Hex("#ebebed")
		t.SurfacePressed = ui.Hex("#e0e0e3")
		t.Border = ui.Hex("#e4e4e7")
	}
	return &t
}

// chosenGray is the face of what is chosen in a list: gray, as a source
// list shows it, not the accent.
func chosenGray(t *ui.Theme) ui.Color {
	if t.Dark {
		return ui.Hex("#3a3a40")
	}
	return ui.Hex("#e3e3e6")
}

// grayChoice makes the widgets fn builds show what is chosen in gray, the
// text as it is.
func grayChoice(c *ui.Context, fn func()) {
	prev := c.Theme()
	t := *prev
	t.Accent, t.AccentText = chosenGray(prev), prev.Text
	c.SetTheme(&t)
	fn()
	c.SetTheme(prev)
}

// The details' width, which the user drags between these.
const (
	defaultDetails = 300
	minDetails     = 240
	maxDetails     = 520
)

// mainPane is what is right of the sidebar: the pages, and the details.
func (a *app) mainPane(c *ui.Context) {
	ui.Row(c.Key("main-pane")).Grow(1).Shrink(1).MinWidth(0).Fill().Children(func() {
		if !a.detailsShown(c) {
			a.pagePane(c)
			return
		}
		w := a.mainWidth(c)
		first := max(w-a.detailsW, 240)
		split := ui.Split(c.Key("details-split"), &first, func() { a.pagePane(c) }, func() { a.detailsPane(c) }).Fill()
		if split.Changed() {
			a.detailsW = min(max(w-first, minDetails), maxDetails)
			a.store.DetailsWidth = a.detailsW
			a.sidebarMoved = true
		}
	})
}

// minPageWidth is the narrowest the pages get beside the details: in a
// narrower window, the details hide, to show again as it widens.
const minPageWidth = 640

// mainWidth is the width right of the sidebar.
func (a *app) mainWidth(c *ui.Context) float32 {
	w, _ := c.Size()
	if !a.sidebarHidden {
		w -= a.sidebarW
	}
	return w
}

// detailsShown reports whether the details show: not hidden, and room for
// them.
func (a *app) detailsShown(c *ui.Context) bool {
	return !a.detailsHidden && !a.searchLanding() && a.mainWidth(c)-a.detailsW >= minPageWidth
}

func (a *app) toggleDetails() {
	a.detailsHidden = !a.detailsHidden
	a.store.DetailsHidden = a.detailsHidden
	a.save()
}

// headBar is a row at the top of a column, as high as the toolbar beside
// it, which moves the window without the system's title bar.
func (a *app) headBar(c *ui.Context, key string) ui.Element {
	t := c.Theme()
	bar := ui.Row(c.Key(key)).Height(a.toolbarHeight(c)).Shrink(0).Gap(6).AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border)
	if a.hiddenTitleBar {
		bar.DragWindow()
	}
	return bar
}

// sidebarHead is the head of the sidebar: the app's name and the button
// hiding the sidebar, clear of window controls on the left.
func (a *app) sidebarHead(c *ui.Context) {
	t := c.Theme()
	left := float32(14)
	if a.hiddenTitleBar {
		left += c.TitleBar().Left
	}
	a.headBar(c, "sidebar-head").Padding(0, 8, 0, left).Children(func() {
		ui.Icon(c, iconBackup).FontSize(17).TextColor(t.Accent)
		ui.Text(c, "FileSync").Bold().SingleLine().Shrink(1).MinWidth(0)
		ui.Spacer(c)
		flatIcon(c, "toggle-sidebar", iconSidebar, "隐藏侧边栏", "隐藏侧边栏（Ctrl+B）", false, a.toggleSidebar)
	})
}

// tabBar is the head of the pages: their tabs, and the buttons showing
// what is hidden.
func (a *app) tabBar(c *ui.Context) {
	left := float32(10)
	if a.hiddenTitleBar && a.sidebarHidden {
		left += c.TitleBar().Left
	}
	shown := a.detailsShown(c)
	right := float32(10)
	if !shown {
		right += c.TitleBar().Right
	}
	a.headBar(c, "tab-bar").Padding(0, right, 0, left).Children(func() {
		if a.sidebarHidden {
			flatIcon(c, "toggle-sidebar", iconSidebar, "显示侧边栏", "显示侧边栏（Ctrl+B）", false, a.toggleSidebar)
		}
		a.tabStrip(c)
		flatIcon(c, "new-tab", iconPlus, "新建标签页", "新建标签页（Ctrl+T）", len(a.tabs) >= maxTabs, func() { a.newTab("") })
		ui.Spacer(c).MinWidth(16)
		if !shown {
			// Hidden for want of room: the button widens the window's share.
			// The page of the search without one has nothing to detail.
			if !a.searchLanding() {
				flatIcon(c, "toggle-details", iconDetails, "显示详情", "显示详情面板（Ctrl+I）", !a.detailsHidden, a.toggleDetails)
			}
			if a.ownControls {
				a.windowControls(c)
			}
		}
	})
}

// tabStrip shows the tabs, the one shown raised; a click shows one, its
// cross or Ctrl+W closes it.
func (a *app) tabStrip(c *ui.Context) {
	t := c.Theme()
	// The tab shown's title, as it is now.
	cur := a.currentTab()
	cur.title, cur.icon = a.tabTitle()
	shown := a.tab
	tabs := ui.TabsBase(c.Key("tabs"), &shown, len(a.tabs))
	tabs.List.Gap(2).Padding(3).Radius(t.Radius * 1.5).Background(t.Surface).Shrink(1).MinWidth(0).Label("标签页")
	tabs.List.Children(func() {
		for i, tab := range a.tabs {
			on := i == a.tab
			item := tabs.Tab(i).Padding(4, 6, 4, 10).Gap(6).AlignItems(ui.Center).Radius(t.Radius).
				MinWidth(64).MaxWidth(200).Shrink(1).Label("标签页 " + tab.title).Tooltip(tab.title)
			switch {
			case on:
				item.Background(t.Background).Shadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, 0.12))
			case item.Hovered():
				item.Background(t.SurfaceHover)
			}
			item.ContextMenu(func(m *ui.Menu) { a.tabMenu(m, i) })
			item.Children(func() {
				color := t.TextMuted
				if tab.icon == iconFolder {
					color = folderColor
				}
				if tab.icon != nil {
					ui.Icon(c, tab.icon).FontSize(13).TextColor(color)
				}
				text := ui.Text(c, tab.title).FontSize(13).SingleLine().Grow(1).Shrink(1).MinWidth(0)
				if !on {
					text.TextColor(t.TextMuted)
				}
				if len(a.tabs) > 1 {
					x := ui.ButtonBase(c.Key(fmt.Sprintf("close-tab/%d", tab.id))).Size(16, 16).Radius(4).
						Label("关闭标签页 " + tab.title).Tooltip("关闭（Ctrl+W）").OnClick(func() { a.closeTab(i) })
					hover := x.Hovered()
					if hover {
						x.Background(t.SurfacePressed)
					}
					x.Children(func() {
						if on || hover || item.Hovered() {
							ui.Icon(c, iconClose).FontSize(10).TextColor(t.TextMuted)
						}
					})
				}
			})
		}
	})
	if shown != a.tab {
		a.switchTab(shown)
	}
}

// tabMenu is the menu of a tab.
func (a *app) tabMenu(m *ui.Menu, i int) {
	if m.Item("复制标签页").Disabled(len(a.tabs) >= maxTabs).Chosen() {
		a.switchTab(i)
		a.newTab("")
	}
	m.Separator()
	if m.Item("向左移动").Disabled(i == 0).Chosen() {
		a.moveTab(i, i-1)
	}
	if m.Item("向右移动").Disabled(i == len(a.tabs)-1).Chosen() {
		a.moveTab(i, i+1)
	}
	m.Separator()
	if m.Item("关闭").Shortcut(ui.Ctrl, ui.KeyW).Disabled(len(a.tabs) == 1).Chosen() {
		a.closeTab(i)
	}
	if m.Item("关闭其他标签页").Disabled(len(a.tabs) == 1).Chosen() {
		keep := a.tabs[i]
		a.switchTab(i)
		for j := len(a.tabs) - 1; j >= 0; j-- {
			if a.tabs[j] != keep {
				a.closeTab(j)
			}
		}
	}
	if m.Item("关闭右侧标签页").Disabled(i == len(a.tabs)-1).Chosen() {
		if a.tab > i {
			a.switchTab(i)
		}
		for j := len(a.tabs) - 1; j > i; j-- {
			a.closeTab(j)
		}
	}
}

// flatIcon is a button of an icon without a face until hovered, as the
// buttons of a toolbar.
func flatIcon(c *ui.Context, key string, svg *ui.SVG, label, tip string, disabled bool, click func()) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c.Key(key)).Size(28, 28).Radius(t.Radius).Label(label).Tooltip(tip).Disabled(disabled).OnClick(click)
	if b.Hovered() && !disabled {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		color := t.TextMuted
		if disabled {
			color = color.Alpha(0.4)
		}
		ui.Icon(c, svg).FontSize(16).TextColor(color)
	})
	return b
}

// sectionTitle is the title of a section of the sidebar: small and gray,
// over what it holds.
func sectionTitle(c *ui.Context, title string) {
	ui.Text(c, title).FontSize(12).Bold().TextColor(c.Theme().TextMuted).SingleLine().Grow(1)
}

// pageTools are the tools of the page of files shown, at the end of the
// toolbar: how its files show, and the menu of its folder.
func (a *app) pageTools(c *ui.Context) {
	switch {
	case a.onRoot():
		b := a.br
		viewToggle(c, "view-toggle", &b.grid)
		moreMenu(c, "more", func(m *ui.Menu) { a.entryMenu(c, m, nil, !b.root.ReadOnly && !b.loading) })
	case a.onLocal():
		b := a.local
		viewToggle(c, "local-view-toggle", &b.grid)
		moreMenu(c, "local-more", func(m *ui.Menu) {
			a.localMenu(c, m, nil)
			if idx := a.localIdx; idx != nil {
				m.Separator()
				if m.Item("更新本机索引").Disabled(idx.running || idx.loading).Chosen() {
					a.startLocalScan()
				}
				if m.Item("本机索引范围…").Disabled(idx.running || idx.loading).Chosen() {
					a.editLocalIndex()
				}
			}
		})
	case a.onSearch() && !a.searchLanding():
		viewToggle(c, "search-view-toggle", &a.gsearch.grid)
	case a.onArchive():
		viewToggle(c, "archive-view", &a.arc.grid)
		moreMenu(c, "archive-more", func(m *ui.Menu) { a.archiveMenu(c, m, nil) })
	}
}
