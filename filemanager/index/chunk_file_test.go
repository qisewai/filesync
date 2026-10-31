package index

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFileSnapshotMemoryAndPage(t *testing.T) {
	ctx := context.Background()
	original := syntheticIndex(100_003)
	opts := ScanOptions{Roots: []string{original.snap.Load().roots[0].path}}
	file := filepath.Join(t.TempDir(), "index.bin")
	if err := original.SaveSnapshot(ctx, file, opts, SnapshotMeta{}); err != nil {
		t.Fatal(err)
	}
	loaded := New()
	if _, err := loaded.LoadSnapshot(ctx, file, opts, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := loaded.Status().Bytes, original.Status().Bytes/4; got >= want {
		t.Fatalf("resident index estimate %d, want < %d", got, want)
	}
	for _, q := range []Query{{Text: "报告", Sort: "size"}, {Text: "report", Sort: "path", Desc: true}, {Text: "报告0000999", Sort: "mtime"}, {Hidden: true}} {
		a, b := search(t, original, q), search(t, loaded, q)
		if a.Total() != b.Total() {
			t.Fatalf("query %+v counts %d %d", q, a.Total(), b.Total())
		}
		for _, offset := range []int{0, 51, 500} {
			if offset >= a.Total() {
				continue
			}
			first, second := page(t, a, offset, 20), page(t, b, offset, 20)
			if len(first) != len(second) {
				t.Fatalf("different page length")
			}
			for i := range first {
				if first[i] != second[i] {
					t.Fatalf("query %+v offset %d: %+v != %+v", q, offset, first[i], second[i])
				}
			}
		}
	}
}

func TestFileSnapshotUpdateAndRetainedResults(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	old := writeFile(t, root, "doc/old.txt", "abc")
	writeFile(t, root, "doc/keep.txt", "hello")
	opts := ScanOptions{Roots: []string{root}}
	x := New()
	scan(t, x, opts)
	file := filepath.Join(t.TempDir(), "index.bin")
	if err := x.SaveSnapshot(ctx, file, opts, SnapshotMeta{}); err != nil {
		t.Fatal(err)
	}
	y := New()
	if _, err := y.LoadSnapshot(ctx, file, opts, nil); err != nil {
		t.Fatal(err)
	}
	retained := search(t, y, Query{Text: "old"})
	if got, ok := y.Subtree(filepath.Join(root, "doc")); !ok || got.Files != 2 || got.Size != 8 {
		t.Fatalf("subtree %+v %v", got, ok)
	}
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "doc/new.txt", "new")
	update(t, y, opts)
	if err := y.SaveSnapshot(ctx, file, opts, SnapshotMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := y.LoadSnapshot(ctx, file, opts, nil); err != nil {
		t.Fatal(err)
	}
	if search(t, y, Query{Text: "old"}).Total() != 0 || search(t, y, Query{Text: "new"}).Total() != 1 {
		t.Fatal("updated snapshot lost changes")
	}
	// Both the obsolete inode and its mapping must survive collection while
	// a result from that immutable version is still pinned.
	runtime.GC()
	if got := page(t, retained, 0, 10); len(got) != 1 || got[0].Name != "old.txt" {
		t.Fatalf("old search changed: %+v", got)
	}
	if got, ok := y.Subtree(filepath.Join(root, "doc")); !ok || got.Files != 2 || got.Size != 8 {
		t.Fatalf("updated subtree %+v %v", got, ok)
	}
}

func TestFileSnapshotReadFailure(t *testing.T) {
	ctx := context.Background()
	x := syntheticIndex(100)
	opts := ScanOptions{Roots: []string{x.snap.Load().roots[0].path}}
	file := filepath.Join(t.TempDir(), "index.bin")
	if err := x.SaveSnapshot(ctx, file, opts, SnapshotMeta{}); err != nil {
		t.Fatal(err)
	}
	y := New()
	if _, err := y.LoadSnapshot(ctx, file, opts, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(file, 104); err != nil {
		t.Fatal(err)
	}
	if _, err := y.Search(ctx, Query{Text: "报告"}); err == nil {
		t.Fatal("search accepted truncated snapshot")
	}
}
