package fileindex

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"filemanager/engine"
)

// Upload sends files and directories of this computer into the directory
// loc, each under its name, as one tar stream, reporting to h. What loc
// has of the same names is replaced, directories merged.
func (c *Client) Upload(ctx context.Context, loc Location, paths []string, h engine.Hooks) *engine.Result {
	var items []engine.Action
	local := map[string]string{} // item path → file of this computer
	var bytes int64
	var skipped []engine.FileError
	add := func(rel, abs string, info fs.FileInfo) {
		a := engine.Action{Path: rel, ModTime: info.ModTime(), Mode: info.Mode().Perm()}
		switch m := info.Mode(); {
		case m.IsDir():
			a.Kind = engine.ActMkdir
		case m&fs.ModeSymlink != 0:
			link, err := os.Readlink(abs)
			if err != nil {
				skipped = append(skipped, engine.FileError{Action: a, Err: err})
				return
			}
			a.Kind, a.Link = engine.ActCreate, link
		case m.IsRegular():
			a.Kind, a.Size = engine.ActCreate, info.Size()
			bytes += a.Size
		default:
			skipped = append(skipped, engine.FileError{Action: engine.Action{Kind: engine.ActSkip, Path: rel}, Err: errors.New("特殊文件，已跳过")})
			return
		}
		items = append(items, a)
		local[rel] = abs
	}
	names := map[string]bool{}
	for _, p := range paths {
		p = filepath.Clean(p)
		base := filepath.Base(p)
		if names[base] {
			return &engine.Result{Fatal: fmt.Errorf("上传项有重名：%s，请分别上传到不同目录", base)}
		}
		names[base] = true
		err := filepath.WalkDir(p, func(abs string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			rel, _ := filepath.Rel(p, abs)
			rel = path.Join(base, filepath.ToSlash(rel))
			if err != nil {
				skipped = append(skipped, engine.FileError{Action: engine.Action{Path: rel}, Err: err})
				return nil
			}
			info, err := d.Info()
			if err != nil {
				skipped = append(skipped, engine.FileError{Action: engine.Action{Path: rel}, Err: err})
				return nil
			}
			add(rel, abs, info)
			return nil
		})
		if err != nil {
			return &engine.Result{Fatal: err, Canceled: ctx.Err() != nil, Errors: skipped}
		}
	}
	run := engine.NewRun(len(items), bytes, h)
	c.sendTree(ctx, loc, items, func(a engine.Action) string { return local[a.Path] }, run)
	res := run.Finish(ctx)
	res.Errors = append(res.Errors, skipped...)
	return res
}

// Progress is how far a download is.
type Progress struct {
	Done, Total int64 // bytes; Total is an estimate, 0 when unknown
	Current     string
}

