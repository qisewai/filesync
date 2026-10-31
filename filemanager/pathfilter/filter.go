package pathfilter

import (
	"fmt"
	"path"
	"runtime"
	"strings"
)

// Filter decides which paths a task leaves out. Its patterns are globs, one
// per rule, matched against paths relative to the task's directories:
//
//   - a pattern without a slash matches the name of a file or directory at
//     any depth: "*.tmp", "Thumbs.db", "node_modules";
//   - a pattern ending in a slash matches directories only: "cache/";
//   - a pattern with a slash elsewhere matches the path from the root:
//     "build/output", "/docs/*.pdf".
//
// An excluded directory is left out with everything inside it. Excluded paths
// are neither copied nor, in mirror mode, deleted from the target.
type Filter struct {
	rules []rule
	fold  bool
	only  map[string]bool // the top-level names kept, nil for all
}

type rule struct {
	pattern  string
	dirOnly  bool
	anchored bool
}

// NewFilter compiles patterns, ignoring blank lines and lines starting with
// "#".
func NewFilter(patterns []string) (*Filter, error) {
	f := &Filter{fold: runtime.GOOS == "windows"}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		p = strings.ReplaceAll(p, `\`, "/")
		r := rule{}
		if strings.HasSuffix(p, "/") {
			r.dirOnly = true
			p = strings.TrimRight(p, "/")
		}
		if strings.Contains(p, "/") {
			r.anchored = true
			p = strings.TrimLeft(p, "/")
		}
		if p == "" {
			continue
		}
		if f.fold {
			p = strings.ToLower(p)
		}
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("排除规则 %q 无效: %w", p, err)
		}
		r.pattern = p
		f.rules = append(f.rules, r)
	}
	return f, nil
}

// Only keeps the paths in the top-level entries named, and leaves out the
// others; none keeps all.
func (f *Filter) Only(names []string) {
	f.only = nil
	for _, n := range names {
		if f.only == nil {
			f.only = map[string]bool{}
		}
		if f.fold {
			n = strings.ToLower(n)
		}
		f.only[n] = true
	}
}

// Excluded reports whether the slash-separated relative path rel is left out.
func (f *Filter) Excluded(rel string, isDir bool) bool {
	if f == nil {
		return false
	}
	if f.fold {
		rel = strings.ToLower(rel)
	}
	if f.only != nil {
		top, _, _ := strings.Cut(rel, "/")
		if !f.only[top] {
			return true
		}
	}
	name := path.Base(rel)
	for _, r := range f.rules {
		if r.dirOnly && !isDir {
			continue
		}
		subject := name
		if r.anchored {
			subject = rel
		}
		if ok, _ := path.Match(r.pattern, subject); ok {
			return true
		}
	}
	return false
}
