package index

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

func TestJournalReopensAndBindsSnapshot(t *testing.T) {
	file := filepath.Join(t.TempDir(), "changes.log")
	base := sha256.Sum256([]byte("snapshot"))
	j, err := OpenJournal(file, base)
	if err != nil {
		t.Fatal(err)
	}
	for i, value := range []string{"add", "move", "delete", ""} {
		seq, err := j.Append([]byte(value))
		if err != nil || seq != uint64(i+1) {
			t.Fatalf("append: %d %v", seq, err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(file, sha256.Sum256([]byte("another"))); !errors.Is(err, ErrJournalBase) {
		t.Fatalf("wrong base: %v", err)
	}
	j, err = OpenJournal(file, base)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	var values []string
	if err := j.Replay(func(seq uint64, payload []byte) error {
		if seq != uint64(len(values)+1) {
			t.Fatalf("sequence: %d", seq)
		}
		values = append(values, string(payload))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(values, []string{"add", "move", "delete", ""}) {
		t.Fatalf("replay: %v", values)
	}
	if seq, err := j.Append([]byte("after reopen")); seq != 5 || err != nil {
		t.Fatalf("append after reopen: %d %v", seq, err)
	}
}

func TestJournalRejectsTruncatedAndCorruptRecords(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "changes.log")
	base := sha256.Sum256([]byte("snapshot"))
	j, err := OpenJournal(file, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append([]byte("data")); err != nil {
		t.Fatal(err)
	}
	j.Close()
	good, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty existing file", nil},
		{"short header", slices.Clone(good[:10])},
		{"short frame", slices.Clone(good[:journalHeaderSize+5])},
		{"short payload", slices.Clone(good[:len(good)-1])},
		{"bad checksum", append(slices.Clone(good[:len(good)-1]), good[len(good)-1]^1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := filepath.Join(dir, tc.name)
			if err := os.WriteFile(bad, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenJournal(bad, base); !errors.Is(err, ErrJournalCorrupt) {
				t.Fatalf("accepted damaged log: %v", err)
			}
			got, _ := os.ReadFile(bad)
			if !slices.Equal(got, tc.data) {
				t.Fatal("opening a damaged log modified it")
			}
		})
	}
}

func TestJournalReplayFailureDoesNotChangeLog(t *testing.T) {
	base := sha256.Sum256([]byte("snapshot"))
	j, err := OpenJournal(filepath.Join(t.TempDir(), "changes.log"), base)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err := j.Append([]byte("one")); err != nil {
		t.Fatal(err)
	}
	want := errors.New("stop")
	if err := j.Replay(func(uint64, []byte) error { return want }); !errors.Is(err, want) {
		t.Fatalf("replay error: %v", err)
	}
	if seq, err := j.Append([]byte("two")); err != nil || seq != 2 {
		t.Fatalf("append after failed replay: %d %v", seq, err)
	}
}

func TestJournalConcurrentAppends(t *testing.T) {
	base := sha256.Sum256([]byte("snapshot"))
	j, err := OpenJournal(filepath.Join(t.TempDir(), "changes.log"), base)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := j.Append([]byte(fmt.Sprint(i))); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	var count int
	if err := j.Replay(func(seq uint64, payload []byte) error {
		count++
		return nil
	}); err != nil || count != 20 {
		t.Fatalf("replay: %d %v", count, err)
	}
}
