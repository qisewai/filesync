// Package fitest is a fileindex server for tests: it speaks the part of the
// API FileSync uses, over two roots on directories of the test.
package fitest

import (
	"archive/tar"
	"cmp"
	"encoding/json"
	archiveio "filesync/internal/archive"
	"fmt"
	"hash/fnv"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Password is the password the server takes.
const Password = "pw"

const (
	cookieName = "fi_session"
	token      = "tok"
)

// Server is a fake fileindex.
type Server struct {
	// Root is the directory of root 1, "nas", and ReadOnly that of root 2,
	// "只读", which cannot be written.
	Root, ReadOnly string
	// Trash receives what is deleted, at its path from the root.
	Trash string
	// Expired makes the server refuse sessions until the next login.
	Expired          atomic.Bool
	HoldArchiveLists atomic.Bool
	ArchiveExtracts  atomic.Int64
	// Writable makes root 2 writable, as when its read-only setting is
	// turned off.
	Writable atomic.Bool
	// StopAfter makes the server stop reading a stream after that many
	// entries, as when the connection breaks, if not zero.
	StopAfter atomic.Int32
	// HoldAnswers makes the server answer only once the whole stream is in:
	// a client waiting for answers before sending more would never end.
	HoldAnswers atomic.Bool
	// OldServer answers as fileindex did before telling whether the
	// directories listed hold directories.
	OldServer atomic.Bool
	// Streams counts the tar streams received, and Searches the pages of
	// searches answered.
	Streams        atomic.Int32
	Searches       atomic.Int32
	DropTaskEvents atomic.Bool
	HoldScans      atomic.Bool

	srv          *httptest.Server
	tasks        atomic.Int64
	mu           sync.Mutex
	subs         map[chan []byte]bool
	taskRecords  map[int64]map[string]any
	trashItems   map[int64]trashItem
	trashSeq     int64
	ManageData   string
	managerRoots map[int64]managerRoot
	scanRecords  map[int64][]scanJob
	scanSeq      int64
	dupesLastRun int64
}

type manifestEntry struct {
	Path  string `json:"p"`
	Dir   bool   `json:"d,omitempty"`
	Size  int64  `json:"s,omitempty"`
	MTime int64  `json:"m,omitempty"`
	Link  string `json:"l,omitempty"`
}

// New starts a server, which stops with the test.
func New(t testing.TB) *Server {
	s := &Server{ManageData: t.TempDir(), managerRoots: map[int64]managerRoot{}, scanRecords: map[int64][]scanJob{}, Root: t.TempDir(), ReadOnly: t.TempDir(), Trash: t.TempDir(), subs: map[chan []byte]bool{}, taskRecords: map[int64]map[string]any{}, trashItems: map[int64]trashItem{}}
	s.initManagement()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"setup":false,"authed":%v}`, s.authed(r))
	})
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Password string }
		json.NewDecoder(r.Body).Decode(&req)
		if req.Password != Password {
			s.fail(w, 401, "密码错误")
			return
		}
		s.Expired.Store(false)
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token})
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	})
	api := http.NewServeMux()
	s.managementRoutes(api)
	api.HandleFunc("GET /api/roots", s.roots)
	api.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		last := s.dupesLastRun
		s.mu.Unlock()
		fmt.Fprintf(w, `{"thumb_exts":["jpg","png"],"dupes_last_run":%d}`, last)
	})
	api.HandleFunc("GET /api/trash", s.listTrash)
	api.HandleFunc("POST /api/trash/restore", s.restoreTrash)
	api.HandleFunc("POST /api/trash/delete", s.purgeTrash)
	api.HandleFunc("GET /api/search", s.search)
	api.HandleFunc("GET /api/thumb/{id}", s.thumb)
	api.HandleFunc("GET /api/download/{id}", s.download)
	archives := &archiveio.Handler{Resolve: func(r *http.Request) (string, error) {
		base, ok := s.rootDir(r.URL.Query().Get("root"))
		if !ok {
			return "", fmt.Errorf("no root")
		}
		rel, err := archiveio.Clean(r.URL.Query().Get("path"))
		if err != nil {
			return "", err
		}
		return filepath.Join(base, filepath.FromSlash(rel)), nil
	}}
	api.HandleFunc("GET /api/archive/list", func(w http.ResponseWriter, r *http.Request) {
		if s.HoldArchiveLists.Load() {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Write([]byte("{\"read\":0,\"size\":100}\n"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		archives.ServeHTTP(w, r)
	})
	api.Handle("GET /api/archive/file", archives)
	api.Handle("GET /api/archive/export", archives)
	api.HandleFunc("POST /api/archive/extract", s.extractArchive)
	api.HandleFunc("GET /api/backup/export", s.export)
	api.HandleFunc("POST /api/fs/rename", s.rename)
	api.HandleFunc("POST /api/fs/transfer", s.transfer)
	api.HandleFunc("GET /api/events", s.events)
	api.HandleFunc("GET /api/tasks", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := []map[string]any{}
		for _, task := range s.taskRecords {
			out = append(out, task)
		}
		json.NewEncoder(w).Encode(out)
	})
	api.HandleFunc("POST /api/tasks/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		task := map[string]any{"id": id, "status": "canceled", "finished_at": time.Now().Unix()}
		s.mu.Lock()
		s.taskRecords[id] = task
		s.mu.Unlock()
		s.publish(map[string]any{"type": "task", "task": task})
		fmt.Fprint(w, `{}`)
	})
	api.HandleFunc("GET /api/roots/{id}/list", s.list)
	api.HandleFunc("POST /api/fs/mkdir", s.mkdir)
	api.HandleFunc("GET /api/backup/manifest", s.manifest)
	api.HandleFunc("POST /api/backup/tar", s.tar)
	api.HandleFunc("POST /api/backup/file", s.file)
	api.HandleFunc("POST /api/fs/delete", s.delete)
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) || r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("X-Requested-With") != "fileindex" {
			s.fail(w, 401, "未登录")
			return
		}
		api.ServeHTTP(w, r)
	}))
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

// Entry is an entry of a listing or a search.
type Entry struct {
	ID       int64  `json:"id"`
	Root     int64  `json:"root_id"`
	RootName string `json:"root_name"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Parent   string `json:"parent"`
	IsDir    bool   `json:"is_dir"`
	Size     int64  `json:"size"`
	MTime    int64  `json:"mtime"`
	Ext      string `json:"ext"`
	HasDirs  *bool  `json:"has_dirs,omitempty"`
}

