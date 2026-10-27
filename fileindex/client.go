// Package fileindex backs up to a fileindex server, the file index running
// on the NAS: it lists the target directory there in one request, sends
// the files as one tar stream over one connection, which the server
// unpacks and acknowledges file by file, and moves what a mirror deletes
// to the server's trash.
package fileindex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrAuth means the server wants the password: the session is missing or
// expired.
var ErrAuth = errors.New("需要登录 NAS")

const (
	cookieName = "fi_session"
	csrfHeader = "X-Requested-With"
	csrfValue  = "fileindex"
)

// Client talks to one fileindex server.
type Client struct {
	// Addr is the server's host and port, as 192.168.0.235:8090.
	Addr string
	// Token is the session, which Login sets.
	Token string

	ArchiveSource *ArchiveSource
	hc            *http.Client
}

// transport keeps connections to the server open between requests, with
// buffers large enough to keep a gigabit link busy, and never goes through
// a proxy: the server is on the local network.
var transport = &http.Transport{
	Proxy:               nil,
	DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	MaxIdleConnsPerHost: 32,
	IdleConnTimeout:     90 * time.Second,
	WriteBufferSize:     256 << 10,
	ReadBufferSize:      256 << 10,
	DisableCompression:  true,
}

// New returns a client of the server at addr, with a session if token is
// not empty.
func New(addr, token string) *Client {
	return &Client{Addr: addr, Token: token, hc: &http.Client{Transport: transport}}
}

func (c *Client) url(path string, q url.Values) string {
	path, q = c.archiveURL(path, q)
	u := "http://" + c.Addr + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// do sends a request and decodes the JSON answer into out, if not nil.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body io.Reader, out any) error {
	res, err := c.send(ctx, method, path, q, body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if out == nil {
		io.Copy(io.Discard, res.Body)
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("NAS 的回答无法解析: %w", err)
	}
	return nil
}

// send sends a request and returns the answer when it succeeded.
func (c *Client) send(ctx context.Context, method, path string, q url.Values, body io.Reader) (*http.Response, error) {
	return c.sendHeaders(ctx, method, path, q, body, nil)
}

func (c *Client) sendHeaders(ctx context.Context, method, path string, q url.Values, body io.Reader, headers http.Header) (*http.Response, error) {
	if c.InArchive() && method != "GET" && method != "HEAD" {
		return nil, errors.New("压缩包内容只读")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path, q), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set(csrfHeader, csrfValue)
	for key, values := range headers {
		req.Header[key] = values
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: c.Token})
	}
	res, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("无法连接 NAS %s: %w", c.Addr, err)
	}
	if res.StatusCode/100 == 2 {
		return res, nil
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return nil, ErrAuth
	}
	var e struct {
		Error string `json:"error"`
	}
	json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&e)
	if e.Error == "" {
		e.Error = res.Status
	}
	return nil, &StatusError{Code: res.StatusCode, Msg: e.Error}
}

// StatusError is an error the server answered.
type StatusError struct {
	Code int
	Msg  string
}

func (e *StatusError) Error() string { return "NAS: " + e.Msg }

// Login signs in with the password, setting Token.
func (c *Client) Login(ctx context.Context, password string) error {
	b, _ := json.Marshal(map[string]string{"password": password})
	res, err := c.send(ctx, "POST", "/api/auth/login", nil, strings.NewReader(string(b)))
	if err != nil {
		var se *StatusError
		if errors.Is(err, ErrAuth) || errors.As(err, &se) && se.Code == http.StatusUnauthorized {
			return errors.New("密码错误")
		}
		return err
	}
	defer res.Body.Close()
	for _, ck := range res.Cookies() {
		if ck.Name == cookieName {
			c.Token = ck.Value
			return nil
		}
	}
	return errors.New("NAS 没有返回登录会话")
}

// Logout ends the session on the server.
func (c *Client) Logout(ctx context.Context) error {
	err := c.do(ctx, "POST", "/api/auth/logout", nil, nil, nil)
	c.Token = ""
	return err
}

