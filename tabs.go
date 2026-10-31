package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
)

// Tabs: each shows a place of its own, with its own history, as a
// browser's do. The pages keep their state on the app (a.place, a.br,
// a.local, …), which is the state of the tab shown; the others keep theirs
// in a tabState until they show again. What the pages read while hidden,
// the answers of the server they left, is dropped as theirs is (the app
// checks a.br == b and the like), and read again as they show.

// tabState is what a tab shows: the fields of the app it swaps in.
type tabState struct {
	id       int
	title    string // as it was when it showed last
	icon     *ui.SVG
	place    string
	nav      *ui.Router
	navShown string

	br        *browser
	local     *localBrowser
	gsearch   *globalSearch
	admin     *adminPage
	recycle   *recycleBrowser
	arc       *archiveBrowser
	taskShown string
}

// maxTabs keeps the strip readable.
const maxTabs = 12

func newRouter() *ui.Router {
	r := ui.NewRouter("/")
	r.Transition = ui.TransitionFade
	return r
}

// initTabs makes the first tab, of the state the app starts with.
func (a *app) initTabs() {
	a.tabSeq++
	a.tabs = []*tabState{{id: a.tabSeq}}
	a.tab = 0
}

// saveTab keeps the state shown in t.
func (a *app) saveTab(t *tabState) {
	t.title, t.icon = a.tabTitle()
	t.place, t.nav, t.navShown = a.place, a.nav, a.navShown
	t.br, t.local, t.gsearch, t.admin, t.recycle, t.arc = a.br, a.local, a.gsearch, a.admin, a.recycle, a.arc
	t.taskShown = a.taskShown
}

// loadTab shows the state t kept.
func (a *app) loadTab(t *tabState) {
	a.place, a.nav, a.navShown = t.place, t.nav, t.navShown
	a.br, a.local, a.gsearch, a.admin, a.recycle, a.arc = t.br, t.local, t.gsearch, t.admin, t.recycle, t.arc
	a.taskShown = t.taskShown
}

// currentTab is the tab shown.
func (a *app) currentTab() *tabState { return a.tabs[a.tab] }

// switchTab shows the tab at i.
func (a *app) switchTab(i int) {
	if i < 0 || i >= len(a.tabs) || i == a.tab {
		return
	}
	a.leaveTab()
	a.tab = i
	a.loadTab(a.tabs[i])
	a.resumeTab()
}

// leaveTab keeps the state of the tab shown, before another shows.
func (a *app) leaveTab() {
	a.pathEditing = nil
	a.saveTab(a.currentTab())
}

// resumeTab reads again what the tab shown was reading as it hid, whose
// answers were dropped, and what may have changed meanwhile.
func (a *app) resumeTab() {
	switch {
	case a.onRoot():
		b := a.br
		b.refreshPending = false
		if b.loading || len(b.items) == 0 {
			b.loading, b.quiet = false, false
			a.browse(b.path)
		} else {
			a.reloadQuietly()
		}
	case a.onLocal():
		if a.local.loading {
			a.openLocal(a.local.dir)
		}
	case a.onSearch():
		if a.gsearch.busy() {
			a.runSearch(0)
		}
	case a.onArchive():
		if a.arc.loading {
			a.archiveBrowse(a.arc.path)
		}
	case a.recycle != nil && a.place == trashPlace(a.recycle.server):
		if a.recycle.loading {
			a.recycle.loading = false
			a.loadTrash()
		}
	case a.onAdmin():
		if a.admin.loading {
			a.admin.loading = false
			a.reloadAdmin()
		}
	}
}

// newTab opens a tab after the one shown, showing loc ("" for a blank
// search page), and shows it.
func (a *app) newTab(loc string) {
	if len(a.tabs) >= maxTabs {
		a.errorMsg = fmt.Sprintf("最多打开 %d 个标签页", maxTabs)
		return
	}
	if loc == "" {
		loc = "/search"
	}
	a.leaveTab()
	a.tabSeq++
	t := &tabState{id: a.tabSeq}
	at := a.tab + 1
	a.tabs = append(a.tabs[:at], append([]*tabState{t}, a.tabs[at:]...)...)
	a.tab = at
	// A tab of its own: nothing of the others' pages.
	a.place, a.nav, a.navShown = "", newRouter(), "/"
	a.br, a.local, a.gsearch, a.admin, a.recycle, a.arc = nil, nil, nil, nil, nil, nil
	a.taskShown = ""
	a.goLocation(loc)
	if a.searchLanding() {
		a.focusSearch, a.focusTries = true, 0
	}
}

// openInNewTab opens a folder in a tab of its own.
func (a *app) openFolderInNewTab(server string, root int64, dir string) {
	a.newTab(nasLocation(server, root, dir))
}

func (a *app) openLocalInNewTab(dir string) { a.newTab(localLocation(dir)) }