func (s *Server) entry(root int64, rel string, info os.FileInfo) Entry {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d/%s", root, rel)
	s.mu.Lock()
	rootName := s.managerRoots[root].Name
	s.mu.Unlock()
	e := Entry{RootName: rootName, ID: int64(h.Sum64() >> 1), Root: root, Name: info.Name(), Path: rel, Parent: path.Dir(rel), IsDir: info.IsDir(), MTime: info.ModTime().Unix()}
	if e.Parent == "." {
		e.Parent = ""
	}
	if !e.IsDir {
		e.Size = info.Size()
		e.Ext = strings.TrimPrefix(strings.ToLower(path.Ext(rel)), ".")
	}
	return e
}

// Addr is the server's host and port.
func (s *Server) Addr() string { return strings.TrimPrefix(s.srv.URL, "http://") }

func (s *Server) authed(r *http.Request) bool {
	ck, err := r.Cookie(cookieName)
	return err == nil && ck.Value == token && !s.Expired.Load()
}

func (s *Server) fail(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *Server) rootDir(id string) (string, bool) {
	rootID, _ := strconv.ParseInt(id, 10, 64)
	s.mu.Lock()
	root, ok := s.managerRoots[rootID]
	s.mu.Unlock()
	return root.Disk, ok
}
func (s *Server) roots(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]any{"roots": s.managedRoots(), "data_roots": []string{"/data"}})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	dir, ok := s.rootDir(r.PathValue("id"))
	if !ok {
		s.fail(w, 404, "不存在")
		return
	}
	ents, err := os.ReadDir(filepath.Join(dir, filepath.FromSlash(r.URL.Query().Get("path"))))
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	items := []Entry{}
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue
		}
		it := s.entry(id, path.Join(r.URL.Query().Get("path"), e.Name()), info)
		if it.IsDir && !s.OldServer.Load() {
			// As the index tells: whether it holds directories.
			sub, _ := os.ReadDir(filepath.Join(dir, filepath.FromSlash(it.Path)))
			has := slices.ContainsFunc(sub, func(d os.DirEntry) bool { return d.IsDir() })
			it.HasDirs = &has
		}
		items = append(items, it)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].IsDir && !items[j].IsDir })
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 1)
	if size <= 0 {
		size = 100
	}
	from, to := min((page-1)*size, len(items)), min(page*size, len(items))
	json.NewEncoder(w).Encode(map[string]any{"items": items[from:to], "total": len(items)})
}

