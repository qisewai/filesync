// Package service is the common file service. The caller supplies transport,
// authentication and root configuration; filesystem paths stay in this process.
package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"unicode/utf8"

	"filemanager/api"
	fmfs "filemanager/fs"
)

type openedRoot struct {
	config api.Root
	handle *os.Root
}
type Service struct {
	desktop  bool // Explicit local desktop transport; never set for network services.
	Metadata *fmfs.CreationPolicy
	// BeforeChange starts the embedding application’s index bridge. The returned
	// callback runs after the operation, including failures with partial changes.
	BeforeChange func([]api.Location) (func(), error)
	backups      *backupService
	indexes      *indexService
	tasks        *taskManager
	desktopTrash bool
	roots        map[string]openedRoot
	writer       *mutationWriter
	// HiddenPath lets the embedding application protect its bookkeeping and
	// preserve configured exclusions. Set it before serving any requests.
	HiddenPath func(rootID, relativePath string) bool
}

func New(roots []api.Root) (*Service, error) {
	s := &Service{roots: make(map[string]openedRoot)}
	for _, r := range roots {
		if r.ID == "" {
			s.Close()
			return nil, fmt.Errorf("empty root id")
		}
		if _, ok := s.roots[r.ID]; ok {
			s.Close()
			return nil, fmt.Errorf("duplicate root id: %s", r.ID)
		}
		abs, err := filepath.Abs(r.Path)
		if err != nil {
			s.Close()
			return nil, err
		}
		abs, err = filepath.EvalSymlinks(abs)
		if err != nil {
			s.Close()
			return nil, err
		}
		h, err := os.OpenRoot(abs)
		if err != nil {
			s.Close()
			return nil, err
		}
		r.Path = abs
		s.roots[r.ID] = openedRoot{r, h}
	}
	return s, nil
}

// Close is called only after serving requests has stopped.
func (s *Service) Close() {
	s.StopTasks()
	s.stopIndexes()
	if s.writer != nil {
		s.writer.log.Close()
	}
	for _, r := range s.roots {
		r.handle.Close()
	}
}

func relative(p string) (string, error) {
	if p == "" || p == "." {
		return ".", nil
	}
	if strings.ContainsRune(p, 0) || !validRelative(p) || !filepath.IsLocal(filepath.FromSlash(p)) {
		return "", &api.Error{Code: "invalid_path", Message: "path must be root-relative"}
	}
	return filepath.FromSlash(p), nil
}

// fs.ValidPath requires UTF-8; Unix filesystem paths do not. Keep its segment
// constraints without changing arbitrary filename bytes.
func validRelative(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func (s *Service) resolve(loc api.Location) (openedRoot, string, error) {
	r, ok := s.roots[loc.Root]
	if !ok {
		return r, "", &api.Error{Code: "unknown_root", Message: "unknown root"}
	}
	if loc.PathBytes != nil && loc.Path != "" && loc.Path != string(loc.PathBytes) {
		return r, "", &api.Error{Code: "invalid_path", Message: "conflicting path encodings"}
	}
	p, err := relative(loc.NativePath())
	if err == nil && s.hidden(loc.Root, filepath.ToSlash(p)) {
		return r, "", &api.Error{Code: "permission_denied", Message: "protected path"}
	}
	return r, p, err
}

func (s *Service) hidden(rootID, p string) bool {
	return s.HiddenPath != nil && s.HiddenPath(rootID, p)
}

// Resolve read-only aliases, then use a root-relative handle for the final
// access. Absolute symlinks inside the root are allowed; escapes are rejected.
func readPath(root openedRoot, p string, followLeaf bool) (string, error) {
	base := p
	if !followLeaf {
		base = filepath.Dir(p)
	}
	real, err := filepath.EvalSymlinks(filepath.Join(root.config.Path, base))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root.config.Path, real)
	if err != nil || !filepath.IsLocal(rel) {
		return "", &api.Error{Code: "permission_denied", Message: "symlink escapes root"}
	}
	if !followLeaf {
		rel = filepath.Join(rel, filepath.Base(p))
	}
	return rel, nil
}

