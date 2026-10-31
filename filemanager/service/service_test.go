package service_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"filemanager/api"
	"filemanager/client"
	"filemanager/service"
)

func TestRootBoundaryAndBrowse(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("hello"), 0600)
	os.WriteFile(filepath.Join(dir, ".hidden"), nil, 0600)
	os.Mkdir(filepath.Join(dir, "sub"), 0700)
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	s, err := service.New([]api.Root{{ID: "test", Path: dir}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	c := client.New(server.URL, server.Client())
	ctx := context.Background()
	if _, err := c.Info(ctx); err != nil {
		t.Fatal(err)
	}
	es, err := c.List(ctx, api.ListRequest{Location: api.Location{Root: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 3 || es[0].Name != "sub" {
		t.Fatalf("entries: %+v", es)
	}
	for _, p := range []string{"../", "/etc", "sub/../../", "escape"} {
		if _, err := c.List(ctx, api.ListRequest{Location: api.Location{Root: "test", Path: p}}); err == nil {
			t.Fatalf("escaped root via %s", p)
		}
	}
	if _, err := c.Stat(ctx, api.Location{Root: "missing"}); err == nil {
		t.Fatal("unknown root accepted")
	}
	os.WriteFile(filepath.Join(dir, "sub", "child"), nil, 0600)
	if err := os.Symlink(filepath.Join(dir, "sub"), filepath.Join(dir, "inside")); err != nil {
		t.Fatal(err)
	}
	inside, err := c.List(ctx, api.ListRequest{Location: api.Location{Root: "test", Path: "inside"}})
	if err != nil || len(inside) != 1 || inside[0].Path != "inside/child" {
		t.Fatalf("root-internal alias: %+v %v", inside, err)
	}
}

func TestUnixFilenameBytesSurviveProtocol(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix filenames")
	}
	dir := t.TempDir()
	name := string([]byte{'n', 0xff, '\\', 'x'})
	if err := os.WriteFile(filepath.Join(dir, name), []byte("bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := service.New([]api.Root{{ID: "test", Path: dir}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := client.New(srv.URL, srv.Client())
	entries, err := c.List(context.Background(), api.ListRequest{Location: api.Location{Root: "test"}})
	if err != nil || len(entries) != 1 || entries[0].Name != name || entries[0].Path != name {
		t.Fatalf("filename changed: %+v %v", entries, err)
	}
	e, err := c.Stat(context.Background(), api.Location{Root: "test", Path: name})
	if err != nil || e.Name != name || e.Size != 5 {
		t.Fatalf("stat: %+v %v", e, err)
	}
}