func (s *Server) mkdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Root int64  `json:"root"`
		Dir  string `json:"dir"`
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	dir, _ := s.rootDir(strconv.FormatInt(req.Root, 10))
	if req.Root != 1 {
		s.fail(w, 403, "只读")
		return
	}
	if err := os.Mkdir(filepath.Join(dir, filepath.FromSlash(req.Dir), req.Name), 0o755); err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	fmt.Fprint(w, `{}`)
}

func (s *Server) manifest(w http.ResponseWriter, r *http.Request) {
	dir, _ := s.rootDir(r.URL.Query().Get("root"))
	base := filepath.Join(dir, filepath.FromSlash(r.URL.Query().Get("path")))
	if _, err := os.Stat(base); err != nil {
		s.fail(w, 404, "目录不存在")
		return
	}
	enc := json.NewEncoder(w)
	filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == base {
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		info, _ := d.Info()
		e := manifestEntry{Path: filepath.ToSlash(rel), Dir: d.IsDir(), MTime: info.ModTime().UnixNano()}
		switch {
		case d.Type()&os.ModeSymlink != 0:
			e.Link, _ = os.Readlink(p)
		case !d.IsDir():
			e.Size = info.Size()
		}
		return enc.Encode(e)
	})
}

// tar unpacks a tar stream, answering each entry as fileindex does,
// while the stream still comes.
func (s *Server) tar(w http.ResponseWriter, r *http.Request) {
	s.Streams.Add(1)
	if r.URL.Query().Get("root") != "1" {
		s.fail(w, 403, "只读")
		return
	}
	http.NewResponseController(w).EnableFullDuplex()
	w.WriteHeader(200)
	enc := json.NewEncoder(w)
	var held []map[string]string
	answer := func(p string, err error) {
		a := map[string]string{"p": p}
		if err != nil {
			a["e"] = err.Error()
		}
		if s.HoldAnswers.Load() {
			held = append(held, a)
			return
		}
		enc.Encode(a)
		http.NewResponseController(w).Flush()
	}
	defer func() {
		for _, a := range held {
			enc.Encode(a)
		}
	}()
	base := filepath.Join(s.Root, filepath.FromSlash(r.URL.Query().Get("path")))
	tr := tar.NewReader(r.Body)
	last := ""
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			held = append(held, map[string]string{"end": "", "error": err.Error()})
			return
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == ".fileindex-backup-discard" {
			p := h.PAXRecords["FILEINDEX.discard"]
			if p == last {
				os.Remove(filepath.Join(base, filepath.FromSlash(p)))
			}
			answer(p, fmt.Errorf("已丢弃"))
			continue
		}
		dst := filepath.Join(base, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(dst), 0o755)
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(dst, 0o755)
		case tar.TypeSymlink:
			os.Remove(dst)
			err = os.Symlink(h.Linkname, dst)
		default:
			var out *os.File
			if out, err = os.Create(dst); err == nil {
				_, err = io.Copy(out, tr)
				out.Close()
				os.Chtimes(dst, h.ModTime, h.ModTime)
			}
		}
		last = name
		answer(name, err)
		if n := s.StopAfter.Load(); n > 0 {
			if s.StopAfter.Add(-1) == 0 {
				return // no end: the stream broke
			}
		}
	}
	held = append(held, nil) // the end, written below
	for _, a := range held {
		if a == nil {
			enc.Encode(map[string]any{"end": true})
		} else if _, end := a["end"]; end {
			enc.Encode(map[string]any{"end": true, "error": a["error"]})
		} else {
			enc.Encode(a)
		}
	}
	held = nil
}