func (s *Service) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /v1/info", func(w http.ResponseWriter, r *http.Request) {
		caps := []string{"list", "stat", "read", "tally"}
		if s.backups != nil {
			caps = append(caps, "local_backup", "remote_coordinator")
		}
		if s.indexes != nil {
			caps = append(caps, "local_index")
		}
		if s.tasks != nil {
			caps = append(caps, "transfer_tasks")
			if s.unrestrictedLocal() {
				caps = append(caps, "local_transfer_tasks")
			}
		}
		if s.writer != nil {
			caps = append(caps, "mkdir", "rename", "delete")
			if s.desktopTrash {
				caps = append(caps, "trash")
			}
		}
		info := api.Info{Version: api.Version, Capabilities: caps}
		if s.indexes != nil {
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			m := &api.MemoryInfo{HeapAlloc: mem.HeapAlloc, HeapInuse: mem.HeapInuse, HeapIdle: mem.HeapIdle, HeapReleased: mem.HeapReleased, Limit: debug.SetMemoryLimit(-1)}
			s.indexes.mu.Lock()
			m.QueryResults, m.IndexSessions = len(s.indexes.results), len(s.indexes.sessions)
			s.indexes.mu.Unlock()
			info.Memory = m
		}
		write(w, info)
	})
	m.HandleFunc("GET /v1/roots", func(w http.ResponseWriter, r *http.Request) {
		roots := make([]api.Root, 0, len(s.roots))
		for _, v := range s.roots {
			roots = append(roots, v.config)
		}
		slices.SortFunc(roots, func(a, b api.Root) int { return strings.Compare(a.ID, b.ID) })
		write(w, roots)
	})
	m.HandleFunc("POST /v1/list", s.list)
	m.HandleFunc("POST /v1/stat", s.stat)
	m.HandleFunc("POST /v1/read", s.read)
	m.HandleFunc("POST /v1/tally", s.tally)
	m.HandleFunc("POST /v1/mutate", s.mutate)
	m.HandleFunc("POST /v1/index", s.indexCommand)
	m.HandleFunc("POST /v1/backup", s.backupCommand)
	m.HandleFunc("POST /v1/remote", s.remoteCommand)
	m.HandleFunc("POST /v1/transfers", s.submitTransfer)
	m.HandleFunc("GET /v1/tasks/{id}", s.getTask)
	m.HandleFunc("POST /v1/tasks/{id}/cancel", s.cancelTask)
	return m
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, &api.Error{Code: "invalid_request", Message: err.Error()})
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		fail(w, &api.Error{Code: "invalid_request", Message: "expected one JSON value"})
		return false
	}
	return true
}
func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, err error) {
	e := &api.Error{Code: "io", Message: err.Error()}
	status := http.StatusInternalServerError
	var known *api.Error
	if errors.As(err, &known) {
		e = known
		status = http.StatusBadRequest
		switch e.Code {
		case "permission_denied":
			status = http.StatusForbidden
		case "unavailable":
			status = http.StatusServiceUnavailable
		case "outcome_unknown":
			status = http.StatusInternalServerError
		case "conflict":
			status = http.StatusConflict
		case "unsupported":
			status = http.StatusNotImplemented
		case "unknown_root":
			status = http.StatusNotFound
		}
	}
	if errors.Is(err, fs.ErrNotExist) {
		e.Code = "not_found"
		status = http.StatusNotFound
	}
	if errors.Is(err, fs.ErrExist) {
		e.Code = "conflict"
		status = http.StatusConflict
	}
	if errors.Is(err, fs.ErrPermission) {
		e.Code = "permission_denied"
		status = http.StatusForbidden
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(e)
}
func entry(root openedRoot, p string, fi fs.FileInfo) api.Entry {
	link := fi.Mode()&os.ModeSymlink != 0
	dir := fi.IsDir()
	if link {
		if resolved, err := readPath(root, p, true); err == nil {
			if target, err := root.handle.Stat(resolved); err == nil {
				dir = target.IsDir()
			}
		}
	}
	e := api.Entry{Location: api.Location{Root: root.config.ID, Path: filepath.ToSlash(p)}.Wire(), Name: fi.Name(), Size: fi.Size(), MTime: fi.ModTime().Unix(), IsDir: dir, Link: link}
	if !utf8.ValidString(e.Name) {
		e.NameBytes = []byte(e.Name)
		e.Name = ""
	}
	return e
}
func (s *Service) stat(w http.ResponseWriter, req *http.Request) {
	var loc api.Location
	if !decode(w, req, &loc) {
		return
	}
	r, p, err := s.resolve(loc)
	if err != nil {
		fail(w, err)
		return
	}
	resolved, err := readPath(r, p, false)
	if err != nil {
		fail(w, err)
		return
	}
	if s.hidden(loc.Root, filepath.ToSlash(resolved)) {
		fail(w, &api.Error{Code: "permission_denied", Message: "protected path"})
		return
	}
	fi, err := r.handle.Lstat(resolved)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, entry(r, p, fi))
}
func (s *Service) list(w http.ResponseWriter, req *http.Request) {
	var q api.ListRequest
	if !decode(w, req, &q) {
		return
	}
	r, p, err := s.resolve(q.Location)
	if err != nil {
		fail(w, err)
		return
	}
	resolved, err := readPath(r, p, true)
	if err != nil {
		fail(w, err)
		return
	}
	if s.hidden(q.Root, filepath.ToSlash(resolved)) {
		fail(w, &api.Error{Code: "permission_denied", Message: "protected path"})
		return
	}
	f, err := r.handle.Open(resolved)
	if err != nil {
		fail(w, err)
		return
	}
	defer f.Close()
	out := make([]api.Entry, 0)
	for {
		if req.Context().Err() != nil {
			return
		}
		ds, readErr := f.ReadDir(256)
		for _, d := range ds {
			if !q.Hidden && strings.HasPrefix(d.Name(), ".") {
				continue
			}
			name := path.Join(filepath.ToSlash(p), d.Name())
			if s.hidden(q.Root, name) {
				continue
			}
			actual := filepath.Join(resolved, d.Name())
			if s.hidden(q.Root, filepath.ToSlash(actual)) {
				continue
			}
			fi, err := r.handle.Lstat(actual)
			// A child may disappear or deny metadata access (for example Windows
			// compatibility junctions). Neither should hide its readable siblings.
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) || metadataLocked(err) {
				continue
			}
			if err != nil {
				fail(w, err)
				return
			}
			if !q.Hidden && hiddenFileInfo(fi) {
				continue
			}
			e := entry(r, filepath.FromSlash(name), fi)
			if !q.DirsOnly || e.IsDir {
				out = append(out, e)
				if q.Probe {
					write(w, out)
					return
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			fail(w, readErr)
			return
		}
	}
	slices.SortStableFunc(out, func(a, b api.Entry) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	write(w, out)
}

// unrestrictedLocal gates APIs which accept absolute paths, independently of
// the transport used by the caller. Windows desktop roots span attached drives.
func (s *Service) unrestrictedLocal() bool {
	if s.HiddenPath != nil {
		return false
	}
	if s.desktop {
		return true
	}
	if len(s.roots) != 1 {
		return false
	}
	for _, r := range s.roots {
		if r.config.Path != "/" || r.config.ReadOnly {
			return false
		}
	}
	return true
}
