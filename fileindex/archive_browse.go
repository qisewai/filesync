package fileindex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	archiveio "filesync/internal/archive"
)

// ArchiveSource is an immutable archive version. Member paths replace ordinary
// root paths for reads and exports; all writes remain unavailable in the UI.
type ArchiveSource struct {
	Path, Version string
	Local         bool
}

func (c *Client) InArchive() bool { return c.ArchiveSource != nil }
func (c *Client) Archive(root int64, p, version string) *Client {
	out := *c
	out.ArchiveSource = &ArchiveSource{Path: p, Version: version}
	return &out
}
func (c *Client) ArchiveList(ctx context.Context, root int64, p, dir string) (items []Entry, version string, sequential bool, err error) {
	q := url.Values{"root": {strconv.FormatInt(root, 10)}, "path": {p}, "dir": {dir}}
	var res struct {
		Items      []Entry `json:"items"`
		Version    string  `json:"version"`
		Sequential bool    `json:"sequential"`
	}
	err = c.do(ctx, "GET", "/api/archive/list", q, nil, &res)
	for i := range res.Items {
		res.Items[i].Root = root
	}
	return res.Items, res.Version, res.Sequential, archiveAPIError(err)
}
func (c *Client) archiveURL(endpoint string, q url.Values) (string, url.Values) {
	if !c.InArchive() {
		return endpoint, q
	}
	out := url.Values{}
	for k, v := range q {
		out[k] = append([]string{}, v...)
	}
	switch {
	case strings.HasPrefix(endpoint, "/api/download/"):
		root := strings.TrimPrefix(endpoint, "/api/download/")
		out.Set("root", root)
		out.Set("entry", out.Get("path"))
		out.Set("path", c.ArchiveSource.Path)
		endpoint = "/api/archive/file"
	case endpoint == "/api/backup/export":
		out["entry"] = out["path"]
		out["path"] = []string{c.ArchiveSource.Path}
		endpoint = "/api/archive/export"
	default:
		return endpoint, q
	}
	out.Set("version", c.ArchiveSource.Version)
	return endpoint, out
}
func (c *Client) clone() *Client {
	out := *c
	if c.ArchiveSource != nil {
		s := *c.ArchiveSource
		out.ArchiveSource = &s
	}
	return &out
}
func NewLocalArchive(filename string) *Client {
	h := &archiveio.Handler{Resolve: func(*http.Request) (string, error) { return filename, nil }}
	return &Client{Addr: "local", hc: &http.Client{Transport: localArchiveTransport{h}}, ArchiveSource: &ArchiveSource{Path: filename, Local: true}}
}

// localArchiveTransport uses the exact server handler without a listener or a
// disk extraction. A pipe supplies backpressure and propagates decode failures.
type localArchiveTransport struct{ handler http.Handler }

func (t localArchiveTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(r.Context())
	r = r.Clone(ctx)
	pr, pw := io.Pipe()
	ready := make(chan *http.Response, 1)
	w := &archiveResponse{header: make(http.Header), ready: ready, w: pw}
	go func() {
		var failure error
		defer func() {
			if p := recover(); p != nil {
				failure = io.ErrUnexpectedEOF
			}
			if !w.started {
				w.WriteHeader(200)
			}
			for k := range w.response.Trailer {
				w.response.Trailer[k] = w.header.Values(k)
			}
			pw.CloseWithError(failure)
		}()
		t.handler.ServeHTTP(w, r)
	}()
	select {
	case res := <-ready:
		res.Body = &cancelBody{ReadCloser: pr, cancel: cancel}
		return res, nil
	case <-ctx.Done():
		cancel()
		pr.Close()
		return nil, ctx.Err()
	}
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error { b.cancel(); return b.ReadCloser.Close() }

type archiveResponse struct {
	header   http.Header
	ready    chan *http.Response
	w        io.Writer
	started  bool
	response *http.Response
}

func (w *archiveResponse) Header() http.Header { return w.header }
func (w *archiveResponse) WriteHeader(status int) {
	if w.started {
		return
	}
	w.started = true
	res := &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Header: w.header.Clone(), ContentLength: -1, Trailer: make(http.Header)}
	if size := res.Header.Get("Content-Length"); size != "" {
		res.ContentLength, _ = strconv.ParseInt(size, 10, 64)
	}
	for _, key := range strings.Split(res.Header.Get("Trailer"), ",") {
		if key = strings.TrimSpace(key); key != "" {
			res.Trailer[key] = nil
		}
	}
	w.response = res
	w.ready <- res
}
func (w *archiveResponse) Write(p []byte) (int, error) {
	if !w.started {
		w.WriteHeader(200)
	}
	return w.w.Write(p)
}

func (c *Client) ArchiveListProgress(ctx context.Context, root int64, p, dir string, progress func(int64, int64)) ([]Entry, string, bool, error) {
	q := url.Values{"root": {strconv.FormatInt(root, 10)}, "path": {p}, "dir": {dir}, "progress": {"1"}}
	res, err := c.send(ctx, "GET", "/api/archive/list", q, nil)
	if err != nil {
		return nil, "", false, archiveAPIError(err)
	}
	defer res.Body.Close()
	dec := json.NewDecoder(res.Body)
	for {
		var result struct {
			End        bool   `json:"end"`
			Error      string `json:"error"`
			Read, Size int64
			Items      []Entry `json:"items"`
			Version    string  `json:"version"`
			Sequential bool    `json:"sequential"`
		}
		if err := dec.Decode(&result); err != nil {
			return nil, "", false, err
		}
		if result.Error != "" {
			return nil, "", false, errors.New(result.Error)
		}
		if result.End {
			for i := range result.Items {
				result.Items[i].Root = root
			}
			return result.Items, result.Version, result.Sequential, nil
		}
		if progress != nil {
			progress(result.Read, result.Size)
		}
	}
}
func (w *archiveResponse) Flush() {
	if !w.started {
		w.WriteHeader(200)
	}
}

func archiveAPIError(err error) error {
	var status *StatusError
	if errors.As(err, &status) && status.Code == http.StatusNotFound && (status.Msg == "not found" || status.Msg == "404 Not Found") {
		return errors.New("NAS 不支持压缩包浏览，请先更新 NAS 上的 fileindex")
	}
	return err
}

// ExtractArchive queues extraction on the NAS; file data never passes through
// this client. Empty entries extracts the entire archive, keeping both names.
func (c *Client) ExtractArchive(ctx context.Context, root int64, filename, version string, entries []string, dstRoot int64, dir string) (*Task, error) {
	base := *c
	base.ArchiveSource = nil
	var out struct {
		Task *Task `json:"task"`
	}
	err := base.post(ctx, "/api/archive/extract", map[string]any{
		"root": root, "path": filename, "version": version, "entries": entries,
		"dst_root": dstRoot, "dst_dir": dir, "conflict": "rename",
	}, &out)
	var status *StatusError
	if errors.As(err, &status) && status.Code == http.StatusNotFound && status.Msg == "not found" {
		return nil, errors.New("NAS 不支持服务器本地解压，请先更新 NAS 上的 fileindex")
	}
	if err == nil && out.Task == nil {
		err = errors.New("NAS 没有返回解压任务")
	}
	return out.Task, err
}
