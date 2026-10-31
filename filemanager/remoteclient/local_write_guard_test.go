package fileindex

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractionGuardsEachActualDestination(t *testing.T) {
	root := t.TempDir()
	protected := filepath.Join(root, "operations")
	if err := os.WriteFile(protected, []byte("journal"), 0600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range []string{"ordinary", "operations"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: 3}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("new")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	x := &extractor{dest: root, guard: func(p string) error {
		if p == protected {
			return errors.New("protected")
		}
		return nil
	}}
	if err := x.extract(tar.NewReader(&buf), func(int64, string) {}); err != nil {
		t.Fatal(err)
	}
	if len(x.errs) != 1 {
		t.Fatalf("guard errors: %v", x.errs)
	}
	data, err := os.ReadFile(protected)
	if err != nil || string(data) != "journal" {
		t.Fatalf("protected file changed: %q %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(root, "ordinary"))
	if err != nil || string(data) != "new" {
		t.Fatalf("ordinary file: %q %v", data, err)
	}
}
