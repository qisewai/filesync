package fileindex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Entry is a file or directory of a root, as the server lists it.
type Entry struct {
	Link      bool   `json:"link,omitempty"`
	Encrypted bool   `json:"encrypted,omitempty"`
	ID        int64  `json:"id"`
	Root      int64  `json:"root_id"`
	RootName  string `json:"root_name"`
	Name      string `json:"name"`
	Path      string `json:"path"` // relative to the root, with slashes
	Parent    string `json:"parent"`
	IsDir     bool   `json:"is_dir"`
	Size      int64  `json:"size"`
	MTime     int64  `json:"mtime"` // unix seconds
	Ext       string `json:"ext"`
	// HasDirs, of a directory listed, tells whether it holds directories;
	// nil when the server does not tell.
	HasDirs *bool `json:"has_dirs,omitempty"`
}

// ModTime is the entry's modification time.
func (e Entry) ModTime() time.Time { return time.Unix(e.MTime, 0) }

// List lists the directory dir of a root, directories first, then by
// name: from the index, or from the disk with live, which wakes sleeping
// disks but is up to date.
func (c *Client) List(ctx context.Context, root int64, dir string, live bool) ([]Entry, error) {
	const size = 1000
	var out []Entry
	for page := 1; ; page++ {
		var res struct {
			Items []Entry `json:"items"`
			Total int     `json:"total"`
		}
		q := url.Values{"path": {dir}, "size": {strconv.Itoa(size)}, "page": {strconv.Itoa(page)}}
		if live {
			q.Set("live", "1")
		}
		if err := c.do(ctx, "GET", fmt.Sprintf("/api/roots/%d/list", root), q, nil, &res); err != nil {
			return nil, err
		}
		out = append(out, res.Items...)
		if page*size >= res.Total || len(res.Items) == 0 {
			return out, nil
		}
	}
}

// SearchOptions narrow and order a search, and choose a page of it.
type SearchOptions struct {
	// Root is the root searched, 0 for all, and Dir the directory below
	// which, in it.
	Root int64
	Dir  string
	// Sort is "name" (the default), "size", "mtime" or "path".
	Sort string
	Desc bool
	// Page counts from 1, of Size entries, at most SearchPageSize.
	Page, Size int
	// What the results are: Type "file" or "dir", "" for both; Category a
	// kind of file, as "image" or "doc"; Exts extensions, without dots.
	Type, Category string
	Exts           []string
	// MinSize and MaxSize bound the size, in bytes, and From and To the
	// time of the last change, in Unix seconds; 0 bounds nothing.
	MinSize, MaxSize int64
	From, To         int64
}

// SearchPageSize is the most entries a page of a search may have.
const SearchPageSize = 1000

// Search finds the entries whose names match q, a page of them, and how
// many there are.
func (c *Client) Search(ctx context.Context, q string, o SearchOptions) (items []Entry, total int, err error) {
	size := o.Size
	if size <= 0 || size > SearchPageSize {
		size = SearchPageSize
	}
	v := url.Values{"q": {q}, "size": {strconv.Itoa(size)}, "page": {strconv.Itoa(max(o.Page, 1))}}
	if o.Root != 0 {
		v.Set("root", strconv.FormatInt(o.Root, 10))
		if o.Dir != "" {
			v.Set("dir", o.Dir)
		}
	}
	if o.Sort != "" {
		v.Set("sort", o.Sort)
	}
	if o.Desc {
		v.Set("desc", "1")
	}
	for name, value := range map[string]string{"type": o.Type, "cat": o.Category, "ext": strings.Join(o.Exts, ",")} {
		if value != "" {
			v.Set(name, value)
		}
	}
	for name, n := range map[string]int64{"min": o.MinSize, "max": o.MaxSize, "from": o.From, "to": o.To} {
		if n > 0 {
			v.Set(name, strconv.FormatInt(n, 10))
		}
	}
	var res struct {
		Items []Entry `json:"items"`
		Total int     `json:"total"`
	}
	err = c.do(ctx, "GET", "/api/search", v, nil, &res)
	return res.Items, res.Total, err
}

