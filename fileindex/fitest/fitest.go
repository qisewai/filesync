// Package fitest is a fileindex server for tests: it speaks the part of the
// API FileSync uses, over two roots on directories of the test.
package fitest

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
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
	Expired atomic.Bool
	// Writable makes root 2 writable, as when its read-only setting is
	// turned off.
	Writable atomic.Bool
	// StopAfter makes the server stop reading a stream after that many
	// entries, as when the connection breaks, if not zero.
	StopAfter atomic.Int32
	// HoldAnswers makes the server answer only once the whole stream is in:
	// a client waiting for answers before sending more would never end.
	HoldAnswers atomic.Bool
	// Streams counts the tar streams received.
	Streams atomic.Int32

	srv *httptest.Server
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
	s := &Server{Root: t.TempDir(), ReadOnly: t.TempDir(), Trash: t.TempDir()}
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
	api.HandleFunc("GET /api/roots", s.roots)
	api.HandleFunc("GET /api/roots/{id}/list", s.list)
	api.HandleFunc("POST /api/fs/mkdir", s.mkdir)
	api.HandleFunc("GET /api/backup/manifest", s.manifest)
	api.HandleFunc("POST /api/backup/tar", s.tar)
	api.HandleFunc("POST /api/backup/file", s.file)
	api.HandleFunc("POST /api/fs/delete", s.delete)
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) || r.Method != "GET" && r.Header.Get("X-Requested-With") != "fileindex" {
			s.fail(w, 401, "未登录")
			return
		}
		api.ServeHTTP(w, r)
	}))
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
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
	switch id {
	case "1":
		return s.Root, true
	case "2":
		return s.ReadOnly, true
	}
	return "", false
}

func (s *Server) roots(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]any{"roots": []map[string]any{
		{"id": 1, "name": "nas", "path": "/data/nas", "readonly": false},
		{"id": 2, "name": "只读", "path": "/data/ro", "readonly": !s.Writable.Load()},
	}})
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
	type entry struct {
		Name  string `json:"name"`
		IsDir bool   `json:"is_dir"`
	}
	items := []entry{}
	for _, e := range ents {
		items = append(items, entry{e.Name(), e.IsDir()})
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

func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths []string `json:"paths"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	for _, p := range req.Paths {
		dst := filepath.Join(s.Trash, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := os.Rename(filepath.Join(s.Root, filepath.FromSlash(p)), dst); err != nil {
			s.fail(w, 400, err.Error())
			return
		}
	}
	fmt.Fprint(w, `{"trashed":1}`)
}
