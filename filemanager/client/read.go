package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"filemanager/api"
)

func (c *Client) readRequest(ctx context.Context, route string, value any) (*http.Response, error) {
	if c == nil {
		return nil, errors.New("本机后台不可用")
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL+route, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, values := range c.Headers {
		for _, v := range values {
			req.Header.Add(key, v)
		}
	}
	if c.SessionCookie != nil {
		req.AddCookie(c.SessionCookie)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := *c.HTTP
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		var e api.Error
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&e); err != nil {
			return nil, err
		}
		return nil, &e
	}
	return resp, nil
}
func (c *Client) Read(ctx context.Context, q api.ReadRequest) (io.ReadCloser, error) {
	q.Location = q.Location.Wire()
	resp, err := c.readRequest(ctx, "/v1/read", q)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
func (c *Client) Tally(ctx context.Context, loc api.Location, progress func(api.Tally)) (api.Tally, error) {
	resp, err := c.readRequest(ctx, "/v1/tally", loc.Wire())
	if err != nil {
		return api.Tally{}, err
	}
	defer resp.Body.Close()
	decoder := json.NewDecoder(resp.Body)
	for {
		var t api.Tally
		if err := decoder.Decode(&t); err != nil {
			return t, err
		}
		if progress != nil {
			progress(t)
		}
		if t.Done {
			if t.Error != nil {
				return t, t.Error
			}
			return t, nil
		}
	}
}