// Info is what the server tells of itself.
type Info struct {
	// ThumbExts are the extensions of files the server makes thumbnails
	// of.
	ThumbExts    []string `json:"thumb_exts"`
	DupesLastRun int64    `json:"dupes_last_run"`
}

// Info asks the server about itself.
func (c *Client) Info(ctx context.Context) (*Info, error) {
	var out Info
	err := c.do(ctx, "GET", "/api/info", nil, nil, &out)
	return &out, err
}

// Thumb returns the thumbnail of a file, a JPEG.
func (c *Client) Thumb(ctx context.Context, root int64, p string) ([]byte, error) {
	res, err := c.send(ctx, "GET", fmt.Sprintf("/api/thumb/%d", root), url.Values{"path": {p}}, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return io.ReadAll(io.LimitReader(res.Body, 8<<20))
}

func (c *Client) post(ctx context.Context, path string, req, out any) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return c.do(ctx, "POST", path, nil, bytes.NewReader(b), out)
}

// Rename renames the entry p of a root to name, in its directory.
func (c *Client) Rename(ctx context.Context, root int64, p, name string) error {
	return c.post(ctx, "/api/fs/rename", map[string]any{"root": root, "path": p, "name": name}, nil)
}

// Task is an operation the server runs in the background: a copy or a
// move.
type Task struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"` // copy | move | delete | purge | extract
	Title      string `json:"title"`
	Status     string `json:"status"` // queued | running | done | failed | canceled
	TotalBytes int64  `json:"total_bytes"`
	DoneBytes  int64  `json:"done_bytes"`
	TotalItems int64  `json:"total_items"`
	DoneItems  int64  `json:"done_items"`
	Current    string `json:"current"`
	Message    string `json:"message"`
	FinishedAt int64  `json:"finished_at"`
}

// Finished reports whether the task has ended.
func (t *Task) Finished() bool {
	return t.FinishedAt != 0 || t.Status == "done" || t.Status == "failed" || t.Status == "canceled"
}

// Transfer copies, or moves with move, the paths of a root into the
// directory dir of another (or the same), as a task of the server. Names
// taken get a number.
func (c *Client) Transfer(ctx context.Context, move bool, root int64, paths []string, toRoot int64, dir string) (*Task, error) {
	op := "copy"
	if move {
		op = "move"
	}
	var out struct {
		Task *Task `json:"task"`
	}
	err := c.post(ctx, "/api/fs/transfer", map[string]any{
		"op": op, "src_root": root, "paths": paths, "dst_root": toRoot, "dst_dir": dir, "conflict": "rename",
	}, &out)
	if err == nil && out.Task == nil {
		err = fmt.Errorf("NAS 没有返回传输任务")
	}
	return out.Task, err
}

// Tasks reconciles events missed while connecting or reconnecting.
func (c *Client) Tasks(ctx context.Context) ([]Task, error) {
	var tasks []Task
	err := c.do(ctx, "GET", "/api/tasks", nil, nil, &tasks)
	return tasks, err
}

// CancelTask asks the server to stop a task.
func (c *Client) CancelTask(ctx context.Context, id int64) error {
	return c.do(ctx, "POST", fmt.Sprintf("/api/tasks/%d/cancel", id), nil, nil, nil)
}

// Event is what the server tells as it happens: a task's progress, or a
// root's files changed.
type Event struct {
	Type    string   `json:"type"` // task | fs | …
	Task    *Task    `json:"task"`
	Job     *ScanJob `json:"job"`
	Current string   `json:"current"`
	Root    int64    `json:"root_id"`
}

// Events calls fn with the server's events until ctx ends or the stream
// breaks, which it returns.
func (c *Client) Events(ctx context.Context, fn func(Event)) error {
	res, err := c.send(ctx, "GET", "/api/events", nil, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue // pings and blank lines
		}
		var e Event
		if json.Unmarshal([]byte(data), &e) == nil {
			fn(e)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}
