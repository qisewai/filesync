package fitest

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type scanJob struct {
	ID         int64  `json:"id"`
	RootID     int64  `json:"root_id"`
	Mode       string `json:"mode"`
	Status     string `json:"status"`
	CreatedAt  int64  `json:"created_at"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt int64  `json:"finished_at"`
	Scanned    int64  `json:"scanned"`
	Added      int64  `json:"added"`
	Updated    int64  `json:"updated"`
	Deleted    int64  `json:"deleted"`
	Errors     int64  `json:"errors"`
	Message    string `json:"message"`
}
type managerRoot struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Path       string   `json:"path"`
	Excludes   []string `json:"excludes"`
	ReadOnly   bool     `json:"readonly"`
	Schedule   string   `json:"schedule"`
	LastScanAt int64    `json:"last_scan_at"`
	FileCount  int64    `json:"file_count"`
	DirCount   int64    `json:"dir_count"`
	TotalSize  int64    `json:"total_size"`
	NextScanAt int64    `json:"next_scan_at"`
	Active     any      `json:"active"`
	LastJob    *scanJob `json:"last_job"`
	Disk       string   `json:"-"`
}

func (s *Server) initManagement() {
	s.managerRoots[1] = managerRoot{ID: 1, Name: "nas", Path: "/data/nas", Disk: s.Root}
	s.managerRoots[2] = managerRoot{ID: 2, Name: "只读", Path: "/data/ro", Disk: s.ReadOnly}
	os.MkdirAll(filepath.Join(s.ManageData, "new"), 0755)
}
func (s *Server) managedRoots() []managerRoot {
	s.mu.Lock()
	var roots []managerRoot
	for _, root := range s.managerRoots {
		if root.ID == 2 {
			root.ReadOnly = !s.Writable.Load()
		}
		roots = append(roots, root)
	}
	s.mu.Unlock()
	for i := range roots {
		root := &roots[i]
		root.FileCount = 0
		root.DirCount = 0
		root.TotalSize = 0
		filepath.WalkDir(root.Disk, func(p string, d os.DirEntry, err error) error {
			if err != nil || p == root.Disk {
				return nil
			}
			if d.IsDir() {
				root.DirCount++
			} else if info, err := d.Info(); err == nil {
				root.FileCount++
				root.TotalSize += info.Size()
			}
			return nil
		})
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].ID < roots[j].ID })
	return roots
}
func (s *Server) managementRoutes(api *http.ServeMux) {
	api.HandleFunc("POST /api/roots", s.saveManagedRoot)
	api.HandleFunc("PUT /api/roots/{id}", s.saveManagedRoot)
	api.HandleFunc("DELETE /api/roots/{id}", s.removeManagedRoot)
	api.HandleFunc("GET /api/fs/dirs", s.serverDirs)
	api.HandleFunc("POST /api/roots/{id}/scan", s.startScan)
	api.HandleFunc("POST /api/roots/{id}/cancel", s.cancelScan)
	api.HandleFunc("GET /api/roots/{id}/jobs", s.scanJobs)
	api.HandleFunc("GET /api/stats/overview", s.overview)
	api.HandleFunc("GET /api/stats/largest", s.largest)
	api.HandleFunc("GET /api/stats/dirs", s.dirUsage)
	api.HandleFunc("POST /api/dupes/find", s.findDupes)
	api.HandleFunc("GET /api/dupes", s.dupes)
}
func (s *Server) saveManagedRoot(w http.ResponseWriter, r *http.Request) {
	var root managerRoot
	json.NewDecoder(r.Body).Decode(&root)
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method == "POST" {
		if !strings.HasPrefix(root.Path, "/data/") || strings.Contains(root.Path, "..") {
			s.fail(w, 400, "目录必须位于 /data 之下")
			return
		}
		for _, old := range s.managerRoots {
			if root.Path == old.Path || strings.HasPrefix(root.Path, old.Path+"/") || strings.HasPrefix(old.Path, root.Path+"/") {
				s.fail(w, 400, "与已有目录重叠")
				return
			}
		}
		root.Disk = filepath.Join(s.ManageData, strings.TrimPrefix(root.Path, "/data/"))
		if st, err := os.Stat(root.Disk); err != nil || !st.IsDir() {
			s.fail(w, 400, "目录不存在")
			return
		}
		root.ID = 3
		for id := range s.managerRoots {
			if id >= root.ID {
				root.ID = id + 1
			}
		}
		if root.Name == "" {
			root.Name = path.Base(root.Path)
		}
	} else {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		old, ok := s.managerRoots[id]
		if !ok {
			s.fail(w, 404, "不存在")
			return
		}
		root.ID = id
		root.Path = old.Path
		root.Disk = old.Disk
		root.LastScanAt = old.LastScanAt
		root.LastJob = old.LastJob
		root.Active = old.Active
		if root.Name == "" {
			root.Name = old.Name
		}
		if id == 2 {
			s.Writable.Store(!root.ReadOnly)
		}
	}
	s.managerRoots[root.ID] = root
	json.NewEncoder(w).Encode(root)
}
func (s *Server) removeManagedRoot(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.mu.Lock()
	defer s.mu.Unlock()
	root, ok := s.managerRoots[id]
	if !ok {
		s.fail(w, 404, "不存在")
		return
	}
	if root.Active != nil {
		s.fail(w, 409, "正在扫描")
		return
	}
	delete(s.managerRoots, id)
	delete(s.scanRecords, id)
	for id, it := range s.trashItems {
		if it.RootID == root.ID {
			delete(s.trashItems, id)
		}
	}
	fmt.Fprint(w, `{"ok":true}`)
}
func (s *Server) serverDirs(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		p = "/data"
	}
	dirs := []map[string]string{}
	if p == "/data" {
		dirs = append(dirs, map[string]string{"name": "nas", "path": "/data/nas"}, map[string]string{"name": "ro", "path": "/data/ro"})
	}
	disk := filepath.Join(s.ManageData, strings.TrimPrefix(p, "/data"))
	if p == "/data/nas" {
		disk = s.Root
	}
	if p == "/data/ro" {
		disk = s.ReadOnly
	}
	entries, err := os.ReadDir(disk)
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, map[string]string{"name": e.Name(), "path": path.Join(p, e.Name())})
		}
	}
	json.NewEncoder(w).Encode(map[string]any{"path": p, "dirs": dirs})
}
func (s *Server) startScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.mu.Lock()
	root, ok := s.managerRoots[id]
	if !ok {
		s.mu.Unlock()
		s.fail(w, 404, "不存在")
		return
	}
	if root.Active != nil {
		s.mu.Unlock()
		s.fail(w, 409, "正在扫描")
		return
	}
	s.scanSeq++
	j := scanJob{ID: s.scanSeq, RootID: id, Mode: req.Mode, Status: "queued", CreatedAt: time.Now().Unix()}
	root.Active = map[string]any{"job": j}
	s.managerRoots[id] = root
	s.scanRecords[id] = append([]scanJob{j}, s.scanRecords[id]...)
	s.mu.Unlock()
	json.NewEncoder(w).Encode(j)
	s.publish(map[string]any{"type": "job", "job": j})
	time.AfterFunc(80*time.Millisecond, func() {
		if s.HoldScans.Load() {
			return
		}
		s.mu.Lock()
		root, ok := s.managerRoots[id]
		if !ok {
			s.mu.Unlock()
			return
		}
		if s.scanRecords[id][0].Status == "canceled" {
			s.mu.Unlock()
			return
		}
		j.Status = "done"
		j.StartedAt = j.CreatedAt
		j.FinishedAt = time.Now().Unix()
		j.Scanned = 7
		j.Added = 3
		root.Active = nil
		root.LastScanAt = j.FinishedAt
		root.LastJob = &j
		s.managerRoots[id] = root
		s.scanRecords[id][0] = j
		s.mu.Unlock()
		if !s.DropTaskEvents.Load() {
			s.publish(map[string]any{"type": "job", "job": j})
		}
		s.publish(map[string]any{"type": "fs", "root_id": id})
	})
}
func (s *Server) cancelScan(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.mu.Lock()
	root, ok := s.managerRoots[id]
	if !ok || root.Active == nil {
		s.mu.Unlock()
		s.fail(w, 404, "没有扫描")
		return
	}
	j := s.scanRecords[id][0]
	j.Status = "canceled"
	j.FinishedAt = time.Now().Unix()
	root.Active = nil
	root.LastJob = &j
	s.managerRoots[id] = root
	s.scanRecords[id][0] = j
	s.mu.Unlock()
	s.publish(map[string]any{"type": "job", "job": j})
	fmt.Fprint(w, `{"ok":true}`)
}
func (s *Server) scanJobs(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := append([]scanJob{}, s.scanRecords[id]...)
	json.NewEncoder(w).Encode(jobs)
}
func (s *Server) managedEntries(rootID int64) []Entry {
	var out []Entry
	for _, root := range s.managedRoots() {
		if rootID != 0 && root.ID != rootID {
			continue
		}
		filepath.WalkDir(root.Disk, func(p string, d os.DirEntry, err error) error {
			if err != nil || p == root.Disk {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root.Disk, p)
			e := s.entry(root.ID, filepath.ToSlash(rel), info)
			out = append(out, e)
			return nil
		})
	}
	return out
}
func rootQuery(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.URL.Query().Get("root"), 10, 64)
	return id
}
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	var files, dirs, size int64
	var exts []map[string]any
	byExt := map[string]int64{}
	countByExt := map[string]int64{}
	for _, e := range s.managedEntries(rootQuery(r)) {
		if e.IsDir {
			dirs++
		} else {
			files++
			size += e.Size
			byExt[e.Ext] += e.Size
			countByExt[e.Ext]++
		}
	}
	for ext, n := range byExt {
		exts = append(exts, map[string]any{"key": ext, "size": n, "count": countByExt[ext]})
	}
	sort.Slice(exts, func(i, j int) bool { return exts[i]["key"].(string) < exts[j]["key"].(string) })
	json.NewEncoder(w).Encode(map[string]any{"files": files, "dirs": dirs, "size": size, "exts": exts, "categories": []map[string]any{{"key": "doc", "size": size, "count": files}}, "years": []map[string]any{{"key": "2026", "size": size, "count": files}}})
}
func (s *Server) largest(w http.ResponseWriter, r *http.Request) {
	var out []Entry
	for _, e := range s.managedEntries(rootQuery(r)) {
		if !e.IsDir {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	if len(out) > 50 {
		out = out[:50]
	}
	if out == nil {
		out = []Entry{}
	}
	json.NewEncoder(w).Encode(out)
}
func (s *Server) dirUsage(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	groups := map[string]map[string]any{}
	for _, e := range s.managedEntries(rootQuery(r)) {
		if e.IsDir || !strings.HasPrefix(e.Path, prefix) {
			continue
		}
		rel := strings.TrimPrefix(e.Path, prefix)
		name, _, nested := strings.Cut(rel, "/")
		if !nested {
			name = ""
		}
		item := groups[name]
		if item == nil {
			p := ""
			if nested {
				p = prefix + name
			}
			item = map[string]any{"name": name, "path": p, "is_dir": nested, "files": int64(0), "size": int64(0)}
			groups[name] = item
		}
		item["files"] = item["files"].(int64) + 1
		item["size"] = item["size"].(int64) + e.Size
	}
	out := []map[string]any{}
	for _, g := range groups {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["size"].(int64) > out[j]["size"].(int64) })
	json.NewEncoder(w).Encode(out)
}
func (s *Server) findDupes(w http.ResponseWriter, r *http.Request) {
	id := s.tasks.Add(1)
	task := map[string]any{"id": id, "kind": "dupes", "status": "queued"}
	s.mu.Lock()
	s.taskRecords[id] = task
	s.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{"task": task})
	time.AfterFunc(30*time.Millisecond, func() {
		done := map[string]any{"id": id, "kind": "dupes", "status": "done", "finished_at": time.Now().Unix()}
		s.mu.Lock()
		s.taskRecords[id] = done
		s.dupesLastRun = time.Now().Unix()
		s.mu.Unlock()
		s.publish(map[string]any{"type": "task", "task": done})
		s.publish(map[string]any{"type": "dupes"})
	})
}
func (s *Server) dupes(w http.ResponseWriter, r *http.Request) {
	rootID := rootQuery(r)
	minSize, _ := strconv.ParseInt(r.URL.Query().Get("min"), 10, 64)
	groups := map[string][]Entry{}
	for _, e := range s.managedEntries(rootID) {
		if e.IsDir || e.Size < max(minSize, 1) {
			continue
		}
		dir, _ := s.rootDir(fmt.Sprint(e.Root))
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(e.Path)))
		if err != nil {
			continue
		}
		hash := fmt.Sprintf("%x", sha256.Sum256(data))
		groups[hash] = append(groups[hash], e)
	}
	out := []map[string]any{}
	var files, wasted int64
	for hash, entries := range groups {
		if len(entries) < 2 {
			continue
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
		size := entries[0].Size
		n := size * int64(len(entries)-1)
		files += int64(len(entries))
		wasted += n
		out = append(out, map[string]any{"hash": hash, "size": size, "count": len(entries), "wasted": n, "files": entries})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["hash"].(string) < out[j]["hash"].(string) })
	total := len(out)
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	offset := (max(page, 1) - 1) * 50
	offset = min(offset, total)
	out = out[offset:min(offset+50, total)]
	json.NewEncoder(w).Encode(map[string]any{"groups": out, "summary": map[string]any{"groups": total, "files": files, "wasted": wasted}})
}
