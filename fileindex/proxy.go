package fileindex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

// LocalProxy gives system applications a loopback URL without the NAS session.
// Each unpredictable URL refers to an immutable source; it cannot choose another
// NAS path. Revoking a source also interrupts its active streams.
type LocalProxy struct {
	mu         sync.Mutex
	entries    map[string]proxySource
	srv        *http.Server
	addr       string
	closed     bool
	authFailed func(string, *Client)
}
type proxySource struct {
	owner  string
	client *Client
	root   int64
	path   string
	ctx    context.Context
	cancel context.CancelFunc
}

func NewLocalProxy(onAuth ...func(string, *Client)) (*LocalProxy, error) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &LocalProxy{entries: map[string]proxySource{}, addr: ln.Addr().String()}
	if len(onAuth) > 0 {
		p.authFailed = onAuth[0]
	}
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go p.srv.Serve(ln)
	return p, nil
}
func (p *LocalProxy) Register(owner string, c *Client, root int64, rel string) (string, error) {
	var secret [24]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	key := "/file/" + hex.EncodeToString(secret[:]) + "/" + path.Base(rel)
	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		cancel()
		return "", fmt.Errorf("本机代理已关闭")
	}
	p.entries[key] = proxySource{owner: owner, client: New(c.Addr, c.Token), root: root, path: rel, ctx: ctx, cancel: cancel}
	return (&url.URL{Scheme: "http", Host: p.addr, Path: key}).String(), nil
}
func (p *LocalProxy) Revoke(owner string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, src := range p.entries {
		if src.owner == owner {
			src.cancel()
			delete(p.entries, key)
		}
	}
}
func (p *LocalProxy) Close() error {
	p.mu.Lock()
	p.closed = true
	for key, src := range p.entries {
		src.cancel()
		delete(p.entries, key)
	}
	p.mu.Unlock()
	return p.srv.Close()
}
func (p *LocalProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check Host as well as the listener to reject DNS rebinding; browser-origin
	// requests cannot read file capabilities via this service.
	if r.Host != p.addr || r.Header.Get("Origin") != "" || r.URL.RawQuery != "" {
		http.Error(w, "Forbidden", 403)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", 405)
		return
	}
	p.mu.Lock()
	src, ok := p.entries[r.URL.Path]
	p.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(src.ctx, cancel)
	defer stop()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, src.client.url(fmt.Sprintf("/api/download/%d", src.root), url.Values{"path": {src.path}, "inline": {"1"}}), nil)
	if err != nil {
		http.Error(w, "Invalid source", 502)
		return
	}
	req.AddCookie(&http.Cookie{Name: cookieName, Value: src.client.Token})
	for _, h := range []string{"Range", "If-Range", "If-Modified-Since", "If-None-Match"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	res, err := src.client.hc.Do(req)
	if err != nil {
		http.Error(w, "NAS connection failed", 502)
		return
	}
	defer res.Body.Close()
	if res.StatusCode == 401 && p.authFailed != nil {
		p.authFailed(src.owner, New(src.client.Addr, src.client.Token))
	}
	// Only copy representation headers: never cookies, authentication, redirects,
	// or arbitrary server headers into the loopback application.
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := res.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+url.PathEscape(path.Base(src.path)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if res.StatusCode/100 != 2 && res.StatusCode != 304 && res.StatusCode != 416 {
		// Do not return the NAS error body, which may contain local paths.
		for _, h := range []string{"Content-Length", "Content-Range", "Content-Type"} {
			w.Header().Del(h)
		}
		http.Error(w, "NAS: "+http.StatusText(res.StatusCode), res.StatusCode)
		return
	}
	w.WriteHeader(res.StatusCode)
	if r.Method == "GET" {
		if _, err := io.Copy(w, res.Body); err != nil {
			panic(http.ErrAbortHandler)
		}
	}
}

// DownloadProxyFile materializes a capability URL for applications (Office,
// PDF readers) that require a local file. It uses the same atomic file writer
// as regular downloads; incomplete responses never replace an existing cache.
func DownloadProxyFile(ctx context.Context, address, dest string, onProgress func(Progress)) error {
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return err
	}
	hc := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == 401 {
		return ErrAuth
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("本机代理: %s", res.Status)
	}
	mtime, _ := http.ParseTime(res.Header.Get("Last-Modified"))
	pr := Progress{Total: max(res.ContentLength, 0), Current: strings.TrimPrefix(req.URL.Path, "/file/")}
	return writeFile(dest, res.Body, 0600, mtime, func(n int64) {
		pr.Done += n
		if onProgress != nil {
			onProgress(pr)
		}
	})
}
