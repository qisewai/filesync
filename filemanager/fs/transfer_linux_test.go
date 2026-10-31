package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestMergedMovePreservesUnsupportedSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	dest := filepath.Join(dir, "dest")
	os.Mkdir(source, 0700)
	os.Mkdir(dest, 0700)
	os.WriteFile(filepath.Join(source, "file"), []byte("source"), 0600)
	if err := syscall.Mkfifo(filepath.Join(source, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	err := MoveTree(context.Background(), source, dest, func(int64, string) {})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected unsupported: %v", err)
	}
	for _, name := range []string{"file", "pipe"} {
		if _, err := os.Lstat(filepath.Join(source, name)); err != nil {
			t.Fatalf("source lost: %s %v", name, err)
		}
	}
}

func TestCopyDoesNotTruncateOldPartialName(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	dest := filepath.Join(dir, "dest")
	os.WriteFile(source, []byte("new"), 0600)
	os.WriteFile(dest+".filesync-part", []byte("unrelated"), 0600)
	if err := CopyTree(context.Background(), source, dest, func(int64, string) {}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dest + ".filesync-part")
	if string(b) != "unrelated" {
		t.Fatal("existing file overwritten")
	}
}
