package main

import (
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"filesync/config"
	"filemanager/remoteclient"

	"github.com/egoist/mygo/ui"
)

// The favorites: folders of this computer and of the servers, pinned in
// the sidebar, offered by the pickers of a target too.

// favorites is what the app keeps of them besides the store.
type favorites struct {
	// missing are the local folders found gone, checked off the main
	// thread every so often, as the sidebar shows them.
	missing  map[string]bool
	checked  time.Time
	checking bool
	gen      int // renewed to check again, dropping the checks under way
	// renaming is the favorite being renamed, to newName.
	renaming *config.Favorite
	newName  string
}

// favoriteCheck is how often the local favorites are checked to exist.
const favoriteCheck = 5 * time.Second

// favoriteHere is the folder shown, as a favorite, nil where none is.
func (a *app) favoriteHere() *config.Favorite {
	switch {
	case a.onLocal():
		return &config.Favorite{Name: localName(a.local.dir), Local: a.local.dir}
	case a.onRoot():
		b := a.br
		name := b.root.Name
		if b.path != "" {
			name = pathBase(b.path)
		}
		return &config.Favorite{Name: name, Server: b.server, Root: b.root.ID, RootName: b.root.Name, Path: b.path}
	}
	return nil
}

// localName is the name a local folder shows under.
func localName(dir string) string {
	names, _ := localCrumbs(dir)
	return names[len(names)-1]
}

// favoriteOf is the favorite of a directory of a server.
func favoriteOf(server string, root fileindex.Root, dir string) *config.Favorite {
	name := root.Name
	if dir != "" {
		name = pathBase(dir)
	}
	return &config.Favorite{Name: name, Server: server, Root: root.ID, RootName: root.Name, Path: dir}
}

// isFavorite reports whether a folder is pinned.
func (a *app) isFavorite(f *config.Favorite) bool { return f != nil && a.store.Favorite(f) != nil }

// toggleFavorite pins a folder, or unpins it.
func (a *app) toggleFavorite(f *config.Favorite) {
	if f == nil {
		return
	}
	if a.isFavorite(f) {
		a.store.RemoveFavorite(f)
	} else {
		a.store.AddFavorite(f)
		*a.sectionOpen("favorites") = true
	}
	a.save()
}

// favoriteMenuItem is the item of a menu pinning a folder, or unpinning.
func (a *app) favoriteMenuItem(m *ui.Menu, f *config.Favorite) {
	label := "添加到收藏"
	if a.isFavorite(f) {
		label = "从收藏中移除"
	}
	if m.Item(label).Chosen() {
		a.toggleFavorite(f)
	}
}

// favoriteWhere says where a favorite is, as people read it.
func (a *app) favoriteWhere(f *config.Favorite) string {
	if f.Local != "" {
		return "本机 · " + f.Local
	}
	return a.target(f.Server, f.RootName, f.Path)
}

// favoriteProblem says why a favorite cannot be opened now, "" when it
// can, or may once its server answers.
func (a *app) favoriteProblem(f *config.Favorite) string {
	if f.Local != "" {
		if a.favs.missing[f.Local] {
			return "文件夹不存在或无法访问"
		}
		return ""
	}
	sv := a.store.Server(f.Server)
	switch {
	case sv == nil:
		return "服务器已删除"
	case sv.Token == "":
		return "服务器未登录"
	case a.unreachable(sv):
		return "服务器无法连接"
	}
	if roots, ok := a.roots[sv.ID]; ok && a.rootsLoading[sv.ID] == nil && a.rootOf(roots, f.Root) == nil {
		return "服务器上已没有这个索引目录"
	}
	return ""
}

func (a *app) rootOf(roots []fileindex.Root, id int64) *fileindex.Root {
	for i := range roots {
		if roots[i].ID == id {
			return &roots[i]
		}
	}
	return nil
}

// openFavorite shows a favorite's folder, or why it cannot.
func (a *app) openFavorite(f *config.Favorite) {
	if f.Local != "" {
		if fi, err := os.Stat(f.Local); err != nil || !fi.IsDir() {
			a.favs.missing[f.Local] = true
			a.errorMsg = "收藏的文件夹“" + f.Name + "”不存在或无法访问：" + f.Local
			return
		}
		delete(a.favs.missing, f.Local)
		a.openLocal(f.Local)
		return
	}
	if why := a.favoriteProblem(f); why != "" {
		a.errorMsg = "无法打开收藏的“" + f.Name + "”：" + why
		return
	}
	sv := a.store.Server(f.Server)
	roots, ok := a.roots[sv.ID]
	if !ok || a.rootsLoading[sv.ID] != nil {
		a.loadRoots(sv)
		a.errorMsg = "正在连接服务器“" + sv.Title() + "”，请稍后再打开"
		return
	}
	a.errorMsg = ""
	a.showDirectory(sv.ID, *a.rootOf(roots, f.Root), f.Path)
}

// favoriteShown reports whether the page shows the favorite's folder.
func (a *app) favoriteShown(f *config.Favorite) bool {
	here := a.favoriteHere()
	return here != nil && here.Same(f)
}

