package localindex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func update(t *testing.T, x *Index, opts ScanOptions) Progress {
	t.Helper()
	p, err := x.Update(context.Background(), opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestManualUpdateChecksFilesInUnchangedDirectory(t *testing.T) {
	root := t.TempDir()
	file := writeFile(t, root, "doc/a.txt", "a")
	x := New()
	opts := ScanOptions{Roots: []string{root}}
	scan(t, x, opts)
	dir := filepath.Dir(file)
	before, _ := os.Stat(dir)
	writeFile(t, root, "doc/a.txt", "changed content")
	stamp := time.Unix(1500000000, 0)
	os.Chtimes(file, stamp, stamp)
	os.Chtimes(dir, before.ModTime(), before.ModTime())
	p := update(t, x, opts)
	if p.ReadDirs != 0 || p.ReusedDirs != 2 {
		t.Fatalf("unchanged directory enumerated: %+v", p)
	}
	es := page(t, search(t, x, Query{Text: "a.txt"}), 0, 10)
	if len(es) != 1 || es[0].Size != 15 || es[0].MTime != stamp.Unix() {
		t.Fatalf("missed metadata change: %+v", es)
	}
	filename := filepath.Join(t.TempDir(), "snapshot.bin")
	if err := x.SaveSnapshot(context.Background(), filename, opts, SnapshotMeta{}); err != nil {
		t.Fatal(err)
	}
	y := New()
	if _, err := y.LoadSnapshot(context.Background(), filename, opts, nil); err != nil {
		t.Fatal(err)
	}
	if p := update(t, y, opts); p.ReadDirs != 0 || p.ReusedDirs != 2 {
		t.Fatalf("cache stamps not restored: %+v", p)
	}
}

func TestUpdateAddsDeletesMovesAndChangedExclusions(t *testing.T) {
	root := t.TempDir()
	remove := writeFile(t, root, "old.txt", "old")
	moved := writeFile(t, root, "doc/move.txt", "move")
	x := New()
	opts := ScanOptions{Roots: []string{root}}
	scan(t, x, opts)
	os.Remove(remove)
	os.Rename(moved, filepath.Join(root, "renamed.txt"))
	writeFile(t, root, "new/sub/added.pdf", "add")
	p := update(t, x, opts)
	if p.ReadDirs == 0 || p.Errors != 0 {
		t.Fatalf("update %+v", p)
	}
	for _, q := range []string{"old.txt", "move.txt"} {
		if search(t, x, Query{Text: q}).Total() != 0 {
			t.Errorf("deleted %s remains", q)
		}
	}
	if search(t, x, Query{Text: "added"}).Total() != 1 || search(t, x, Query{Text: "renamed"}).Total() != 1 {
		t.Fatal("new name missing")
	}
	opts.Excludes = []string{"*.txt"}
	update(t, x, opts)
	if search(t, x, Query{Exts: []string{"txt"}}).Total() != 0 {
		t.Fatal("new exclusion not applied")
	}
	opts.Excludes = nil
	update(t, x, opts)
	if search(t, x, Query{Text: "renamed"}).Total() != 1 {
		t.Fatal("relaxed rules failed to rediscover files")
	}
}

func TestUpdateFallsBackWhenDirectoryStampIsRestored(t *testing.T) {
	root := t.TempDir()
	old := writeFile(t, root, "old.txt", "old")
	x := New()
	opts := ScanOptions{Roots: []string{root}}
	scan(t, x, opts)
	before, _ := os.Stat(root)
	os.Remove(old)
	writeFile(t, root, "replacement.txt", "new")
	os.Chtimes(root, before.ModTime(), before.ModTime())
	p := update(t, x, opts)
	if p.ReadDirs != 1 || search(t, x, Query{Text: "replacement"}).Total() != 1 || search(t, x, Query{Text: "old"}).Total() != 0 {
		t.Fatalf("fallback %+v", p)
	}
}

func TestLargeDirectoriesStreamOnUpdate(t *testing.T) {
	root := t.TempDir()
	for i := range maxCachedDirectory + 1 {
		writeFile(t, root, fmt.Sprintf("file%d.txt", i), "")
	}
	x := New()
	opts := ScanOptions{Roots: []string{root}}
	scan(t, x, opts)
	if p := update(t, x, opts); p.ReadDirs != 1 || p.ReusedDirs != 0 || p.Count != maxCachedDirectory+1 {
		t.Fatalf("large directory %+v", p)
	}
}
