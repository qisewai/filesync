// Package config keeps the servers, the backup tasks and their history in a
// JSON file.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"filesync/engine"
)

// MaxHistory is how many runs a task remembers; older runs' logs are deleted.
const MaxHistory = 20

// Server is a fileindex server, on a NAS, that backups go to.
type Server struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Addr is its host and port, as 192.168.0.235:8090.
	Addr string `json:"addr"`
	// Token is the login session, "" when signed out. No password is kept.
	Token string `json:"token,omitempty"`
}

// Title is the server's name, or its address.
func (s *Server) Title() string {
	if s.Name != "" {
		return s.Name
	}
	return s.Addr
}

// Task is a backup the user set up: a local directory copied to a
// directory of a fileindex server.
type Task struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Source string `json:"source"`
	// Server is the ID of the server backed up to.
	Server string `json:"server"`
	// Root is the ID of the server's indexed directory, and Path the
	// directory in it, with slashes ("" for the root itself).
	Root int64  `json:"root"`
	Path string `json:"path"`
	// RootName is the name of the root, to show the target.
	RootName string `json:"rootName"`
	// WithDir backs up the source directory itself, as a directory of its
	// name in the target, rather than only what it holds.
	WithDir bool `json:"withDir,omitempty"`
	// Items, when set, are the names of the entries of Source backed up,
	// straight into the target; the others are left out.
	Items []string `json:"items,omitempty"`
	// Compress compresses an archive: "" (not), "gzip" or "zstd".
	Compress string      `json:"compress,omitempty"`
	Mode     engine.Mode `json:"mode"`
	Excludes []string    `json:"excludes"`
	History  []Run       `json:"history,omitempty"`
}

// BackupPath is the directory of the root the files go to: Path, or the
// directory named as the source in it. An archive goes to Path, the
// directory named as the source being in it.
func (t *Task) BackupPath() string {
	if t.Mode == engine.Archive {
		return t.Path
	}
	return BackupPath(t.Path, t.Source, t.WithDir && len(t.Items) == 0)
}

