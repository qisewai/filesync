package service

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"filemanager/api"
)

func (s *Service) read(w http.ResponseWriter, req *http.Request) {
	var q api.ReadRequest
	if !decode(w, req, &q) {
		return
	}
	if q.Offset < 0 || q.Limit < 1 || q.Limit > 64<<20 {
		fail(w, &api.Error{Code: "invalid_request", Message: "invalid read bounds"})
		return
	}
	root, p, err := s.resolve(q.Location)
	if err != nil {
		fail(w, err)
		return
	}
	p, err = readPath(root, p, true)
	if err != nil {
		fail(w, err)
		return
	}
	if s.hidden(q.Root, filepath.ToSlash(p)) {
		fail(w, &api.Error{Code: "permission_denied", Message: "protected path"})
		return
	}
	pre, err := root.handle.Stat(p)
	if err != nil {
		fail(w, err)
		return
	}
	if !pre.Mode().IsRegular() {
		fail(w, &api.Error{Code: "unsupported", Message: "only regular files can be read"})
		return
	}
	f, err := openReadFile(root.handle, p)
	if err != nil {
		fail(w, err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		fail(w, err)
		return
	}
	if !fi.Mode().IsRegular() {
		fail(w, &api.Error{Code: "unsupported", Message: "only regular files can be read"})
		return
	}
	if _, err := f.Seek(q.Offset, io.SeekStart); err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	buf := make([]byte, 64<<10)
	remaining := min(q.Limit, max(0, fi.Size()-q.Offset))
	w.Header().Set("Content-Length", strconv.FormatInt(remaining, 10))
	for remaining > 0 && req.Context().Err() == nil {
		n, err := f.Read(buf[:min(int64(len(buf)), remaining)])
		if n > 0 {
			if _, e := w.Write(buf[:n]); e != nil {
				return
			}
			remaining -= int64(n)
		}
		if err != nil {
			return
		}
	}
}

func (s *Service) tally(w http.ResponseWriter, req *http.Request) {
	var loc api.Location
	if !decode(w, req, &loc) {
		return
	}
	root, p, err := s.resolve(loc)
	if err != nil {
		fail(w, err)
		return
	}
	p, err = readPath(root, p, true)
	if err != nil {
		fail(w, err)
		return
	}
	if s.hidden(loc.Root, filepath.ToSlash(p)) {
		fail(w, &api.Error{Code: "permission_denied", Message: "protected path"})
		return
	}
	fi, err := root.handle.Stat(p)
	if err != nil {
		fail(w, err)
		return
	}
	if !fi.IsDir() {
		fail(w, &api.Error{Code: "invalid_request", Message: "tally requires directory"})
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	var t api.Tally
	enc := json.NewEncoder(w)
	shown := time.Now()
	err = fs.WalkDir(root.handle.FS(), filepath.ToSlash(p), func(path string, e fs.DirEntry, err error) error {
		if err := req.Context().Err(); err != nil {
			return err
		}
		if s.hidden(loc.Root, path) {
			if e != nil && e.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if err != nil {
			t.Unreadable++
			return nil
		}
		if path == filepath.ToSlash(p) {
			return nil
		}
		if e.IsDir() {
			t.Dirs++
		} else {
			t.Files++
			if e.Type().IsRegular() {
				fi, err := e.Info()
				if err != nil {
					t.Unreadable++
				} else {
					t.Size += fi.Size()
				}
			}
		}
		if time.Since(shown) >= 200*time.Millisecond {
			if err := enc.Encode(t); err != nil {
				return err
			}
			http.NewResponseController(w).Flush()
			shown = time.Now()
		}
		return nil
	})
	t.Done = true
	if err != nil {
		t.Error = &api.Error{Code: "io", Message: err.Error()}
	}
	enc.Encode(t)
}
