package service

import (
	"context"
	"errors"
	"io/fs"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"filemanager/api"
	"filemanager/client"
)

func TestWindowsListDeniedJunctionAndHiddenAttribute(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"visible.txt", "hidden.txt", "locked.dat"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	locked, err := syscall.UTF16PtrFromString(filepath.Join(dir, "locked.dat"))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(locked, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(handle)
	hidden, err := syscall.UTF16PtrFromString(filepath.Join(dir, "hidden.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.SetFileAttributes(hidden, syscall.FILE_ATTRIBUTE_HIDDEN); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(dir, "Application Data")
	target := t.TempDir()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v: %s", err, out)
	}
	// Reproduce the access rule on Windows profile compatibility junctions.
	if out, err := exec.Command("icacls", junction, "/deny", "*S-1-1-0:(RD)", "/L").CombinedOutput(); err != nil {
		t.Fatalf("deny junction listing: %v: %s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("icacls", junction, "/remove:d", "*S-1-1-0", "/L").CombinedOutput(); err != nil {
			t.Errorf("restore test junction ACL: %v: %s", err, out)
		}
	})
	s, err := New([]api.Root{{ID: "r", Path: dir}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, statErr := root.Lstat("Application Data")
	root.Close()
	if !errors.Is(statErr, fs.ErrPermission) {
		t.Fatalf("fixture must reproduce denied Lstat, got %v", statErr)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := client.New(srv.URL, srv.Client())
	for _, showHidden := range []bool{false, true} {
		entries, err := c.List(context.Background(), api.ListRequest{Location: api.Location{Root: "r"}, Hidden: showHidden})
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if showHidden {
			want = 2
		}
		if len(entries) != want {
			t.Fatalf("hidden=%v: %+v", showHidden, entries)
		}
	}
	if _, err := c.List(context.Background(), api.ListRequest{Location: api.Location{Root: "r", Path: "Application Data"}}); err == nil {
		t.Fatal("direct access to denied junction must still fail")
	}
}
