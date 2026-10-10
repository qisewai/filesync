package main

import (
	"context"
	"errors"
	"path"
	"strings"

	"filesync/config"
	"filesync/fileindex"

	"github.com/egoist/mygo/ui"
)

// Only expanded directories are listed; collapsed branches never wake disks.
type directoryNode struct {
	open, loaded, loading bool
	dirs                  []fileindex.Entry
	err                   string
	gen                   int
}

func treeKey(server string, root int64, dir string) string {
	return rootPlace(server, root) + "/" + dir
}

func (a *app) treeNode(server string, root int64, dir string) *directoryNode {
	key := treeKey(server, root, dir)
	if n := a.tree[key]; n != nil {
		return n
	}
	n := &directoryNode{}
	a.tree[key] = n
	return n
}

func (a *app) revealDirectory(b *browser) {
	dir := ""
	a.treeNode(b.server, b.root.ID, dir).open = true
	if b.path == "" {
		return
	}
	for _, part := range strings.Split(b.path, "/") {
		dir = path.Join(dir, part)
		a.treeNode(b.server, b.root.ID, dir).open = true
	}
}

func (a *app) cacheDirectories(server string, root int64, dir string, items []fileindex.Entry) {
	n := a.treeNode(server, root, dir)
	n.gen++ // a browser listing supersedes any older tree request
	n.dirs, n.err, n.loaded, n.loading = nil, "", true, false
	for _, e := range items {
		if e.IsDir {
			n.dirs = append(n.dirs, e)
		}
	}
}

func (a *app) directoryView(c *ui.Context, sv *config.Server, root fileindex.Root, dir, name string) {
	key := treeKey(sv.ID, root.ID, dir)
	n := a.treeNode(sv.ID, root.ID, dir)
	item := ui.TreeItem(c.Key(key), "", &n.open, func() {
		if !n.loaded && !n.loading {
			a.loadDirectory(sv, root.ID, dir, n)
		}
		switch {
		case n.loading:
			ui.Text(c, "正在读取…").FontSize(12).Padding(0, 20)
		case n.err != "":
			ui.Button(c.Key(key+"/retry"), "重试读取目录").Tooltip(n.err).OnClick(func() { n.loaded = false })
		default:
			for _, e := range n.dirs {
				a.directoryView(c, sv, root, e.Path, e.Name)
			}
		}
	})
	chosen := a.br != nil && a.place == rootPlace(sv.ID, root.ID) && a.br.server == sv.ID && a.br.root.ID == root.ID && a.br.path == dir
	color := ui.Hex("#d99a2b")
	if chosen {
		color = c.Theme().AccentText
	}
	item.Children(func() { labeled(c, iconFolder, name, color) }).Label("文件夹 " + name)
	item.Selected(a.br != nil && a.place == rootPlace(sv.ID, root.ID) && a.br.server == sv.ID && a.br.root.ID == root.ID && a.br.path == dir).
		OnClick(func() {
			a.goPlace(rootPlace(sv.ID, root.ID))
			if a.br != nil && (a.br.path != dir || a.br.results) {
				a.browse(dir)
			}
		})
}

func (a *app) loadDirectory(sv *config.Server, root int64, dir string, n *directoryNode) {
	n.loading = true
	n.gen++
	gen := n.gen
	c := fileindex.New(sv.Addr, sv.Token)
	key := treeKey(sv.ID, root, dir)
	live := a.br != nil && a.br.server == sv.ID && a.br.root.ID == root && a.br.live
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		items, err := c.List(ctx, root, dir, live)
		a.post(func() {
			if a.closed || a.tree[key] != n || n.gen != gen || sv.Token != c.Token || sv.Addr != c.Addr {
				return
			}
			n.loading, n.loaded = false, true
			if err != nil {
				n.err = err.Error()
				if errors.Is(err, fileindex.ErrAuth) {
					a.signedOut(sv)
				}
				return
			}
			a.cacheDirectories(sv.ID, root, dir, items)
		})
	}()
}

func (a *app) invalidateTree(server string, root int64) {
	prefix := rootPlace(server, root) + "/"
	for key, n := range a.tree {
		if strings.HasPrefix(key, prefix) {
			n.gen++
			n.loaded, n.loading = false, false
		}
	}
}
