package engine

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// write makes the files of a tree, by slash-separated paths; a path ending
// in a slash is a directory.
func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// files lists a tree as path → content, with directories ending in "/".
func files(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		b, _ := os.ReadFile(p)
		out[rel] = string(b)
		return nil
	})
	return out
}

func run(t *testing.T, opts Options) (*Plan, *Result) {
	t.Helper()
	p, err := Prepare(context.Background(), opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := Execute(context.Background(), p, Hooks{})
	if r.Fatal != nil || len(r.Errors) > 0 {
		t.Fatalf("fatal %v, errors %v", r.Fatal, r.Errors)
	}
	return p, r
}

func kinds(p *Plan) []string {
	var out []string
	for _, a := range p.Actions {
		out = append(out, a.Kind.String()+" "+a.Path)
	}
	sort.Strings(out)
	return out
}

func equal(t *testing.T, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("tree\n got %v\nwant %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("tree\n got %v\nwant %v", got, want)
		}
	}
}

func TestIncrementalKeepsTargetExtras(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, map[string]string{"a.txt": "a", "sub/b.txt": "b", "empty/": ""})
	write(t, dst, map[string]string{"old.txt": "old"})
	p, r := run(t, Options{Source: src, Target: dst, Mode: Incremental})
	if r.Created != 2 || r.Mkdirs != 2 || p.Summary.Delete != 0 {
		t.Errorf("plan %v, result %+v", kinds(p), r)
	}
	equal(t, files(t, dst), map[string]string{"a.txt": "a", "sub/": "", "sub/b.txt": "b", "empty/": "", "old.txt": "old"})

	// Nothing changed: nothing to do.
	p, _ = run(t, Options{Source: src, Target: dst, Mode: Incremental})
	if p.Summary.Changes() != 0 {
		t.Errorf("second run plans %v", kinds(p))
	}
}

func TestMirrorDeletesExtras(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, map[string]string{"a.txt": "new", "keep/x": "x"})
	write(t, dst, map[string]string{"a.txt": "older", "gone.txt": "g", "deep/er/f": "f", "keep/y": "y"})
	_, r := run(t, Options{Source: src, Target: dst, Mode: Mirror})
	if r.Updated != 1 || r.Created != 1 || r.Deleted != 3 || r.Rmdirs != 2 {
		t.Errorf("result %+v", r)
	}
	equal(t, files(t, dst), files(t, src))
}

func TestChangedFilesByTimeAndSize(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, map[string]string{"same": "1234", "size": "12345", "time": "abcd", "close": "wxyz"})
	write(t, dst, map[string]string{"same": "1234", "size": "1234", "time": "abcd", "close": "wxyz"})
	now := time.Now().Truncate(time.Second)
	for name, offset := range map[string]time.Duration{"same": 0, "size": 0, "time": 10 * time.Second, "close": time.Second} {
		os.Chtimes(filepath.Join(src, name), now, now)
		os.Chtimes(filepath.Join(dst, name), now.Add(offset), now.Add(offset))
	}
	p, err := Prepare(context.Background(), Options{Source: src, Target: dst}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(kinds(p), ","); got != "更新 size,更新 time" {
		t.Errorf("plan %s", got)
	}
}

func TestCopyKeepsModTime(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, map[string]string{"f": "data"})
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(filepath.Join(src, "f"), old, old)
	_, r := run(t, Options{Source: src, Target: dst})
	info, _ := os.Stat(filepath.Join(dst, "f"))
	if !info.ModTime().Equal(old) || r.TimesLost != 0 {
		t.Errorf("mod time %v, lost %d", info.ModTime(), r.TimesLost)
	}
}

func TestExcludes(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, map[string]string{
		"a.txt": "a", "a.tmp": "t", "node_modules/m.js": "m", "docs/cache/c": "c",
		"docs/keep/cache": "file named cache", "build/out/o": "o", "x/build/out/o": "o",
	})
	write(t, dst, map[string]string{"note.tmp": "kept although mirror", "node_modules/old": "kept"})
	run(t, Options{Source: src, Target: dst, Mode: Mirror, Excludes: []string{"*.tmp", "node_modules", "cache/", "/build/out", "# comment", ""}})
	equal(t, files(t, dst), map[string]string{
		"a.txt": "a", "docs/": "", "docs/keep/": "", "docs/keep/cache": "file named cache",
		"build/": "", "x/": "", "x/build/": "", "x/build/out/": "", "x/build/out/o": "o",
		"note.tmp": "kept although mirror", "node_modules/": "", "node_modules/old": "kept",
	})
}

