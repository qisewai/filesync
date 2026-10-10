package fileindex

import (
	"context"
	"os"
	"testing"
	"time"

	"filesync/engine"
)

// TestE2E backs up to a real fileindex server, and times it:
//
//	FILESYNC_E2E_ADDR=127.0.0.1:18090 FILESYNC_E2E_TOKEN=… FILESYNC_E2E_SRC=dir go test -run E2E -v ./fileindex
func TestE2E(t *testing.T) {
	addr, src := os.Getenv("FILESYNC_E2E_ADDR"), os.Getenv("FILESYNC_E2E_SRC")
	if addr == "" {
		t.Skip("set FILESYNC_E2E_ADDR")
	}
	loc := Location{Addr: addr, Root: 1, Path: "e2e"}
	if p := os.Getenv("FILESYNC_E2E_PATH"); p != "" {
		loc.Path = p
	}
	target := NewTarget(loc, os.Getenv("FILESYNC_E2E_TOKEN"))
	for _, mode := range []engine.Mode{engine.Mirror, engine.Mirror} {
		start := time.Now()
		p, err := engine.Prepare(context.Background(), engine.Options{Source: src, Mode: mode, Dest: target}, nil)
		if err != nil {
			t.Fatal(err)
		}
		scan := time.Since(start)
		start = time.Now()
		r := engine.Execute(context.Background(), p, engine.Hooks{})
		d := time.Since(start)
		if r.Fatal != nil || len(r.Errors) > 0 {
			t.Fatalf("fatal %v, %d errors, first %v", r.Fatal, len(r.Errors), r.Errors[:min(1, len(r.Errors))])
		}
		files := r.Created + r.Updated
		t.Logf("scan %v (%d source files, %d target files); copied %d files, %d MB in %v: %.0f files/s, %.1f MB/s",
			scan.Round(time.Millisecond), p.SourceFiles, p.TargetFiles, files, r.Bytes>>20, d.Round(time.Millisecond),
			float64(files)/d.Seconds(), float64(r.Bytes)/(1<<20)/d.Seconds())
	}
}
