package service

import (
	"context"
	"filemanager/api"
	"filemanager/client"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestReadAndTallyRespectRootAndHiddenPaths(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(dir, "file"), []byte("abcdef"), 0600)
	os.WriteFile(filepath.Join(dir, "secret"), []byte("private"), 0600)
	os.WriteFile(filepath.Join(outside, "external"), []byte("external"), 0600)
	os.Symlink(outside, filepath.Join(dir, "escape"))
	os.Symlink("secret", filepath.Join(dir, "alias"))
	s, err := New([]api.Root{{ID: "r", Path: dir}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.HiddenPath = func(root, path string) bool { return path == "secret" }
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := client.New(srv.URL, srv.Client())
	ctx := context.Background()
	body, err := c.Read(ctx, api.ReadRequest{Location: api.Location{Root: "r", Path: "file"}, Offset: 1, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(body)
	body.Close()
	if err != nil || string(b) != "bcd" {
		t.Fatalf("read %q %v", b, err)
	}
	for _, path := range []string{"escape/external", "secret", "alias", "../external", "."} {
		body, err := c.Read(ctx, api.ReadRequest{Location: api.Location{Root: "r", Path: path}, Limit: 100})
		if body != nil {
			body.Close()
		}
		if err == nil {
			t.Fatalf("read accepted %q", path)
		}
	}
	tally, err := c.Tally(ctx, api.Location{Root: "r"}, nil)
	if err != nil || tally.Files != 3 || tally.Dirs != 0 || tally.Size != 6 {
		t.Fatalf("tally %+v %v", tally, err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Tally(cancelCtx, api.Location{Root: "r"}, nil); err == nil {
		t.Fatal("ignored cancellation")
	}
}
