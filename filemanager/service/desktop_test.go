package service

import (
	"filemanager/api"
	"testing"
)

func TestRestrictedRootCannotEnableDesktopAPIs(t *testing.T) {
	s, err := New([]api.Root{{ID: "local", Path: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.unrestrictedLocal() {
		t.Fatal("restricted root marked as desktop")
	}
	if s.EnableLocalIndex() == nil {
		t.Fatal("restricted root enabled absolute-path index API")
	}
	if s.EnableLocalBackup() == nil {
		t.Fatal("restricted root enabled absolute-path backup API")
	}
	if s.EnableDesktopTrash() == nil {
		t.Fatal("restricted root enabled desktop trash")
	}
}
