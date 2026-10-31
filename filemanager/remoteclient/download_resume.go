package fileindex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// downloadCheckpoint is written before file bytes. It contains no credentials.
// Only a strong ETag permits appending; older servers can still download, but
// restart from zero when retried rather than risk joining different versions.
type downloadCheckpoint struct {
	Source   string `json:"source"`
	ETag     string `json:"etag"`
	Modified string `json:"modified"`
	Size     int64  `json:"size"`
}

// RemoveDownloadPartial removes only our sidecars next to this destination.
func RemoveDownloadPartial(dest, partial string) {
	if validDownloadPartial(dest, partial) {
		os.Remove(partial)
		os.Remove(partial + ".json")
	}
}

func validDownloadPartial(dest, partial string) bool {
	return filepath.IsAbs(dest) && filepath.IsAbs(partial) &&
		filepath.Dir(dest) == filepath.Dir(partial) && dest != partial &&
		strings.HasPrefix(filepath.Base(partial), ".filesync-download-") && strings.HasSuffix(partial, ".part")
}

// DownloadFileResumable preserves partial on interruption. A successful full
// response replaces stale partial bytes; a partial response must match both
// the saved version and the exact range requested. dest is replaced only once
// the complete file has been received and synced.
func (c *Client) DownloadFileResumable(ctx context.Context, root int64, p, dest, partial string, progress func(Progress)) error {
	if c.InArchive() || !validDownloadPartial(dest, partial) {
		return errors.New("续传下载的路径无效")
	}
	if c.BeforeLocalWrite != nil {
		for _, target := range []string{dest, partial, partial + ".json"} {
			if err := c.BeforeLocalWrite(target); err != nil {
				return err
			}
		}
	}
	q := url.Values{"path": {p}}
	endpoint := fmt.Sprintf("/api/download/%d", root)
	source := c.url(endpoint, q)
	var saved downloadCheckpoint
	if b, err := os.ReadFile(partial + ".json"); err == nil {
		_ = json.Unmarshal(b, &saved)
	}
	var offset int64
	if info, err := os.Lstat(partial); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("下载临时文件不是普通文件")
		}
		if saved.Source == source && strongETag(saved.ETag) && info.Size() > 0 && info.Size() < saved.Size {
			offset = info.Size()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	headers := http.Header{}
	if offset > 0 {
		headers.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		headers.Set("If-Range", saved.ETag)
	}
	res, err := c.sendHeaders(ctx, "GET", endpoint, q, nil, headers)
	// A source that shrank (or a server rejecting ranges) starts over safely.
	var status *StatusError
	if offset > 0 && errors.As(err, &status) && status.Code == http.StatusRequestedRangeNotSatisfiable {
		offset = 0
		res, err = c.send(ctx, "GET", endpoint, q, nil)
	}
	if err != nil {
		return err
	}
	defer res.Body.Close()
	next := downloadCheckpoint{Source: source, ETag: res.Header.Get("ETag"), Modified: res.Header.Get("Last-Modified"), Size: res.ContentLength}
	switch res.StatusCode {
	case http.StatusOK:
		offset = 0
	case http.StatusPartialContent:
		var start, end, total int64
		rangeHeader := res.Header.Get("Content-Range")
		n, scanErr := fmt.Sscanf(rangeHeader, "bytes %d-%d/%d", &start, &end, &total)
		if offset == 0 || next.ETag != saved.ETag || scanErr != nil || n != 3 ||
			rangeHeader != fmt.Sprintf("bytes %d-%d/%d", start, end, total) ||
			start != offset || total != saved.Size || end != total-1 ||
			res.ContentLength != total-start {
			return errors.New("NAS 返回的续传版本或范围不匹配，请重试")
		}
		next.Size = total
	default:
		return errors.New("NAS 返回了无效的下载响应")
	}
	if next.Size < 0 || res.Header.Get("Content-Encoding") != "" {
		return errors.New("NAS 未提供可校验的文件长度")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	actual, err := os.Lstat(partial)
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) || offset > 0 && info.Size() != offset {
		return errors.New("下载临时文件已改变，请重试")
	}
	if offset == 0 {
		if err := f.Truncate(0); err != nil {
			return err
		}
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	// Atomic checkpoint replacement ensures a crash never associates old
	// bytes with a new version. Truncation above always precedes this write.
	if err := saveDownloadCheckpoint(partial+".json", next); err != nil {
		return err
	}
	pr := Progress{Done: offset, Total: next.Size, Current: p}
	if progress != nil {
		progress(pr)
	}
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := res.Body.Read(buf)
		if int64(n) > next.Size-pr.Done {
			return errors.New("下载数据超过文件长度")
		}
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			pr.Done += int64(n)
			if progress != nil {
				progress(pr)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return readErr
		}
	}
	if pr.Done != next.Size {
		return io.ErrUnexpectedEOF
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if mt, err := http.ParseTime(next.Modified); err == nil {
		if err := os.Chtimes(partial, mt, mt); err != nil {
			return err
		}
	}
	if c.BeforeLocalWrite != nil {
		if err := c.BeforeLocalWrite(dest); err != nil {
			return err
		}
	}
	if err := os.Rename(partial, dest); err != nil {
		return err
	}
	os.Remove(partial + ".json")
	return nil
}

func strongETag(s string) bool {
	return len(s) >= 2 && strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`)
}

func saveDownloadCheckpoint(filename string, state downloadCheckpoint) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(filename), ".filesync-checkpoint-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), filename)
}
