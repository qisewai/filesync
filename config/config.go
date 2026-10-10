// Package config keeps the backup tasks and their history in a JSON file.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"filesync/engine"
)

// MaxHistory is how many runs a task remembers; older runs' logs are deleted.
const MaxHistory = 20

// Task is a backup the user set up.
type Task struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Source   string      `json:"source"`
	Target   string      `json:"target"`
	Mode     engine.Mode `json:"mode"`
	Excludes []string    `json:"excludes"`
	// TimeTolerance is in seconds; 0 is engine.DefaultTimeTolerance.
	TimeTolerance int   `json:"timeTolerance,omitempty"`
	History       []Run `json:"history,omitempty"`
}

// Options returns the engine options of the task.
func (t *Task) Options() engine.Options {
	return engine.Options{
		Source:        t.Source,
		Target:        t.Target,
		Mode:          t.Mode,
		Excludes:      t.Excludes,
		TimeTolerance: time.Duration(t.TimeTolerance) * time.Second,
	}
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

// Store is the tasks, saved in a directory.
type Store struct {
	Dir   string
	Tasks []*Task
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

// Load reads the tasks in dir; a directory without them has none.
func Load(dir string) (*Store, error) {
	s := &Store{Dir: dir}
	b, err := os.ReadFile(s.file())
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var data struct {
		Tasks []*Task `json:"tasks"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, err
	}
	s.Tasks = data.Tasks
	return s, nil
}

// Save writes the tasks, through a temporary file so that a crash cannot
// leave half of them.
func (s *Store) Save() error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	tasks := s.Tasks
	if tasks == nil {
		tasks = []*Task{}
	}
	b, err := json.MarshalIndent(map[string]any{"tasks": tasks}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.file() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.file())
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
	var b [6]byte
	rand.Read(b[:])
	t.ID = hex.EncodeToString(b[:])
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
