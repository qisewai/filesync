package index

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestSearchPatterns(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	x := New()
	for _, n := range []string{"年度报告.PDF", "报告甲.pdf", "报告ab.pdf", "report-2026.xlsx", "star*.txt", "what?.txt", "[abc].txt", "hello world.txt"} {
		writeFile(t, root, n, "x")
	}
	opts := ScanOptions{Roots: []string{root}}
	scan(t, x, opts)
	for _, disk := range []bool{false, true} {
		if disk {
			file := filepath.Join(t.TempDir(), "snapshot")
			if e := x.SaveSnapshot(ctx, file, opts, SnapshotMeta{}); e != nil {
				t.Fatal(e)
			}
			if _, e := x.LoadSnapshot(ctx, file, opts, nil); e != nil {
				t.Fatal(e)
			}
		}
		for _, tt := range []struct {
			q     string
			regex bool
			want  int
		}{
			{"报告", false, 3}, {"报告*.pdf", false, 2}, {"年度 *.pdf", false, 1}, {"报告?.pdf", false, 1},
			{"report-202?.xlsx", false, 1}, {`star\*.txt`, false, 1}, {`what\?.txt`, false, 1}, {"[abc]", false, 1},
			{"*", false, 8}, {"*不存在*", false, 0}, {`^报告.\.pdf$`, true, 1}, {`\.(pdf|xlsx)$`, true, 4}, {"hello world", true, 1},
		} {
			r, e := x.Search(ctx, Query{Text: tt.q, Regex: tt.regex, Hidden: true})
			if e != nil || r.Total() != tt.want {
				t.Fatalf("disk=%v %+v got %v error %v", disk, tt, r, e)
			}
		}
	}
	for _, q := range []string{"[", `(?=a)`, `(a)\1`} {
		if _, e := x.Search(ctx, Query{Text: q, Regex: true}); !errors.Is(e, ErrQuery) {
			t.Fatalf("invalid regex %q: %v", q, e)
		}
	}
	if _, e := New().Search(ctx, Query{Text: "[", Regex: true}); !errors.Is(e, ErrQuery) {
		t.Fatal("empty index must validate regex", e)
	}
}

func TestGlobUnicodeAndBacktracking(t *testing.T) {
	for _, tt := range []struct {
		pattern, name string
		want          bool
	}{
		{"*a*b?c", "xxaab甲c", true}, {"*a*b?c", "xxaab甲乙c", false}, {"?", "中", true}, {"?", "😀", true}, {"??", "中", false},
		{"**a**", "a", true}, {"a*", "ba", false}, {"*.pdf", "a.pdf.bak", false}, {`*\?*`, "what?.txt", true}, {`*\?*`, "what.txt", false},
	} {
		m, err := compileMatcher(tt.pattern, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := m.matchPatterns([]byte(tt.name), []byte(tt.name)); got != tt.want {
			t.Fatalf("%q %q = %v", tt.pattern, tt.name, got)
		}
	}
}
