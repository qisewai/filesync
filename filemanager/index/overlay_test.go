package index

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func overlayFixture(t *testing.T) (*Index, ScanOptions, string) {
	t.Helper()
	root := t.TempDir()
	opts := ScanOptions{Roots: []string{root}, Excludes: []string{"skip/", "*.tmp"}}
	if err := os.MkdirAll(filepath.Join(root, "old", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for p, data := range map[string]string{"old/nested/report.txt": "original", "stable.txt": "stable"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(p)), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	x := New()
	if _, err := x.Scan(context.Background(), opts, nil); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "snapshot")
	if err := x.SaveSnapshot(context.Background(), file, opts, SnapshotMeta{}); err != nil {
		t.Fatal(err)
	}
	x = New()
	if _, err := x.LoadSnapshot(context.Background(), file, opts, nil); err != nil {
		t.Fatal(err)
	}
	return x, opts, file
}
func resultPaths(t *testing.T, x *Index, q Query) []string {
	t.Helper()
	r, err := x.Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := r.Page(context.Background(), 0, r.Total())
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range entries {
		paths = append(paths, e.Path)
	}
	slices.Sort(paths)
	return paths
}
func assertMatchesFreshScan(t *testing.T, x *Index, opts ScanOptions) {
	t.Helper()
	fresh := New()
	if _, err := fresh.Scan(context.Background(), opts, nil); err != nil {
		t.Fatal(err)
	}
	for _, q := range []Query{{Hidden: true}, {Text: "report"}, {Category: "file", Sort: "size", Desc: true}, {Dir: opts.Roots[0], Hidden: true}, {Exts: []string{"txt"}, MinSize: 2}, {Sort: "path"}} {
		actual, expected := resultPaths(t, x, q), resultPaths(t, fresh, q)
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("query %+v\ngot %v\nwant %v", q, actual, expected)
		}
		r1, _ := x.Search(context.Background(), q)
		r2, _ := fresh.Search(context.Background(), q)
		a, _ := r1.Page(context.Background(), 0, 100)
		b, _ := r2.Page(context.Background(), 0, 100)
		// Metadata and pagination order must agree with an independent full scan.
		if len(a) != len(b) {
			t.Fatal("count mismatch")
		}
		for i := range a {
			if a[i].Path != b[i].Path || a[i].Size != b[i].Size || a[i].MTime != b[i].MTime {
				t.Fatalf("entry mismatch %+v %+v", a[i], b[i])
			}
		}
	}
	if x.Status().Count != fresh.Status().Count {
		t.Fatalf("status count: %d %d", x.Status().Count, fresh.Status().Count)
	}
}
func TestOverlayMoveDeleteRecreateAndImmutablePages(t *testing.T) {
	x, opts, _ := overlayFixture(t)
	ctx := context.Background()
	root := opts.Roots[0]
	before, _ := x.Search(ctx, Query{Text: "report"})
	src, dst := filepath.Join(root, "old"), filepath.Join(root, "renamed")
	if err := os.Rename(src, dst); err != nil {
		t.Fatal(err)
	}
	if err := x.Reconcile(ctx, opts, []string{src, dst}); err != nil {
		t.Fatal(err)
	}
	assertMatchesFreshScan(t, x, opts)
	previous, err := before.Page(ctx, 0, 10)
	if err != nil || len(previous) != 1 || previous[0].Parent != filepath.Join(src, "nested") {
		t.Fatalf("old page changed: %+v %v", previous, err)
	}
	if _, ok := x.Subtree(src); ok {
		t.Fatal("deleted subtree remains")
	}
	if stat, ok := x.Subtree(dst); !ok || stat.Files != 1 || stat.Dirs != 1 || stat.Size != 8 {
		t.Fatalf("subtree: %+v %v", stat, ok)
	}
	if err := os.RemoveAll(dst); err != nil {
		t.Fatal(err)
	}
	if err := x.Reconcile(ctx, opts, []string{dst}); err != nil {
		t.Fatal(err)
	}
	assertMatchesFreshScan(t, x, opts)
	if err := os.MkdirAll(filepath.Join(dst, "new"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "new", "report.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := x.Reconcile(ctx, opts, []string{filepath.Join(dst, "new", "report.txt")}); err != nil {
		t.Fatal(err)
	}
	assertMatchesFreshScan(t, x, opts)
}
func TestOverlayReplayUnfinishedAndDuplicateAcrossRestart(t *testing.T) {
	x, opts, snapshot := overlayFixture(t)
	root := opts.Roots[0]
	filename := filepath.Join(t.TempDir(), "operations")
	log, err := OpenOperationLog(filename)
	if err != nil {
		t.Fatal(err)
	}
	source, target := filepath.Join(root, "old"), filepath.Join(root, "moved")
	if _, err = log.Begin("move", source, target); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(source, target); err != nil {
		t.Fatal(err)
	}
	log.Close()
	log, err = OpenOperationLog(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	paths, mismatch, err := log.ReplayPaths(context.Background(), snapshot)
	if err != nil || mismatch {
		t.Fatalf("replay %v %v", mismatch, err)
	}
	for range 2 {
		if err = x.Reconcile(context.Background(), opts, paths); err != nil {
			t.Fatal(err)
		}
		assertMatchesFreshScan(t, x, opts)
	}
	// Save only the base. Reopening plus journal reconciliation restores changes.
	if err = x.SaveSnapshot(context.Background(), snapshot, opts, SnapshotMeta{}); err != nil {
		t.Fatal(err)
	}
	restarted := New()
	if _, err = restarted.LoadSnapshot(context.Background(), snapshot, opts, nil); err != nil {
		t.Fatal(err)
	}
	if err = restarted.Reconcile(context.Background(), opts, paths); err != nil {
		t.Fatal(err)
	}
	assertMatchesFreshScan(t, restarted, opts)
}
func TestOverlayScopeHiddenExclusionsAndPartialCopy(t *testing.T) {
	x, opts, _ := overlayFixture(t)
	root := opts.Roots[0]
	for _, p := range []string{".hidden/report.txt", "skip/report.txt", "new/report.txt", "new/ignored.tmp"} {
		p = filepath.Join(root, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(p), 0700)
		if err := os.WriteFile(p, []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := x.Reconcile(context.Background(), opts, []string{filepath.Join(root, ".hidden"), filepath.Join(root, "skip"), filepath.Join(root, "new")}); err != nil {
		t.Fatal(err)
	}
	assertMatchesFreshScan(t, x, opts)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	old := x.Status().Version
	if err := x.Reconcile(ctx, opts, []string{root}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if x.Status().Version != old {
		t.Fatal("canceled refresh published")
	}
}
func TestOverlaySurvivesNewBasePublication(t *testing.T) {
	x, opts, snapshot := overlayFixture(t)
	root := opts.Roots[0]
	p := filepath.Join(root, "new.txt")
	if err := os.WriteFile(p, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := x.Reconcile(context.Background(), opts, []string{p}); err != nil {
		t.Fatal(err)
	}
	if _, err := x.LoadSnapshot(context.Background(), snapshot, opts, nil); err != nil {
		t.Fatal(err)
	}
	assertMatchesFreshScan(t, x, opts)
	if _, err := x.Scan(context.Background(), opts, nil); err != nil {
		t.Fatal(err)
	}
	assertMatchesFreshScan(t, x, opts)
	x.ClearDeltas()
	if x.Status().Delta {
		t.Fatal("checkpoint did not clear overlay")
	}
	assertMatchesFreshScan(t, x, opts)
}

func TestOverlayDownloadCreatesAncestorsAndNeverFollowsReplacedRoot(t *testing.T) {
	x, opts, _ := overlayFixture(t)
	root := opts.Roots[0]
	target := filepath.Join(root, "downloads", "nested", "report.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := x.Reconcile(context.Background(), opts, []string{target}); err != nil {
		t.Fatal(err)
	}
	assertMatchesFreshScan(t, x, opts)
	// A replaced ancestor is indexed as a link, not traversed to another tree.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(root, "moved")
	if err := os.Rename(filepath.Join(root, "downloads"), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "downloads")); err != nil {
		t.Skip(err)
	}
	if err := x.Reconcile(context.Background(), opts, []string{target, moved}); err != nil {
		t.Fatal(err)
	}
	if got := resultPaths(t, x, Query{Text: "secret", Hidden: true}); len(got) != 0 {
		t.Fatalf("followed symlink: %v", got)
	}
	assertMatchesFreshScan(t, x, opts)
}

func TestOverlayCrossRootMoveAndMoveOutsideScope(t *testing.T) {
	x, opts, _ := overlayFixture(t)
	root := opts.Roots[0]
	second := t.TempDir()
	opts.Roots = append(opts.Roots, second)
	if _, err := x.Scan(context.Background(), opts, nil); err != nil {
		t.Fatal(err)
	}
	src, dst := filepath.Join(root, "old"), filepath.Join(second, "moved")
	if err := os.Rename(src, dst); err != nil {
		t.Fatal(err)
	}
	if err := x.Reconcile(context.Background(), opts, []string{src, dst}); err != nil {
		t.Fatal(err)
	}
	assertMatchesFreshScan(t, x, opts)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Rename(dst, outside); err != nil {
		t.Fatal(err)
	}
	if err := x.Reconcile(context.Background(), opts, []string{dst, outside}); err != nil {
		t.Fatal(err)
	}
	assertMatchesFreshScan(t, x, opts)
}

func TestReplayCheckpointMismatchRequiresWholeScope(t *testing.T) {
	x, opts, snapshot := overlayFixture(t)
	log, err := OpenOperationLog(filepath.Join(t.TempDir(), "operations"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if err = log.StartCheckpoint(); err != nil {
		t.Fatal(err)
	}
	if err = log.CommitCheckpoint(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	log.EndCheckpoint()
	if _, mismatch, err := log.ReplayPaths(context.Background(), snapshot, opts); err != nil || mismatch {
		t.Fatalf("matching base %v %v", mismatch, err)
	}
	changed := opts
	changed.Excludes = nil
	if _, mismatch, err := log.ReplayPaths(context.Background(), snapshot, changed); err != nil || !mismatch {
		t.Fatalf("scope mismatch %v %v", mismatch, err)
	}
	if _, err = x.Scan(context.Background(), opts, nil); err != nil {
		t.Fatal(err)
	}
	if err = x.SaveSnapshot(context.Background(), snapshot, opts, SnapshotMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, mismatch, err := log.ReplayPaths(context.Background(), snapshot, opts); err != nil || !mismatch {
		t.Fatalf("generation mismatch %v %v", mismatch, err)
	}
}

func TestOverlayConcurrentSearchAndBaseReload(t *testing.T) {
	x, opts, snapshot := overlayFixture(t)
	ctx := context.Background()
	errorsFound := make(chan error, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 20 {
			r, err := x.Search(ctx, Query{Hidden: true})
			if err != nil {
				errorsFound <- err
				return
			}
			if _, err = r.Page(ctx, 0, 10); err != nil {
				errorsFound <- err
				return
			}
		}
	}()
	p := filepath.Join(opts.Roots[0], "changing.txt")
	for i := range 10 {
		if err := os.WriteFile(p, make([]byte, i+1), 0600); err != nil {
			t.Fatal(err)
		}
		if err := x.Reconcile(ctx, opts, []string{p}); err != nil {
			t.Fatal(err)
		}
		if _, err := x.LoadSnapshot(ctx, snapshot, opts, nil); err != nil {
			t.Fatal(err)
		}
	}
	<-done
	select {
	case err := <-errorsFound:
		t.Fatal(err)
	default:
	}
	assertMatchesFreshScan(t, x, opts)
}
