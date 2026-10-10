package fileindex_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filesync/fileindex"
	"filesync/fileindex/fitest"
)

func TestLocalProxyRangesSnapshotsAndRevocation(t *testing.T) {
	f := fitest.New(t)
	os.MkdirAll(filepath.Join(f.Root, "目录"), 0755)
	os.WriteFile(filepath.Join(f.Root, "目录", "媒体 #?.mp4"), []byte("0123456789"), 0644)
	p, err := fileindex.NewLocalProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c := fileindex.New(f.Addr(), "tok")
	address, err := p.Register("nas", c, 1, "目录/媒体 #?.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(address, "tok") || strings.Contains(address, f.Addr()) {
		t.Fatal("NAS credentials/address exposed")
	}
	c.Token = "changed"
	c.Addr = "unavailable"
	req, _ := http.NewRequest("GET", address, nil)
	req.Header.Set("Range", "bytes=3-5")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 206 || string(body) != "345" || res.Header.Get("Content-Range") != "bytes 3-5/10" {
		t.Fatalf("range %d %q %v", res.StatusCode, body, res.Header)
	}
	if res.Header.Get("Set-Cookie") != "" {
		t.Fatal("cookie leaked")
	}
	res, err = http.Head(address)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || res.ContentLength != 10 {
		t.Fatalf("HEAD %d %d", res.StatusCode, res.ContentLength)
	}
	for _, test := range []struct {
		method, host, origin, suffix string
		code                         int
	}{
		{"GET", "evil.example", "", "", 403}, {"GET", "", "http://example.com", "", 403}, {"POST", "", "", "", 405}, {"GET", "", "", "?path=secret", 403}, {"GET", "", "", "x", 404},
	} {
		req, _ := http.NewRequest(test.method, address+test.suffix, nil)
		if test.host != "" {
			req.Host = test.host
		}
		req.Header.Set("Origin", test.origin)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != test.code {
			t.Fatalf("%+v: %d", test, res.StatusCode)
		}
	}
	f.Expired.Store(true)
	res, err = http.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("expired %d", res.StatusCode)
	}
	p.Revoke("nas")
	res, err = http.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("revoked %d", res.StatusCode)
	}
	p.Close()
	if _, err := p.Register("nas", c, 1, "x"); err == nil {
		t.Fatal("registered after close")
	}
	if res, err := http.Get(address); err == nil {
		res.Body.Close()
		t.Fatal("still serving after close")
	}
}
func TestLocalProxyRevokesActiveStream(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	nas := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(stopped)
	}))
	defer nas.Close()
	p, err := fileindex.NewLocalProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	u, _ := url.Parse(nas.URL)
	address, _ := p.Register("nas", fileindex.New(u.Host, "tok"), 1, "video.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", address, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
	}()
	<-started
	p.Revoke("nas")
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("upstream not canceled")
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("downstream not stopped")
	}
}
func TestPreviewBoundedRangeAndEmpty(t *testing.T) {
	f := fitest.New(t)
	c := fileindex.New(f.Addr(), "tok")
	os.WriteFile(filepath.Join(f.Root, "text.txt"), []byte("123456789"), 0644)
	os.WriteFile(filepath.Join(f.Root, "empty.txt"), nil, 0644)
	data, cut, err := c.ReadPreview(context.Background(), 1, "text.txt", 4)
	if err != nil || !cut || string(data) != "1234" {
		t.Fatalf("%q %v %v", data, cut, err)
	}
	data, cut, err = c.ReadPreview(context.Background(), 1, "empty.txt", 4)
	if err != nil || cut || len(data) != 0 {
		t.Fatalf("empty %q %v %v", data, cut, err)
	}
	f.Expired.Store(true)
	if _, _, err = c.ReadPreview(context.Background(), 1, "text.txt", 4); err != fileindex.ErrAuth {
		t.Fatal(err)
	}
}
func TestProxyDownloadInterruptedKeepsCache(t *testing.T) {
	nas := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("partial"))
	}))
	defer nas.Close()
	p, err := fileindex.NewLocalProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	u, _ := url.Parse(nas.URL)
	address, _ := p.Register("nas", fileindex.New(u.Host, "tok"), 1, "document.pdf")
	dest := filepath.Join(t.TempDir(), "document.pdf")
	os.WriteFile(dest, []byte("old"), 0600)
	if err := fileindex.DownloadProxyFile(context.Background(), address, dest, nil); err == nil {
		t.Fatal("accepted interrupted body")
	}
	data, _ := os.ReadFile(dest)
	if string(data) != "old" {
		t.Fatalf("cache replaced %q", data)
	}
}
