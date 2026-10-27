package fileindex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This starts the real NAS server in an isolated directory; it never writes to
// an existing NAS. Build fileindex/cmd/fileindex, then set
// FILESYNC_RESUME_SERVER_BINARY=/path/to/fileindex to run the cross-project test.
func TestDownloadResumeRealServer(t *testing.T) {
	binary := os.Getenv("FILESYNC_RESUME_SERVER_BINARY")
	if binary == "" {
		t.Skip("set FILESYNC_RESUME_SERVER_BINARY")
	}
	base := t.TempDir()
	data := filepath.Join(base, "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), "FI_ADDR="+addr, "FI_DATA="+data, "FI_CONFIG="+filepath.Join(base, "config"), "FI_GOTENBERG_URL=")
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-done
		}
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	hc := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		res, err := hc.Get("http://" + addr + "/api/auth/status")
		if err == nil {
			res.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	req, _ := http.NewRequest("POST", "http://"+addr+"/api/auth/setup", bytes.NewBufferString(`{"password":"isolated-e2e-password"}`))
	req.Header.Set(csrfHeader, csrfValue)
	req.Header.Set("Content-Type", "application/json")
	res, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("setup status %d", res.StatusCode)
	}
	c := New(addr, "")
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	if err := c.Login(ctx, "isolated-e2e-password"); err != nil {
		t.Fatal(err)
	}
	root, err := c.SaveRoot(ctx, 0, RootSettings{Name: "resume-e2e", Path: data})
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("NAS backup content 0123456789\n"), 1<<20)
	source := filepath.Join(data, "large.bin")
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(base, "large.bin")
	partial := filepath.Join(base, ".filesync-download-e2e.part")
	for _, changed := range []bool{false, true} {
		cancelCtx, cancel := context.WithCancel(ctx)
		err := c.DownloadFileResumable(cancelCtx, root.ID, "large.bin", dest, partial, func(p Progress) {
			if p.Done >= 1<<20 {
				cancel()
			}
		})
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("interrupt: %v", err)
		}
		info, err := os.Stat(partial)
		if err != nil {
			t.Fatal(err)
		}
		if changed {
			content = bytes.Repeat([]byte("NEW backup content 9876543210\n"), 1<<20)
			if err := os.WriteFile(source, content, 0600); err != nil {
				t.Fatal(err)
			}
			mt := time.Now().Add(time.Second)
			if err := os.Chtimes(source, mt, mt); err != nil {
				t.Fatal(err)
			}
		}
		fresh := New(addr, c.Token)
		first := int64(-1)
		err = fresh.DownloadFileResumable(ctx, root.ID, "large.bin", dest, partial, func(p Progress) {
			if first < 0 {
				first = p.Done
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		want := info.Size()
		if changed {
			want = 0
		}
		if first != want {
			t.Fatalf("changed=%v: started at %d, want %d", changed, first, want)
		}
		b, err := os.ReadFile(dest)
		if err != nil || sha256.Sum256(b) != sha256.Sum256(content) {
			t.Fatalf("content verification failed: %v", err)
		}
		t.Logf("real NAS server: changed=%v, preserved=%d, restart offset=%d, verified %d bytes by SHA-256", changed, info.Size(), first, len(b))
	}
}
