package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReceiptPreservesChangedAndNewEntries(t *testing.T) {
	for _, change := range []string{"modify", "replace", "new"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "source")
			os.Mkdir(p, 0700)
			file := filepath.Join(p, "file")
			os.WriteFile(file, []byte("old"), 0600)
			r, err := Capture(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "modify":
				os.WriteFile(file, []byte("changed content"), 0600)
			case "replace":
				os.Rename(file, file+".old")
				os.WriteFile(file, []byte("new"), 0600)
			case "new":
				os.WriteFile(filepath.Join(p, "new"), []byte("new"), 0600)
			}
			err = r.RemoveCopied(context.Background())
			if err == nil {
				t.Fatal("changed source removed")
			}
			if change != "new" && !errors.Is(err, ErrSourceChanged) {
				t.Fatal(err)
			}
			keep := file
			if change == "new" {
				keep = filepath.Join(p, "new")
			}
			if _, err := os.Lstat(keep); err != nil {
				t.Fatalf("new source lost: %v", err)
			}
		})
	}
}

func TestReceiptCancellationAndCompleteRemoval(t *testing.T) {
	p := filepath.Join(t.TempDir(), "source")
	os.MkdirAll(filepath.Join(p, "child"), 0700)
	os.WriteFile(filepath.Join(p, "child", "file"), nil, 0600)
	r, err := Capture(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = r.RemoveCopied(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = r.RemoveCopied(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("source remains")
	}
}

func TestCopyRejectsRecursiveDestination(t *testing.T) {
	root := t.TempDir()
	if err := CopyTree(context.Background(), root, filepath.Join(root, "child"), nil); err == nil {
		t.Fatal("recursive destination allowed")
	}
	if _, err := os.Stat(filepath.Join(root, "child")); !os.IsNotExist(err) {
		t.Fatal("invalid copy changed disk")
	}
}
