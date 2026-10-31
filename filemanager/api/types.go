// Package api defines the versioned transport contract. Paths are always
// relative to a configured root, not paths supplied to the remote OS directly.
package api

import "unicode/utf8"

const Version = 1

type Root struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"`
	ReadOnly bool   `json:"read_only"`
}
type Location struct {
	Root      string `json:"root"`
	Path      string `json:"path"`
	PathBytes []byte `json:"path_bytes,omitempty"`
}

// Wire preserves non-UTF-8 Unix paths without JSON's replacement characters.
func (l Location) Wire() Location {
	if !utf8.ValidString(l.Path) {
		l.PathBytes = []byte(l.Path)
		l.Path = ""
	}
	return l
}
func (l Location) NativePath() string {
	if l.PathBytes != nil {
		return string(l.PathBytes)
	}
	return l.Path
}

type Entry struct {
	Location
	Name      string `json:"name"`
	NameBytes []byte `json:"name_bytes,omitempty"`
	Size      int64  `json:"size"`
	MTime     int64  `json:"mtime"`
	IsDir     bool   `json:"is_dir"`
	Link      bool   `json:"link"`
}
type MemoryInfo struct {
	HeapAlloc     uint64 `json:"heap_alloc_bytes"`
	HeapInuse     uint64 `json:"heap_inuse_bytes"`
	HeapIdle      uint64 `json:"heap_idle_bytes"`
	HeapReleased  uint64 `json:"heap_released_bytes"`
	Limit         int64  `json:"limit_bytes"`
	QueryResults  int    `json:"query_results"`
	IndexSessions int    `json:"index_sessions"`
}
type Info struct {
	Memory       *MemoryInfo `json:"memory,omitempty"`
	Version      int         `json:"version"`
	Capabilities []string    `json:"capabilities"`
}
type ListRequest struct {
	Location
	Hidden   bool `json:"hidden"`
	DirsOnly bool `json:"dirs_only"`
	// Probe stops at the first matching entry; order is unspecified. Used only
	// to decide whether to show a sidebar directory's expansion affordance.
	Probe bool `json:"probe,omitempty"`
}
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
