package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"filemanager/api"
	"filemanager/engine"
)

func (c *Client) backupCommand(ctx context.Context, q api.BackupCommand, fn func(api.BackupFrame)) (api.BackupFrame, error) {
	if c == nil {
		return api.BackupFrame{}, errors.New("本机后台不可用")
	}
	b, err := json.Marshal(q)
	if err != nil {
		return api.BackupFrame{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL+"/v1/backup", bytes.NewReader(b))
	if err != nil {
		return api.BackupFrame{}, err
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
		return api.BackupFrame{}, err
	}
	defer resp.Body.Close()
	d := json.NewDecoder(resp.Body)
	if resp.StatusCode/100 != 2 {
		var e api.Error
		if err := d.Decode(&e); err != nil {
			return api.BackupFrame{}, err
		}
		return api.BackupFrame{}, &e
	}
	for {
		var f api.BackupFrame
		if err := d.Decode(&f); err != nil {
			return f, err
		}
		if fn != nil {
			fn(f)
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
func (c *Client) PrepareBackup(ctx context.Context, spec api.BackupSpec, progress func(engine.ScanProgress)) (*engine.Plan, error) {
	f, err := c.backupCommand(ctx, api.BackupCommand{Action: "prepare", Spec: spec}, func(f api.BackupFrame) {
		if progress != nil && f.Scan != nil {
			progress(*f.Scan)
		}
	})
	if err != nil {
		return nil, err
	}
	if f.Plan == nil {
		return nil, errors.New("missing backend backup preview")
	}
	return engine.RemotePlan(f.Plan, func(ctx context.Context, h engine.Hooks) *engine.Result {
		started := time.Now()
		outcome, err := c.backupCommand(ctx, api.BackupCommand{Action: "execute", ID: f.ID}, func(f api.BackupFrame) {
			if h.Progress != nil && f.Progress != nil {
				h.Progress(*f.Progress)
			}
			if h.Done != nil && f.Action != nil {
				var e error
				if f.ActionError != "" {
					e = errors.New(f.ActionError)
				}
				h.Done(*f.Action, e)
			}
		})
		if err != nil {
			r := &engine.Result{Fatal: err, Canceled: ctx.Err() != nil, Started: started, Finished: time.Now()}
			if r.Canceled {
				r.Fatal = nil
			}
			return r
		}
		if outcome.Outcome == nil {
			return &engine.Result{Fatal: errors.New("missing backup outcome"), Started: started, Finished: time.Now()}
		}
		result := outcome.Outcome.Result
		if outcome.Outcome.Fatal != "" {
			result.Fatal = errors.New(outcome.Outcome.Fatal)
		}
		for _, e := range outcome.Outcome.Errors {
			result.Errors = append(result.Errors, engine.FileError{Action: e.Action, Err: errors.New(e.Message)})
		}
		return &result
	}), nil
}
