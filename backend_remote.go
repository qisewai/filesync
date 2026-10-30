package main

import (
	"context"
	"errors"
	"time"

	"filemanager/api"
	"filemanager/engine"
	fileindex "filemanager/remoteclient"
)

func remoteEndpoint(c *fileindex.Client) api.Endpoint {
	e := api.Endpoint{Address: c.Addr, Token: c.Token}
	if c.ArchiveSource != nil {
		e.ArchivePath = c.ArchiveSource.Path
		e.ArchiveVersion = c.ArchiveSource.Version
		e.LocalArchive = c.ArchiveSource.Local
	}
	return e
}
func (a *app) remoteProtection() []api.Location {
	if a.localCachePath == "" {
		return nil
	}
	var paths []api.Location
	for _, p := range []string{a.localCachePath, a.localCachePath + ".operations", a.localCachePath + ".operations.lock"} {
		paths = append(paths, backendLocation(p))
	}
	return paths
}
func remoteError(err error) error {
	var e *api.Error
	if errors.As(err, &e) && e.Code == "authentication_required" {
		return fileindex.ErrAuth
	}
	return err
}
func (a *app) backendUpload(ctx context.Context, c *fileindex.Client, loc fileindex.Location, paths []string, h engine.Hooks) *engine.Result {
	q := api.RemoteCommand{Kind: "upload", Target: remoteEndpoint(c), TargetRoot: loc.Root, TargetPath: loc.Path}
	for _, p := range paths {
		q.LocalPaths = append(q.LocalPaths, []byte(p))
	}
	started := time.Now()
	f, err := a.backend.Remote(ctx, q, func(f api.RemoteFrame) {
		if f.EngineProgress != nil && h.Progress != nil {
			h.Progress(*f.EngineProgress)
		}
		if f.Action != nil && h.Done != nil {
			var e error
			if f.ActionError != "" {
				e = errors.New(f.ActionError)
			}
			h.Done(*f.Action, e)
		}
	})
	if err != nil {
		r := &engine.Result{Fatal: remoteError(err), Canceled: ctx.Err() != nil, Started: started, Finished: time.Now()}
		if r.Canceled {
			r.Fatal = nil
		}
		return r
	}
	if f.Outcome == nil {
		return &engine.Result{Fatal: errors.New("后台未返回上传结果"), Started: started, Finished: time.Now()}
	}
	result := f.Outcome.Result
	if f.Outcome.Fatal != "" {
		result.Fatal = errors.New(f.Outcome.Fatal)
	}
	for _, e := range f.Outcome.Errors {
		result.Errors = append(result.Errors, engine.FileError{Action: e.Action, Err: errors.New(e.Message)})
	}
	return &result
}
func (a *app) backendDownload(ctx context.Context, c *fileindex.Client, kind string, root int64, paths []string, dest, partial string, progress func(fileindex.Progress)) ([]error, error) {
	f, err := a.backend.Remote(ctx, api.RemoteCommand{Kind: kind, Source: remoteEndpoint(c), Root: root, Paths: paths, Destination: []byte(dest), Partial: []byte(partial), Protected: a.remoteProtection()}, func(f api.RemoteFrame) {
		if f.Progress != nil && progress != nil {
			progress(fileindex.Progress{Done: f.Progress.Done, Total: f.Progress.Total, Current: f.Progress.Current})
		}
	})
	var es []error
	for _, e := range f.Errors {
		es = append(es, errors.New(e))
	}
	return es, remoteError(err)
}
func (a *app) backendRelay(ctx context.Context, src *fileindex.Client, root int64, paths []string, dst *fileindex.Client, toRoot int64, dir string, progress func(fileindex.Progress)) (map[string]bool, []error, error) {
	f, err := a.backend.Remote(ctx, api.RemoteCommand{Kind: "relay", Source: remoteEndpoint(src), Target: remoteEndpoint(dst), Root: root, TargetRoot: toRoot, Paths: paths, TargetPath: dir}, func(f api.RemoteFrame) {
		if f.Progress != nil && progress != nil {
			progress(fileindex.Progress{Done: f.Progress.Done, Total: f.Progress.Total, Current: f.Progress.Current})
		}
	})
	var es []error
	for _, e := range f.Errors {
		es = append(es, errors.New(e))
	}
	return f.Whole, es, remoteError(err)
}
