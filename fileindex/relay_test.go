package fileindex

import (
	"archive/tar"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filesync/fileindex/fitest"
)

func TestRelayBetweenServers(t *testing.T) {
	from, to := fitest.New(t), fitest.New(t)
	write(t, from.Root, map[string]string{"项目/main.go": "package main", "项目/lib/x.so.1": "elf", "单个.txt": "one"})
	os.Symlink("x.so.1", filepath.Join(from.Root, "项目", "lib", "x.so"))
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(filepath.Join(from.Root, "单个.txt"), old, old)
	write(t, to.Root, map[string]string{"收/单个.txt": "replaced", "收/项目/留下.txt": "kept"})

	var last Progress
	whole, errs, err := Relay(context.Background(), New(from.Addr(), "tok"), 1, []string{"项目", "单个.txt"},
		New(to.Addr(), "tok"), 1, "收", func(p Progress) { last = p })
	if err != nil || len(errs) > 0 {
		t.Fatalf("relay: %v %v", errs, err)
	}
	if !whole["项目"] || !whole["单个.txt"] || len(whole) != 2 {
		t.Errorf("whole %v", whole)
	}
	got := tree(t, filepath.Join(to.Root, "收"))
	if got["项目/main.go"] != "package main" || got["项目/lib/x.so.1"] != "elf" || got["单个.txt"] != "one" || got["项目/留下.txt"] != "kept" {
		t.Fatalf("target has %v", got)
	}
	if l, err := os.Readlink(filepath.Join(to.Root, "收", "项目", "lib", "x.so")); err != nil || l != "x.so.1" {
		t.Errorf("link %q %v", l, err)
	}
	if fi, _ := os.Stat(filepath.Join(to.Root, "收", "单个.txt")); !fi.ModTime().Equal(old) {
		t.Errorf("mtime %v", fi.ModTime())
	}
	if last.Done == 0 {
		t.Errorf("progress %+v", last)
	}
	if got := tree(t, from.Root); got["单个.txt"] != "one" {
		t.Errorf("source changed: %v", got)
	}
}

func TestRelayReportsTargetFailures(t *testing.T) {
	from, to := fitest.New(t), fitest.New(t)
	write(t, from.Root, map[string]string{"a/1.txt": "1", "a/2.txt": "2", "b.txt": "b"})
	src, dst := New(from.Addr(), "tok"), New(to.Addr(), "tok")

	if _, _, err := Relay(context.Background(), src, 1, []string{"a"}, dst, 2, "", nil); err == nil {
		t.Error("read-only target reported success")
	}
	to.StopAfter.Store(2)
	if whole, _, err := Relay(context.Background(), src, 1, []string{"a", "b.txt"}, dst, 1, "", nil); err == nil || whole != nil {
		t.Errorf("broken target stream: %v %v", whole, err)
	}
}

// An export cut short never reaches the target as a whole stream.
func TestRelayRefusesIncompleteExport(t *testing.T) {
	var full bytes.Buffer
	tw := tar.NewWriter(&full)
	tw.WriteHeader(&tar.Header{Name: "a.txt", Typeflag: tar.TypeReg, Size: 1024, Mode: 0644})
	tw.Write(bytes.Repeat([]byte("a"), 1024))
	tw.Close()
	cases := map[string]http.HandlerFunc{
		"truncated": func(w http.ResponseWriter, r *http.Request) { w.Write(full.Bytes()[:600]) },
		// Between entries, as tar readers accept.
		"no end blocks": func(w http.ResponseWriter, r *http.Request) { w.Write(full.Bytes()[:1536]) },
		"not announced complete": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Trailer", "X-Export-Complete")
			w.Write(full.Bytes())
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			to := fitest.New(t)
			_, _, err := Relay(context.Background(), New(strings.TrimPrefix(srv.URL, "http://"), ""), 1, []string{"a.txt"},
				New(to.Addr(), "tok"), 1, "", nil)
			if err == nil || !strings.Contains(err.Error(), "源服务器") {
				t.Fatalf("incomplete export: %v", err)
			}
		})
	}
}