// Named is the path whose name an archive of the task takes: the entry
// backed up when it is one, or the source.
func (t *Task) Named() string {
	if len(t.Items) == 1 {
		return path.Join(strings.ReplaceAll(t.Source, `\`, "/"), t.Items[0])
	}
	return t.Source
}

// Contents says what the task backs up, as people read it.
func (t *Task) Contents() string {
	return Contents(t.Source, t.Items)
}

// Contents says what a backup of the entries items of source holds: source
// itself without them.
func Contents(source string, items []string) string {
	switch {
	case len(items) == 0:
		return source
	case len(items) <= 3:
		return source + " 中的 " + strings.Join(items, "、")
	}
	return fmt.Sprintf("%s 中的 %s 等 %d 项", source, strings.Join(items[:3], "、"), len(items))
}

// ArchiveName is the name of an archive of the source made at t, as
// xiaomai-20261010-160500.tar.gz.
func ArchiveName(source string, t time.Time, compress string) string {
	name := SourceName(source)
	if name == "" {
		name = "backup"
	}
	return name + "-" + t.Format("20060102-150405") + ArchiveExt(compress)
}

// ArchiveExt is the extension of an archive compressed so.
func ArchiveExt(compress string) string {
	switch compress {
	case "gzip":
		return ".tar.gz"
	case "zstd":
		return ".tar.zst"
	}
	return ".tar"
}

// TargetPath is where the files go, as people read it, as 王宁/备份/xiaomai.
func (t *Task) TargetPath() string {
	return path.Join(t.RootName, t.BackupPath())
}

// BackupPath is the directory below dir where a backup of source goes:
// dir itself, or with withDir the directory named as source in it.
func BackupPath(dir, source string, withDir bool) string {
	if name := SourceName(source); withDir && name != "" {
		return path.Join(dir, name)
	}
	return dir
}

// SourceName is the name of a source directory, "" for a root such as /
// or C:\.
func SourceName(source string) string {
	source = strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(source), `\`, "/"), "/")
	name := path.Base(source)
	if source == "" || name == "." || name == "/" || strings.HasSuffix(name, ":") {
		return ""
	}
	return name
}

// Status is how a run ended.
type Status string

const (
	StatusOK       Status = "ok"
	StatusErrors   Status = "errors"
	StatusCanceled Status = "canceled"
	StatusFailed   Status = "failed"
)

// Run is a backup that ran.
type Run struct {
	Started  time.Time   `json:"started"`
	Duration float64     `json:"duration"` // seconds
	Mode     engine.Mode `json:"mode"`
	Status   Status      `json:"status"`
	Copied   int         `json:"copied"`
	Deleted  int         `json:"deleted"`
	Errors   int         `json:"errors"`
	Bytes    int64       `json:"bytes"`
	Message  string      `json:"message,omitempty"`
	Log      string      `json:"log,omitempty"` // the log file's path
}

// DefaultExcludes are the excludes new tasks start with.
var DefaultExcludes = []string{"Thumbs.db", "desktop.ini", ".DS_Store", "$RECYCLE.BIN/", "System Volume Information/"}

// Store is the servers and the tasks, saved in a directory.
type Store struct {
	Dir     string
	Servers []*Server
	Tasks   []*Task
	// SidebarWidth is the width the user dragged the sidebar to, 0 for
	// the default.
	SidebarWidth float32
	// QuitOnClose quits as the window closes, rather than keeping the app
	// in the tray.
	QuitOnClose bool
	LocalIndex  *LocalIndexSettings
	// RecentSearches are the searches made last, the latest first.
	RecentSearches []string
	// Favorites are the folders pinned in the sidebar, in its order.
	Favorites []*Favorite
}

// Favorite is a folder pinned in the sidebar: of this computer when Local
// is set, else the directory Path of a root of a server.
type Favorite struct {
	Name     string `json:"name"`
	Local    string `json:"local,omitempty"`
	Server   string `json:"server,omitempty"`
	Root     int64  `json:"root,omitempty"`
	RootName string `json:"rootName,omitempty"`
	Path     string `json:"path,omitempty"`
}

// Same reports whether f is the folder g is.
func (f *Favorite) Same(g *Favorite) bool {
	return f.Local == g.Local && f.Server == g.Server && f.Root == g.Root && f.Path == g.Path
}

// Favorite returns the favorite of the folder f is, or nil.
func (s *Store) Favorite(f *Favorite) *Favorite {
	for _, g := range s.Favorites {
		if g.Same(f) {
			return g
		}
	}
	return nil
}

// AddFavorite pins a folder last, once.
func (s *Store) AddFavorite(f *Favorite) {
	if s.Favorite(f) == nil {
		s.Favorites = append(s.Favorites, f)
	}
}

// RemoveFavorite unpins a folder.
func (s *Store) RemoveFavorite(f *Favorite) {
	s.Favorites = slices.DeleteFunc(s.Favorites, func(g *Favorite) bool { return g.Same(f) })
}

// MoveFavorite moves a favorite by delta places, within the list.
func (s *Store) MoveFavorite(f *Favorite, delta int) {
	i := slices.IndexFunc(s.Favorites, func(g *Favorite) bool { return g.Same(f) })
	j := i + delta
	if i < 0 || j < 0 || j >= len(s.Favorites) {
		return
	}
	s.Favorites[i], s.Favorites[j] = s.Favorites[j], s.Favorites[i]
}

// maxRecentSearches is how many searches RecentSearches keeps.
const maxRecentSearches = 8

// AddRecentSearch puts a search first among the recent ones, once.
func (s *Store) AddRecentSearch(q string) {
	q = strings.TrimSpace(q)
	if q == "" {
		return
	}
	kept := []string{q}
	for _, r := range s.RecentSearches {
		if r != q && len(kept) < maxRecentSearches {
			kept = append(kept, r)
		}
	}
	s.RecentSearches = kept
}

// LocalIndexSettings persists scan scopes, not search data. Scans are manual.
type LocalIndexSettings struct {
	Roots    []string `json:"roots"`
	Excludes []string `json:"excludes"`
}

// DefaultDir is where the app keeps its settings: ~/.config/filesync on
// Linux, %AppData%\filesync on Windows.
func DefaultDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "filesync"), nil
}

func (s *Store) file() string { return filepath.Join(s.Dir, "tasks.json") }

// LogDir is where run logs go.
func (s *Store) LogDir() string { return filepath.Join(s.Dir, "logs") }

type fileData struct {
	Servers []*Server `json:"servers"`
	Tasks   []*Task   `json:"tasks"`
	// Sessions are the logins of version 0.1, by address, which become
	// servers.
	Sessions     map[string]string   `json:"sessions,omitempty"`
	SidebarWidth float32             `json:"sidebarWidth,omitempty"`
	QuitOnClose  bool                `json:"quitOnClose,omitempty"`
	LocalIndex   *LocalIndexSettings `json:"localIndex,omitempty"`
	Recent       []string            `json:"recentSearches,omitempty"`
	Favorites    []*Favorite         `json:"favorites,omitempty"`
}

// Load reads the store in dir; a directory without it has nothing.
func Load(dir string) (*Store, error) {
	s := &Store{Dir: dir}
	b, err := os.ReadFile(s.file())
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var data fileData
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, err
	}
	s.Servers = data.Servers
	s.SidebarWidth, s.QuitOnClose = data.SidebarWidth, data.QuitOnClose
	s.LocalIndex = data.LocalIndex
	s.RecentSearches = data.Recent
	s.Favorites = data.Favorites
	addrs := make([]string, 0, len(data.Sessions))
	for addr := range data.Sessions {
		addrs = append(addrs, addr)
	}
	sort.Strings(addrs)
	for _, addr := range addrs {
		if s.ServerByAddr(addr) == nil {
			s.AddServer(&Server{Addr: addr, Token: data.Sessions[addr]})
		}
	}
	// Tasks of version 0.1 copied to any directory: only those to a server
	// remain.
	for _, t := range data.Tasks {
		if t.Server != "" {
			s.Tasks = append(s.Tasks, t)
		}
	}
	return s, nil
}

// Save writes the store, through a temporary file so that a crash cannot
// leave half of it. Only the user reads it: it holds the sessions.
func (s *Store) Save() error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	data := fileData{Servers: s.Servers, Tasks: s.Tasks, SidebarWidth: s.SidebarWidth, QuitOnClose: s.QuitOnClose, LocalIndex: s.LocalIndex, Recent: s.RecentSearches, Favorites: s.Favorites}
	if data.Servers == nil {
		data.Servers = []*Server{}
	}
	if data.Tasks == nil {
		data.Tasks = []*Task{}
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.file() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.file())
}

func newID() string {
	var b [6]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Server returns the server with the ID, or nil.
func (s *Store) Server(id string) *Server {
	for _, sv := range s.Servers {
		if sv.ID == id {
			return sv
		}
	}
	return nil
}

// ServerByAddr returns the server at the address, or nil.
func (s *Store) ServerByAddr(addr string) *Server {
	for _, sv := range s.Servers {
		if sv.Addr == addr {
			return sv
		}
	}
	return nil
}

// AddServer adds a server, giving it an ID.
func (s *Store) AddServer(sv *Server) {
	sv.ID = newID()
	s.Servers = append(s.Servers, sv)
}

// TasksOf returns the tasks backing up to a server.
func (s *Store) TasksOf(serverID string) []*Task {
	var out []*Task
	for _, t := range s.Tasks {
		if t.Server == serverID {
			out = append(out, t)
		}
	}
	return out
}

// RemoveServer removes a server no task uses.
func (s *Store) RemoveServer(id string) error {
	if n := len(s.TasksOf(id)); n > 0 {
		return errors.New("还有任务备份到这个服务器，请先修改或删除这些任务")
	}
	for i, sv := range s.Servers {
		if sv.ID == id {
			s.Servers = append(s.Servers[:i], s.Servers[i+1:]...)
			break
		}
	}
	s.Favorites = slices.DeleteFunc(s.Favorites, func(f *Favorite) bool { return f.Server == id })
	return nil
}

// Task returns the task with the ID, or nil.
func (s *Store) Task(id string) *Task {
	for _, t := range s.Tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// Add adds a task, giving it an ID.
func (s *Store) Add(t *Task) {
	t.ID = newID()
	s.Tasks = append(s.Tasks, t)
}

// Remove removes a task and its logs.
func (s *Store) Remove(id string) {
	for i, t := range s.Tasks {
		if t.ID == id {
			for _, r := range t.History {
				removeLog(r.Log)
			}
			s.Tasks = append(s.Tasks[:i], s.Tasks[i+1:]...)
			return
		}
	}
}

// AddRun records a run of a task, newest first, forgetting the oldest runs.
func (t *Task) AddRun(r Run) {
	t.History = append([]Run{r}, t.History...)
	for len(t.History) > MaxHistory {
		removeLog(t.History[len(t.History)-1].Log)
		t.History = t.History[:len(t.History)-1]
	}
}

func removeLog(path string) {
	if path != "" {
		os.Remove(path)
	}
}
