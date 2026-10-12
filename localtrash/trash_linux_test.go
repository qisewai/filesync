package localtrash

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMoveToHomeTrash(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "报告 1.txt"), []byte("one"), 0o644)
	os.MkdirAll(filepath.Join(src, "照片"), 0o755)
	os.WriteFile(filepath.Join(src, "照片", "a.jpg"), []byte("a"), 0o644)
	for _, name := range []string{"报告 1.txt", "照片"} {
		if err := Move(filepath.Join(src, name)); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(src, name)); !os.IsNotExist(err) {
			t.Fatalf("%s still there", name)
		}
	}
	// The same name again is numbered.
	os.WriteFile(filepath.Join(src, "报告 1.txt"), []byte("two"), 0o644)
	if err := Move(filepath.Join(src, "报告 1.txt")); err != nil {
		t.Fatal(err)
	}
	trash := filepath.Join(data, "Trash")
	for name, want := range map[string]string{"报告 1.txt": "one", "报告 1.2.txt": "two", "照片/a.jpg": "a"} {
		if b, err := os.ReadFile(filepath.Join(trash, "files", name)); err != nil || string(b) != want {
			t.Errorf("%s: %q %v", name, b, err)
		}
	}
	info, err := os.ReadFile(filepath.Join(trash, "info", "报告 1.2.txt.trashinfo"))
	if err != nil {
		t.Fatal(err)
	}
	want := "Path=" + escape(filepath.Join(src, "报告 1.txt")) + "\n"
	if !strings.HasPrefix(string(info), "[Trash Info]\n") || !strings.Contains(string(info), want) || !strings.Contains(string(info), "%20") {
		t.Errorf("info %q", info)
	}
}

func TestMoveMissing(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := Move(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("moved nothing")
	}
}
