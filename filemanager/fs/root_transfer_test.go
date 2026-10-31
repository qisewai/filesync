package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRootTransferBoundariesAndPolicies(t *testing.T) {
	source, target, outside := t.TempDir(), t.TempDir(), t.TempDir()
	src, err := os.OpenRoot(source)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	os.Mkdir(filepath.Join(source, "dir"), 0700)
	os.WriteFile(filepath.Join(source, "dir", "file"), []byte("safe"), 0600)
	os.Symlink(outside, filepath.Join(target, "escape"))
	if err := RootTransfer(context.Background(), src, "dir", dst, "escape/new", RootTransferOptions{}); err == nil {
		t.Fatal("escaped root through parent")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("modified outside root")
	}
	if err := RootTransfer(context.Background(), src, "dir", dst, "copy", RootTransferOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := RootTransfer(context.Background(), src, "dir", dst, "copy", RootTransferOptions{Move: true}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("conflict: %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "dir", "file")); err != nil {
		t.Fatal(err)
	}
	if err := RootTransfer(context.Background(), src, "dir", dst, "copy", RootTransferOptions{Move: true, Policy: TransferPolicy{true, true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "dir")); !os.IsNotExist(err) {
		t.Fatal("source not removed")
	}
	if b, _ := os.ReadFile(filepath.Join(target, "copy", "file")); string(b) != "safe" {
		t.Fatal("bad target")
	}
}
func TestRootTransferPreservesSourceChangesAndCancels(t *testing.T) {
	root := t.TempDir()
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	os.Mkdir(filepath.Join(root, "source"), 0700)
	os.Mkdir(filepath.Join(root, "dest"), 0700)
	os.WriteFile(filepath.Join(root, "source", "file"), make([]byte, 2<<20), 0600)
	os.WriteFile(filepath.Join(root, "dest", "file"), []byte("old"), 0600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = RootTransfer(ctx, r, "source", r, "dest", RootTransferOptions{Move: true, Policy: TransferPolicy{true, true}, Progress: func(int64, string) { cancel() }})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "dest", "file")); string(b) != "old" {
		t.Fatal("partial replacement")
	}
	if _, err := os.Stat(filepath.Join(root, "source", "file")); err != nil {
		t.Fatal(err)
	}
	err = RootTransfer(context.Background(), r, "source", r, "dest", RootTransferOptions{Move: true, Policy: TransferPolicy{true, true}, Progress: func(int64, string) { os.WriteFile(filepath.Join(root, "source", "new"), []byte("new"), 0600) }})
	if err == nil {
		t.Fatal("new source child should prevent directory removal")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "source", "new")); string(b) != "new" {
		t.Fatal("removed newly added source file")
	}
}
func TestRootTransferRejectsOverlappingRootAliases(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "folder"), 0700)
	os.WriteFile(filepath.Join(root, "folder", "file"), []byte("keep"), 0600)
	a, _ := os.OpenRoot(root)
	defer a.Close()
	b, _ := os.OpenRoot(filepath.Join(root, "folder"))
	defer b.Close()
	if err := RootTransfer(context.Background(), a, "folder", b, "nested", RootTransferOptions{Move: true}); err == nil {
		t.Fatal("overlapping roots accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "folder", "file")); err != nil {
		t.Fatal(err)
	}
}

func TestRootTransferRejectsSameFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file"), []byte("keep"), 0600)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, move := range []bool{false, true} {
		if err := RootTransfer(context.Background(), root, "file", root, "file", RootTransferOptions{Move: move, Policy: TransferPolicy{ReplaceFiles: true}}); err == nil {
			t.Fatal("same file accepted")
		}
		if b, err := os.ReadFile(filepath.Join(dir, "file")); err != nil || string(b) != "keep" {
			t.Fatalf("source changed: %q %v", b, err)
		}
	}
}