// DownloadFile downloads the file p of a root as the file dest of this
// computer, through a temporary file beside it, keeping its time.
func (c *Client) DownloadFile(ctx context.Context, root int64, p, dest string, onProgress func(Progress)) error {
	res, err := c.send(ctx, "GET", fmt.Sprintf("/api/download/%d", root), url.Values{"path": {p}}, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	pr := Progress{Total: res.ContentLength, Current: p}
	if pr.Total < 0 {
		pr.Total = 0
	}
	mtime, _ := http.ParseTime(res.Header.Get("Last-Modified"))
	if c.BeforeLocalWrite != nil {
		if err := c.BeforeLocalWrite(dest); err != nil {
			return err
		}
	}
	return writeFile(dest, res.Body, 0o644, mtime, func(n int64) {
		pr.Done += n
		if onProgress != nil {
			onProgress(pr)
		}
	})
}

// writeFile writes r to dest through a temporary file renamed over it.
func writeFile(dest string, r io.Reader, mode os.FileMode, mtime time.Time, onBytes func(int64)) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".filesync-*.tmp")
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	buf := make([]byte, 1<<20)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				return werr
			}
			if onBytes != nil {
				onBytes(int64(n))
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	done = true
	if runtime.GOOS != "windows" {
		os.Chmod(tmp.Name(), mode)
	}
	if !mtime.IsZero() {
		os.Chtimes(tmp.Name(), mtime, mtime)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Download downloads files and directories of a root into the directory
// dest of this computer, each under its name, as one tar stream. What dest
// has of the same names is replaced, directories merged. It returns the
// entries it could not write.
func (c *Client) Download(ctx context.Context, root int64, paths []string, dest string, onProgress func(Progress)) ([]error, error) {
	q := url.Values{"root": {strconv.FormatInt(root, 10)}, "path": paths}
	res, err := c.send(ctx, "GET", "/api/backup/export", q, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	pr := Progress{}
	pr.Total, _ = strconv.ParseInt(res.Header.Get("X-Total-Size"), 10, 64)
	x := &extractor{dest: dest, guard: c.BeforeLocalWrite}
	if x.guard != nil {
		if err := x.guard(dest); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	if x.dest, err = filepath.Abs(dest); err != nil {
		return nil, err
	}
	body := &archiveTail{r: res.Body}
	err = x.extract(tar.NewReader(body), func(n int64, current string) {
		pr.Done += n
		pr.Current = current
		if onProgress != nil {
			onProgress(pr)
		}
	})
	if err == nil {
		_, err = io.Copy(io.Discard, body)
		if err == nil && !body.complete() {
			err = errors.New("下载中断：目录归档不完整")
		}
		if err == nil {
			if complete, announced := res.Trailer["X-Export-Complete"]; announced && (len(complete) != 1 || complete[0] != "1") {
				err = errors.New("下载中断：服务器未完成目录导出")
			}
		}
	}
	if ctx.Err() != nil {
		return x.errs, ctx.Err()
	}
	return x.errs, err
}

// tar.Reader accepts EOF between entries, even without the archive's end
// blocks. Keep the tail so a broken export cannot be reported as success.
type archiveTail struct {
	r    io.Reader
	tail [1024]byte
	n    int64
}

func (r *archiveTail) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n >= len(r.tail) {
		copy(r.tail[:], p[n-len(r.tail):n])
	} else if n > 0 {
		copy(r.tail[:], r.tail[n:])
		copy(r.tail[len(r.tail)-n:], p[:n])
	}
	r.n += int64(n)
	return n, err
}
func (r *archiveTail) complete() bool {
	if r.n < int64(len(r.tail)) {
		return false
	}
	for _, b := range r.tail {
		if b != 0 {
			return false
		}
	}
	return true
}

// extractor unpacks a tar stream into a directory, never out of it.
type extractor struct {
	guard    func(string) error
	dest     string
	errs     []error
	dirTimes []dirTime
}

type dirTime struct {
	path string
	t    time.Time
}

// target returns where an entry goes, refusing names that would lead out
// of dest, by "..", or through a symbolic link already there.
func (x *extractor) target(name string) (string, error) {
	name = strings.ReplaceAll(name, `\`, "/")
	if path.IsAbs(name) || strings.Contains(name, "\x00") {
		return "", fmt.Errorf("名称无效：%s", name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("名称无效：%s", name)
	}
	rel := clean
	if runtime.GOOS == "windows" {
		for _, part := range strings.Split(rel, "/") {
			if strings.ContainsAny(part, `<>:"|?*`) {
				return "", fmt.Errorf("%s：名称含有 Windows 不允许的字符", name)
			}
		}
	}
	// Each directory on the way must be a directory, not a link.
	cur := x.dest
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		if !fi.IsDir() {
			return "", fmt.Errorf("%s：路径中的 %s 不是文件夹", name, part)
		}
	}
	return filepath.Join(x.dest, filepath.FromSlash(rel)), nil
}

func (x *extractor) extract(tr *tar.Reader, onBytes func(int64, string)) error {
	defer func() {
		for i := len(x.dirTimes) - 1; i >= 0; i-- {
			os.Chtimes(x.dirTimes[i].path, x.dirTimes[i].t, x.dirTimes[i].t)
		}
	}()
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("下载中断: %w", err)
		}
		dst, err := x.target(h.Name)
		if err != nil {
			x.errs = append(x.errs, err)
			continue
		}
		if x.guard != nil {
			if err := x.guard(dst); err != nil {
				x.errs = append(x.errs, err)
				continue
			}
		}
		name := strings.TrimSuffix(h.Name, "/")
		switch h.Typeflag {
		case tar.TypeDir:
			if fi, err := os.Lstat(dst); err == nil && !fi.IsDir() {
				x.errs = append(x.errs, fmt.Errorf("%s: 本机同名项不是文件夹", name))
				continue
			}
			if err := os.MkdirAll(dst, 0o755); err != nil {
				x.errs = append(x.errs, fmt.Errorf("%s: %w", name, err))
				continue
			}
			if !h.ModTime.IsZero() {
				x.dirTimes = append(x.dirTimes, dirTime{dst, h.ModTime})
			}
		case tar.TypeSymlink:
			if err := writeSymlink(dst, h.Linkname); err != nil {
				x.errs = append(x.errs, fmt.Errorf("%s: 无法创建符号链接: %w", name, err))
			}
		case tar.TypeReg:
			if fi, err := os.Lstat(dst); err == nil && fi.IsDir() {
				x.errs = append(x.errs, fmt.Errorf("%s: 本机已有同名文件夹", name))
				continue
			}
			err := writeFile(dst, tr, os.FileMode(h.Mode).Perm()|0o600, h.ModTime, func(n int64) { onBytes(n, name) })
			if err != nil {
				if errors.Is(err, io.ErrUnexpectedEOF) {
					return fmt.Errorf("下载中断: %w", err)
				}
				x.errs = append(x.errs, fmt.Errorf("%s: %w", name, err))
			}
		}
	}
}

func writeSymlink(dst, link string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".filesync-link-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	tmp.Close()
	os.Remove(name)
	defer os.Remove(name)
	if err := os.Symlink(link, name); err != nil {
		return err
	}
	return os.Rename(name, dst)
}
