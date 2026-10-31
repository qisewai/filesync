package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"filemanager/api"
	"filemanager/index"
)

type Index struct {
	client *Client
	id     string
	mu     sync.Mutex
	status index.Status
}

func (c *Client) Index() *index.Index {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return index.NewRemote(&Index{client: c, id: hex.EncodeToString(id[:])})
}
func (x *Index) command(ctx context.Context, q api.IndexCommand, progress func(api.IndexFrame)) (api.IndexFrame, error) {
	if x.client == nil {
		return api.IndexFrame{}, errors.New("本机后台不可用")
	}
	q.Session = x.id
	q.EncodePaths()
	b, err := json.Marshal(q)
	if err != nil {
		return api.IndexFrame{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", x.client.URL+"/v1/index", bytes.NewReader(b))
	if err != nil {
		return api.IndexFrame{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, vs := range x.client.Headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if x.client.SessionCookie != nil {
		req.AddCookie(x.client.SessionCookie)
	}
	if x.client.Token != "" {
		req.Header.Set("Authorization", "Bearer "+x.client.Token)
	}
	// Scans may outlive the generic client's request timeout; caller context
	// controls cancellation. Transport/auth remain the same as other commands.
	hc := *x.client.HTTP
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return api.IndexFrame{}, err
	}
	defer resp.Body.Close()
	decoder := json.NewDecoder(resp.Body)
	if resp.StatusCode/100 != 2 {
		var e api.Error
		if err := decoder.Decode(&e); err != nil {
			return api.IndexFrame{}, err
		}
		return api.IndexFrame{}, &e
	}
	for {
		var f api.IndexFrame
		if err := decoder.Decode(&f); err != nil {
			if err == io.EOF {
				err = errors.New("index response ended without outcome")
			}
			return f, err
		}
		if progress != nil {
			progress(f)
		}
		if f.Done {
			x.mu.Lock()
			x.status = f.Status
			x.mu.Unlock()
			if f.Error != nil {
				switch f.Error.Code {
				case "not_found":
					return f, fmt.Errorf("%w: %s", os.ErrNotExist, f.Error.Message)
				case "snapshot_scope":
					return f, index.ErrSnapshotScope
				case "canceled":
					return f, context.Canceled
				}
				return f, f.Error
			}
			return f, nil
		}
	}
}
func (x *Index) Status() index.Status { x.mu.Lock(); defer x.mu.Unlock(); return x.status }
func (x *Index) Scan(ctx context.Context, o index.ScanOptions, p func(index.Progress)) (index.Progress, error) {
	return x.scan(ctx, "scan", o, p)
}
func (x *Index) Update(ctx context.Context, o index.ScanOptions, p func(index.Progress)) (index.Progress, error) {
	return x.scan(ctx, "update", o, p)
}
func (x *Index) scan(ctx context.Context, a string, o index.ScanOptions, p func(index.Progress)) (index.Progress, error) {
	f, e := x.command(ctx, api.IndexCommand{Action: a, Options: o}, func(f api.IndexFrame) {
		if p != nil && f.Progress != nil {
			p(*f.Progress)
		}
	})
	if f.Progress == nil {
		return index.Progress{}, e
	}
	return *f.Progress, e
}
func (x *Index) LoadSnapshot(ctx context.Context, file string, o index.ScanOptions, p func(index.LoadProgress)) (index.SnapshotMeta, error) {
	f, e := x.command(ctx, api.IndexCommand{Action: "load", Filename: file, Options: o}, func(f api.IndexFrame) {
		if p != nil && f.Loading != nil {
			p(*f.Loading)
		}
	})
	return f.Meta, e
}
func (x *Index) SaveSnapshot(ctx context.Context, file string, o index.ScanOptions, m index.SnapshotMeta) error {
	_, e := x.command(ctx, api.IndexCommand{Action: "save", Filename: file, Options: o, Meta: m}, nil)
	return e
}
func (x *Index) Reconcile(ctx context.Context, o index.ScanOptions, p []string) error {
	_, e := x.command(ctx, api.IndexCommand{Action: "reconcile", Options: o, Paths: p}, nil)
	return e
}
func (x *Index) ClearDeltas() error {
	ctx, c := context.WithTimeout(context.Background(), 30*time.Second)
	defer c()
	_, err := x.command(ctx, api.IndexCommand{Action: "clear"}, nil)
	return err
}
func (x *Index) RefreshPaths() []string {
	ctx, c := context.WithTimeout(context.Background(), 30*time.Second)
	defer c()
	f, _ := x.command(ctx, api.IndexCommand{Action: "refresh"}, nil)
	paths := make([]string, len(f.Paths))
	for i, path := range f.Paths {
		paths[i] = string(path)
	}
	return paths
}
func (x *Index) Subtree(p string) (index.Subtree, bool) {
	ctx, c := context.WithTimeout(context.Background(), 30*time.Second)
	defer c()
	f, e := x.command(ctx, api.IndexCommand{Action: "subtree", Dir: p}, nil)
	return f.Subtree, e == nil && f.Found
}
func (x *Index) Search(ctx context.Context, q index.Query) (*index.Results, error) {
	f, e := x.command(ctx, api.IndexCommand{Action: "search", Query: q}, nil)
	if e != nil {
		return nil, e
	}
	return index.RemoteResults(f.Total, f.Version, func(ctx context.Context, offset, limit int) ([]index.Entry, error) {
		page, e := x.command(ctx, api.IndexCommand{Action: "page", Result: f.Result, Offset: offset, Limit: limit}, nil)
		var out []index.Entry
		for _, entry := range page.Entries {
			out = append(out, entry.Decode())
		}
		return out, e
	}, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := x.command(ctx, api.IndexCommand{Action: "release", Result: f.Result}, nil)
		return err
	}), nil
}
func (x *Index) Close() {
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	x.command(ctx, api.IndexCommand{Action: "close"}, nil)
}