// Authed reports whether the session is valid.
func (c *Client) Authed(ctx context.Context) (bool, error) {
	var st struct {
		Authed bool `json:"authed"`
	}
	if err := c.do(ctx, "GET", "/api/auth/status", nil, nil, &st); err != nil {
		return false, err
	}
	return st.Authed, nil
}

// Root is a directory the server indexes, under which backups go.
type Root struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	ReadOnly   bool       `json:"readonly"`
	Excludes   []string   `json:"excludes"`
	LastScanAt int64      `json:"last_scan_at"`
	FileCount  int64      `json:"file_count"`
	DirCount   int64      `json:"dir_count"`
	TotalSize  int64      `json:"total_size"`
	Schedule   string     `json:"schedule"`
	NextScanAt int64      `json:"next_scan_at"`
	Active     *ScanEvent `json:"active"`
	LastJob    *ScanJob   `json:"last_job"`
}

// Roots lists the server's directories.
func (c *Client) Roots(ctx context.Context) ([]Root, error) {
	var out struct {
		Roots []Root `json:"roots"`
	}
	err := c.do(ctx, "GET", "/api/roots", nil, nil, &out)
	return out.Roots, err
}

// Dirs lists the subdirectories of dir in a root, from the disk.
func (c *Client) Dirs(ctx context.Context, root int64, dir string) ([]string, error) {
	const size = 1000
	var out []string
	for page := 1; ; page++ {
		var res struct {
			Items []struct {
				Name  string `json:"name"`
				IsDir bool   `json:"is_dir"`
			} `json:"items"`
			Total int `json:"total"`
		}
		q := url.Values{"path": {dir}, "live": {"1"}, "size": {strconv.Itoa(size)}, "page": {strconv.Itoa(page)}}
		if err := c.do(ctx, "GET", fmt.Sprintf("/api/roots/%d/list", root), q, nil, &res); err != nil {
			return nil, err
		}
		for _, it := range res.Items {
			if !it.IsDir {
				return out, nil // directories come first
			}
			out = append(out, it.Name)
		}
		if page*size >= res.Total {
			return out, nil
		}
	}
}

// Mkdir makes the directory name in dir of a root.
func (c *Client) Mkdir(ctx context.Context, root int64, dir, name string) error {
	b, _ := json.Marshal(map[string]any{"root": root, "dir": dir, "name": name})
	return c.do(ctx, "POST", "/api/fs/mkdir", nil, strings.NewReader(string(b)), nil)
}

// ManifestEntry is an entry of a directory the server lists for a backup.
type ManifestEntry struct {
	Path    string `json:"p"`
	Dir     bool   `json:"d"`
	Size    int64  `json:"s"`
	MTime   int64  `json:"m"` // unix nanoseconds
	Mode    uint32 `json:"o"`
	Link    string `json:"l"` // the target of a symbolic link
	Special bool   `json:"x"`
	Err     string `json:"e"`
	// Error, on the last line, says the listing stopped before its end.
	Error string `json:"error"`
}

// Manifest lists everything below path in a root, calling fn for each
// entry, parents first. missing is true when path does not exist.
func (c *Client) Manifest(ctx context.Context, root int64, path string, fn func(ManifestEntry)) (missing bool, err error) {
	q := url.Values{"root": {strconv.FormatInt(root, 10)}, "path": {path}}
	res, err := c.send(ctx, "GET", "/api/backup/manifest", q, nil)
	if err != nil {
		var se *StatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound && se.Msg == "目录不存在" {
			return true, nil
		}
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return false, errors.New("NAS 不支持备份接口，请先更新 NAS 上的 fileindex")
		}
		return false, err
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e ManifestEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return false, fmt.Errorf("NAS 的目录清单无法解析: %w", err)
		}
		if e.Error != "" {
			return false, errors.New("NAS 列目录出错: " + e.Error)
		}
		fn(e)
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, fmt.Errorf("读取 NAS 的目录清单出错: %w", err)
	}
	return false, nil
}

