package fileindex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"filesync/engine"
)

// fakeServer speaks the backup API of fileindex for one root, on a
// directory.
type fakeServer struct {
	t       *testing.T
	root    string // the root's directory
	trash   string
	srv     *httptest.Server
	expired atomic.Bool // sessions are refused
	puts    atomic.Int32
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{t: t, root: t.TempDir(), trash: t.TempDir()}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Password string }
		json.NewDecoder(r.Body).Decode(&req)
		if req.Password != "pw" {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":"密码错误"}`)
			return
		}
		f.expired.Store(false)
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "tok"})
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("GET /api/backup/manifest", f.manifest)
	mux.HandleFunc("POST /api/backup/put", f.put)
	mux.HandleFunc("POST /api/fs/delete", f.delete)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/login" {
			ck, err := r.Cookie(cookieName)
			if err != nil || ck.Value != "tok" || f.expired.Load() || r.Header.Get(csrfHeader) != csrfValue {
				w.WriteHeader(401)
				fmt.Fprint(w, `{"error":"未登录"}`)
				return
			}
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) addr() string { return strings.TrimPrefix(f.srv.URL, "http://") }

func (f *fakeServer) manifest(w http.ResponseWriter, r *http.Request) {
	base := filepath.Join(f.root, r.URL.Query().Get("path"))
	if _, err := os.Stat(base); err != nil {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":"目录不存在"}`)
		return
	}
	enc := json.NewEncoder(w)
	filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if p == base {
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		info, _ := d.Info()
		e := ManifestEntry{Path: filepath.ToSlash(rel), Dir: d.IsDir(), MTime: info.ModTime().UnixNano()}
		if !d.IsDir() {
			e.Size = info.Size()
		}
		return enc.Encode(e)
	})
}

func (f *fakeServer) put(w http.ResponseWriter, r *http.Request) {
	f.puts.Add(1)
	base := filepath.Join(f.root, r.URL.Query().Get("path"))
	body := bufio.NewReader(r.Body)
	res := PutResult{}
	for {
		line, err := body.ReadBytes('\n')
		if err != nil {
			break
		}
		var it Item
		json.Unmarshal(line, &it)
		dst := filepath.Join(base, filepath.FromSlash(it.Path))
		if it.Dir {
			os.MkdirAll(dst, 0o755)
			res.Written++
			continue
		}
		os.MkdirAll(filepath.Dir(dst), 0o755)
		out, _ := os.Create(dst)
		if _, err := io.CopyN(out, body, it.Size); err != nil {
			out.Close()
			res.Error = err.Error()
			break
		}
		out.Close()
		mt := time.Unix(0, it.MTime)
		os.Chtimes(dst, mt, mt)
		res.Written++
	}
	json.NewEncoder(w).Encode(res)
}

func (f *fakeServer) delete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths []string `json:"paths"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	for _, p := range req.Paths {
		dst := filepath.Join(f.trash, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := os.Rename(filepath.Join(f.root, filepath.FromSlash(p)), dst); err != nil {
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error":%q}`, err.Error())
			return
		}
	}
	fmt.Fprint(w, `{"trashed":1}`)
}

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if strings.HasSuffix(rel, "/") {
			os.MkdirAll(p, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		b, _ := os.ReadFile(p)
		out[rel] = string(b)
		return nil
	})
	return out
}

func (f *fakeServer) backup(t *testing.T, src string, mode engine.Mode, excludes ...string) (*engine.Plan, *engine.Result) {
	t.Helper()
	target := NewTarget(Location{Addr: f.addr(), Root: 1, Path: "备份/电脑"}, "tok")
	p, err := engine.Prepare(context.Background(), engine.Options{Source: src, Mode: mode, Excludes: excludes, Dest: target}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := engine.Execute(context.Background(), p, engine.Hooks{})
	if r.Fatal != nil || len(r.Errors) > 0 {
		t.Fatalf("fatal %v, errors %v", r.Fatal, r.Errors)
	}
	return p, r
}

func TestLocation(t *testing.T) {
	for _, s := range []string{"fileindex://192.168.0.235:8090/3", "fileindex://nas:8090/12/备份/笔记本 1"} {
		l, err := Parse(s)
		if err != nil || l.String() != s {
			t.Errorf("%s: %+v, %v", s, l, err)
		}
	}
	if l, _ := Parse("fileindex://h:1/3/a/b/"); l.Root != 3 || l.Path != "a/b" {
		t.Errorf("%+v", l)
	}
	for _, s := range []string{"smb://h/x", "fileindex://h:1/x/y", "fileindex:///3"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("parsed %s", s)
		}
	}
}

