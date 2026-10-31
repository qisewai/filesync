package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTransferConflictPolicies(t *testing.T) {
	root := t.TempDir()
	from, to := filepath.Join(root, "from"), filepath.Join(root, "to")
	os.Mkdir(from, 0700)
	os.Mkdir(to, 0700)
	os.WriteFile(filepath.Join(from, "file"), []byte("new"), 0600)
	os.WriteFile(filepath.Join(to, "file"), []byte("old"), 0600)
	os.WriteFile(filepath.Join(to, "unique"), []byte("keep"), 0600)
	progress := func(int64, string) {}
	if err := Transfer(context.Background(), from, to, true, TransferPolicy{}, progress, nil); err == nil {
		t.Fatal("merged without approval")
	}
	if err := Transfer(context.Background(), from, to, true, TransferPolicy{MergeDirectories: true}, progress, nil); err == nil {
		t.Fatal("replaced without approval")
	}
	if b, _ := os.ReadFile(filepath.Join(to, "file")); string(b) != "old" {
		t.Fatal("conflict overwrote file")
	}
	if _, err := os.Stat(from); err != nil {
		t.Fatal("failed move removed source")
	}
	if err := Transfer(context.Background(), from, to, true, TransferPolicy{MergeDirectories: true, ReplaceFiles: true}, progress, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(to, "file")); string(b) != "new" {
		t.Fatal("missing replacement")
	}
	if b, _ := os.ReadFile(filepath.Join(to, "unique")); string(b) != "keep" {
		t.Fatal("lost destination-only file")
	}
}

func TestCanceledMovePreservesSourceAndUncommittedDestination(t *testing.T) {
	root := t.TempDir()
	from, to := filepath.Join(root, "source"), filepath.Join(root, "target")
	os.Mkdir(from, 0700)
	os.Mkdir(to, 0700)
	source := filepath.Join(from, "large")
	dest := filepath.Join(to, "large")
	os.WriteFile(source, make([]byte, 3<<20), 0600)
	os.WriteFile(dest, []byte("old"), 0600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := Transfer(ctx, from, to, true, TransferPolicy{ReplaceFiles: true, MergeDirectories: true}, func(int64, string) { cancel() }, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation: %v", err)
	}
	if st, err := os.Stat(source); err != nil || st.Size() != 3<<20 {
		t.Fatalf("source lost: %v %v", st, err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "old" {
		t.Fatal("partial copy replaced destination")
	}
	entries, err := os.ReadDir(to)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files leaked: %v %v", entries, err)
	}
}
