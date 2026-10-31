// Package client implements the same protocol over HTTP(S) and local IPC.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"filemanager/api"
)

type Client struct {
	URL           string
	HTTP          *http.Client
	Token         string
	Headers       http.Header
	SessionCookie *http.Cookie
}

func New(base string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{URL: strings.TrimRight(base, "/"), HTTP: hc}
}
func Unix(socket string) *Client {
	t := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return New("http://filemanager", &http.Client{Transport: t})
}
func (c *Client) Close() { c.HTTP.CloseIdleConnections() }
func (c *Client) call(ctx context.Context, method, route string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	r, err := http.NewRequestWithContext(ctx, method, c.URL+route, body)
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	for key, values := range c.Headers {
		for _, value := range values {
			r.Header.Add(key, value)
		}
	}
	if c.SessionCookie != nil {
		r.AddCookie(c.SessionCookie)
	}
	if c.Token != "" {
		r.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e api.Error
		if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&e) == nil && e.Code != "" {
			return &e
		}
		return fmt.Errorf("filemanager: HTTP %d", resp.StatusCode)
	}
	if out == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
func (c *Client) Info(ctx context.Context) (api.Info, error) {
	var v api.Info
	err := c.call(ctx, "GET", "/v1/info", nil, &v)
	if err == nil && v.Version != api.Version {
		err = fmt.Errorf("unsupported filemanager protocol: %d", v.Version)
	}
	return v, err
}
func (c *Client) Roots(ctx context.Context) ([]api.Root, error) {
	var v []api.Root
	err := c.call(ctx, "GET", "/v1/roots", nil, &v)
	return v, err
}
func (c *Client) List(ctx context.Context, q api.ListRequest) ([]api.Entry, error) {
	var v []api.Entry
	q.Location = q.Location.Wire()
	err := c.call(ctx, "POST", "/v1/list", q, &v)
	for i := range v {
		nativeEntry(&v[i])
	}
	return v, err
}
func (c *Client) Stat(ctx context.Context, q api.Location) (api.Entry, error) {
	var v api.Entry
	err := c.call(ctx, "POST", "/v1/stat", q.Wire(), &v)
	nativeEntry(&v)
	return v, err
}

func nativeEntry(e *api.Entry) {
	e.Path = e.NativePath()
	if e.NameBytes != nil {
		e.Name = string(e.NameBytes)
	}
}

func (c *Client) Mutate(ctx context.Context, q api.MutationRequest) (api.MutationResult, error) {
	q.Source = q.Source.Wire()
	q.Target = q.Target.Wire()
	var v api.MutationResult
	err := c.call(ctx, "POST", "/v1/mutate", q, &v)
	return v, err
}

func (c *Client) SubmitTransfer(ctx context.Context, q api.TransferRequest) (api.Task, error) {
	q.Source = q.Source.Wire()
	q.Target = q.Target.Wire()
	for i := range q.Protected {
		q.Protected[i] = q.Protected[i].Wire()
	}
	var t api.Task
	err := c.call(ctx, "POST", "/v1/transfers", q, &t)
	return t, err
}
func (c *Client) Task(ctx context.Context, id string) (api.Task, error) {
	var t api.Task
	err := c.call(ctx, "GET", "/v1/tasks/"+url.PathEscape(id), nil, &t)
	return t, err
}
func (c *Client) CancelTask(ctx context.Context, id string) (api.Task, error) {
	var t api.Task
	err := c.call(ctx, "POST", "/v1/tasks/"+url.PathEscape(id)+"/cancel", nil, &t)
	return t, err
}
