package main

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"

	fileindex "filemanager/remoteclient"
)

// submitLocation resolves the shared address/search field only on submission.
// An asynchronous NAS reply must not replace a newer submission or navigation.
func (a *app) submitLocation(input string) {
	q := strings.TrimSpace(input)
	if q == "" {
		return
	}
	a.addressRequest++
	a.addressPending = false
	request, location := a.addressRequest, a.location()
	var folder *searchFolder
	switch {
	case a.onLocal():
		folder = &searchFolder{source: localPlace, dir: a.local.dir, name: filepath.Base(a.local.dir)}
	case a.onRoot():
		b := a.br
		folder = &searchFolder{source: b.server, root: b.root.ID, rootName: b.root.Name, dir: b.path, name: path.Base(b.path)}
	case a.onSearch():
		folder = a.gsearch.folder
	}
	fallback := func() {
		if a.onSearch() {
			a.gsearch.query = q
			if folder != nil {
				a.gsearch.scope = "folder"
			}
			a.remember(q)
			a.runSearch(0)
		} else {
			a.searchFrom(q, "folder")
		}
	}
	p := q
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~/"))
			if q == "~" {
				p = home
			}
		}
	}
	if isLocalPath(p) || folder != nil && folder.source == localPlace {
		candidate := p
		if !isLocalPath(candidate) {
			candidate = filepath.Join(folder.dir, candidate)
		}
		if _, err := os.Stat(candidate); err == nil {
			a.goLocal(filepath.Clean(candidate))
			return
		}
		if isLocalPath(p) {
			fallback()
			return
		}
	}
	var server string
	var root *fileindex.Root
	var rel string
	parts := strings.Split(strings.Trim(q, "/"), "/")
	if sv := a.serverTitled(parts[0]); sv != nil && len(parts) > 1 {
		if !a.serverConnected(sv.ID) {
			fallback()
			return
		}
		server = sv.ID
		root = a.rootNamed(a.roots[server], parts[1])
		rel = strings.Join(parts[2:], "/")
	} else if folder != nil && folder.source != localPlace {
		server = folder.source
		if !a.serverConnected(server) {
			fallback()
			return
		}
		root = a.rootOf(a.roots[server], folder.root)
		rel = path.Join(folder.dir, q)
	}
	if root == nil {
		fallback()
		return
	}
	rel = path.Clean(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		fallback()
		return
	}
	if rel == "." || rel == "" {
		a.showDirectory(server, *root, "")
		return
	}
	c, ok := a.serverClient(server)
	if !ok {
		fallback()
		return
	}
	target := *root
	a.addressPending = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		parent := path.Dir(rel)
		if parent == "." {
			parent = ""
		}
		entries, err := c.List(ctx, target.ID, parent)
		a.post(func() {
			if a.addressRequest != request {
				return
			}
			a.addressPending = false
			if a.closed || a.location() != location {
				return
			}
			sv := a.store.Server(server)
			if sv == nil || sv.Addr != c.Addr || sv.Token != c.Token || !a.serverConnected(server) {
				return
			}
			if err != nil {
				var status *fileindex.StatusError
				if errors.As(err, &status) && status.Code == 404 {
					fallback()
					return
				}
				a.errorMsg = connectError(err)
				return
			}
			for _, entry := range entries {
				if entry.Name != path.Base(rel) {
					continue
				}
				if entry.IsDir {
					a.showDirectory(server, target, rel)
				} else {
					a.showDirectory(server, target, parent, entry.Name)
				}
				return
			}
			fallback()
		})
	}()
}
