package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"filemanager/api"
	"filemanager/client"
)

func TestMutationRoundTripAndJournal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("atomic rename runtime")
	}
	root := t.TempDir()
	filename := filepath.Join(t.TempDir(), "mutations.log")
	s, err := New([]api.Root{{ID: "r", Path: root}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnableWrites(filename); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s.Handler())
	defer func() { server.Close(); s.Close() }()
	c := client.New(server.URL, server.Client())
	loc := func(p string) api.Location { return api.Location{Root: "r", Path: p} }
	call := func(action, from, to string) error {
		q := api.MutationRequest{Action: action}
		if from != "" {
			q.Source = loc(from)
		}
		if to != "" {
			q.Target = loc(to)
		}
		_, err := c.Mutate(context.Background(), q)
		return err
	}
	if err := call("mkdir", "", "original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "original", "file"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := call("mkdir", "", "taken"); err != nil {
		t.Fatal(err)
	}
	if err := call("rename", "original", "taken"); err == nil {
		t.Fatal("overwrote destination")
	}
	if _, err := os.Stat(filepath.Join(root, "original", "file")); err != nil {
		t.Fatal(err)
	}
	name := "new-\xff"
	if err := call("rename", "original", name); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "precious"), []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, name, "link")); err != nil {
		t.Fatal(err)
	}
	if err := call("delete", name, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "precious")); err != nil {
		t.Fatal(err)
	}
	var records []mutationRecord
	if err := s.writer.log.Replay(func(_ uint64, b []byte) error {
		var r mutationRecord
		if err := json.Unmarshal(b, &r); err != nil {
			return err
		}
		records = append(records, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(records) != 10 || records[4].Phase != "begin" || records[5].Phase != "failed" || records[9].Phase != "done" {
		t.Fatalf("records: %+v", records)
	}
}

func TestMutationBoundaries(t *testing.T) {
	root := t.TempDir()
	s, err := New([]api.Root{{ID: "r", Path: root}, {ID: "ro", Path: root, ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	log := filepath.Join(root, "state", "mutations.log")
	if err := s.EnableWrites(log); err != nil {
		t.Fatal(err)
	}
	s.HiddenPath = func(_ string, p string) bool { return p == "private" || p == "dir/nested/private" }
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	c := client.New(server.URL, server.Client())
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(root, "escape"))
	os.Symlink(filepath.Join(root, "state"), filepath.Join(root, "alias"))
	os.Link(log, filepath.Join(root, "hardlink"))
	for _, q := range []api.MutationRequest{
		{Action: "mkdir", Target: api.Location{Root: "r", Path: "../escape"}},
		{Action: "mkdir", Target: api.Location{Root: "r", Path: "escape/new"}},
		{Action: "mkdir", Target: api.Location{Root: "ro", Path: "new"}},
		{Action: "delete", Source: api.Location{Root: "r", Path: "."}},
		{Action: "delete", Source: api.Location{Root: "r", Path: "state"}},
		{Action: "delete", Source: api.Location{Root: "r", Path: "alias/mutations.log"}},
		{Action: "delete", Source: api.Location{Root: "r", Path: "hardlink"}},
		{Action: "delete", Source: api.Location{Root: "r", Path: "private"}},
		{Action: "trash", Source: api.Location{Root: "r", Path: "state"}},
	} {
		if _, err := c.Mutate(context.Background(), q); err == nil {
			t.Fatalf("allowed %+v", q)
		}
	}
	if err := s.EnableDesktopTrash(); err == nil {
		t.Fatal("restricted root enabled desktop trash")
	}
	os.MkdirAll(filepath.Join(root, "dir/nested/private"), 0700)
	if _, err := c.Mutate(context.Background(), api.MutationRequest{Action: "delete", Source: api.Location{Root: "r", Path: "dir"}}); err == nil {
		t.Fatal("deleted hidden descendant")
	}
	if _, err := os.Stat(filepath.Join(root, "dir/nested/private")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.removeTree(ctx, s.roots["r"], "dir"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	second, _ := New(nil)
	defer second.Close()
	if err := second.EnableWrites(log); err == nil {
		t.Fatal("two journal owners")
	}
}

func TestMutationDisabledByDefault(t *testing.T) {
	s, err := New([]api.Root{{ID: "r", Path: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	c := client.New(server.URL, server.Client())
	if _, err := c.Mutate(context.Background(), api.MutationRequest{Action: "mkdir", Target: api.Location{Root: "r", Path: "new"}}); err == nil {
		t.Fatal("writes enabled without journal")
	}
}

func TestUnfinishedMutationIsNotReexecuted(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(t.TempDir(), "operations.log")
	s, _ := New([]api.Root{{ID: "r", Path: root}})
	if err := s.EnableWrites(log); err != nil {
		t.Fatal(err)
	}
	q := api.MutationRequest{Action: "delete", Source: api.Location{Root: "r", Path: "keep"}}
	b, _ := json.Marshal(mutationRecord{Phase: "begin", Request: &q})
	if _, err := s.writer.log.Append(b); err != nil {
		t.Fatal(err)
	}
	s.Close()
	os.WriteFile(filepath.Join(root, "keep"), []byte("keep"), 0600)
	s, _ = New([]api.Root{{ID: "r", Path: root}})
	defer s.Close()
	if err := s.EnableWrites(log); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "keep")); err != nil {
		t.Fatal(err)
	}
	count := 0
	s.writer.log.Replay(func(_ uint64, _ []byte) error { count++; return nil })
	if count != 1 {
		t.Fatalf("pending evidence lost: %d", count)
	}
}

func TestDeleteCancellationRecordsPartialOutcome(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "dir"), 0700)
	for _, name := range []string{"a", "b", "c", "d"} {
		os.WriteFile(filepath.Join(root, "dir", name), []byte(name), 0600)
	}
	s, err := New([]api.Root{{ID: "r", Path: root}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.EnableWrites(filepath.Join(t.TempDir(), "mutations.log")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.HiddenPath = func(_ string, p string) bool {
		// Cancel during traversal, after the durable intent and before the next entry.
		if filepath.Dir(p) == "dir" {
			cancel()
		}
		return false
	}
	q := api.MutationRequest{Action: "delete", Source: api.Location{Root: "r", Path: "dir"}}
	body, _ := json.Marshal(q)
	req := httptest.NewRequest("POST", "/v1/mutate", bytes.NewReader(body)).WithContext(ctx)
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, req)
	if response.Code/100 == 2 {
		t.Fatal("canceled operation reported success")
	}
	children, err := os.ReadDir(filepath.Join(root, "dir"))
	if err != nil || len(children) == 0 {
		t.Fatalf("cancellation lost remainder: %v %v", children, err)
	}
	var records []mutationRecord
	s.writer.log.Replay(func(_ uint64, p []byte) error {
		var r mutationRecord
		json.Unmarshal(p, &r)
		records = append(records, r)
		return nil
	})
	if len(records) != 2 || records[0].Phase != "begin" || records[1].Phase != "failed" || records[1].Error != context.Canceled.Error() {
		t.Fatalf("outcome: %+v", records)
	}
}
