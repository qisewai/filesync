package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompanionSnapshotSurvivesUpdate(t *testing.T) {
	source := filepath.Join(t.TempDir(), "filemanager")
	if err := os.WriteFile(source, []byte("old backend"), 0700); err != nil {
		t.Fatal(err)
	}
	path, cleanup, err := snapshotCompanion(source)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	// An update replaces the installed helper while the frontend keeps running.
	if err := os.WriteFile(source, []byte("new backend"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "old backend" {
		t.Fatalf("snapshot = %q, %v", got, err)
	}
	cleanup()
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cleanup: %v", err)
	}
}

func TestCompanionSnapshotMissing(t *testing.T) {
	path, cleanup, err := snapshotCompanion(filepath.Join(t.TempDir(), "missing"))
	cleanup()
	if err == nil || path != "" {
		t.Fatal("missing companion accepted")
	}
}
