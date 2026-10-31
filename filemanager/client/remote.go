package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"filemanager/api"
)

func (c *Client) Remote(ctx context.Context, q api.RemoteCommand, progress func(api.RemoteFrame)) (api.RemoteFrame, error) {
	if c == nil {
		return api.RemoteFrame{}, errors.New("本机后台不可用")
	}
	for i := range q.Protected {
		q.Protected[i] = q.Protected[i].Wire()
	}
	b, err := json.Marshal(q)
	if err != nil {
		return api.RemoteFrame{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL+"/v1/remote", bytes.NewReader(b))
	if err != nil {
		return api.RemoteFrame{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, vs := range c.Headers {
		for _, v := range vs {
			req.Header.Add(k, v)
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
		return api.RemoteFrame{}, err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	if resp.StatusCode/100 != 2 {
		var e api.Error
		if err := dec.Decode(&e); err != nil {
			return api.RemoteFrame{}, err
		}
		return api.RemoteFrame{}, &e
	}
	for {
		var f api.RemoteFrame
		if err := dec.Decode(&f); err != nil {
			return f, err
		}
		if progress != nil {
			progress(f)
		}
		if f.Done {
			if f.Error != nil {
				if f.Error.Code == "canceled" {
					return f, context.Canceled
				}
				return f, f.Error
			}
			return f, nil
		}
	}
}
