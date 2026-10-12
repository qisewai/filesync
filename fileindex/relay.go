package fileindex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// Relay copies files and directories of a root of src into the directory
// dir of a root of dst, each under its name: the tar stream src exports
// goes on to dst as it comes, through this computer. What dir has of the
// same names is replaced, directories merged.
//
// It returns the top-level names dst wrote whole, when the stream ended
// well, and the entries dst could not write.
func Relay(ctx context.Context, src *Client, root int64, paths []string, dst *Client, toRoot int64, dir string, onProgress func(Progress)) (whole map[string]bool, errs []error, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	q := url.Values{"root": {strconv.FormatInt(root, 10)}, "path": paths}
	res, err := src.send(ctx, "GET", "/api/backup/export", q, nil)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	var mu sync.Mutex
	pr := Progress{}
	pr.Total, _ = strconv.ParseInt(res.Header.Get("X-Total-Size"), 10, 64)
	report := func(update func(*Progress)) {
		mu.Lock()
		update(&pr)
		p := pr
		mu.Unlock()
		if onProgress != nil {
			onProgress(p)
		}
	}
	body := &exported{res: res, onBytes: func(n int64) { report(func(p *Progress) { p.Done += n }) }}
	answers, err := dst.Tar(ctx, toRoot, dir, body)
	if err != nil {
		return nil, nil, err
	}
	defer answers.Close()
	failed := map[string]bool{}
	seen := map[string]bool{}
	err = readAnswers(answers, func(a Answer) {
		top, _, _ := strings.Cut(a.Path, "/")
		seen[top] = true
		if a.Error != "" {
			failed[top] = true
			errs = append(errs, fmt.Errorf("%s: %s", a.Path, a.Error))
			return
		}
		report(func(p *Progress) { p.Current = a.Path })
	})
	// What broke on the side of src says more than dst stopping for it.
	if serr := body.failure(); serr != nil {
		err = serr
	}
	if ctx.Err() != nil {
		return nil, errs, ctx.Err()
	}
	if err != nil {
		return nil, errs, err
	}
	whole = map[string]bool{}
	for top := range seen {
		if !failed[top] {
			whole[top] = true
		}
	}
	return whole, errs, nil
}

// readAnswers reads the answers to Tar, giving each entry to fn, until
// the end.
func readAnswers(r io.Reader, fn func(Answer)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var a Answer
		if err := json.Unmarshal(sc.Bytes(), &a); err != nil {
			return fmt.Errorf("NAS 的回答无法解析: %w", err)
		}
		if a.End {
			if a.Fatal != "" {
				return errors.New("NAS: " + a.Fatal)
			}
			return nil
		}
		fn(a)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("NAS 的回答意外结束")
}

// exported passes on an export, holding back its last blocks until it is
// known complete: an export cut short ends the stream with an error, so
// the receiver cannot take it as whole.
type exported struct {
	res     *http.Response
	onBytes func(int64)
	buf     []byte
	held    []byte // the last bytes read, up to a tar end's worth
	ended   bool   // the export was read to its end, and held is all left

	mu  sync.Mutex
	err error
}

const tarEnd = 1024 // two zero blocks end a tar archive

func (e *exported) Read(p []byte) (int, error) {
	for !e.ended && len(e.held) <= tarEnd {
		if e.buf == nil {
			e.buf = make([]byte, 64<<10)
		}
		n, err := e.res.Body.Read(e.buf)
		e.held = append(e.held, e.buf[:n]...)
		if err == io.EOF {
			if err := e.check(); err != nil {
				return 0, e.fail(err)
			}
			e.ended = true
		} else if err != nil {
			return 0, e.fail(fmt.Errorf("读取源服务器失败: %w", err))
		}
	}
	keep := tarEnd
	if e.ended {
		keep = 0
	}
	n := copy(p, e.held[:len(e.held)-keep])
	if n == 0 && e.ended {
		return 0, io.EOF
	}
	e.held = e.held[n:]
	if e.onBytes != nil {
		e.onBytes(int64(n))
	}
	return n, nil
}

// check says whether the export read whole is complete: it ends with the
// end blocks, and the server says so when it can.
func (e *exported) check() error {
	if len(e.held) < tarEnd {
		return errors.New("源服务器的导出不完整")
	}
	for _, b := range e.held[len(e.held)-tarEnd:] {
		if b != 0 {
			return errors.New("源服务器的导出不完整")
		}
	}
	if complete, announced := e.res.Trailer["X-Export-Complete"]; announced && (len(complete) != 1 || complete[0] != "1") {
		return errors.New("源服务器未完成导出")
	}
	return nil
}

func (e *exported) fail(err error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.err = err
	return err
}

func (e *exported) failure() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}
