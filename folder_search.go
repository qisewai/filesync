package main

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

func (a *app) onFolderSearch() bool {
	return a.onSearch() && a.gsearch.scope == "folder" && a.gsearch.folder != nil
}

func (f *searchFolder) location() string {
	if f.source == localPlace {
		return localLocation(f.dir)
	}
	return nasLocation(f.source, f.root, f.dir)
}

// History carries the search origin, rather than relying on whichever browser
// happens to be cached after visiting another folder or switching tabs.
func (a *app) folderAt(location string) *searchFolder {
	u, err := url.Parse(location)
	if err != nil {
		return nil
	}
	if u.Path == "/local" && filepath.IsAbs(u.Query().Get("dir")) {
		dir := u.Query().Get("dir")
		return &searchFolder{source: localPlace, dir: dir, name: filepath.Base(dir)}
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 3 && parts[0] == "nas" {
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return nil
		}
		if root := a.rootOf(a.roots[parts[1]], id); root != nil {
			dir, name := u.Query().Get("path"), root.Name
			if dir != "" {
				name = pathBase(dir)
			}
			return &searchFolder{source: parts[1], root: id, rootName: root.Name, dir: dir, name: name}
		}
	}
	return nil
}

func (a *app) folderSearchPath(c *ui.Context) {
	f := a.gsearch.folder
	if f.source == localPlace {
		a.localPathBar(c, f.dir)
	} else if root := a.rootOf(a.roots[f.source], f.root); root != nil {
		a.serverPathBar(c, f.source, *root, f.dir)
	}
}

func (a *app) folderSearchBar(c *ui.Context) {
	if !a.onFolderSearch() {
		// This draft belongs to this directory and this tab, never the address.
		holder := ui.Row(c.Key(a.location())).Grow(1).MinWidth(0)
		query := ui.Local(holder, "query", func() string { return "" })
		holder.Children(func() {
			changed, submitted, _ := a.searchBox(c, "folder-search", "搜索当前文件夹", query, "", "搜索当前文件夹及子文件夹", nil)
			if (changed || submitted) && strings.TrimSpace(*query) != "" {
				q := *query
				*query = ""
				a.recordHistory()
				a.searchFrom(q, "folder")
			}
		})
		return
	}
	s := a.gsearch
	placeholder := "搜索“" + s.folder.name + "”及子文件夹"
	if s.filter.regex {
		placeholder = "输入正则表达式"
	}
	changed, submitted, cleared := a.searchField(c, "global-search", "搜索当前文件夹", &s.query, "", placeholder, nil, false)
	switch {
	case cleared || changed && strings.TrimSpace(s.query) == "":
		origin := s.folder.location()
		s.closeSearch()
		s.groups = nil
		if a.nav.CanGoBack() {
			a.nav.Back()
			// Clear behaves like Back, without recording an empty search page.
			a.followHistory()
		} else {
			a.goLocation(origin)
		}
		// Browsing builds a different input. Hand focus to it after the
		// router restores the directory so typing can continue immediately.
		a.focusSearch, a.focusTries = true, 0
	case changed:
		a.runSearch(250 * time.Millisecond)
	case submitted:
		a.remember(s.query)
		a.runSearch(0)
	}
}
