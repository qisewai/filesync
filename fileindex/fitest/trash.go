package fitest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type trashItem struct {
	ID        int64  `json:"id"`
	RootID    int64  `json:"root_id"`
	RootName  string `json:"root_name"`
	OrigPath  string `json:"orig_path"`
	Name      string `json:"name"`
	IsDir     bool   `json:"is_dir"`
	Size      int64  `json:"size"`
	DeletedAt int64  `json:"deleted_at"`
	Disk      string `json:"-"`
}

func (s *Server) listTrash(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := []trashItem{}
	for _, it := range s.trashItems {
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	json.NewEncoder(w).Encode(items)
}
func (s *Server) requestedTrash(w http.ResponseWriter, r *http.Request) ([]trashItem, bool) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []trashItem
	for _, id := range req.IDs {
		it, ok := s.trashItems[id]
		if !ok {
			s.fail(w, 404, "不存在")
			return nil, false
		}
		if it.RootID == 2 && !s.Writable.Load() {
			s.fail(w, 403, "只读")
			return nil, false
		}
		out = append(out, it)
	}
	return out, true
}
func (s *Server) restoreTrash(w http.ResponseWriter, r *http.Request) {
	items, ok := s.requestedTrash(w, r)
	if !ok {
		return
	}
	restored := []string{}
	for _, it := range items {
		dir := s.Root
		if it.RootID == 2 {
			dir = s.ReadOnly
		}
		rel := it.OrigPath
		dest := filepath.Join(dir, filepath.FromSlash(rel))
		for n := 1; ; n++ {
			if _, err := os.Stat(dest); os.IsNotExist(err) {
				break
			}
			rel = it.OrigPath + fmt.Sprintf(" (%d)", n)
			dest = filepath.Join(dir, filepath.FromSlash(rel))
		}
		os.MkdirAll(filepath.Dir(dest), 0755)
		if err := os.Rename(it.Disk, dest); err != nil {
			s.fail(w, 400, err.Error())
			return
		}
		s.mu.Lock()
		delete(s.trashItems, it.ID)
		s.mu.Unlock()
		restored = append(restored, rel)
		s.publish(map[string]any{"type": "fs", "root_id": it.RootID})
	}
	s.publish(map[string]any{"type": "trash"})
	json.NewEncoder(w).Encode(map[string]any{"restored": restored})
}
func (s *Server) purgeTrash(w http.ResponseWriter, r *http.Request) {
	items, ok := s.requestedTrash(w, r)
	if !ok {
		return
	}
	for _, it := range items {
		os.RemoveAll(it.Disk)
		s.mu.Lock()
		delete(s.trashItems, it.ID)
		s.mu.Unlock()
	}
	id := s.tasks.Add(1)
	task := map[string]any{"id": id, "kind": "purge", "status": "queued", "total_items": len(items)}
	s.mu.Lock()
	s.taskRecords[id] = task
	s.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{"task": task})
	time.AfterFunc(20*time.Millisecond, func() {
		done := map[string]any{"id": id, "kind": "purge", "status": "done", "total_items": len(items), "done_items": len(items), "finished_at": time.Now().Unix()}
		s.mu.Lock()
		s.taskRecords[id] = done
		s.mu.Unlock()
		s.publish(map[string]any{"type": "task", "task": done})
		s.publish(map[string]any{"type": "trash"})
	})
}
