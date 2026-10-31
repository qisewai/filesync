package winrename

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicConflictAndReplace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "target"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if err := At(parent, "source", parent, "target", false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected conflict: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "target")); string(b) != "old" {
		t.Fatal("conflict overwrote destination")
	}
	if err := At(parent, "source", parent, "target", true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "target")); string(b) != "new" {
		t.Fatal("replacement missing")
	}
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := At(parent, "folder", parent, "moved", false); err != nil {
		t.Fatal(err)
	}
	if err := At(parent, "../escape", parent, "target", true); err == nil {
		t.Fatal("accepted escape")
	}
}
