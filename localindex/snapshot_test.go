package localindex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestSnapshotMixedCaseAndChunkBoundaries(t *testing.T) {
	ctx := context.Background()
	positions := []int{0, 1, 62, 63, 64, 65, 127, 128, 4094, 4095, 4096, 8191, 8192, 8194}
	opts := ScanOptions{Roots: []string{filepath.Join(t.TempDir(), "root1"), filepath.Join(t.TempDir(), "root2")}}
	x := New()
	var roots []*rootData
	var want []int64
	for ri, root := range opts.Roots {
		b := newBuilder(root)
		for i := range 8195 {
			name := fmt.Sprintf("plain%05d.txt", i)
			size := int64(ri*100_000 + i + 1)
			if slices.Contains(positions, i) {
				// Lowercasing İ changes its UTF-8 length, so display and search
				// offsets must remain independent in these chunks.
				name = fmt.Sprintf("İSTANBUL-报告%05d.TXT", i)
				want = append(want, size)
			}
			b.add(Entry{Name: name, Ext: "txt", Size: size}, 0)
		}
		roots = append(roots, b.snapshot())
	}
	x.publish(roots, time.Now())
	filename := filepath.Join(t.TempDir(), "index.bin")
	if err := x.SaveSnapshot(ctx, filename, opts, SnapshotMeta{}); err != nil {
		t.Fatal(err)
	}
	y := New()
	if _, err := y.LoadSnapshot(ctx, filename, opts, nil); err != nil {
		t.Fatal(err)
	}
	for _, index := range []*Index{x, y} {
		r := search(t, index, Query{Text: "istanbul 报告", Sort: "size"})
		if r.Total() != len(want) || search(t, index, Query{}).Total() != 2*8195 {
			t.Fatalf("lost matches: %d", r.Total())
		}
		// Retained results must survive replacement of the current version.
		index.publish(nil, time.Now())
		for offset := 0; offset < len(want); offset += 5 {
			es := page(t, r, offset, 5)
			got := make([]int64, len(es))
			for i, e := range es {
				got[i] = e.Size
				if !bytes.HasPrefix([]byte(e.Name), []byte("İSTANBUL-报告")) {
					t.Fatalf("display name changed: %q", e.Name)
				}
			}
			if !slices.Equal(got, want[offset:min(offset+5, len(want))]) {
				t.Fatalf("page %d: %v", offset, got)
			}
		}
	}
}

func TestSnapshotRoundTripWithoutFilesystemScan(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "文档/İSTANBUL2.TXT", "text")
	writeFile(t, root, ".hidden/keep.pdf", "hidden")
	x := New()
	opts := ScanOptions{Roots: []string{root}, Excludes: []string{"*.tmp"}}
	scan(t, x, opts)
	filename := filepath.Join(t.TempDir(), "cache", "index.bin")
	if err := x.SaveSnapshot(context.Background(), filename, opts, SnapshotMeta{Partial: true, Errors: 3, Messages: []string{"不可读目录：权限不足"}}); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(filename)
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("permissions %v", st.Mode())
	}
	old := page(t, search(t, x, Query{Hidden: true}), 0, 100)
	// Loading an offline scope demonstrates that loading never visits its paths.
	moved := root + "-offline"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Rename(moved, root) })
	y := New()
	progress := 0
	meta, err := y.LoadSnapshot(context.Background(), filename, opts, func(p LoadProgress) {
		progress++
		if p.Read > p.Total {
			t.Error("progress exceeds size")
		}
	})
	if err != nil || meta.Errors != 3 || !meta.Partial || progress == 0 || !slices.Equal(meta.Messages, []string{"不可读目录：权限不足"}) {
		t.Fatalf("load %+v, %v", meta, err)
	}
	if es := page(t, search(t, y, Query{Hidden: true}), 0, 100); !slices.Equal(old, es) {
		t.Fatalf("entries %+v", es)
	}
	if !x.Status().Updated.Equal(y.Status().Updated) {
		t.Fatal("timestamp changed")
	}
	if search(t, y, Query{Text: "istanbul", Category: "doc"}).Total() != 1 {
		t.Fatal("folded name/type lost")
	}
}

