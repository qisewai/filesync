package catalog

import (
	"context"
	"errors"
	"filemanager/index"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

func paths(t *testing.T, c *Catalog, r Root) []string {
	t.Helper()
	result, err := c.Search(context.Background(), r, index.Query{Hidden: true})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	err = result.Each(context.Background(), func(e index.Entry) error { p, _ := filepath.Rel(r.Path, e.Path); out = append(out, p); return nil })
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}
func TestSnapshotIntentRecoveryAndScope(t *testing.T) {
	ctx := context.Background()
	data, state := t.TempDir(), t.TempDir()
	r := Root{ID: "one", Path: data}
	os.Mkdir(filepath.Join(data, "old"), 0700)
	os.WriteFile(filepath.Join(data, "old", "file-\xff"), []byte("data"), 0600)
	c, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Scan(ctx, r, true, nil); err != nil {
		t.Fatal(err)
	}
	pinned, err := c.Search(ctx, r, index.Query{Hidden: true})
	if err != nil {
		t.Fatal(err)
	}
	// Crash after filesystem commit but before index reconciliation.
	if err := c.Record(ctx, r, []string{"old", "new"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(data, "old"), filepath.Join(data, "new")); err != nil {
		t.Fatal(err)
	}
	c.Close()
	c, err = New(state)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := paths(t, c, r); !slices.Equal(got, []string{"new", "new/file-\xff"}) {
		t.Fatalf("replay %q", got)
	}
	entries, err := pinned.Page(ctx, 0, 10)
	if err != nil || len(entries) != 2 || entries[0].Name != "old" {
		t.Fatalf("pinned %+v %v", entries, err)
	}
	r.Excludes = []string{"new"}
	if _, err := c.Scan(ctx, r, true, nil); err != nil {
		t.Fatal(err)
	}
	if got := paths(t, c, r); len(got) != 0 {
		t.Fatalf("scope leaked %q", got)
	}
	if err := c.Record(ctx, r, []string{"../escape"}); err == nil {
		t.Fatal("traversal accepted")
	}
}
func TestScanAndMoveReconcile(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	r := Root{ID: "one", Path: data}
	c, _ := New(t.TempDir())
	defer c.Close()
	os.Mkdir(filepath.Join(data, "old"), 0700)
	for _, name := range []string{"a", "b"} {
		os.WriteFile(filepath.Join(data, "old", name), []byte(name), 0600)
	}
	if _, err := c.Scan(ctx, r, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Record(ctx, r, []string{"old", "new"}); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	var wg sync.WaitGroup
	var reconcileErr error
	_, err := c.Scan(ctx, r, true, func(index.Progress) {
		once.Do(func() {
			if err := os.Rename(filepath.Join(data, "old"), filepath.Join(data, "new")); err != nil {
				t.Error(err)
			}
			wg.Add(1)
			go func() { defer wg.Done(); reconcileErr = c.Reconcile(ctx, r, []string{"old", "new"}) }()
		})
	})
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}
	if got := paths(t, c, r); !slices.Equal(got, []string{"new", "new/a", "new/b"}) {
		t.Fatalf("after scan+move %v", got)
	}
}
func TestDuplicatesInvalidateAndCancel(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	r := Root{ID: "one", Path: data}
	c, _ := New(t.TempDir())
	defer c.Close()
	for _, name := range []string{"a", "b"} {
		os.WriteFile(filepath.Join(data, name), []byte("same"), 0600)
	}
	os.Symlink("a", filepath.Join(data, "link"))
	if _, err := c.Scan(ctx, r, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.FindDuplicates(ctx, []Root{r}, 1, nil); err != nil {
		t.Fatal(err)
	}
	groups, err := c.DuplicateFiles(ctx, []Root{r}, 1)
	if err != nil || len(groups) != 1 || len(groups[0]) != 2 {
		t.Fatalf("groups %+v %v", groups, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := c.FindDuplicates(canceled, []Root{r}, 1, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
	os.WriteFile(filepath.Join(data, "b"), []byte("diff"), 0600)
	now := time.Now().Add(time.Second)
	os.Chtimes(filepath.Join(data, "b"), now, now)
	groups, err = c.DuplicateFiles(ctx, []Root{r}, 1)
	if err != nil || len(groups) != 0 {
		t.Fatalf("stale hashes %+v %v", groups, err)
	}
}

func TestCompletedScanReleasesResidentChunks(t *testing.T) {
	ctx := context.Background()
	r := Root{ID: "memory", Path: t.TempDir()}
	for i := 0; i < 1024; i++ {
		if err := os.WriteFile(filepath.Join(r.Path, fmt.Sprintf("entry-%04d.txt", i)), []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.Scan(ctx, r, true, nil); err != nil {
		t.Fatal(err)
	}
	status, err := c.Status(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if status.Count != 1024 || status.Bytes > 8192 {
		t.Fatalf("scan retained chunks: %+v", status)
	}
	pinned, err := c.Search(ctx, r, index.Query{Text: "entry-0000"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(r.Path, "entry-0000.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Scan(ctx, r, true, nil); err != nil {
		t.Fatal(err)
	}
	old, err := pinned.Page(ctx, 0, 10)
	if err != nil || len(old) != 1 {
		t.Fatalf("pinned file-backed result: %+v %v", old, err)
	}
	fresh, err := c.Search(ctx, r, index.Query{Text: "entry-0000"})
	if err != nil || fresh.Total() != 0 {
		t.Fatalf("new version: %v %v", fresh, err)
	}
}