// file saves the body as a file, as fileindex saves archives.
func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("root") != "1" {
		s.fail(w, 403, "只读")
		return
	}
	dir := filepath.Join(s.Root, filepath.FromSlash(q.Get("path")))
	os.MkdirAll(dir, 0o755)
	out, err := os.Create(filepath.Join(dir, q.Get("name")))
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	n, err := io.Copy(out, r.Body)
	out.Close()
	if sent := r.Trailer.Get("X-Backup-Size"); err == nil && sent != strconv.FormatInt(n, 10) {
		err = fmt.Errorf("文件不完整：发送了 %q 字节，收到 %d 字节", sent, n)
	}
	if err != nil {
		os.Remove(out.Name())
		s.fail(w, 400, err.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"path": path.Join(q.Get("path"), q.Get("name")), "size": n})
}

// categories are the kinds of files the search filters by, as fileindex
// has them, in part.
var categories = map[string][]string{
	"image":   {"jpg", "jpeg", "png", "gif", "webp"},
	"video":   {"mp4", "mkv", "mov"},
	"audio":   {"mp3", "flac", "wav"},
	"doc":     {"pdf", "doc", "docx", "txt", "md"},
	"archive": {"zip", "rar", "7z", "tar", "gz"},
	"disk":    {"iso", "img", "vmdk", "qcow2"},
	"code":    {"go", "py", "js", "ts"},
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	q := strings.ToLower(v.Get("q"))
	roots := []int64{1, 2}
	if id, err := strconv.ParseInt(v.Get("root"), 10, 64); err == nil && id != 0 {
		roots = []int64{id}
	}
	below := strings.Trim(v.Get("dir"), "/")
	exts := map[string]bool{}
	for _, e := range strings.Split(v.Get("ext"), ",") {
		if e != "" {
			exts[strings.ToLower(e)] = true
		}
	}
	for _, e := range categories[v.Get("cat")] {
		exts[e] = true
	}
	bound := func(name string) int64 {
		n, _ := strconv.ParseInt(v.Get(name), 10, 64)
		return n
	}
	minSize, maxSize, since, until := bound("min"), bound("max"), bound("from"), bound("to")
	keep := func(e Entry) bool {
		switch {
		case v.Get("type") == "file" && e.IsDir, v.Get("type") == "dir" && !e.IsDir:
			return false
		case len(exts) > 0 && !exts[e.Ext]:
			return false
		case minSize > 0 && e.Size < minSize, maxSize > 0 && e.Size > maxSize:
			return false
		case since > 0 && e.MTime < since, until > 0 && e.MTime > until:
			return false
		}
		return true
	}
	items := []Entry{}
	for _, id := range roots {
		dir, _ := s.rootDir(strconv.FormatInt(id, 10))
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || p == dir || !strings.Contains(strings.ToLower(d.Name()), q) {
				return nil
			}
			info, _ := d.Info()
			rel, _ := filepath.Rel(dir, p)
			rel = filepath.ToSlash(rel)
			if below != "" && !strings.HasPrefix(rel, below+"/") {
				return nil
			}
			if e := s.entry(id, rel, info); keep(e) {
				items = append(items, e)
			}
			return nil
		})
	}
	// Ordered as the server does: by name unless asked, then a page.
	slices.SortStableFunc(items, func(a, b Entry) int {
		var c int
		switch v.Get("sort") {
		case "size":
			c = cmp.Compare(a.Size, b.Size)
		case "mtime":
			c = cmp.Compare(a.MTime, b.MTime)
		case "path":
			c = strings.Compare(strings.ToLower(a.Path), strings.ToLower(b.Path))
		default:
			c = strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}
		if v.Get("desc") == "1" {
			c = -c
		}
		return c
	})
	total := len(items)
	size, _ := strconv.Atoi(v.Get("size"))
	if size <= 0 || size > 1000 {
		size = 100
	}
	page, _ := strconv.Atoi(v.Get("page"))
	page = max(page, 1)
	from := min((page-1)*size, total)
	s.Searches.Add(1)
	json.NewEncoder(w).Encode(map[string]any{"items": items[from:min(from+size, total)], "total": total})
}

