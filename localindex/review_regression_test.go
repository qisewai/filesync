package localindex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOperationLogRejectsAliasesAndDirectoryDownloads(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(real, "operations")
	l, err := OpenOperationLog(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err = l.Begin("download", "", real); err == nil {
		t.Fatal("accepted directory download over journal")
	}
	alias := filepath.Join(dir, "alias")
	if err = os.Symlink(real, alias); err != nil {
		t.Skip(err)
	}
	for _, action := range []string{"delete", "rename", "move", "trash"} {
		if _, err = l.Begin(action, filepath.Join(alias, "operations"), ""); err == nil {
			t.Fatalf("accepted %s via alias", action)
		}
	}
	if _, err = l.Begin("download", "", filepath.Join(alias, "operations")); err == nil {
		t.Fatal("accepted alias write")
	}
	if err = l.CheckWritePath(filepath.Join(alias, "operations")); err == nil {
		t.Fatal("entry guard accepted alias")
	}
	// A parent that does not exist yet must still resolve its existing ancestors.
	got, err := resolveOperationPath(filepath.Join(alias, "missing", "child"))
	if err != nil || got != filepath.Join(real, "missing", "child") {
		t.Fatalf("resolved=%q err=%v", got, err)
	}
}

func TestOperationLogPreservesNativePathBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix filename bytes")
	}
	path := filepath.Join(t.TempDir(), "operations")
	target := filepath.Join(t.TempDir(), string([]byte{'f', 255}))
	l, err := OpenOperationLog(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := l.Begin("mkdir", "", target)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err = l.Finish(id, nil); err != nil {
		t.Fatal(err)
	}
	l.Close()
	l, err = OpenOperationLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err = l.Replay(func(_ uint64, op Operation) error {
		if op.Phase == "begin" && op.Target != target {
			t.Errorf("path changed: %q", op.Target)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestJournalDetectsReplacedStorage(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "operations")
	l, err := OpenOperationLog(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	id, err := l.Begin("mkdir", "", filepath.Join(t.TempDir(), "new"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filename, filename+".old"); err != nil {
		t.Skip(err)
	}
	if err = os.WriteFile(filename, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = l.Finish(id, nil); err == nil {
		t.Fatal("reported durable success after replacement")
	}
}

func makeCheckpointSnapshot(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("contents"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := ScanOptions{Roots: []string{root}}
	x := New()
	if p, err := x.Scan(ctx, opts, nil); err != nil || p.Errors != 0 {
		t.Fatalf("scan: %+v %v", p, err)
	}
	filename := filepath.Join(t.TempDir(), "snapshot")
	if err := x.SaveSnapshot(ctx, filename, opts, SnapshotMeta{}); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestOperationCheckpointExcludesLiveOperationsAndReopens(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "operations")
	l, err := OpenOperationLog(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	id, err := l.Begin("mkdir", "", filepath.Join(t.TempDir(), "new"))
	if err != nil {
		t.Fatal(err)
	}
	if err = l.StartCheckpoint(); err == nil {
		t.Fatal("checkpoint accepted active operation")
	}
	if err = l.Finish(id, nil); err != nil {
		t.Fatal(err)
	}
	if err = l.StartCheckpoint(); err != nil {
		t.Fatal(err)
	}
	if _, err = l.Begin("mkdir", "", filepath.Join(t.TempDir(), "other")); err == nil {
		t.Fatal("mutation accepted during checkpoint")
	}
	snapshot := makeCheckpointSnapshot(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = l.CommitCheckpoint(canceled, snapshot); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	count := 0
	l.Replay(func(_ uint64, _ Operation) error { count++; return nil })
	if count != 2 {
		t.Fatal("canceled checkpoint lost records")
	}
	if err = l.CommitCheckpoint(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	l.EndCheckpoint()
	l.Close()
	l, err = OpenOperationLog(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	count = 0
	if err = l.Replay(func(seq uint64, op Operation) error {
		count++
		if seq != 1 || op.Phase != "checkpoint" || op.Checkpoint.PreviousSequence != 2 {
			t.Fatalf("checkpoint: %+v", op)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal(count)
	}
	id, err = l.Begin("mkdir", "", filepath.Join(t.TempDir(), "later"))
	if err != nil || id != 2 {
		t.Fatalf("append after checkpoint: %d %v", id, err)
	}
	if err = l.Finish(id, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDamagedLogRecoveryRequiresCheckpointAndPreservesEvidence(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "operations")
	original := []byte("interrupted journal")
	if err := os.WriteFile(filename, original, 0600); err != nil {
		t.Fatal(err)
	}
	l, err := OpenRecoverableOperationLog(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if !l.RecoveryNeeded() {
		t.Fatal("missing recovery flag")
	}
	if _, err = l.Begin("mkdir", "", filepath.Join(t.TempDir(), "new")); err == nil {
		t.Fatal("mutated damaged journal")
	}
	if err = l.StartCheckpoint(); err != nil {
		t.Fatal(err)
	}
	if err = l.CommitCheckpoint(context.Background(), makeCheckpointSnapshot(t)); err != nil {
		t.Fatal(err)
	}
	l.EndCheckpoint()
	backups, err := filepath.Glob(filename + ".damaged-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v %v", backups, err)
	}
	data, err := os.ReadFile(backups[0])
	if err != nil || string(data) != string(original) {
		t.Fatalf("evidence changed %q %v", data, err)
	}
	if l.RecoveryNeeded() {
		t.Fatal("still blocked after recovery")
	}
	l.Close()
	l, err = OpenOperationLog(filename)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
}

func TestSearchSharesVersionDirectoryCacheAndRetriesCancellation(t *testing.T) {
	x := New()
	root := t.TempDir()
	opts := ScanOptions{Roots: []string{root}}
	if err := os.Mkdir(filepath.Join(root, "Dir"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Scan(context.Background(), opts, nil); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := x.Search(canceled, Query{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	a, err := x.Search(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := x.Search(context.Background(), Query{Text: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if &a.foldDirs[0][0] != &b.foldDirs[0][0] {
		t.Fatal("search results duplicate directory paths")
	}
	if _, err = x.Scan(context.Background(), opts, nil); err != nil {
		t.Fatal(err)
	}
	c, err := x.Search(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if &a.foldDirs[0][0] == &c.foldDirs[0][0] {
		t.Fatal("cache shared across mutable versions")
	}
}

func TestOperationLogCapacityBlocksOnlyNewIntents(t *testing.T) {
	l, err := OpenOperationLog(filepath.Join(t.TempDir(), "operations"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.journal.mu.Lock()
	size := l.journal.size
	l.journal.size = maxOperationLogSize
	l.journal.mu.Unlock()
	if _, err = l.Begin("mkdir", "", filepath.Join(t.TempDir(), "new")); err == nil {
		t.Fatal("accepted new intent over capacity")
	}
	l.journal.mu.Lock()
	l.journal.size = size
	l.journal.mu.Unlock()
	if err = l.StartCheckpoint(); err != nil {
		t.Fatal(err)
	}
	if err = l.CommitCheckpoint(context.Background(), makeCheckpointSnapshot(t)); err != nil {
		t.Fatal(err)
	}
	l.EndCheckpoint()
	if _, err = l.Begin("mkdir", "", filepath.Join(t.TempDir(), "after")); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointPreservesUnfinishedIntentUntilSuccessfulRebuild(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "operations")
	l, err := OpenOperationLog(filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.Begin("download", "", filepath.Join(t.TempDir(), "partial")); err != nil {
		t.Fatal(err)
	}
	l.Close()
	l, err = OpenOperationLog(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err = l.StartCheckpoint(); err != nil {
		t.Fatal(err)
	}
	if err = l.CommitCheckpoint(context.Background(), filepath.Join(t.TempDir(), "missing-snapshot")); err == nil {
		t.Fatal("accepted missing snapshot")
	}
	l.EndCheckpoint()
	if len(l.pending) != 1 {
		t.Fatal("unfinished intent lost after failed rebuild")
	}
	if err = l.StartCheckpoint(); err != nil {
		t.Fatal(err)
	}
	if err = l.CommitCheckpoint(context.Background(), makeCheckpointSnapshot(t)); err != nil {
		t.Fatal(err)
	}
	l.EndCheckpoint()
	if len(l.pending) != 0 {
		t.Fatal("unfinished intent not reconciled")
	}
}

func TestJournalDetectsInPlaceTruncation(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "operations")
	l, err := OpenOperationLog(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	id, err := l.Begin("mkdir", "", filepath.Join(t.TempDir(), "new"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Truncate(filename, 0); err != nil {
		t.Fatal(err)
	}
	if err = l.Finish(id, nil); err == nil {
		t.Fatal("accepted append after truncation")
	}
}
