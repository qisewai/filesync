package index

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestOperationLogReopenAndUnfinishedIntent(t *testing.T) {
	file := filepath.Join(t.TempDir(), "operations")
	source, target := filepath.Join(t.TempDir(), "source"), filepath.Join(t.TempDir(), "target")
	l, err := OpenOperationLog(file)
	if err != nil {
		t.Fatal(err)
	}
	id, err := l.Begin("move", source, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOperationLog(file); err == nil {
		t.Fatal("a second writer acquired the same journal")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l, err = OpenOperationLog(file)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var found Operation
	if err := l.Replay(func(seq uint64, op Operation) error { found = op; return nil }); err != nil {
		t.Fatal(err)
	}
	if found.Phase != "begin" || found.Source != source || found.Target != target {
		t.Fatalf("pending intent: %+v", found)
	}
	if err := l.Finish(id, errors.New("interrupted")); err != nil {
		t.Fatal(err)
	}
	if err := l.Finish(id, nil); err == nil {
		t.Fatal("duplicate completion accepted")
	}
}

func TestOperationLogRejectsInvalidIntent(t *testing.T) {
	l, err := OpenOperationLog(filepath.Join(t.TempDir(), "operations"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, tc := range []struct{ action, source, target string }{
		{"other", "/a", ""}, {"move", "relative", ""}, {"mkdir", "", ""},
	} {
		if _, err := l.Begin(tc.action, tc.source, tc.target); err == nil {
			t.Errorf("accepted invalid operation: %+v", tc)
		}
	}
}

func TestOperationLogProtectsItsOwnStorage(t *testing.T) {
	root := t.TempDir()
	filename := filepath.Join(root, "cache", "operations")
	l, err := OpenOperationLog(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, tc := range []struct{ action, source, target string }{
		{"delete", filename, ""},
		{"trash", root, ""},
		{"rename", filepath.Dir(filename), filepath.Join(root, "renamed")},
		{"download", "", filename},
		{"copy", filepath.Join(root, "source"), filepath.Dir(filename)},
	} {
		if _, err := l.Begin(tc.action, tc.source, tc.target); err == nil {
			t.Errorf("accepted operation affecting its log: %+v", tc)
		}
	}
}
