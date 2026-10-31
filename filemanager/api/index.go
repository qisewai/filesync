package api

import (
	"encoding/base64"
	"filemanager/index"
)

// IndexCommand is restricted to a trusted local backend. Restricted roots use
// root-scoped indexing; they must not expose arbitrary absolute scan paths.
type IndexCommand struct {
	EncodedPaths  bool
	Session       string
	Action        string
	Options       index.ScanOptions
	Filename      string
	Meta          index.SnapshotMeta
	Paths         []string
	Query         index.Query
	Result        string
	Offset, Limit int
	Dir           string
}
type IndexFrame struct {
	Done     bool
	Error    *Error
	Status   index.Status
	Progress *index.Progress
	Loading  *index.LoadProgress
	Meta     index.SnapshotMeta
	Paths    [][]byte
	Result   string
	Total    int
	Version  uint64
	Entries  []IndexEntry
	Subtree  index.Subtree
	Found    bool
}

// Encode filesystem strings as bytes so JSON never normalizes Unix filenames.
type IndexEntry struct {
	Name, Path, Parent, Ext []byte
	Size, MTime             int64
	IsDir, Hidden, Link     bool
}

func EncodeIndexEntry(e index.Entry) IndexEntry {
	return IndexEntry{[]byte(e.Name), []byte(e.Path), []byte(e.Parent), []byte(e.Ext), e.Size, e.MTime, e.IsDir, e.Hidden, e.Link}
}
func (e IndexEntry) Decode() index.Entry {
	return index.Entry{Name: string(e.Name), Path: string(e.Path), Parent: string(e.Parent), Ext: string(e.Ext), Size: e.Size, MTime: e.MTime, IsDir: e.IsDir, Hidden: e.Hidden, Link: e.Link}
}

func (q *IndexCommand) EncodePaths() {
	if q.EncodedPaths {
		return
	}
	q.EncodedPaths = true
	enc := func(p string) string { return base64.StdEncoding.EncodeToString([]byte(p)) }
	list := func(ps []string) []string {
		out := make([]string, len(ps))
		for i, p := range ps {
			out[i] = enc(p)
		}
		return out
	}
	q.Filename = enc(q.Filename)
	q.Dir = enc(q.Dir)
	q.Query.Dir = enc(q.Query.Dir)
	q.Paths = list(q.Paths)
	q.Options.Roots = list(q.Options.Roots)
	q.Options.Skip = list(q.Options.Skip)
}
func (q *IndexCommand) DecodePaths() error {
	if !q.EncodedPaths {
		return nil
	}
	dec := func(p *string) error {
		b, e := base64.StdEncoding.DecodeString(*p)
		if e == nil {
			*p = string(b)
		}
		return e
	}
	for _, p := range []*string{&q.Filename, &q.Dir, &q.Query.Dir} {
		if e := dec(p); e != nil {
			return e
		}
	}
	for _, ps := range [][]string{q.Paths, q.Options.Roots, q.Options.Skip} {
		for i := range ps {
			if e := dec(&ps[i]); e != nil {
				return e
			}
		}
	}
	q.EncodedPaths = false
	return nil
}