func TestSnapshotRejectsDamageAndScopeChanges(t *testing.T) {
	x := syntheticIndex(300)
	opts := ScanOptions{Roots: []string{x.snap.Load().roots[0].path}}
	filename := filepath.Join(t.TempDir(), "index.bin")
	if err := x.SaveSnapshot(context.Background(), filename, opts, SnapshotMeta{}); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(filename)
	for _, tc := range []struct {
		name   string
		change func([]byte) []byte
	}{
		{"magic", func(b []byte) []byte { b[0] ^= 1; return b }},
		{"version", func(b []byte) []byte { b[8]++; return b }},
		{"timestamp", func(b []byte) []byte { b[24] ^= 1; return b }},
		{"body", func(b []byte) []byte { b[len(b)-1] ^= 1; return b }},
		{"truncate", func(b []byte) []byte { return b[:len(b)-1] }},
		{"extra", func(b []byte) []byte { return append(b, 0) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			os.WriteFile(filename, tc.change(slices.Clone(good)), 0o600)
			version := x.Status().Version
			if _, err := x.LoadSnapshot(context.Background(), filename, opts, nil); !errors.Is(err, ErrSnapshotFormat) {
				t.Fatalf("accepted damage: %v", err)
			}
			if x.Status().Version != version || search(t, x, Query{}).Total() != 300 {
				t.Fatal("damaged file replaced live index")
			}
		})
	}
	os.WriteFile(filename, good, 0o600)
	if _, err := New().LoadSnapshot(context.Background(), filename, ScanOptions{Roots: opts.Roots, Excludes: []string{"*.txt"}}, nil); !errors.Is(err, ErrSnapshotScope) {
		t.Fatalf("changed rules: %v", err)
	}
	// A checksum alone is insufficient: invalid array references must also fail.
	x.snap.Load().roots[0].chunks[0].parent[0] = 999999
	if err := x.SaveSnapshot(context.Background(), filename, opts, SnapshotMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := New().LoadSnapshot(context.Background(), filename, opts, nil); !errors.Is(err, ErrSnapshotFormat) {
		t.Fatalf("invalid parent: %v", err)
	}
}

type cancelAfterChecks struct {
	context.Context
	calls  atomic.Int32
	limit  int32
	cancel context.CancelFunc
}

func (c *cancelAfterChecks) Err() error {
	if c.calls.Add(1) >= c.limit {
		c.cancel()
	}
	return c.Context.Err()
}
func cancelContext(limit int32) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	return &cancelAfterChecks{Context: ctx, limit: limit, cancel: cancel}
}

func TestSnapshotInterruptedSaveAndLoad(t *testing.T) {
	x := syntheticIndex(12000)
	opts := ScanOptions{Roots: []string{x.snap.Load().roots[0].path}}
	filename := filepath.Join(t.TempDir(), "index.bin")
	if err := x.SaveSnapshot(context.Background(), filename, opts, SnapshotMeta{}); err != nil {
		t.Fatal(err)
	}
	old, _ := os.ReadFile(filename)
	if err := x.SaveSnapshot(cancelContext(8), filename, opts, SnapshotMeta{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("save cancel: %v", err)
	}
	current, _ := os.ReadFile(filename)
	if !bytes.Equal(old, current) {
		t.Fatal("interrupted write replaced the saved snapshot")
	}
	files, _ := os.ReadDir(filepath.Dir(filename))
	if len(files) != 1 {
		t.Fatal("temporary snapshot leaked")
	}
	y := syntheticIndex(10)
	v := y.Status().Version
	if _, err := y.LoadSnapshot(cancelContext(8), filename, opts, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("load cancel: %v", err)
	}
	if y.Status().Version != v || y.Status().Count != 10 {
		t.Fatal("interrupted load replaced live index")
	}
}

func BenchmarkMillionSnapshot(b *testing.B) {
	x := syntheticIndex(1_000_000)
	opts := ScanOptions{Roots: []string{x.snap.Load().roots[0].path}}
	filename := filepath.Join(b.TempDir(), "index.bin")
	ctx := context.Background()
	if err := x.SaveSnapshot(ctx, filename, opts, SnapshotMeta{}); err != nil {
		b.Fatal(err)
	}
	st, _ := os.Stat(filename)
	b.Run("Load", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		var indexBytes int64
		for b.Loop() {
			y := New()
			if _, err := y.LoadSnapshot(ctx, filename, opts, nil); err != nil {
				b.Fatal(err)
			}
			status := y.Status()
			if status.Count != 1_000_000 {
				b.Fatal("lost entries")
			}
			indexBytes = status.Bytes
		}
		b.ReportMetric(float64(st.Size())/1024/1024, "file-MiB")
		b.ReportMetric(float64(indexBytes)/1024/1024, "index-MiB")
	})
	b.Run("Save", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			if err := x.SaveSnapshot(ctx, filename, opts, SnapshotMeta{}); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(st.Size())/1024/1024, "file-MiB")
	})
}