// Tar starts sending a tar stream below path of a root, body being the
// stream. It returns as soon as the server answers, while body still goes:
// the answer is a JSON line for each entry as it lands, which Answer
// decodes.
func (c *Client) Tar(ctx context.Context, root int64, path string, body io.Reader) (io.ReadCloser, error) {
	q := url.Values{"root": {strconv.FormatInt(root, 10)}, "path": {path}}
	req, err := http.NewRequestWithContext(ctx, "POST", c.url("/api/backup/tar", q), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set(csrfHeader, csrfValue)
	req.Header.Set("Content-Type", "application/x-tar")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: c.Token})
	res, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("发送到 NAS 失败: %w", err)
	}
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		res.Body.Close()
		return nil, ErrAuth
	case res.StatusCode == http.StatusNotFound:
		res.Body.Close()
		return nil, errors.New("NAS 不支持备份接口，请先更新 NAS 上的 fileindex")
	case res.StatusCode/100 != 2:
		defer res.Body.Close()
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&e)
		if e.Error == "" {
			e.Error = res.Status
		}
		return nil, &StatusError{Code: res.StatusCode, Msg: e.Error}
	}
	return res.Body, nil
}

// Answer is a line of the answer to Tar: an entry written, or that failed,
// or the end.
type Answer struct {
	Path  string `json:"p"`
	Error string `json:"e"`
	End   bool   `json:"end"`
	// Fatal, at the end, says the server stopped reading the stream.
	Fatal string `json:"error"`
}

// Discard is the entry that tells the server to remove the file just
// sent, with its path in the PAX record DiscardKey.
const (
	DiscardName = ".fileindex-backup-discard"
	DiscardKey  = "FILEINDEX.discard"
)

// SaveFile sends body as the file name in dir of a root, which the server
// shows under that name only once complete, numbered if the name is taken.
// It returns the path the file got.
func (c *Client) SaveFile(ctx context.Context, root int64, dir, name string, body io.Reader) (string, error) {
	q := url.Values{"root": {strconv.FormatInt(root, 10)}, "path": {dir}, "name": {name}}
	sized := &sizeTrailer{r: body}
	req, err := http.NewRequestWithContext(ctx, "POST", c.url("/api/backup/file", q), sized)
	if err != nil {
		return "", err
	}
	// The body ends with its length, for the server to tell a file cut
	// short.
	req.Trailer = http.Header{sizeTrailerName: nil}
	sized.req = req
	req.Header.Set(csrfHeader, csrfValue)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: c.Token})
	res, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("发送到 NAS 失败: %w", err)
	}
	defer res.Body.Close()
	var out struct {
		Path  string `json:"path"`
		Error string `json:"error"`
	}
	json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out)
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		return "", ErrAuth
	case res.StatusCode == http.StatusNotFound && out.Error == "not found":
		return "", errors.New("NAS 不支持 tar 归档，请先更新 NAS 上的 fileindex")
	case res.StatusCode/100 != 2:
		if out.Error == "" {
			out.Error = res.Status
		}
		return "", &StatusError{Code: res.StatusCode, Msg: out.Error}
	}
	return out.Path, nil
}

const sizeTrailerName = "X-Backup-Size"

// sizeTrailer counts a request body, and sets its length as the trailer
// once the body ends well.
type sizeTrailer struct {
	r   io.Reader
	req *http.Request
	n   int64
}

func (s *sizeTrailer) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	s.n += int64(n)
	if err == io.EOF {
		s.req.Trailer.Set(sizeTrailerName, strconv.FormatInt(s.n, 10))
	}
	return n, err
}

// DeleteForever deletes paths of a root, not to its trash, as a task of
// the server.
func (c *Client) DeleteForever(ctx context.Context, root int64, paths []string) (*Task, error) {
	b, _ := json.Marshal(map[string]any{"root": root, "paths": paths, "permanent": true})
	var out struct {
		Task *Task `json:"task"`
	}
	err := c.do(ctx, "POST", "/api/fs/delete", nil, strings.NewReader(string(b)), &out)
	if err == nil && out.Task == nil {
		err = fmt.Errorf("NAS 没有返回删除任务")
	}
	return out.Task, err
}

// Trash moves paths of a root to its trash, where they stay for a while.
func (c *Client) Trash(ctx context.Context, root int64, paths []string) error {
	b, _ := json.Marshal(map[string]any{"root": root, "paths": paths})
	return c.do(ctx, "POST", "/api/fs/delete", nil, strings.NewReader(string(b)), nil)
}
