package localindex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"filesync/engine"
)

func writeFile(t *testing.T, root, path, content string) string {
	t.Helper()
	p := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
func scan(t *testing.T, x *Index, opts ScanOptions) Progress {
	t.Helper()
	p, err := x.Scan(context.Background(), opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func search(t *testing.T, x *Index, q Query) *Results {
	t.Helper()
	r, err := x.Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func page(t *testing.T, r *Results, offset, limit int) []Entry {
	t.Helper()
	es, err := r.Page(context.Background(), offset, limit)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func TestScanSearchScopesAndFilters(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "文档/项目 报告10.PDF", "ten")
	writeFile(t, root, "文档/项目 报告2.pdf", "two")
	writeFile(t, root, "文档/İSTANBUL.TXT", "unicode case folding changes byte length")
	writeFile(t, root, "图片/报告.jpg", "image")
	writeFile(t, root, ".隐藏/报告.pdf", "hidden descendant")
	writeFile(t, root, ".cache/cache.txt", "excluded")
	writeFile(t, root, "文档/node_modules/ignored.js", "excluded")
	writeFile(t, root, "文档/skip.tmp", "excluded")
	writeFile(t, root, "系统.GHO", "disk")
	x := New()
	opts := ScanOptions{Roots: []string{filepath.Join(root, "文档"), root, root}, Excludes: []string{".cache/", "node_modules/", "*.tmp"}}
	p := scan(t, x, opts)
	if p.Errors != 0 || p.Count != 9 || x.Status().Updated.IsZero() {
		t.Fatalf("scan %+v, status %+v", p, x.Status())
	}
	r := search(t, x, Query{Text: "项目 pdf", Category: "doc"})
	es := page(t, r, 0, 100)
	if len(es) != 2 || es[0].Name != "项目 报告2.pdf" || es[1].Name != "项目 报告10.PDF" || es[1].Ext != "pdf" {
		t.Fatalf("AND/natural order: %+v", es)
	}
	for _, tc := range []struct {
		q    Query
		want int
	}{
		{Query{Text: "报告"}, 3},
		{Query{Text: "报告", Hidden: true}, 4},
		{Query{Text: "报告", Dir: filepath.Join(root, "文档")}, 2},
		{Query{Text: "报告", Exts: []string{".PDF"}}, 2},
		{Query{Text: "报告", Category: "image"}, 1},
		{Query{Category: "disk"}, 1},
		{Query{Category: "dir"}, 2},
		{Query{Category: "file"}, 5},
		{Query{Text: "istanbul"}, 1},
		{Query{Text: "文档"}, 1}, // names, not full paths
		{Query{Text: "ignored"}, 0},
		{Query{Text: "\x00"}, 0},
	} {
		if got := search(t, x, tc.q).Total(); got != tc.want {
			t.Errorf("query %+v: %d, want %d", tc.q, got, tc.want)
		}
	}
}

func TestManualRebuildAndPinnedPages(t *testing.T) {
	root := t.TempDir()
	a := writeFile(t, root, "a.txt", "a")
	b := writeFile(t, root, "b.txt", "b")
	x := New()
	opts := ScanOptions{Roots: []string{root}}
	scan(t, x, opts)
	old := search(t, x, Query{})
	writeFile(t, root, "a.txt", "changed content")
	mtime := time.Unix(1234567890, 0)
	os.Chtimes(a, mtime, mtime)
	os.Remove(b)
	writeFile(t, root, "new.txt", "new")
	if es := page(t, search(t, x, Query{}), 0, 10); len(es) != 2 || es[0].Size != 1 || es[1].Path != b {
		t.Fatalf("changed without a manual scan: %+v", es)
	}
	scan(t, x, opts)
	next := search(t, x, Query{})
	if next.Version() == old.Version() {
		t.Fatal("no new version")
	}
	if es := page(t, next, 0, 10); len(es) != 2 || es[0].Size != 15 || es[0].MTime != mtime.Unix() || es[1].Name != "new.txt" {
		t.Fatalf("manual updates: %+v", es)
	}
	if es := page(t, old, 1, 10); len(es) != 1 || es[0].Path != b || es[0].Size != 1 {
		t.Fatalf("mixed versions: %+v", es)
	}
	// Unmounted/offline scopes must not silently become empty.
	moved := root + "-offline"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Rename(moved, root) })
	if p := scan(t, x, opts); p.Errors == 0 || p.Count != 2 {
		t.Fatalf("offline root: %+v", p)
	}
}

func TestPartialFailureRetainsOnlyUnreadableRecords(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory permissions")
	}
	root := t.TempDir()
	writeFile(t, root, "locked/keep.txt", "keep")
	remove := writeFile(t, root, "remove.txt", "remove")
	x := New()
	opts := ScanOptions{Roots: []string{root}}
	scan(t, x, opts)
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	if f, err := os.Open(locked); err == nil {
		f.Close()
		t.Skip("permissions bypassed by this account")
	}
	os.Remove(remove)
	writeFile(t, root, "new.txt", "new")
	p := scan(t, x, opts)
	if p.Errors == 0 || search(t, x, Query{Text: "keep"}).Total() != 1 || search(t, x, Query{Text: "remove"}).Total() != 0 || search(t, x, Query{Text: "new"}).Total() != 1 {
		t.Fatalf("partial failure: %+v", p)
	}
}

func TestCancelScanAndConcurrentReaders(t *testing.T) {
	root := t.TempDir()
	for i := range 800 {
		writeFile(t, root, fmt.Sprintf("file%d.txt", i), "")
	}
	x := New()
	opts := ScanOptions{Roots: []string{root}, Workers: 2}
	ctx, cancel := context.WithCancel(context.Background())
	observed := 0
	p, err := x.Scan(ctx, opts, func(p Progress) {
		observed = search(t, x, Query{}).Total()
		if _, e := x.Scan(context.Background(), opts, nil); !errors.Is(e, ErrScanning) {
			t.Errorf("parallel writer: %v", e)
		}
		cancel()
	})
	if !errors.Is(err, context.Canceled) || p.Count == 0 || observed == 0 || p.Count >= 800 {
		t.Fatalf("partial initial scan: %+v, %v, searchable %d", p, err, observed)
	}
	scan(t, x, opts)
	old := search(t, x, Query{})
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	_, err = x.Scan(ctx, opts, func(Progress) { cancel() })
	if !errors.Is(err, context.Canceled) || search(t, x, Query{}).Total() != old.Total() {
		t.Fatalf("canceled rescan: %v", err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 12 {
				r := search(t, x, Query{Text: "file"})
				if len(page(t, r, 200, 20)) != 20 {
					t.Error("inconsistent page")
				}
				x.Status()
			}
		})
	}
	for range 3 {
		update(t, x, opts)
	}
	wg.Wait()
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := x.Search(ctx, Query{}); !errors.Is(err, context.Canceled) {
		t.Errorf("search: %v", err)
	}
	if _, err := old.Page(ctx, 10, 10); !errors.Is(err, context.Canceled) {
		t.Errorf("page: %v", err)
	}
}

