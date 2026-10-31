package index

import (
	"path/filepath"
	"testing"
)

// What the index holds below a folder, as a walk counts it; nothing for
// a folder it does not hold.
func TestSubtree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "文档/a.txt", "abc")
	writeFile(t, root, "文档/深/b.txt", "12345")
	writeFile(t, root, "文档/深/更深/c.txt", "1")
	writeFile(t, root, "图片/d.jpg", "xx")
	writeFile(t, root, "缓存/e.tmp", "skipped")
	x := New()
	scan(t, x, ScanOptions{Roots: []string{root}, Excludes: []string{"缓存/"}})

	got, ok := x.Subtree(filepath.Join(root, "文档"))
	if !ok || got.Files != 3 || got.Dirs != 2 || got.Size != 9 || got.Updated.IsZero() {
		t.Fatalf("文档: %+v %v", got, ok)
	}
	got, ok = x.Subtree(root)
	if !ok || got.Files != 4 || got.Dirs != 4 || got.Size != 11 {
		t.Fatalf("root: %+v %v", got, ok)
	}
	if _, ok := x.Subtree(filepath.Join(root, "缓存")); ok {
		t.Error("counted an excluded folder")
	}
	if _, ok := x.Subtree(t.TempDir()); ok {
		t.Error("counted a folder outside the index")
	}
}