// abs is the file of ?path= in the root of the path's {id}.
func (s *Server) abs(r *http.Request) (string, bool) {
	dir, ok := s.rootDir(r.PathValue("id"))
	p := path.Clean("/" + r.URL.Query().Get("path"))
	return filepath.Join(dir, filepath.FromSlash(p)), ok
}

func (s *Server) thumb(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.abs(r); !ok {
		s.fail(w, 404, "不存在")
		return
	}
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	w.Header().Set("Content-Type", "image/jpeg")
	jpeg.Encode(w, img, nil)
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	abs, ok := s.abs(r)
	if !ok {
		s.fail(w, 404, "不存在")
		return
	}
	http.ServeFile(w, r, abs)
}

func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	dir, _ := s.rootDir(r.URL.Query().Get("root"))
	w.Header().Set("X-Total-Size", "0")
	tw := tar.NewWriter(w)
	for _, rel := range r.URL.Query()["path"] {
		abs := filepath.Join(dir, filepath.FromSlash(path.Clean("/"+rel)))
		base := filepath.Dir(abs)
		filepath.WalkDir(abs, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			info, _ := d.Info()
			name, _ := filepath.Rel(base, p)
			h := &tar.Header{Name: filepath.ToSlash(name), ModTime: info.ModTime(), Mode: int64(info.Mode().Perm())}
			switch {
			case d.IsDir():
				h.Typeflag, h.Name = tar.TypeDir, h.Name+"/"
				return tw.WriteHeader(h)
			case d.Type()&os.ModeSymlink != 0:
				h.Typeflag, h.Linkname = tar.TypeSymlink, mustReadlink(p)
				return tw.WriteHeader(h)
			}
			h.Typeflag, h.Size = tar.TypeReg, info.Size()
			tw.WriteHeader(h)
			f, _ := os.Open(p)
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		})
	}
	tw.Close()
}

func mustReadlink(p string) string {
	l, _ := os.Readlink(p)
	return l
}

func (s *Server) rename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Root int64  `json:"root"`
		Path string `json:"path"`
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	dir, _ := s.rootDir(strconv.FormatInt(req.Root, 10))
	from := filepath.Join(dir, filepath.FromSlash(req.Path))
	if err := os.Rename(from, filepath.Join(filepath.Dir(from), req.Name)); err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	fmt.Fprint(w, `{}`)
}

