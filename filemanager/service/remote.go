package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"filemanager/api"
	"filemanager/engine"
	fileindex "filemanager/remoteclient"
)

func endpointClient(e api.Endpoint) *fileindex.Client {
	c := fileindex.New(e.Address, e.Token)
	if e.ArchivePath != "" {
		if e.LocalArchive {
			c = fileindex.NewLocalArchive(e.ArchivePath)
			c.ArchiveSource.Version = e.ArchiveVersion
		} else {
			c.ArchiveSource = &fileindex.ArchiveSource{Path: e.ArchivePath, Version: e.ArchiveVersion}
		}
	}
	return c
}
func (s *Service) remoteCommand(w http.ResponseWriter, r *http.Request) {
	// The local coordinator may reach user-configured remote addresses and
	// local paths. Never enable this capability on a restricted/public server.
	if s.backups == nil {
		fail(w, &api.Error{Code: "unsupported", Message: "local remote coordinator not enabled"})
		return
	}
	var q api.RemoteCommand
	if !decode(w, r, &q) {
		return
	}
	switch q.Kind {
	case "upload", "download", "download_file", "resume", "relay":
	default:
		fail(w, &api.Error{Code: "invalid_request", Message: "unknown remote operation"})
		return
	}
	if len(q.Paths) > 10000 || len(q.LocalPaths) > 10000 {
		fail(w, &api.Error{Code: "invalid_request", Message: "too many paths"})
		return
	}
	guard, err := s.transferGuard(api.TransferRequest{Protected: q.Protected})
	if err != nil {
		fail(w, err)
		return
	}
	if q.Kind == "download" || q.Kind == "download_file" || q.Kind == "resume" {
		if err := guard(string(q.Destination)); err != nil {
			fail(w, err)
			return
		}
		if q.Kind == "resume" {
			if err := guard(string(q.Partial)); err != nil {
				fail(w, err)
				return
			}
		}
	}
	if q.Kind == "download" || q.Kind == "download_file" || q.Kind == "resume" {
		defer s.notifyIndex(string(q.Destination))
	}
	ctx := r.Context()
	select {
	case s.writer.gate <- struct{}{}:
		defer func() { <-s.writer.gate }()
	case <-ctx.Done():
		fail(w, ctx.Err())
		return
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		fail(w, err)
		return
	}
	opID := hex.EncodeToString(id[:])
	safe := q
	safe.Source.Token = ""
	safe.Target.Token = ""
	b, _ := json.Marshal(struct {
		Phase, ID string
		Request   api.RemoteCommand
	}{"remote_begin", opID, safe})
	if _, err := s.writer.log.Append(b); err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	var mu sync.Mutex
	send := func(f api.RemoteFrame) {
		mu.Lock()
		defer mu.Unlock()
		enc.Encode(f)
		http.NewResponseController(w).Flush()
	}
	out := api.RemoteFrame{Done: true}
	src, dst := endpointClient(q.Source), endpointClient(q.Target)
	src.BeforeLocalWrite = guard
	progress := func(p fileindex.Progress) {
		send(api.RemoteFrame{Progress: &api.RemoteProgress{Done: p.Done, Total: p.Total, Current: p.Current}})
	}
	switch q.Kind {
	case "upload":
		var paths []string
		for _, p := range q.LocalPaths {
			paths = append(paths, string(p))
		}
		result := dst.Upload(ctx, fileindex.Location{Addr: q.Target.Address, Root: q.TargetRoot, Path: q.TargetPath}, paths, engine.Hooks{Progress: func(p engine.Progress) { send(api.RemoteFrame{EngineProgress: &p}) }, Done: func(a engine.Action, e error) {
			f := api.RemoteFrame{Action: &a}
			if e != nil {
				f.ActionError = e.Error()
			}
			send(f)
		}})
		out.Outcome = api.EncodeBackupOutcome(result)
	case "download":
		var es []error
		es, err = src.Download(ctx, q.Root, q.Paths, string(q.Destination), progress)
		for _, e := range es {
			out.Errors = append(out.Errors, e.Error())
		}
	case "download_file", "resume":
		if len(q.Paths) != 1 {
			err = errors.New("single file path required")
			break
		}
		if q.Kind == "resume" {
			err = src.DownloadFileResumable(ctx, q.Root, q.Paths[0], string(q.Destination), string(q.Partial), progress)
		} else {
			err = src.DownloadFile(ctx, q.Root, q.Paths[0], string(q.Destination), progress)
		}
	case "relay":
		var es []error
		out.Whole, es, err = fileindex.Relay(ctx, src, q.Root, q.Paths, dst, q.TargetRoot, q.TargetPath, progress)
		for _, e := range es {
			out.Errors = append(out.Errors, e.Error())
		}
	}
	if err != nil {
		out.Error = &api.Error{Code: "io", Message: err.Error()}
		if errors.Is(err, fileindex.ErrAuth) {
			out.Error.Code = "authentication_required"
		}
		if errors.Is(err, context.Canceled) {
			out.Error.Code = "canceled"
		}
	}
	b, _ = json.Marshal(struct {
		Phase, ID string
		Outcome   api.RemoteFrame
	}{"remote_end", opID, out})
	if _, err := s.writer.log.Append(b); err != nil {
		out.Error = &api.Error{Code: "outcome_unknown", Message: err.Error()}
	}
	send(out)
}