func TestMirrorRefusesEmptySource(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, dst, map[string]string{"precious": "p"})
	if _, err := Prepare(context.Background(), Options{Source: src, Target: dst, Mode: Mirror}, nil); err == nil {
		t.Fatal("mirror of an empty source was planned")
	}
	// An incremental backup of it does nothing, and is allowed.
	if _, err := Prepare(context.Background(), Options{Source: src, Target: dst}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestMassDelete(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, map[string]string{"one": "1"})
	tgt := map[string]string{}
	for _, c := range "abcdefghijkl" {
		tgt[string(c)] = "x"
	}
	write(t, dst, tgt)
	p, err := Prepare(context.Background(), Options{Source: src, Target: dst, Mode: Mirror}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !p.MassDelete || len(p.Warnings) == 0 {
		t.Errorf("mass delete %v, warnings %v", p.MassDelete, p.Warnings)
	}
}

func TestRootsMustNotOverlap(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"in/f": "f"})
	for _, c := range [][2]string{{dir, dir}, {dir, filepath.Join(dir, "in")}, {filepath.Join(dir, "in"), dir}} {
		if _, err := Prepare(context.Background(), Options{Source: c[0], Target: c[1]}, nil); err == nil {
			t.Errorf("planned %s → %s", c[0], c[1])
		}
	}
	if _, err := Prepare(context.Background(), Options{Source: filepath.Join(dir, "missing"), Target: t.TempDir()}, nil); err == nil {
		t.Error("planned a missing source")
	}
}

func TestMissingTargetIsCreated(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "new", "backup")
	write(t, src, map[string]string{"f": "f"})
	p, _ := run(t, Options{Source: src, Target: dst, Mode: Mirror})
	if !p.TargetMissing {
		t.Error("target not reported missing")
	}
	equal(t, files(t, dst), map[string]string{"f": "f"})
}

func TestTypeConflicts(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, map[string]string{"was-dir": "now a file", "was-file/inner": "now a dir"})
	write(t, dst, map[string]string{"was-dir/a/b": "b", "was-file": "file"})

	p, err := Prepare(context.Background(), Options{Source: src, Target: dst}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Summary.Skip != 2 || p.Summary.Changes() != 0 {
		t.Errorf("incremental plans %v", kinds(p))
	}

	run(t, Options{Source: src, Target: dst, Mode: Mirror})
	equal(t, files(t, dst), files(t, src))
}

func TestSymlinksAreSkipped(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, map[string]string{"real": "r"})
	if err := os.Symlink("real", filepath.Join(src, "link")); err != nil {
		t.Skip("no symbolic links:", err)
	}
	p, _ := run(t, Options{Source: src, Target: dst, Mode: Mirror})
	if p.Summary.Skip != 1 {
		t.Errorf("plan %v", kinds(p))
	}
	equal(t, files(t, dst), map[string]string{"real": "r"})
}

func TestLeftoverTempFilesAreCleaned(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, map[string]string{"f": "f"})
	write(t, dst, map[string]string{"sub/.filesync-0123abcd.tmp": "partial"})
	_, r := run(t, Options{Source: src, Target: dst})
	if r.Cleaned != 1 {
		t.Errorf("result %+v", r)
	}
	equal(t, files(t, dst), map[string]string{"f": "f", "sub/": ""})
}

func TestCaseInsensitiveTarget(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, map[string]string{"Report.txt": "newer", "a": "1", "A": "2"})
	write(t, dst, map[string]string{"report.txt": "old"})
	tr, err := scan(context.Background(), src, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	dt, _ := scan(context.Background(), dst, nil, nil)
	// As an SMB share would be, whatever this machine's file system is.
	p := &Plan{Options: Options{Mode: Mirror, TimeTolerance: DefaultTimeTolerance}, CaseInsensitive: true}
	p.build(tr, dt)
	got := strings.Join(kinds(p), ",")
	if got != "新增 A,更新 Report.txt,跳过 a" {
		t.Errorf("plan %s", got)
	}
}

func TestWindowsNames(t *testing.T) {
	for name, bad := range map[string]bool{
		"ok.txt": false, "a:b": true, "what?": true, "CON": true, "nul.txt": true,
		"console": false, "trailing.": true, "space ": true, "中文.doc": false,
	} {
		if got := windowsNameProblem(name) != ""; got != bad {
			t.Errorf("%q: bad %v, want %v", name, got, bad)
		}
	}
}

func TestCancel(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, map[string]string{"a": "a", "b": "b"})
	p, err := Prepare(context.Background(), Options{Source: src, Target: dst}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := Execute(ctx, p, Hooks{Done: func(Action, error) { cancel() }})
	if !r.Canceled || r.Created != 1 {
		t.Errorf("result %+v", r)
	}
	for name := range files(t, dst) {
		if strings.HasPrefix(name, ".filesync-") {
			t.Errorf("temporary file %s left", name)
		}
	}
}

func TestProgress(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, map[string]string{"a": "aaaa", "b": "bb"})
	p, _ := Prepare(context.Background(), Options{Source: src, Target: dst}, nil)
	var last Progress
	Execute(context.Background(), p, Hooks{Progress: func(pr Progress) { last = pr }})
	if last.Done != 2 || last.Total != 2 || last.DoneBytes != 6 || last.TotalBytes != 6 {
		t.Errorf("last progress %+v", last)
	}
}
