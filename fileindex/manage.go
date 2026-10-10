package fileindex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

type ScanJob struct {
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

func (j *ScanJob) Finished() bool {
	return j.FinishedAt != 0 || j.Status == "done" || j.Status == "failed" || j.Status == "canceled"
}

type ScanEvent struct {
	Job     *ScanJob `json:"job"`
	Current string   `json:"current"`
}
type RootSnapshot struct {
	Roots     []Root   `json:"roots"`
	DataRoots []string `json:"data_roots"`
}

func (c *Client) RootDetails(ctx context.Context) (*RootSnapshot, error) {
	var out RootSnapshot
	err := c.do(ctx, "GET", "/api/roots", nil, nil, &out)
	return &out, err
}

type RootSettings struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Excludes []string `json:"excludes"`
	ReadOnly bool     `json:"readonly"`
	Schedule string   `json:"schedule"`
}

func (c *Client) SaveRoot(ctx context.Context, id int64, in RootSettings) (*Root, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	if id == 0 {
		var root Root
		err = c.do(ctx, "POST", "/api/roots", nil, bytes.NewReader(data), &root)
		return &root, err
	}
	err = c.do(ctx, "PUT", fmt.Sprintf("/api/roots/%d", id), nil, bytes.NewReader(data), nil)
	return nil, err
}
func (c *Client) RemoveRoot(ctx context.Context, id int64) error {
	return c.do(ctx, "DELETE", fmt.Sprintf("/api/roots/%d", id), nil, nil, nil)
}
func (c *Client) StartScan(ctx context.Context, root int64, rebuild bool) (*ScanJob, error) {
	mode := "update"
	if rebuild {
		mode = "rebuild"
	}
	var out ScanJob
	err := c.post(ctx, fmt.Sprintf("/api/roots/%d/scan", root), map[string]string{"mode": mode}, &out)
	if err == nil && out.ID == 0 {
		err = fmt.Errorf("NAS 没有返回扫描任务")
	}
	return &out, err
}
func (c *Client) CancelScan(ctx context.Context, root int64) error {
	return c.do(ctx, "POST", fmt.Sprintf("/api/roots/%d/cancel", root), nil, nil, nil)
}
func (c *Client) ScanJobs(ctx context.Context, root int64) ([]ScanJob, error) {
	var out []ScanJob
	err := c.do(ctx, "GET", fmt.Sprintf("/api/roots/%d/jobs", root), nil, nil, &out)
	return out, err
}

type ServerDirectory struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type ServerDirectories struct {
	Path string            `json:"path"`
	Dirs []ServerDirectory `json:"dirs"`
}

func (c *Client) ServerDirs(ctx context.Context, path string) (*ServerDirectories, error) {
	var out ServerDirectories
	err := c.do(ctx, "GET", "/api/fs/dirs", url.Values{"path": {path}}, nil, &out)
	return &out, err
}

type DupeGroup struct {
	Hash   string  `json:"hash"`
	Size   int64   `json:"size"`
	Count  int     `json:"count"`
	Wasted int64   `json:"wasted"`
	Files  []Entry `json:"files"`
}
type DupeSummary struct {
	Groups int64 `json:"groups"`
	Files  int64 `json:"files"`
	Wasted int64 `json:"wasted"`
}
type Dupes struct {
	Groups  []DupeGroup `json:"groups"`
	Summary DupeSummary `json:"summary"`
}

func scopeQuery(root int64) url.Values {
	q := url.Values{}
	if root > 0 {
		q.Set("root", strconv.FormatInt(root, 10))
	}
	return q
}
func (c *Client) DuplicateGroups(ctx context.Context, root, minSize int64, page int) (*Dupes, error) {
	q := scopeQuery(root)
	q.Set("min", strconv.FormatInt(minSize, 10))
	q.Set("page", strconv.Itoa(page))
	q.Set("size", "50")
	var out Dupes
	err := c.do(ctx, "GET", "/api/dupes", q, nil, &out)
	return &out, err
}
func (c *Client) FindDuplicates(ctx context.Context, root, minSize int64) (*Task, error) {
	var out struct {
		Task *Task `json:"task"`
	}
	err := c.post(ctx, "/api/dupes/find", map[string]int64{"root": root, "min_size": minSize}, &out)
	if err == nil && out.Task == nil {
		err = fmt.Errorf("NAS 没有返回查重任务")
	}
	return out.Task, err
}

type Bucket struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
	Size  int64  `json:"size"`
}
type Overview struct {
	Files      int64    `json:"files"`
	Dirs       int64    `json:"dirs"`
	Size       int64    `json:"size"`
	Categories []Bucket `json:"categories"`
	Exts       []Bucket `json:"exts"`
	Years      []Bucket `json:"years"`
}
type DirUsage struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Files int64  `json:"files"`
	Size  int64  `json:"size"`
}

func (c *Client) Statistics(ctx context.Context, root int64) (*Overview, error) {
	var out Overview
	err := c.do(ctx, "GET", "/api/stats/overview", scopeQuery(root), nil, &out)
	return &out, err
}
func (c *Client) LargestFiles(ctx context.Context, root int64) ([]Entry, error) {
	q := scopeQuery(root)
	q.Set("limit", "50")
	var out []Entry
	err := c.do(ctx, "GET", "/api/stats/largest", q, nil, &out)
	return out, err
}
func (c *Client) DirectoryUsage(ctx context.Context, root int64, dir string) ([]DirUsage, error) {
	q := scopeQuery(root)
	q.Set("path", dir)
	var out []DirUsage
	err := c.do(ctx, "GET", "/api/stats/dirs", q, nil, &out)
	return out, err
}