// closeTab closes the tab at i; the last one stays, going home.
func (a *app) closeTab(i int) {
	if i < 0 || i >= len(a.tabs) {
		return
	}
	if len(a.tabs) == 1 {
		return
	}
	if i == a.tab {
		a.pathEditing = nil
		a.stopTabWork(a.currentTab(), true)
		a.tabs = append(a.tabs[:i], a.tabs[i+1:]...)
		a.tab = min(i, len(a.tabs)-1)
		a.loadTab(a.tabs[a.tab])
		a.resumeTab()
		return
	}
	a.stopTabWork(a.tabs[i], false)
	a.tabs = append(a.tabs[:i], a.tabs[i+1:]...)
	if i < a.tab {
		a.tab--
	}
}

// stopTabWork cancels what a tab closed was reading.
func (a *app) stopTabWork(t *tabState, shown bool) {
	if shown {
		a.gsearch.closeSearch()
	} else {
		t.gsearch.closeSearch()
	}
	br, local, admin, recycle, arc := t.br, t.local, t.admin, t.recycle, t.arc
	if shown {
		br, local, admin, recycle, arc = a.br, a.local, a.admin, a.recycle, a.arc
	}
	if br != nil {
		if br.cancel != nil {
			br.cancel()
		}
		if br.thumbCancel != nil {
			br.thumbCancel()
		}
	}
	if local != nil && local.thumbCancel != nil {
		local.thumbCancel()
	}
	if admin != nil && !a.adminShared(admin, t) {
		admin.stop()
	}
	if recycle != nil && recycle.cancel != nil {
		recycle.cancel()
	}
	if arc != nil && arc.cancel != nil {
		arc.cancel()
	}
}

// adminShared reports whether another tab than t shows the management m.
func (a *app) adminShared(m *adminPage, t *tabState) bool {
	for i, o := range a.tabs {
		if o == t {
			continue
		}
		if i == a.tab && a.admin == m || i != a.tab && o.admin == m {
			return true
		}
	}
	return false
}

// moveTab moves the tab at i to j, keeping the one shown.
func (a *app) moveTab(i, j int) {
	if i == j || i < 0 || j < 0 || i >= len(a.tabs) || j >= len(a.tabs) {
		return
	}
	shown := a.tabs[a.tab]
	t := a.tabs[i]
	a.tabs = append(a.tabs[:i], a.tabs[i+1:]...)
	a.tabs = append(a.tabs[:j], append([]*tabState{t}, a.tabs[j:]...)...)
	for k, o := range a.tabs {
		if o == shown {
			a.tab = k
		}
	}
}

// eachTab runs fn with the state of every tab shown in turn, the one
// shown last: for what applies to all of them, as a server removed.
func (a *app) eachTab(fn func()) {
	shown := a.tab
	a.saveTab(a.currentTab())
	for i, t := range a.tabs {
		if i == shown {
			continue
		}
		a.loadTab(t)
		fn()
		a.saveTab(t)
	}
	a.loadTab(a.tabs[shown])
	fn()
	a.saveTab(a.tabs[shown])
}

// tabShortcuts are the keys of the tabs: Ctrl+T opens one as the one
// shown, Ctrl+W closes it, Ctrl+Tab and Ctrl+Shift+Tab go to the next and
// the one before, Ctrl+1…8 to one of the first, Ctrl+9 to the last.
func (a *app) tabShortcuts(c *ui.Context) {
	c.OnShortcut(ui.Ctrl, ui.KeyT, func() { a.newTab("") })
	c.OnShortcut(ui.Ctrl, ui.KeyW, func() { a.closeTab(a.tab) })
	c.OnShortcut(ui.Ctrl, ui.KeyTab, func() { a.switchTab((a.tab + 1) % len(a.tabs)) })
	c.OnShortcut(ui.Ctrl|ui.Shift, ui.KeyTab, func() { a.switchTab((a.tab + len(a.tabs) - 1) % len(a.tabs)) })
	for n := 1; n <= 9; n++ {
		key := ui.Key1 + ui.Key(n-1)
		c.OnShortcut(ui.Ctrl, key, func() {
			if n == 9 {
				a.switchTab(len(a.tabs) - 1)
			} else {
				a.switchTab(n - 1)
			}
		})
	}
}

// tabTitle is the short name of the place shown, for its tab, and its icon.
func (a *app) tabTitle() (string, *ui.SVG) {
	switch {
	case a.onArchive():
		return pathBase(a.arc.file), iconArchive
	case a.place == "backup":
		return "备份任务", iconBackup
	case a.place == "search":
		if q := strings.TrimSpace(a.gsearch.query); q != "" {
			return "搜索 “" + q + "”", iconSearch
		}
		return "搜索", iconSearch
	case a.place == "settings":
		return "设置", iconGear
	case a.onLocal():
		name := filepath.Base(a.local.dir)
		if name == "" || name == string(filepath.Separator) || name == "." {
			name = a.local.dir
		}
		return name, iconFolder
	case a.onAdmin():
		return a.pageTitle(), iconGear
	case a.recycle != nil && a.place == trashPlace(a.recycle.server):
		return a.pageTitle(), iconTrash
	case a.onRoot():
		if a.br.path == "" {
			return a.br.root.Name, iconFolder
		}
		return pathBase(a.br.path), iconFolder
	}
	return "首页", iconHome
}
