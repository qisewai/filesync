package archive

import (
	"encoding/json"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"time"
)

// Handler resolves an authorized archive path before consulting the cache.
// Resolve is also called on cache hits, so removed roots are never accessible.
type Handler struct {
	Cache   *Cache
	Resolve func(*http.Request) (string, error)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fail := func(err error) {
		code := http.StatusBadRequest
		if errors.Is(err, fs.ErrNotExist) {
			code = http.StatusNotFound
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	cache := h.Cache
	if cache == nil {
		cache = &defaultCache
	}
	filename, err := h.Resolve(r)
	if err != nil {
		fail(err)
		return
	}
	if r.URL.Path == "/api/archive/list" && r.URL.Query().Get("progress") == "1" {
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.Header().Set("X-Accel-Buffering", "no")
		enc := json.NewEncoder(w)
		report := func(n, total int64) {
			enc.Encode(map[string]any{"read": n, "size": total})
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		report(0, 0)
		idx, err := cache.LoadProgress(r.Context(), filename, report)
		if err != nil {
			enc.Encode(map[string]any{"end": true, "error": err.Error()})
			return
		}
		items, err := idx.List(r.URL.Query().Get("dir"))
		if err != nil {
			enc.Encode(map[string]any{"end": true, "error": err.Error()})
			return
		}
		enc.Encode(map[string]any{"end": true, "items": items, "version": idx.Version, "sequential": idx.Sequential})
		return
	}
	idx, err := cache.Load(r.Context(), filename)
	if err != nil {
		fail(err)
		return
	}
	if ver := r.URL.Query().Get("version"); ver != "" && ver != idx.Version {
		fail(errors.New("压缩包已变化，请重新进入"))
		return
	}
	q := r.URL.Query()
	switch r.URL.Path {
	case "/api/archive/list":
		items, err := idx.List(q.Get("dir"))
		if err != nil {
			fail(err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]any{"items": items, "version": idx.Version, "sequential": idx.Sequential})
	case "/api/archive/file":
		e, err := idx.File(q.Get("entry"))
		if err != nil {
			fail(err)
			return
		}
		limit := int64(0)
		if q.Has("limit") {
			limit, err = strconv.ParseInt(q.Get("limit"), 10, 64)
			if err != nil || limit <= 0 || limit > 32<<20+1 {
				fail(errors.New("预览长度无效"))
				return
			}
		}
		size := e.Size
		if limit > 0 {
			size = min(size, limit)
		}
		w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(e.Name)))
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+url.PathEscape(e.Name))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.Header().Set("Last-Modified", httpTime(e.MTime))
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == "HEAD" {
			return
		}
		if err := idx.Copy(r.Context(), filename, e.Path, w, limit); err != nil {
			panic(http.ErrAbortHandler)
		}
	case "/api/archive/export":
		selected, total, err := idx.Selection(q["entry"])
		if err != nil {
			fail(err)
			return
		}
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Trailer", "X-Export-Complete")
		w.Header().Set("X-Total-Size", strconv.FormatInt(total, 10))
		if r.Method == "HEAD" {
			return
		}
		if err := idx.Export(r.Context(), filename, selected, w); err != nil {
			panic(http.ErrAbortHandler)
		}
		w.Header().Set("X-Export-Complete", "1")
	default:
		http.NotFound(w, r)
	}
}

func httpTime(t int64) string { return time.Unix(t, 0).UTC().Format(http.TimeFormat) }

var defaultCache Cache
