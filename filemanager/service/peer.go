package service

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"filemanager/api"
	fileindex "filemanager/remoteclient"
)

// PeerHandler is a strictly read-only facade for desktop peers. It deliberately
// does not mount Handler(): trusted-local index/coordinator/mutation APIs must
// never be reachable through a network listener.
func (s *Service) PeerHandler(key string) http.Handler {
	if len(key) < 32 {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peerError(w, 503, errors.New("sharing credentials not configured"))
		})
	}
	mux := http.NewServeMux()
	var mu sync.Mutex
	sessions := map[string]time.Time{}
	auth := func(r *http.Request) bool {
		c, e := r.Cookie("fi_session")
		if e != nil {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		expiry, ok := sessions[c.Value]
		return ok && time.Now().Before(expiry)
	}
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Password string `json:"password"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&q) != nil {
			peerError(w, 400, errors.New("invalid login"))
			return
		}
		got, want := sha256.Sum256([]byte(q.Password)), sha256.Sum256([]byte(key))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			peerError(w, 401, errors.New("访问码不正确"))
			return
		}
		token := rand.Text()
		expiry := time.Now().Add(30 * 24 * time.Hour)
		mu.Lock()
		for t, e := range sessions {
			if time.Now().After(e) {
				delete(sessions, t)
			}
		}
		if len(sessions) >= 256 {
			mu.Unlock()
			peerError(w, 429, errors.New("too many sessions"))
			return
		}
		sessions[token] = expiry
		mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "fi_session", Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expiry})
		write(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/auth/status", func(w http.ResponseWriter, r *http.Request) { write(w, map[string]bool{"authed": auth(r)}) })
	protected := http.NewServeMux()
	protected.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie("fi_session")
		mu.Lock()
		delete(sessions, c.Value)
		mu.Unlock()
		write(w, map[string]bool{"ok": true})
	})
	protected.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"peer": true, "thumb_exts": []string{}})
	})
	protected.HandleFunc("GET /api/roots", func(w http.ResponseWriter, r *http.Request) {
		roots := []fileindex.Root{}
		for _, root := range s.roots {
			id, _ := strconv.ParseInt(root.config.ID, 10, 64)
			roots = append(roots, fileindex.Root{ID: id, Name: root.config.Name, ReadOnly: true})
		}
		sort.Slice(roots, func(i, j int) bool { return roots[i].ID < roots[j].ID })
		write(w, map[string]any{"roots": roots})
	})
	protected.HandleFunc("GET /api/roots/{root}/list", s.peerList)
	protected.HandleFunc("GET /api/roots/{root}/props", s.peerProps)
	protected.HandleFunc("GET /api/download/{root}", s.peerDownload)
	protected.HandleFunc("GET /api/backup/export", s.peerExport)
	protected.HandleFunc("GET /api/tasks", func(w http.ResponseWriter, r *http.Request) { write(w, []fileindex.Task{}) })
	protected.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		peerError(w, 501, errors.New("此设备共享尚未启用远程索引搜索，请进入目录使用文件名过滤"))
	})
	protected.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			if _, err := io.WriteString(w, ": connected\n\n"); err != nil {
				return
			}
			if http.NewResponseController(w).Flush() != nil {
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
			}
		}
	})
	slots := make(chan struct{}, 16)
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			peerError(w, 401, errors.New("需要连接设备"))
			return
		}
		if r.URL.Path != "/api/events" {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-r.Context().Done():
				return
			}
		}
		protected.ServeHTTP(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Header.Get("Origin") != "" || r.Header.Get("X-Requested-With") != "fileindex" {
			peerError(w, 403, errors.New("desktop clients only"))
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func peerError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
func (s *Service) peerResolve(id, p string) (openedRoot, string, error) {
	root, p, err := s.resolve(api.Location{Root: id, Path: p})
	if err != nil {
		return root, p, err
	}
	p, err = readPath(root, p, true)
	if err == nil && s.hidden(id, filepath.ToSlash(p)) {
		err = errors.New("protected path")
	}
	return root, p, err
}
func (s *Service) peerEntry(root openedRoot, p string, info fs.FileInfo) fileindex.Entry {
	id, _ := strconv.ParseInt(root.config.ID, 10, 64)
	p = filepath.ToSlash(p)
	parent := path.Dir(p)
	if parent == "." {
		parent = ""
	}
	return fileindex.Entry{Root: id, RootName: root.config.Name, Path: p, Parent: parent, Name: info.Name(), Size: info.Size(), MTime: info.ModTime().Unix(), IsDir: info.IsDir(), Link: info.Mode()&os.ModeSymlink != 0, Ext: strings.TrimPrefix(strings.ToLower(path.Ext(p)), ".")}
}
func (s *Service) peerList(w http.ResponseWriter, r *http.Request) {
	root, p, err := s.peerResolve(r.PathValue("root"), r.URL.Query().Get("path"))
	if err != nil {
		peerError(w, 403, err)
		return
	}
	dir, err := root.handle.OpenRoot(p)
	if err != nil {
		peerError(w, 404, err)
		return
	}
	defer dir.Close()
	f, err := dir.Open(".")
	if err != nil {
		peerError(w, 404, err)
		return
	}
	defer f.Close()
	items := []fileindex.Entry{}
	for {
		entries, e := f.ReadDir(256)
		for _, de := range entries {
			if r.Context().Err() != nil {
				return
			}
			rel := filepath.Join(p, de.Name())
			if s.hidden(root.config.ID, filepath.ToSlash(rel)) {
				continue
			}
			info, err := de.Info()
			if err != nil {
				peerError(w, 500, err)
				return
			}
			items = append(items, s.peerEntry(root, rel, info))
			if len(items) > 100000 {
				peerError(w, 413, errors.New("目录超过 100000 项，请缩小共享范围"))
				return
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			peerError(w, 500, e)
			return
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDir != items[j].IsDir {
			return items[i].IsDir
		}
		return items[i].Name < items[j].Name
	})
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	page = max(1, min(page, 100001))
	size = max(1, min(size, 1000))
	start := min(len(items), (page-1)*size)
	end := min(len(items), start+size)
	write(w, map[string]any{"items": items[start:end], "total": len(items)})
}
func (s *Service) peerProps(w http.ResponseWriter, r *http.Request) {
	root, p, err := s.peerResolve(r.PathValue("root"), r.URL.Query().Get("path"))
	if err != nil {
		peerError(w, 403, err)
		return
	}
	fi, err := root.handle.Stat(p)
	if err != nil {
		peerError(w, 404, err)
		return
	}
	id, _ := strconv.ParseInt(root.config.ID, 10, 64)
	write(w, fileindex.Props{RootID: id, RootName: root.config.Name, Path: r.URL.Query().Get("path"), Name: fi.Name(), IsDir: fi.IsDir(), Size: fi.Size(), MTime: fi.ModTime().Unix(), ReadOnly: true, Children: -1, Mode: fi.Mode().String(), Perm: fi.Mode().Perm().String()})
}
func (s *Service) peerDownload(w http.ResponseWriter, r *http.Request) {
	root, p, err := s.peerResolve(r.PathValue("root"), r.URL.Query().Get("path"))
	if err != nil {
		peerError(w, 403, err)
		return
	}
	f, err := openReadFile(root.handle, p)
	if err != nil {
		peerError(w, 404, err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		peerError(w, 400, errors.New("only regular files can be read"))
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, fi.ModTime().UnixNano(), fi.Size()))
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}
func (s *Service) peerExport(w http.ResponseWriter, r *http.Request) {
	paths := r.URL.Query()["path"]
	if len(paths) == 0 || len(paths) > 1024 {
		peerError(w, 400, errors.New("invalid selection"))
		return
	}
	type source struct {
		root openedRoot
		p    string
	}
	sources := []source{}
	names := map[string]bool{}
	for _, p := range paths {
		original, relative, err := s.resolve(api.Location{Root: r.URL.Query().Get("root"), Path: p})
		if err != nil {
			peerError(w, 403, err)
			return
		}
		info, err := original.handle.Lstat(relative)
		if err != nil {
			peerError(w, 404, err)
			return
		}
		if info.Mode()&os.ModeSymlink != 0 {
			peerError(w, 400, errors.New("symbolic links cannot be exported"))
			return
		}
		root, rel, err := s.peerResolve(r.URL.Query().Get("root"), p)
		if err != nil {
			peerError(w, 403, err)
			return
		}
		name := filepath.Base(rel)
		if rel == "." || names[name] {
			peerError(w, 400, errors.New("duplicate or invalid export name"))
			return
		}
		names[name] = true
		sources = append(sources, source{root, rel})
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Trailer", "X-Export-Complete")
	w.WriteHeader(200)
	tw := tar.NewWriter(w)
	for _, src := range sources {
		if err := s.peerTar(r.Context(), tw, src.root, src.p, filepath.Base(src.p)); err != nil {
			return
		}
	}
	if tw.Close() == nil {
		w.Header().Set("X-Export-Complete", "1")
	}
}
func (s *Service) peerTar(ctx context.Context, tw *tar.Writer, root openedRoot, p, name string) error {
	if strings.Count(name, "/") > 128 {
		return errors.New("directory nesting exceeds export limit")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.hidden(root.config.ID, filepath.ToSlash(p)) {
		return errors.New("protected path in export")
	}
	info, err := root.handle.Lstat(p)
	if err != nil {
		return err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return errors.New("symbolic links and special files cannot be exported")
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(name)
	if err = tw.WriteHeader(header); err != nil {
		return err
	}
	if !info.IsDir() {
		f, err := openReadFile(root.handle, p)
		if err != nil {
			return err
		}
		defer f.Close()
		now, err := f.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(info, now) || !now.Mode().IsRegular() {
			return errors.New("file changed during export")
		}
		remaining := info.Size()
		buf := make([]byte, 64<<10)
		for remaining > 0 {
			if err = ctx.Err(); err != nil {
				return err
			}
			n, e := io.ReadFull(f, buf[:min(int64(len(buf)), remaining)])
			if e != nil {
				return e
			}
			if _, e = tw.Write(buf[:n]); e != nil {
				return e
			}
			remaining -= int64(n)
		}
		now, err = f.Stat()
		if err != nil {
			return err
		}
		if now.Size() != info.Size() || !now.ModTime().Equal(info.ModTime()) {
			return errors.New("file changed during export")
		}
		return nil
	}
	dir, err := root.handle.OpenRoot(p)
	if err != nil {
		return err
	}
	defer dir.Close()
	now, err := dir.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(info, now) {
		return errors.New("directory changed during export")
	}
	f, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	// Keep the original root for policy checks and resolve each entry within it.
	for {
		entries, e := f.ReadDir(128)
		for _, entry := range entries {
			if err = s.peerTar(ctx, tw, root, filepath.Join(p, entry.Name()), path.Join(name, entry.Name())); err != nil {
				return err
			}
		}
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
	}
}
