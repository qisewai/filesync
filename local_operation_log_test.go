package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	localindex "filemanager/index"
	"filesync/config"
)

func TestLocalOperationsWriteIntentsAndOutcomes(t *testing.T) {
	root := t.TempDir()
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	a := h.a
	a.localCachePath = filepath.Join(t.TempDir(), "index.bin")
	defer a.stopBackground()
	a.localMkdir(root)
	if err := a.naming.submit("文档"); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(root, "文档")
	a.localRename(old)
	if err := a.naming.submit("归档"); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(root, "归档")
	a.localDelete([]string{newPath}, true)
	a.asking.action()
	if _, err := os.Lstat(newPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("delete: %v", err)
	}
	var records []localindex.Operation
	if err := a.localOpLog.Replay(func(_ uint64, op localindex.Operation) error {
		records = append(records, op)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(records) != 6 || records[0].Action != "mkdir" || records[0].Target != old || records[1].Phase != "done" || records[2].Action != "rename" || records[2].Source != old || records[2].Target != newPath || records[3].Phase != "done" || records[4].Action != "delete" || records[4].Source != newPath || records[5].Phase != "done" {
		t.Fatalf("records: %+v", records)
	}
	if records[2].SourceState.Kind != "dir" || records[2].TargetState.Kind != "missing" || records[3].SourceState.Kind != "missing" || records[3].TargetState.Kind != "dir" {
		t.Fatalf("rename states: %+v %+v", records[2], records[3])
	}
}

func TestLocalMoveRecordsDirectoryOperation(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "file.txt"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.localCachePath = filepath.Join(t.TempDir(), "index.bin")
	h.a.localTransfer([]string{source}, true, target)
	h.settle()
	if _, err := os.Stat(filepath.Join(target, "source", "nested", "file.txt")); err != nil {
		t.Fatal(err)
	}
	var records []localindex.Operation
	if err := h.a.localOpLog.Replay(func(_ uint64, op localindex.Operation) error { records = append(records, op); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Action != "move" || records[0].Source != source || records[0].Target != filepath.Join(target, "source") || records[0].SourceState.Kind != "dir" || records[1].Phase != "done" {
		t.Fatalf("move records: %+v", records)
	}
}

func TestLocalOperationDoesNotMutateDiskWhenLogIsCorrupt(t *testing.T) {
	root := t.TempDir()
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	a := h.a
	a.localCachePath = filepath.Join(t.TempDir(), "index.bin")
	defer a.stopBackground()
	if err := os.WriteFile(a.localCachePath+".operations", []byte("damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.localMkdir(root)
	if err := a.naming.submit("new"); err == nil {
		t.Fatal("corrupted journal did not prevent the operation")
	}
	if _, err := os.Lstat(filepath.Join(root, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory was created despite journal failure: %v", err)
	}
}

func TestFailedOperationIsRecorded(t *testing.T) {
	root := t.TempDir()
	l, err := localindex.OpenOperationLog(filepath.Join(t.TempDir(), "operations"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	src, dst := filepath.Join(root, "missing"), filepath.Join(root, "dest")
	opErr, logErr := loggedLocalOperation(l, "rename", src, dst, func() error { return os.Rename(src, dst) })
	if opErr == nil || logErr != nil {
		t.Fatalf("operation=%v log=%v", opErr, logErr)
	}
	var records []localindex.Operation
	if err := l.Replay(func(_ uint64, op localindex.Operation) error { records = append(records, op); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[1].Phase != "failed" || records[1].ID != 1 || records[1].Error == "" {
		t.Fatalf("records: %+v", records)
	}
}

func TestOutcomeWriteFailureKeepsUnfinishedIntent(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "operations")
	target := filepath.Join(t.TempDir(), "file")
	l, err := localindex.OpenOperationLog(filename)
	if err != nil {
		t.Fatal(err)
	}
	opErr, logErr := loggedLocalOperation(l, "download", "", target, func() error {
		if err := os.WriteFile(target, []byte("done"), 0o600); err != nil {
			return err
		}
		return l.Close() // Simulate a completion write failure after disk changed.
	})
	if opErr != nil || logErr == nil {
		t.Fatalf("operation=%v log=%v", opErr, logErr)
	}
	l, err = localindex.OpenOperationLog(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var records []localindex.Operation
	if err := l.Replay(func(_ uint64, op localindex.Operation) error { records = append(records, op); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Phase != "begin" || records[0].Target != target {
		t.Fatalf("unfinished intent: %+v", records)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
}

func TestRecursiveCopyChecksSymlinkedDestination(t *testing.T) {
	root := t.TempDir()
	l, err := localindex.OpenOperationLog(filepath.Join(root, "cache", "operations"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	source, dest := filepath.Join(root, "source"), filepath.Join(root, "dest")
	if err = os.MkdirAll(filepath.Join(source, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "nested", "operations"), []byte("overwrite"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(root, "cache"), filepath.Join(dest, "nested")); err != nil {
		t.Skip(err)
	}
	opErr, logErr := loggedLocalOperation(l, "copy", source, dest, func() error {
		return copyTreeChecked(context.Background(), source, dest, func(int64, string) {}, l.CheckWritePath)
	})
	if opErr == nil || logErr != nil {
		t.Fatalf("op=%v log=%v", opErr, logErr)
	}
	if err = l.Replay(nil); err != nil {
		t.Fatalf("journal damaged: %v", err)
	}
}

func TestFullRebuildRecoversOperationLog(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, &config.Store{Dir: t.TempDir()})
	h.a.localCachePath = filepath.Join(t.TempDir(), "index.bin")
	if err := os.WriteFile(h.a.localCachePath+".operations", []byte("torn log"), 0600); err != nil {
		t.Fatal(err)
	}
	h.a.ensureLocalIndex()
	h.settle()
	h.a.localIdx.settings.Roots = []string{root}
	h.a.localIdx.settings.Excludes = nil
	h.a.rebuildLocalIndex()
	h.settle()
	if h.a.localIdx.err != "" {
		t.Fatal(h.a.localIdx.err)
	}
	if h.a.localOpLog == nil || h.a.localOpLog.RecoveryNeeded() {
		t.Fatal("journal not recovered")
	}
	records := 0
	if err := h.a.localOpLog.Replay(func(_ uint64, op localindex.Operation) error {
		records++
		if op.Phase != "checkpoint" {
			t.Errorf("unexpected record %+v", op)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if records != 1 {
		t.Fatal(records)
	}
	h.a.localMkdir(root)
	if err := h.a.naming.submit("after-recovery"); err != nil {
		t.Fatal(err)
	}
}
