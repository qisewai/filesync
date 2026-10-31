package fileindex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDownloadResumeInterruptedRequest(t *testing.T) {
	content := bytes.Repeat([]byte("NAS photo backup\n"), 65536)
	cut := len(content) / 3
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		cookie, _ := r.Cookie(cookieName)
		if cookie == nil || cookie.Value != "current-session" || r.Header.Get(csrfHeader) != csrfValue {
			t.Error("missing current credentials")
		}
		w.Header().Set("ETag", `"v1"`)
		if requests == 1 {
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			w.Write(content[:cut])
			return
		}
		if r.Header.Get("Range") != fmt.Sprintf("bytes=%d-", cut) || r.Header.Get("If-Range") != `"v1"` {
			t.Errorf("resume headers %v", r.Header)
		}
		http.ServeContent(w, r, "photo.bin", time.Unix(1000, 0), bytes.NewReader(content))
	}))
	defer srv.Close()
	c := New(strings.TrimPrefix(srv.URL, "http://"), "current-session")
	dest := filepath.Join(t.TempDir(), "photo.bin")
	partial := filepath.Join(filepath.Dir(dest), ".filesync-download-test.part")
	os.WriteFile(dest, []byte("original"), 0600)
	if err := c.DownloadFileResumable(context.Background(), 1, "photo.bin", dest, partial, nil); err == nil {
		t.Fatal("truncated response accepted")
	}
	if b, _ := os.ReadFile(dest); string(b) != "original" {
		t.Fatal("interruption replaced original")
	}
	if b, _ := os.ReadFile(partial); !bytes.Equal(b, content[:cut]) {
		t.Fatalf("partial not preserved: %d bytes", len(b))
	}
	// A newly constructed client has no memory of the interrupted request.
	c = New(c.Addr, "current-session")
	var first *Progress
	err := c.DownloadFileResumable(context.Background(), 1, "photo.bin", dest, partial, func(p Progress) {
		if first == nil {
			first = &p
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || first.Done != int64(cut) {
		t.Fatalf("resumed progress %+v", first)
	}
	if b, _ := os.ReadFile(dest); !bytes.Equal(b, content) {
		t.Fatal("resumed bytes differ")
	}
	for _, p := range []string{partial, partial + ".json"} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("sidecar remains %s: %v", p, err)
		}
	}
}

func TestDownloadResumeValidationAndFallback(t *testing.T) {
	for _, scenario := range []string{"changed", "ignored", "no-etag", "weak-etag", "bad-checkpoint", "wrong-range", "wrong-etag", "truncated", "range-rejected", "unauthorized", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "a.bin")
			partial := filepath.Join(filepath.Dir(dest), ".filesync-download-test.part")
			os.WriteFile(dest, []byte("original"), 0600)
			os.WriteFile(partial, []byte("old"), 0600)
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch scenario {
				case "unauthorized":
					w.WriteHeader(401)
					return
				case "range-rejected":
					if calls == 1 {
						w.WriteHeader(416)
						return
					}
				case "wrong-range", "wrong-etag", "truncated":
					w.Header().Set("ETag", `"v1"`)
					w.Header().Set("Content-Range", "bytes 3-8/9")
					w.Header().Set("Content-Length", "6")
					if scenario == "wrong-range" {
						w.Header().Set("Content-Range", "bytes 2-8/9")
					}
					if scenario == "wrong-etag" {
						w.Header().Set("ETag", `"v2"`)
					}
					w.WriteHeader(206)
					if scenario == "truncated" {
						w.Write([]byte("x"))
					} else {
						w.Write([]byte("xxxxxx"))
					}
					return
				}
				if scenario == "no-etag" || scenario == "weak-etag" || scenario == "bad-checkpoint" {
					if r.Header.Get("Range") != "" {
						t.Error("unverifiable version resumed")
					}
				}
				w.Header().Set("ETag", `"v2"`)
				content := "NEW FILE!"
				if scenario == "empty" {
					content = ""
				}
				w.Header().Set("Content-Length", fmt.Sprint(len(content)))
				w.Write([]byte(content))
			}))
			defer srv.Close()
			c := New(strings.TrimPrefix(srv.URL, "http://"), "tok")
			state := downloadCheckpoint{Source: c.url("/api/download/1", map[string][]string{"path": {"a.bin"}}), ETag: `"v1"`, Size: 9}
			if scenario == "no-etag" {
				state.ETag = ""
			}
			if scenario == "weak-etag" {
				state.ETag = `W/"v1"`
			}
			if err := saveDownloadCheckpoint(partial+".json", state); err != nil {
				t.Fatal(err)
			}
			if scenario == "bad-checkpoint" {
				os.WriteFile(partial+".json", []byte("broken"), 0600)
			}
			err := c.DownloadFileResumable(context.Background(), 1, "a.bin", dest, partial, nil)
			bad := scenario == "wrong-range" || scenario == "wrong-etag" || scenario == "truncated" || scenario == "unauthorized"
			if (err != nil) != bad {
				t.Fatalf("error %v, want failure=%v", err, bad)
			}
			want := "NEW FILE!"
			if bad {
				want = "original"
			}
			if scenario == "empty" {
				want = ""
			}
			if b, _ := os.ReadFile(dest); string(b) != want {
				t.Fatalf("destination %q, want %q", b, want)
			}
			if scenario == "unauthorized" && !errors.Is(err, ErrAuth) {
				t.Fatalf("auth error lost: %v", err)
			}
		})
	}
}

func TestDownloadResumeCancellationAndPartialSafety(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Length", "1048576")
		w.Write(bytes.Repeat([]byte("x"), 1024))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := New(strings.TrimPrefix(srv.URL, "http://"), "tok")
	dest := filepath.Join(t.TempDir(), "a.bin")
	partial := filepath.Join(filepath.Dir(dest), ".filesync-download-test.part")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := c.DownloadFileResumable(ctx, 1, "a.bin", dest, partial, func(p Progress) {
		if p.Done > 0 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if info, err := os.Stat(partial); err != nil || info.Size() != 1024 {
		t.Fatalf("partial: %v %v", info, err)
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled file published")
	}
	RemoveDownloadPartial(dest, dest) // must never delete the destination
	RemoveDownloadPartial(dest, partial)
	if _, err := os.Stat(partial); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial not removed")
	}
	// A sidecar symlink cannot redirect download writes into another file.
	os.WriteFile(dest, []byte("keep"), 0600)
	if err := os.Symlink(dest, partial); err != nil {
		t.Skip(err)
	}
	err = c.DownloadFileResumable(context.Background(), 1, "a.bin", dest, partial, nil)
	if err == nil {
		t.Fatal("symlink accepted")
	}
	b, _ := os.ReadFile(dest)
	if string(b) != "keep" {
		t.Fatal("symlink target modified")
	}
}