func TestLinksAndSkippedScopes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "ordinary/a.txt", "a")
	writeFile(t, root, "network/b.txt", "b")
	link := filepath.Join(root, "ordinary", "cycle")
	if err := os.Symlink(root, link); err != nil {
		t.Skip(err)
	}
	x := New()
	p := scan(t, x, ScanOptions{Roots: []string{root}, Skip: []string{filepath.Join(root, "network")}})
	if p.Errors != 0 || p.Count != 3 || search(t, x, Query{Text: "b.txt"}).Total() != 0 {
		t.Fatalf("scan %+v", p)
	}
	if es := page(t, search(t, x, Query{Text: "cycle"}), 0, 1); len(es) != 1 || !es[0].Link {
		t.Fatalf("link: %+v", es)
	}
}

func TestMultipleRootsAndFoldersFirst(t *testing.T) {
	r1, r2 := t.TempDir(), t.TempDir()
	writeFile(t, r1, "a.txt", "1")
	writeFile(t, r1, "folder2/file.txt", "1")
	writeFile(t, r2, "a.txt", "22")
	writeFile(t, r2, "folder10/file.txt", "22")
	x := New()
	if p := scan(t, x, ScanOptions{Roots: []string{r1, r2}}); p.Count != 6 {
		t.Fatalf("scan %+v", p)
	}
	es := page(t, search(t, x, Query{Desc: true}), 0, 10)
	if es[0].Name != "folder10" || es[1].Name != "folder2" || !es[0].IsDir || !es[1].IsDir || es[2].IsDir {
		t.Fatalf("folders %+v", es)
	}
	r := search(t, x, Query{Text: "a.txt", Sort: "size"})
	es = page(t, r, 0, 10)
	if len(es) != 2 || es[0].Path != filepath.Join(r1, "a.txt") || es[1].Path != filepath.Join(r2, "a.txt") || es[1].Size != 2 {
		t.Fatalf("root references %+v", es)
	}
}

