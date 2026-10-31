package fitest

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	archiveio "filemanager/archive"
)

func (s *Server) extractArchive(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Root          int64 `json:"root"`
		DstRoot       int64 `json:"dst_root"`
		Path, Version string
		Entries       []string
		DstDir        string `json:"dst_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.fail(w, 400, err.Error())
		return
	}

	s.ArchiveExtracts.Add(1)
	src, _ := s.rootDir(strconv.FormatInt(req.Root, 10))
	dst, _ := s.rootDir(strconv.FormatInt(req.DstRoot, 10))
	filename := filepath.Join(src, filepath.FromSlash(req.Path))
	idx, err := (&archiveio.Cache{}).Load(r.Context(), filename)
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	if len(req.Entries) == 0 {
		entries, _ := idx.List("")
		for _, e := range entries {
			req.Entries = append(req.Entries, e.Path)
		}
	}
	selected, total, err := idx.Selection(req.Entries)
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	err = idx.Extract(r.Context(), filename, selected, func(e archiveio.Entry, body io.Reader) error {
		target := filepath.Join(dst, filepath.FromSlash(req.DstDir), filepath.FromSlash(e.Path))
		if e.IsDir {
			return os.MkdirAll(target, 0755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		data, err := io.ReadAll(body)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	id := s.tasks.Add(1)
	task := map[string]any{"id": id, "kind": "extract", "status": "queued", "total_bytes": total}
	s.mu.Lock()
	s.taskRecords[id] = task
	s.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{"task": task})
	go func() {
		time.Sleep(20 * time.Millisecond)
		done := map[string]any{"id": id, "kind": "extract", "status": "done", "finished_at": time.Now().Unix(), "done_bytes": total, "total_bytes": total}
		s.mu.Lock()
		if s.taskRecords[id]["status"] == "canceled" {
			s.mu.Unlock()
			return
		}
		s.taskRecords[id] = done
		s.mu.Unlock()
		s.publish(map[string]any{"type": "task", "task": done})
		s.publish(map[string]any{"type": "fs", "root_id": req.DstRoot})
	}()
}