// transfer copies or moves at once, and tells it as a task done.
func (s *Server) transfer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Op      string   `json:"op"`
		SrcRoot int64    `json:"src_root"`
		Paths   []string `json:"paths"`
		DstRoot int64    `json:"dst_root"`
		DstDir  string   `json:"dst_dir"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	src, _ := s.rootDir(strconv.FormatInt(req.SrcRoot, 10))
	dst, _ := s.rootDir(strconv.FormatInt(req.DstRoot, 10))
	for _, p := range req.Paths {
		from := filepath.Join(src, filepath.FromSlash(p))
		to := filepath.Join(dst, filepath.FromSlash(req.DstDir), filepath.Base(from))
		var err error
		if req.Op == "move" {
			err = os.Rename(from, to)
		} else {
			err = copyTree(from, to)
		}
		if err != nil {
			s.fail(w, 400, err.Error())
			return
		}
	}
	id := s.tasks.Add(1)
	task := map[string]any{"id": id, "kind": req.Op, "title": req.Op, "status": "queued"}
	s.mu.Lock()
	s.taskRecords[id] = task
	s.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{"task": task})
	go func() {
		time.Sleep(20 * time.Millisecond)
		done := map[string]any{"id": id, "kind": req.Op, "title": req.Op, "status": "done", "finished_at": time.Now().Unix()}
		s.mu.Lock()
		if s.taskRecords[id]["status"] == "canceled" {
			s.mu.Unlock()
			return
		}
		s.taskRecords[id] = done
		s.mu.Unlock()
		if !s.DropTaskEvents.Load() {
			s.publish(map[string]any{"type": "task", "task": done})
		}
		s.publish(map[string]any{"type": "fs", "root_id": req.DstRoot})
	}()
}

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		t := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(t, b, 0o644)
	})
}

func (s *Server) publish(e any) {
	b, _ := json.Marshal(e)
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- b:
		default:
		}
	}
}

// Publish tells the clients listening for events.
func (s *Server) Publish(e any) { s.publish(e) }

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	ch := make(chan []byte, 16)
	s.mu.Lock()
	s.subs[ch] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	http.NewResponseController(w).Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", b)
			http.NewResponseController(w).Flush()
		}
	}
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Root      int64    `json:"root"`
		Paths     []string `json:"paths"`
		Permanent bool     `json:"permanent"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.Root == 2 && !s.Writable.Load() {
		s.fail(w, 403, "只读")
		return
	}
	dir := s.Root
	if req.Root == 2 {
		dir = s.ReadOnly
	}
	if req.Permanent {
		// A task, as the server's, that ends a moment later.
		id := s.tasks.Add(1)
		task := map[string]any{"id": id, "kind": "delete", "title": "删除", "status": "running"}
		s.mu.Lock()
		s.taskRecords[id] = task
		s.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"task": task})
		go func() {
			time.Sleep(20 * time.Millisecond)
			for _, rel := range req.Paths {
				os.RemoveAll(filepath.Join(dir, filepath.FromSlash(rel)))
			}
			done := map[string]any{"id": id, "kind": "delete", "title": "删除", "status": "done", "finished_at": time.Now().Unix()}
			s.mu.Lock()
			s.taskRecords[id] = done
			s.mu.Unlock()
			s.publish(map[string]any{"type": "task", "task": done})
			s.publish(map[string]any{"type": "fs", "root_id": max(req.Root, 1)})
		}()
		return
	}
	for _, rel := range req.Paths {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Stat(abs)
		if err != nil {
			s.fail(w, 400, err.Error())
			return
		}
		s.mu.Lock()
		s.trashSeq++
		id := s.trashSeq
		s.mu.Unlock()
		dst := filepath.Join(s.Trash, filepath.FromSlash(rel))
		if _, err := os.Stat(dst); err == nil {
			dst += fmt.Sprintf(".%d", id)
		}
		os.MkdirAll(filepath.Dir(dst), 0755)
		if err := os.Rename(abs, dst); err != nil {
			s.fail(w, 400, err.Error())
			return
		}
		root := req.Root
		if root == 0 {
			root = 1
		}
		name := "nas"
		if root == 2 {
			name = "只读"
		}
		s.mu.Lock()
		s.trashItems[id] = trashItem{ID: id, RootID: root, RootName: name, OrigPath: rel, Name: info.Name(), IsDir: info.IsDir(), Size: info.Size(), DeletedAt: time.Now().Unix(), Disk: dst}
		s.mu.Unlock()
	}
	s.publish(map[string]any{"type": "trash"})
	fmt.Fprint(w, `{"trashed":1}`)
}