// Different parent directories and chunks intentionally share names. Every
// sort must order the complete result before pagination, including ties.
func TestGlobalOrderingAndPaging(t *testing.T) {
	x := syntheticIndex(12345)
	for _, order := range []string{"name", "size", "mtime", "path"} {
		for _, desc := range []bool{false, true} {
			r := search(t, x, Query{Sort: order, Desc: desc})
			all := page(t, r, 0, r.Total())
			for i := 1; i < len(all); i++ {
				a, b := all[i-1], all[i]
				n := CompareNames(a.Name, b.Name)
				switch order {
				case "size":
					if a.Size != b.Size {
						n = int(a.Size - b.Size)
					}
				case "mtime":
					if a.MTime != b.MTime {
						n = int(a.MTime - b.MTime)
					}
				case "path":
					if a.Parent != b.Parent {
						n = CompareNames(a.Parent, b.Parent)
					}
				}
				if desc {
					n = -n
				}
				if n > 0 {
					t.Fatalf("%s descending=%v: %s before %s", order, desc, a.Path, b.Path)
				}
			}
			for _, off := range []int{0, 997, 4095, 12290} {
				want := all[off:min(off+73, len(all))]
				if got := page(t, r, off, 73); !slices.Equal(got, want) {
					t.Fatalf("%s desc=%v offset=%d", order, desc, off)
				}
			}
		}
	}
}

func TestRootsAndPreservingOverlaps(t *testing.T) {
	base := t.TempDir()
	r, err := NormalizeRoots([]string{filepath.Join(base, "child"), base, base + string(filepath.Separator), base + "2"})
	if err != nil || !slices.Equal(r, []string{base, base + "2"}) {
		t.Fatalf("roots %v, %v", r, err)
	}
	if _, err := Validate([]string{"relative"}, nil); err == nil {
		t.Error("relative scope accepted")
	}
	if _, err := Validate(nil, []string{"["}); err == nil {
		t.Error("bad glob accepted")
	}
	// A directory can yield some new entries then fail. Merging must retain
	// its missing old entries once, while preserving newly observed metadata.
	old := newBuilder(base)
	old.add(Entry{Name: "a.txt", Size: 1}, 0)
	old.add(Entry{Name: "b.txt", Size: 2}, 0)
	b := newBuilder(base)
	b.add(Entry{Name: "a.txt", Size: 100}, 0)
	filter, _ := engine.NewFilter(nil)
	if err := preserveFailed(context.Background(), b, old.snapshot(), []string{base, filepath.Join(base, "b.txt")}, filter, func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	x := New()
	x.publish([]*rootData{b.snapshot()}, time.Now())
	es := page(t, search(t, x, Query{}), 0, 10)
	if len(es) != 2 || es[0].Size != 100 || es[1].Size != 2 {
		t.Fatalf("merge %+v", es)
	}
	// Old descendants of a newly excluded directory stay excluded even
	// when its parent is unreadable and therefore cannot be walked.
	old = newBuilder(base)
	id := old.directory(filepath.Join(base, "node_modules"))
	old.add(Entry{Name: "hidden.js", Ext: "js"}, id)
	b = newBuilder(base)
	filter, _ = engine.NewFilter([]string{"node_modules/"})
	if err := preserveFailed(context.Background(), b, old.snapshot(), []string{base}, filter, func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if b.data.count != 0 {
		t.Fatal("excluded descendants reappeared")
	}
}

func syntheticIndex(n int) *Index {
	root, _ := filepath.Abs("synthetic-index")
	b := newBuilder(root)
	for i := range n {
		parent := uint32(i/1000 + 1)
		if i%1000 == 0 {
			b.directory(filepath.Join(root, fmt.Sprintf("目录%d", i/1000)))
		}
		b.add(Entry{Name: fmt.Sprintf("报告%07d-report.txt", i%1000), Ext: "txt", Size: int64(n - i), MTime: int64(i % 331)}, parent)
	}
	x := New()
	x.publish([]*rootData{b.snapshot()}, time.Now())
	return x
}

func BenchmarkMillion(b *testing.B) {
	x := syntheticIndex(1_000_000)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		q    Query
		page bool
	}{
		{"SelectiveSearch", Query{Text: "报告0000999"}, false},
		{"BroadSearch", Query{Text: "报告"}, false},
		{"SelectiveFirstPage", Query{Text: "报告0000999"}, true},
		{"BroadFirstPage", Query{Text: "报告"}, true},
		{"SizeFirstPage", Query{Text: "报告", Sort: "size"}, true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				r, err := x.Search(ctx, tc.q)
				if err != nil {
					b.Fatal(err)
				}
				if tc.page {
					if _, err := r.Page(ctx, 0, 1000); err != nil {
						b.Fatal(err)
					}
				}
			}
			b.ReportMetric(float64(x.Status().Bytes)/1024/1024, "index-MiB")
		})
	}
}
