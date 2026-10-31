package service

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"filemanager/api"
	"filemanager/client"
	"filemanager/index"
)

func TestIndexRPCPinsResultsAndReconciles(t *testing.T) {
	root := t.TempDir()
	name := "file-\xff.txt"
	old := filepath.Join(root, name)
	os.WriteFile(old, []byte("one"), 0600)
	s, err := New([]api.Root{{ID: "local", Path: "/"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.EnableWrites(filepath.Join(t.TempDir(), "journal")); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableLocalIndex(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := client.New(srv.URL, srv.Client())
	x := c.Index()
	defer x.Close()
	opts := index.ScanOptions{Roots: []string{root}}
	if _, err := x.Scan(context.Background(), opts, nil); err != nil {
		t.Fatal(err)
	}
	for _, q := range []index.Query{{Text: "file-?.txt", Hidden: true}, {Text: `^file-.*\.txt$`, Regex: true, Hidden: true}} {
		result, err := x.Search(context.Background(), q)
		if err != nil || result.Total() != 1 {
			t.Fatalf("pattern RPC %+v: %v %v", q, result, err)
		}
	}
	if _, err := x.Search(context.Background(), index.Query{Text: "[", Regex: true}); err == nil {
		t.Fatal("invalid regex accepted over RPC")
	}
	before, err := x.Search(context.Background(), index.Query{Hidden: true})
	if err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(root, "renamed")
	os.Rename(old, renamed)
	if err := x.Reconcile(context.Background(), opts, []string{old, renamed}); err != nil {
		t.Fatal(err)
	}
	entries, err := before.Page(context.Background(), 0, 10)
	if err != nil || len(entries) != 1 || entries[0].Path != old {
		t.Fatalf("pinned page: %+v %v", entries, err)
	}
	after, err := x.Search(context.Background(), index.Query{Hidden: true})
	if err != nil {
		t.Fatal(err)
	}
	entries, err = after.Page(context.Background(), 0, 10)
	if err != nil || len(entries) != 1 || entries[0].Path != renamed || after.Version() == before.Version() {
		t.Fatalf("new page: %+v %v", entries, err)
	}
	if paths := x.RefreshPaths(); !slices.Contains(paths, old) {
		t.Fatalf("refresh lost path bytes: %q", paths)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	if err := x.SaveSnapshot(context.Background(), snapshot, opts, index.SnapshotMeta{}); err != nil {
		t.Fatal(err)
	}
	y := c.Index()
	defer y.Close()
	if _, err := y.LoadSnapshot(context.Background(), snapshot, opts, nil); err != nil {
		t.Fatal(err)
	}
	if y.Status().Count == 0 {
		t.Fatal("lost loaded status")
	}
	if err := x.SaveSnapshot(context.Background(), s.writer.filename, opts, index.SnapshotMeta{}); err == nil {
		t.Fatal("overwrote operation journal")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := x.Scan(ctx, opts, nil); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestIndexReplaysBackendJournalWithoutForegroundBridge(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "old")
	os.WriteFile(old, []byte("one"), 0600)
	s, _ := New([]api.Root{{ID: "local", Path: "/"}})
	defer s.Close()
	if err := s.EnableWrites(filepath.Join(t.TempDir(), "journal")); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableLocalIndex(); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableTransfers(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := client.New(srv.URL, srv.Client())
	x := c.Index()
	defer x.Close()
	opts := index.ScanOptions{Roots: []string{root}}
	if _, err := x.Scan(context.Background(), opts, nil); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	if err := x.SaveSnapshot(context.Background(), snapshot, opts, index.SnapshotMeta{}); err != nil {
		t.Fatal(err)
	}
	loc := func(p string) api.Location { return api.Location{Root: "local", Path: strings.TrimPrefix(p, "/")} }
	renamed := filepath.Join(root, "renamed")
	if _, err := c.Mutate(context.Background(), api.MutationRequest{Action: "rename", Source: loc(old), Target: loc(renamed)}); err != nil {
		t.Fatal(err)
	}
	q := api.TransferRequest{ID: strings.Repeat("f", 32), Source: loc(renamed), Target: loc(filepath.Join(root, "copy")), FileConflict: "fail", DirectoryConflict: "fail"}
	if _, err := c.SubmitTransfer(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if state := waitTask(t, c, q.ID); state.State != "done" {
		t.Fatalf("%+v", state)
	}
	if _, err := x.LoadSnapshot(context.Background(), snapshot, opts, nil); err != nil {
		t.Fatal(err)
	}
	if err := x.ClearDeltas(); err != nil {
		t.Fatal(err)
	}
	live, err := x.Search(context.Background(), index.Query{Hidden: true})
	if err != nil || live.Total() != 2 {
		t.Fatalf("checkpoint discarded concurrent changes: %v %v", live, err)
	}
	y := c.Index()
	defer y.Close()
	if _, err := y.LoadSnapshot(context.Background(), snapshot, opts, nil); err != nil {
		t.Fatal(err)
	}
	if err := y.ClearDeltas(); err != nil {
		t.Fatal(err)
	}
	result, err := y.Search(context.Background(), index.Query{Hidden: true})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := result.Page(context.Background(), 0, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("replay %+v %v", entries, err)
	}
	for _, entry := range entries {
		if entry.Path == old {
			t.Fatal("stale source survived journal replay")
		}
	}
}

func TestIndexCloseReleasesResultLeases(t *testing.T) {
	s, err := New([]api.Root{{ID: "local", Path: "/"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.EnableLocalIndex(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	x := client.New(srv.URL, srv.Client()).Index()
	_, err = x.Scan(context.Background(), index.ScanOptions{Roots: []string{t.TempDir()}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := x.Search(context.Background(), index.Query{})
	if err != nil {
		t.Fatal(err)
	}
	x.Close()
	if _, err := result.Page(context.Background(), 0, 10); err == nil {
		t.Fatal("closed result survived")
	}
	s.indexes.mu.Lock()
	defer s.indexes.mu.Unlock()
	if len(s.indexes.sessions) != 0 || len(s.indexes.results) != 0 {
		t.Fatal("closed session was recreated")
	}
}

func TestResultCloseReleasesOnlyItsLease(t *testing.T) {
	s, err := New([]api.Root{{ID: "local", Path: "/"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.EnableLocalIndex(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	x := client.New(srv.URL, srv.Client()).Index()
	defer x.Close()
	if _, err = x.Scan(context.Background(), index.ScanOptions{Roots: []string{t.TempDir()}}, nil); err != nil {
		t.Fatal(err)
	}
	first, err := x.Search(context.Background(), index.Query{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := x.Search(context.Background(), index.Query{})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = first.Close(); err != nil {
			t.Fatal(err)
		}
	}
	s.indexes.mu.Lock()
	n := len(s.indexes.results)
	s.indexes.mu.Unlock()
	if n != 1 {
		t.Fatalf("retained %d results after close", n)
	}
	if _, err = first.Page(context.Background(), 0, 10); err == nil {
		t.Fatal("closed result readable")
	}
	if _, err = second.Page(context.Background(), 0, 10); err != nil {
		t.Fatal(err)
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
	s.indexes.mu.Lock()
	defer s.indexes.mu.Unlock()
	if len(s.indexes.results) != 0 {
		t.Fatal("last lease retained")
	}
}

func TestResultExpiryKeepsRecentlyUsedQueries(t *testing.T) {
	now := time.Now()
	m := &indexService{results: map[string]*searchSession{
		"expired": {used: now.Add(-11 * time.Minute)},
		"active":  {used: now.Add(-time.Minute)},
	}}
	m.expireResults(now)
	if len(m.results) != 1 || m.results["active"] == nil {
		t.Fatal("expiry failed")
	}
}