// checkFavorites finds, off the main thread, which local favorites are
// gone, every so often.
func (a *app) checkFavorites() {
	if a.favs.checking || time.Since(a.favs.checked) < favoriteCheck {
		return
	}
	var dirs []string
	for _, f := range a.store.Favorites {
		if f.Local != "" {
			dirs = append(dirs, f.Local)
		}
	}
	if len(dirs) == 0 {
		a.favs.missing, a.favs.checked = map[string]bool{}, time.Now()
		return
	}
	a.favs.checking = true
	gen := a.favs.gen
	go func() {
		missing := map[string]bool{}
		for _, d := range dirs {
			if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
				missing[d] = true
			}
		}
		a.post(func() {
			if a.favs.gen != gen {
				return // asked again since: another check runs
			}
			a.favs.missing, a.favs.checking, a.favs.checked = missing, false, time.Now()
		})
	}()
}

// recheckFavorites checks the local favorites again at once.
func (a *app) recheckFavorites() {
	a.favs.gen++
	a.favs.checking, a.favs.checked = false, time.Time{}
}

// favoriteItems are the favorites in the sidebar.
func (a *app) favoriteItems(c *ui.Context) {
	t := c.Theme()
	a.checkFavorites()
	if len(a.store.Favorites) == 0 {
		ui.Text(c, "右键文件夹选择“添加到收藏”，或点路径栏旁的 ☆").FontSize(11).MaxLines(2).TextColor(t.TextMuted).Padding(2, 8, 4, 28)
		return
	}
	for i, f := range a.store.Favorites {
		// The servers of favorites are connected to, to tell whether they
		// can be opened.
		if sv := a.store.Server(f.Server); sv != nil && sv.Token != "" {
			if _, ok := a.roots[sv.ID]; !ok {
				a.loadRoots(sv)
			}
		}
		why := a.favoriteProblem(f)
		chosen := a.favoriteShown(f)
		item := ui.TreeItem(c.Key(fmt.Sprintf("favorite/%d", i)), "", nil, nil)
		tip := a.favoriteWhere(f)
		item.Children(func() {
			svg := iconFolder
			if f.Local == "" {
				svg = iconServer
			}
			color := folderColor
			switch {
			case chosen:
				color = t.AccentText
			case why != "":
				color = t.TextMuted.Alpha(0.55)
			}
			ui.Icon(c, svg).FontSize(15).TextColor(color)
			text := ui.Text(c, f.Name).SingleLine().Grow(1).Shrink(1).MinWidth(0)
			if why != "" && !chosen {
				text.TextColor(t.TextMuted.Alpha(0.55))
			}
		}).Label("收藏 " + f.Name)
		if why != "" {
			tip += " · " + why
		}
		item.Tooltip(tip).Selected(chosen).OnClick(func() { a.openFavorite(f) })
		item.ContextMenu(func(m *ui.Menu) {
			if m.Item("打开").Chosen() {
				a.openFavorite(f)
			}
			if m.Item("在新标签页中打开").Disabled(len(a.tabs) >= maxTabs).Chosen() {
				a.newTab(favoriteLocation(f))
			}
			if m.Item("重命名…").Chosen() {
				a.favs.renaming, a.favs.newName = f, f.Name
			}
			m.Separator()
			if m.Item("上移").Disabled(i == 0).Chosen() {
				a.store.MoveFavorite(f, -1)
				a.save()
			}
			if m.Item("下移").Disabled(i == len(a.store.Favorites)-1).Chosen() {
				a.store.MoveFavorite(f, 1)
				a.save()
			}
			m.Separator()
			if m.Item("从收藏中移除").Chosen() {
				a.store.RemoveFavorite(f)
				a.save()
			}
		})
	}
}

// favoriteDialogs is the dialog renaming a favorite.
func (a *app) favoriteDialogs(c *ui.Context) {
	f := a.favs.renaming
	if f == nil {
		return
	}
	open := true
	ui.Modal(c.Key("favorite-rename"), &open, func() {
		ui.Text(c, "重命名收藏").Bold()
		ui.Text(c, a.favoriteWhere(f)).FontSize(12).TextColor(c.Theme().TextMuted).MaxLines(2)
		done := ui.TextInput(c.Key("favorite-name"), &a.favs.newName).AutoFocus().Width(320).Submitted()
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Clicked() {
				open = false
			}
			name := strings.TrimSpace(a.favs.newName)
			if ui.PrimaryButton(c, "确定").Disabled(name == "").Clicked() || done && name != "" {
				f.Name = name
				a.save()
				open = false
			}
		})
	})
	if !open {
		a.favs.renaming = nil
	}
}

// favoriteButton is the star beside the path, pinning the folder shown.
func (a *app) favoriteButton(c *ui.Context) {
	f := a.favoriteHere()
	if f == nil {
		return
	}
	if a.isFavorite(f) {
		iconButton(c, "favorite-toggle", iconStarFilled, "从收藏中移除", "从收藏中移除", false, func() { a.toggleFavorite(f) }).TextColor(ui.Hex("#e0a526"))
	} else {
		iconButton(c, "favorite-toggle", iconStar, "添加到收藏", "添加到收藏", false, func() { a.toggleFavorite(f) })
	}
}

// favoriteTitle is how a favorite reads in a picker: its name, and where
// it is when that is not its name.
func (a *app) favoriteTitle(f *config.Favorite) string {
	if f.Local != "" {
		return f.Name + "（本机）"
	}
	where := path.Join(f.RootName, f.Path)
	if sv := a.store.Server(f.Server); sv != nil {
		where = sv.Title() + " · " + where
	}
	if f.Name == pathBase(f.Path) || f.Path == "" && f.Name == f.RootName {
		return where
	}
	return f.Name + "（" + where + "）"
}
