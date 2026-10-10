package fileindex

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// ReadPreview reads at most limit bytes, requesting only the needed range.
// A server ignoring Range still cannot make the preview consume unbounded memory.
func (c *Client) ReadPreview(ctx context.Context, root int64, path string, limit int64) ([]byte, bool, error) {
	if limit <= 0 {
		return nil, false, fmt.Errorf("预览长度必须大于零")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.url(fmt.Sprintf("/api/download/%d", root), url.Values{"path": {path}}), nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", limit))
	req.AddCookie(&http.Cookie{Name: cookieName, Value: c.Token})
	res, err := c.hc.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer res.Body.Close()
	if res.StatusCode == 401 {
		return nil, false, ErrAuth
	}
	// ServeContent returns 416 for an empty file with a Range request.
	if res.StatusCode == 416 && res.Header.Get("Content-Range") == "bytes */0" {
		return []byte{}, false, nil
	}
	if res.StatusCode/100 != 2 {
		return nil, false, &StatusError{Code: res.StatusCode, Msg: res.Status}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, false, err
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	return data, truncated, nil
}
