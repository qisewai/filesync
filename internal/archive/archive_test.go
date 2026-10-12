package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"golang.org/x/text/encoding/simplifiedchinese"
)

func zipFile(t *testing.T, headers []zip.FileHeader, contents []string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sample.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for i, h := range headers {
		w, err := z.CreateHeader(&h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(contents[i]))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}
func load(t *testing.T, p string) *Index {
	t.Helper()
	idx, err := new(Cache).Load(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}
func TestZipDirectoriesGB18030AndLinks(t *testing.T) {
	raw, _ := simplifiedchinese.GB18030.NewEncoder().String("照片/中文.txt")
	h := zip.FileHeader{Name: raw, NonUTF8: true, Method: zip.Deflate}
	link := zip.FileHeader{Name: "link"}
	link.SetMode(fs.ModeSymlink | 0777)
	p := zipFile(t, []zip.FileHeader{h, link, {Name: "empty/"}}, []string{"你好", "../../outside", ""})
	idx := load(t, p)
	es, err := idx.List("")
	if err != nil || len(es) != 3 {
		t.Fatalf("root %v %v", es, err)
	}
	es, err = idx.List("照片")
	if err != nil || len(es) != 1 || es[0].Name != "中文.txt" {
		t.Fatalf("GB18030 %v %v", es, err)
	}
	var b bytes.Buffer
	if err := idx.Copy(context.Background(), p, es[0].Path, &b, 0); err != nil || b.String() != "你好" {
		t.Fatalf("copy %q %v", b.String(), err)
	}
	if _, err := idx.File("link"); err == nil {
		t.Fatal("followed link")
	}
	if _, _, err := idx.Selection([]string{"link"}); err == nil {
		t.Fatal("exported link")
	}
}
func TestUnsafeAndDuplicateNames(t *testing.T) {
	for _, name := range []string{"../escape", "a/../../escape", "/absolute", `C:\escape`, `a\..\escape`} {
		t.Run(name, func(t *testing.T) {
			p := zipFile(t, []zip.FileHeader{{Name: name}}, []string{"x"})
			if _, err := new(Cache).Load(context.Background(), p); err == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
	for _, names := range [][]string{{"a", "./a"}, {"a", "a/b"}, {"a/b", "a"}} {
		p := zipFile(t, []zip.FileHeader{{Name: names[0]}, {Name: names[1]}}, []string{"a", "b"})
		if _, err := new(Cache).Load(context.Background(), p); err == nil {
			t.Fatalf("collision accepted %v", names)
		}
	}
}
func tarBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, e := range []struct{ name, body string }{{"a.txt", "first"}, {"dir/b.txt", "second"}} {
		w.WriteHeader(&tar.Header{Name: e.name, Size: int64(len(e.body)), Mode: 0644, ModTime: time.Unix(1700000000, 0)})
		w.Write([]byte(e.body))
	}
	w.WriteHeader(&tar.Header{Name: "dir/link", Typeflag: tar.TypeSymlink, Linkname: "../../outside", Mode: 0777})
	w.Close()
	return b.Bytes()
}
func TestTarAndSingleStreams(t *testing.T) {
	raw := tarBytes(t)
	formats := []string{".tar", ".tar.gz", ".tgz", ".tar.xz", ".tar.zst", ".gz", ".xz", ".zst"}
	for _, ext := range formats {
		t.Run(ext, func(t *testing.T) {
			data := raw
			single := ext == ".gz" || ext == ".xz" || ext == ".zst"
			if single {
				data = []byte("hello stream")
			}
			var b bytes.Buffer
			switch {
			case strings.HasSuffix(ext, "gz") || ext == ".tgz":
				w := gzip.NewWriter(&b)
				w.Write(data)
				w.Close()
			case strings.HasSuffix(ext, "xz"):
				w, _ := xz.NewWriter(&b)
				w.Write(data)
				w.Close()
			case strings.HasSuffix(ext, "zst"):
				w, _ := zstd.NewWriter(&b)
				w.Write(data)
				w.Close()
			default:
				b.Write(data)
			}
			p := filepath.Join(t.TempDir(), "sample"+ext)
			os.WriteFile(p, b.Bytes(), 0600)
			idx := load(t, p)
			entry := "dir/b.txt"
			want := "second"
			selection := []string{"dir", "dir/b.txt"}
			if single {
				entry = "sample"
				want = "hello stream"
				selection = []string{entry}
			}
			var out bytes.Buffer
			if err := idx.Copy(context.Background(), p, entry, &out, 0); err != nil || out.String() != want {
				t.Fatalf("read %q %v", out.String(), err)
			}
			out.Reset()
			if err := idx.Copy(context.Background(), p, entry, &out, 3); err != nil || out.String() != want[:3] {
				t.Fatalf("prefix %q %v", out.String(), err)
			}
			chosen, _, err := idx.Selection(selection)
			if err != nil {
				t.Fatal(err)
			}
			out.Reset()
			if err := idx.Export(context.Background(), p, chosen, &out); err != nil {
				t.Fatal(err)
			}
			tr := tar.NewReader(&out)
			found := false
			for {
				h, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if h.Typeflag == tar.TypeSymlink {
					t.Fatal("exported symlink")
				}
				if h.Size > 0 {
					content, _ := io.ReadAll(tr)
					if string(content) != want {
						t.Fatalf("content %q", content)
					}
					found = true
				}
			}
			if !found {
				t.Fatal("empty export")
			}
		})
	}
}
func TestFixtures(t *testing.T) {
	for _, name := range []string{"sample.7z", "sample.tar.bz2", "hello.txt.bz2", "test_read_format_rar.rar", "test_read_format_rar5_stored.rar"} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join("testdata", name)
			idx := load(t, p)
			read := 0
			for _, e := range idx.Entries {
				if e.IsDir || e.Link {
					continue
				}
				var b bytes.Buffer
				if err := idx.Copy(context.Background(), p, e.Path, &b, 0); err != nil {
					t.Fatalf("%s: %v", e.Path, err)
				}
				if int64(b.Len()) != e.Size {
					t.Fatalf("%s size %d != %d", e.Path, b.Len(), e.Size)
				}
				read++
			}
			if read == 0 {
				t.Fatal("no regular members")
			}
		})
	}
}
func TestEncryption(t *testing.T) {
	if _, err := new(Cache).Load(context.Background(), filepath.Join("testdata", "headers-encrypted.7z")); !errors.Is(err, ErrEncrypted) {
		t.Fatalf("header encryption %v", err)
	}
	idx := load(t, filepath.Join("testdata", "encrypted.7z"))
	count := 0
	for p, e := range idx.Entries {
		if !e.IsDir {
			count++
			if !e.Encrypted {
				t.Fatalf("%s not marked encrypted", p)
			}
			if _, err := idx.File(p); !errors.Is(err, ErrEncrypted) {
				t.Fatalf("%v", err)
			}
		}
	}
	if count == 0 {
		t.Fatal("empty")
	}
}
func TestCacheCancellationVersionAndLimits(t *testing.T) {
	p := zipFile(t, []zip.FileHeader{{Name: "a.txt"}}, []string{"first"})
	c := new(Cache)
	idx, err := c.Load(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	next, _ := c.Load(context.Background(), p)
	if idx != next {
		t.Fatal("cache miss")
	}
	os.Chtimes(p, time.Now(), time.Now().Add(time.Hour))
	next, err = c.Load(context.Background(), p)
	if err != nil || idx == next {
		t.Fatalf("stale cache %v", err)
	}
	if err := idx.Copy(context.Background(), p, "a.txt", io.Discard, 0); err == nil {
		t.Fatal("stale file opened")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := new(Cache).Load(ctx, p); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
	bad := &Index{Entries: map[string]Entry{"bomb": {Path: "bomb", Size: MaxFile + 1}}}
	if _, err := bad.File("bomb"); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := c.Load(context.Background(), p); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}
func TestCorruptZipDoesNotReleaseTail(t *testing.T) {
	p := zipFile(t, []zip.FileHeader{{Name: "a.txt", Method: zip.Store}}, []string{"hello unique payload"})
	data, _ := os.ReadFile(p)
	i := bytes.Index(data, []byte("hello unique payload"))
	if i < 0 {
		t.Fatal("payload")
	}
	data[i] = 'X'
	os.WriteFile(p, data, 0600)
	idx := load(t, p)
	var b bytes.Buffer
	if err := idx.Copy(context.Background(), p, "a.txt", &b, 0); err == nil {
		t.Fatal("corrupt checksum accepted")
	}
	if b.Len() != 0 {
		t.Fatalf("released unverified tail %d", b.Len())
	}
}

func TestDeclaredSizeBombAndEncryptedZip(t *testing.T) {
	for _, size := range []uint64{128 << 20, uint64(MaxScan) + 1} {
		p := filepath.Join(t.TempDir(), "bomb.zip")
		f, _ := os.Create(p)
		z := zip.NewWriter(f)
		_, err := z.CreateRaw(&zip.FileHeader{Name: "bomb", Method: zip.Store, UncompressedSize64: size})
		if err != nil {
			t.Fatal(err)
		}
		z.Close()
		f.Close()
		if _, err := new(Cache).Load(context.Background(), p); !errors.Is(err, ErrLimit) {
			t.Fatalf("size %d: %v", size, err)
		}
	}
	p := filepath.Join(t.TempDir(), "encrypted.zip")
	f, _ := os.Create(p)
	z := zip.NewWriter(f)
	_, err := z.CreateRaw(&zip.FileHeader{Name: "secret.txt", Flags: 1, Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	z.Close()
	f.Close()
	idx := load(t, p)
	if !idx.Entries["secret.txt"].Encrypted {
		t.Fatal("encryption hidden")
	}
	if _, err := idx.File("secret.txt"); !errors.Is(err, ErrEncrypted) {
		t.Fatal(err)
	}
}
func TestLRUEvictionAndWaitingCancellation(t *testing.T) {
	c := new(Cache)
	for range 20 {
		p := zipFile(t, []zip.FileHeader{{Name: "a"}}, []string{"a"})
		if _, err := c.Load(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	if c.lru.Len() > 16 || c.used > cacheBytes {
		t.Fatalf("cache %d %d", c.lru.Len(), c.used)
	}
	p := zipFile(t, []zip.FileHeader{{Name: "a"}}, []string{"a"})
	info, _ := os.Stat(p)
	key := fmt.Sprintf("%s\x00%d\x00%d", p, info.Size(), info.ModTime().UnixNano())
	c.pending[key] = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.Load(ctx, p); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait cancel %v", err)
	}
}
func TestCompressedTarFooterIsChecked(t *testing.T) {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write(tarBytes(t))
	w.Close()
	data := b.Bytes()
	data[len(data)-8] ^= 0xff
	p := filepath.Join(t.TempDir(), "bad.tar.gz")
	os.WriteFile(p, data, 0600)
	if _, err := new(Cache).Load(context.Background(), p); err == nil {
		t.Fatal("corrupt compressed tar footer accepted")
	}
}