func TestBackupToServer(t *testing.T) {
	f := newFakeServer(t)
	src := t.TempDir()
	write(t, src, map[string]string{"a.txt": "a", "照片/1.jpg": "jpeg", "empty/": ""})
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(filepath.Join(src, "a.txt"), old, old)

	p, r := f.backup(t, src, engine.Incremental)
	if !p.TargetMissing || r.Created != 2 || r.Mkdirs != 2 {
		t.Errorf("missing %v, result %+v", p.TargetMissing, r)
	}
	dst := filepath.Join(f.root, "备份", "电脑")
	got := tree(t, dst)
	if got["a.txt"] != "a" || got["照片/1.jpg"] != "jpeg" || got["empty/"] != "" || len(got) != 4 {
		t.Fatalf("server has %v", got)
	}
	if info, _ := os.Stat(filepath.Join(dst, "a.txt")); !info.ModTime().Equal(old) {
		t.Errorf("mod time %v", info.ModTime())
	}

	// Nothing changed: nothing to do.
	p, _ = f.backup(t, src, engine.Incremental)
	if p.Summary.Changes() != 0 {
		t.Errorf("second backup plans %+v", p.Summary)
	}
}

func TestMirrorTrashesOnServer(t *testing.T) {
	f := newFakeServer(t)
	src := t.TempDir()
	write(t, src, map[string]string{"keep.txt": "k"})
	dst := filepath.Join(f.root, "备份", "电脑")
	write(t, dst, map[string]string{
		"keep.txt": "k", "gone.txt": "g", "old/a": "a", "old/deep/b": "b",
		"mixed/x": "x", "mixed/cache.tmp": "excluded, stays",
	})
	p, r := f.backup(t, src, engine.Mirror, "*.tmp")
	if r.Deleted != 4 || r.Rmdirs != 2 || r.Kept != 1 {
		t.Errorf("plan %+v, result %+v", p.Summary, r)
	}
	got := tree(t, dst)
	if len(got) != 3 || got["mixed/cache.tmp"] == "" || got["keep.txt"] != "k" {
		t.Errorf("server has %v", got)
	}
	// Whole directories go to the trash in one piece.
	trashed := tree(t, f.trash)
	if trashed["备份/电脑/old/deep/b"] != "b" || trashed["备份/电脑/gone.txt"] != "g" || trashed["备份/电脑/mixed/x"] != "x" {
		t.Errorf("trash has %v", trashed)
	}
}

func TestManySmallAndLargeFiles(t *testing.T) {
	f := newFakeServer(t)
	src := t.TempDir()
	files := map[string]string{"big.bin": strings.Repeat("x", batchBytes+10)}
	for i := range 2500 {
		files[fmt.Sprintf("d%d/f%04d.txt", i%7, i)] = fmt.Sprint(i)
	}
	write(t, src, files)
	var last engine.Progress
	target := NewTarget(Location{Addr: f.addr(), Root: 1, Path: "b"}, "tok")
	p, err := engine.Prepare(context.Background(), engine.Options{Source: src, Dest: target}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := engine.Execute(context.Background(), p, engine.Hooks{Progress: func(pr engine.Progress) { last = pr }})
	if len(r.Errors) > 0 || r.Created != 2501 {
		t.Fatalf("result %+v", r)
	}
	if last.Done != last.Total || last.DoneBytes != last.TotalBytes {
		t.Errorf("progress %+v", last)
	}
	// 2500 small files in batches of 1000, the big one alone.
	if n := f.puts.Load(); n != 4 {
		t.Errorf("%d requests", n)
	}
	if got := tree(t, filepath.Join(f.root, "b")); len(got) != 2501+7 || got["d3/f0010.txt"] != "10" {
		t.Errorf("server has %d entries", len(got))
	}
}

func TestExpiredSession(t *testing.T) {
	f := newFakeServer(t)
	src := t.TempDir()
	write(t, src, map[string]string{"a": "a"})
	target := NewTarget(Location{Addr: f.addr(), Root: 1, Path: "b"}, "tok")
	p, err := engine.Prepare(context.Background(), engine.Options{Source: src, Dest: target}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.expired.Store(true)
	r := engine.Execute(context.Background(), p, engine.Hooks{})
	if r.Fatal == nil {
		t.Fatalf("result %+v", r)
	}

	// A scan says so too, for the app to ask for the password.
	if _, err := engine.Prepare(context.Background(), engine.Options{Source: src, Dest: target}, nil); err == nil || !strings.Contains(err.Error(), ErrAuth.Error()) {
		t.Errorf("scan with an expired session: %v", err)
	}
	c := New(f.addr(), "")
	if err := c.Login(context.Background(), "wrong"); err == nil {
		t.Error("logged in with a wrong password")
	}
	if err := c.Login(context.Background(), "pw"); err != nil || c.Token != "tok" {
		t.Errorf("login: %v, token %q", err, c.Token)
	}
}

func TestBatches(t *testing.T) {
	dirs := []engine.Action{{Kind: engine.ActMkdir, Path: "d"}}
	var copies []engine.Action
	for i := range 1500 {
		copies = append(copies, engine.Action{Kind: engine.ActCreate, Path: fmt.Sprint(i), Size: 10})
	}
	copies = append(copies, engine.Action{Kind: engine.ActCreate, Path: "huge", Size: 1 << 30}, engine.Action{Kind: engine.ActCreate, Path: "big", Size: batchBytes})
	b := makeBatches(dirs, copies)
	if len(b) != 4 || b[0][0].Path != "huge" || b[1][0].Path != "big" || b[2][0].Path != "d" || len(b[2]) != batchFiles || len(b[3]) != 501 {
		t.Errorf("%d batches", len(b))
	}
}
