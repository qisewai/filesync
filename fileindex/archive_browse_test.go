package fileindex

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filesync/fileindex/fitest"
)

func TestArchiveTransportPreviewProxyAndDownload(t *testing.T) {
	f := fitest.New(t)
	p := filepath.Join(f.Root, "a.zip")
	out, _ := os.Create(p)
	z := zip.NewWriter(out)
	w, _ := z.Create("sub/a.txt")
	w.Write([]byte("hello archive"))
	z.Close()
	out.Close()
	for _, c := range []*Client{New(f.Addr(), "tok"), NewLocalArchive(p)} {
		t.Run(c.Addr, func(t *testing.T) {
			file := "a.zip"
			if c.ArchiveSource != nil {
				file = p
			}
			es, version, _, err := c.ArchiveListProgress(context.Background(), 1, file, "sub", nil)
			if err != nil || len(es) != 1 {
				t.Fatalf("list %v %v", es, err)
			}
			ac := c
			if !c.InArchive() {
				ac = c.Archive(1, file, version)
			} else {
				s := *c.ArchiveSource
				s.Version = version
				clone := *c
				clone.ArchiveSource = &s
				ac = &clone
			}
			data, truncated, err := ac.ReadPreview(context.Background(), 1, "sub/a.txt", 5)
			if err != nil || !truncated || string(data) != "hello" {
				t.Fatalf("preview %q %v %v", data, truncated, err)
			}
			proxy, err := NewLocalProxy()
			if err != nil {
				t.Fatal(err)
			}
			defer proxy.Close()
			u, err := proxy.Register("test", ac, 1, "sub/a.txt")
			if err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "a.txt")
			if err := DownloadProxyFile(context.Background(), u, dest, nil); err != nil {
				t.Fatal(err)
			}
			body, _ := os.ReadFile(dest)
			if string(body) != "hello archive" {
				t.Fatalf("proxy %q", body)
			}
			target := t.TempDir()
			errs, err := ac.Download(context.Background(), 1, []string{"sub"}, target, nil)
			if err != nil || len(errs) > 0 {
				t.Fatalf("export %v %v", errs, err)
			}
			body, _ = os.ReadFile(filepath.Join(target, "sub", "a.txt"))
			if string(body) != "hello archive" {
				t.Fatalf("export %q", body)
			}
		})
	}
	f.Expired.Store(true)
	if _, _, _, err := New(f.Addr(), "tok").ArchiveList(context.Background(), 1, "a.zip", ""); !errors.Is(err, ErrAuth) {
		t.Fatalf("auth %v", err)
	}
}
