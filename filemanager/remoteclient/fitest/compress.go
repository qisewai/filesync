package fitest

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// UI/API fixture: the real encoders are exercised against the actual backend.
func (s *Server) compressArchive(w http.ResponseWriter, r *http.Request) {
	if s.OldServer.Load() {
		s.fail(w, 404, "not found")
		return
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	select {
	case s.CompressionBodies <- b:
	default:
	}
	var req struct {
		DstRoot int64  `json:"dst_root"`
		DstDir  string `json:"dst_dir"`
		Name    string `json:"name"`
	}
	if json.Unmarshal(b, &req) != nil {
		s.fail(w, 400, "bad request")
		return
	}
	dir, _ := s.rootDir(strconv.FormatInt(req.DstRoot, 10))
	if dir == "" {
		s.fail(w, 400, "bad root")
		return
	}
	id, created := s.tasks.Add(1), time.Now().Unix()
	task := map[string]any{"id": id, "created_at": created, "kind": "compress", "status": "queued", "title": "压缩为「" + req.Name + "」"}
	s.mu.Lock()
	s.taskRecords[id] = task
	s.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{"task": task})
	go func() {
		time.Sleep(30 * time.Millisecond)
		s.mu.Lock()
		if s.taskRecords[id]["status"] == "canceled" {
			s.mu.Unlock()
			return
		}
		target := filepath.Join(dir, req.DstDir, req.Name)
		os.MkdirAll(filepath.Dir(target), 0755)
		os.WriteFile(target, []byte("archive UI fixture"), 0644)
		done := map[string]any{"id": id, "created_at": created, "kind": "compress", "status": "done", "finished_at": time.Now().Unix(), "title": task["title"], "result": map[string]any{"root": req.DstRoot, "path": filepath.ToSlash(filepath.Join(req.DstDir, req.Name)), "size": 18, "skipped": false}}
		s.taskRecords[id] = done
		s.mu.Unlock()
		s.publish(map[string]any{"type": "task", "task": done})
	}()
}
