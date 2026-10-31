package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"

	"filemanager/api"
	"filemanager/index"
)

type indexSession struct {
	mu, writeMu        sync.Mutex
	options            index.ScanOptions
	clean              bool
	checkpointSequence uint64
	checkpointFile     string
	pending            []string
	updating           bool
	index              *index.Index
	used               time.Time
}
type searchSession struct {
	result  *index.Results
	session string
	used    time.Time
}
type indexService struct {
	ctx      context.Context
	cancel   context.CancelFunc
	stopping bool
	work     sync.WaitGroup
	mu       sync.Mutex
	sessions map[string]*indexSession
	results  map[string]*searchSession
}

func (s *Service) EnableLocalIndex() error {
	if !s.unrestrictedLocal() {
		return errors.New("local index requires unrestricted local root")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.indexes = &indexService{ctx: ctx, cancel: cancel, sessions: map[string]*indexSession{}, results: map[string]*searchSession{}}
	m := s.indexes
	m.work.Add(1)
	go func() {
		defer m.work.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				m.mu.Lock()
				m.expireResults(now)
				m.mu.Unlock()
			}
		}
	}()
	return nil
}
func (s *Service) indexCommand(w http.ResponseWriter, r *http.Request) {
	if s.indexes == nil {
		fail(w, &api.Error{Code: "unsupported", Message: "local index not enabled"})
		return
	}
	var q api.IndexCommand
	if !decode(w, r, &q) {
		return
	}
	if err := q.DecodePaths(); err != nil {
		fail(w, &api.Error{Code: "invalid_request", Message: "invalid encoded index path"})
		return
	}
	key, err := hex.DecodeString(q.Session)
	if err != nil || len(key) != 16 {
		fail(w, &api.Error{Code: "invalid_request", Message: "invalid index session"})
		return
	}
	m := s.indexes
	m.mu.Lock()
	// Query leases bound the number of pinned immutable snapshots. Expiry is
	// explicit: never recompute a page silently against a newer version.
	m.expireResults(time.Now())
	if q.Action == "close" {
		delete(m.sessions, q.Session)
		for id, result := range m.results {
			if result.session == q.Session {
				delete(m.results, id)
			}
		}
		m.mu.Unlock()
		write(w, api.IndexFrame{Done: true})
		return
	}
	v := m.sessions[q.Session]
	if v == nil {
		if q.Action == "page" || q.Action == "release" {
			m.mu.Unlock()
			fail(w, &api.Error{Code: "result_expired", Message: "index session closed"})
			return
		}
		if len(m.sessions) >= 16 {
			m.mu.Unlock()
			fail(w, &api.Error{Code: "unavailable", Message: "index session limit reached"})
			return
		}
		v = &indexSession{index: index.New()}
		m.sessions[q.Session] = v
	}
	v.used = time.Now()
	switch q.Action {
	case "scan", "update", "load", "save", "reconcile":
		v.mu.Lock()
		v.options = q.Options
		v.mu.Unlock()
	}
	x := v.index
	m.mu.Unlock()
	switch q.Action {
	case "scan", "update", "load", "save", "reconcile", "clear":
		v.writeMu.Lock()
		defer v.writeMu.Unlock()
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	encoder := json.NewEncoder(w)
	send := func(f api.IndexFrame) { encoder.Encode(f); http.NewResponseController(w).Flush() }
	frame := api.IndexFrame{Done: true}
	ctx := r.Context()
	switch q.Action {
	case "scan":
		v.clean = false
		v.checkpointFile = ""
		if s.writer != nil {
			select {
			case s.writer.gate <- struct{}{}:
				defer func() { <-s.writer.gate }()
			case <-ctx.Done():
				err = ctx.Err()
			}
			if err != nil {
				break
			}
		}
		var p index.Progress
		p, err = x.Scan(ctx, q.Options, func(p index.Progress) { send(api.IndexFrame{Progress: &p}) })
		frame.Progress = &p
		if err == nil && p.Errors == 0 && s.writer != nil {
			v.checkpointSequence, err = s.writer.log.Sequence()
			v.clean = err == nil
		}
	case "update":
		v.clean = false
		var p index.Progress
		p, err = x.Update(ctx, q.Options, func(p index.Progress) { send(api.IndexFrame{Progress: &p}) })
		frame.Progress = &p
	case "load":
		v.clean = v.clean && v.checkpointFile == q.Filename
		frame.Meta, err = x.LoadSnapshot(ctx, q.Filename, q.Options, func(p index.LoadProgress) { send(api.IndexFrame{Loading: &p}) })
		if err == nil {
			var paths []string
			paths, err = s.replayIndexPaths(ctx)
			if err == nil {
				err = x.Reconcile(ctx, q.Options, paths)
			}
		}
	case "save":
		if s.writer == nil {
			err = errors.New("snapshot writes require mutation journal")
			break
		}
		for _, root := range s.roots {
			err = s.checkMutationPath(root, q.Filename)
			break
		}
		if err == nil {
			var guard func(string) error
			guard, err = s.transferGuard(api.TransferRequest{})
			if err == nil {
				err = guard(q.Filename)
			}
		}
		if err == nil {
			err = x.SaveSnapshot(ctx, q.Filename, q.Options, q.Meta)
			if err == nil && v.clean {
				v.checkpointFile = q.Filename
			}
		}
	case "reconcile":
		err = x.Reconcile(ctx, q.Options, q.Paths)
	case "refresh":
		for _, path := range x.RefreshPaths() {
			frame.Paths = append(frame.Paths, []byte(path))
		}
	case "clear":
		// Only a saved full rebuild can retire overlays. Fence backend writes
		// and verify that its durable sequence is still current.
		if v.clean && v.checkpointFile != "" && s.writer != nil {
			select {
			case s.writer.gate <- struct{}{}:
				seq, e := s.writer.log.Sequence()
				if e != nil {
					err = e
				} else if seq == v.checkpointSequence {
					err = x.ClearDeltas()
				}
				<-s.writer.gate
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
		v.clean = false
	case "status":
	case "subtree":
		frame.Subtree, frame.Found = x.Subtree(q.Dir)
	case "search":
		var result *index.Results
		result, err = x.Search(ctx, q.Query)
		if err == nil {
			var id [16]byte
			_, err = rand.Read(id[:])
			if err != nil {
				break
			}
			m.mu.Lock()
			if ctx.Err() != nil || m.stopping || m.sessions[q.Session] != v {
				m.mu.Unlock()
				err = context.Canceled
				break
			}
			if len(m.results) >= 128 {
				var oldest string
				var used time.Time
				for key, v := range m.results {
					if oldest == "" || v.used.Before(used) {
						oldest, used = key, v.used
					}
				}
				delete(m.results, oldest)
			}
			{
				frame.Result = hex.EncodeToString(id[:])
				frame.Total = result.Total()
				frame.Version = result.Version()
				m.results[frame.Result] = &searchSession{result: result, session: q.Session, used: time.Now()}
			}
			m.mu.Unlock()
		}
	case "release":
		m.mu.Lock()
		if result := m.results[q.Result]; result != nil && result.session == q.Session {
			delete(m.results, q.Result)
		}
		m.mu.Unlock()
	case "page":
		if q.Offset < 0 || q.Limit < 1 || q.Limit > 1000 {
			err = &api.Error{Code: "invalid_request", Message: "invalid page bounds"}
			break
		}
		m.mu.Lock()
		result := m.results[q.Result]
		if result != nil && result.session != q.Session {
			result = nil
		}
		if result != nil {
			result.used = time.Now()
		}
		m.mu.Unlock()
		if result == nil {
			err = &api.Error{Code: "result_expired", Message: "search result expired; repeat the query"}
			break
		}
		var entries []index.Entry
		entries, err = result.result.Page(ctx, q.Offset, q.Limit)
		for _, e := range entries {
			frame.Entries = append(frame.Entries, api.EncodeIndexEntry(e))
		}
		frame.Version = result.result.Version()
		frame.Total = result.result.Total()
	default:
		err = &api.Error{Code: "invalid_request", Message: "unknown index command"}
	}
	frame.Status = x.Status()
	if err != nil {
		frame.Error = &api.Error{Code: "io", Message: err.Error()}
		var known *api.Error
		if errors.As(err, &known) {
			frame.Error = known
		}
		switch {
		case errors.Is(err, os.ErrNotExist):
			frame.Error.Code = "not_found"
		case errors.Is(err, index.ErrSnapshotScope):
			frame.Error.Code = "snapshot_scope"
		case errors.Is(err, context.Canceled):
			frame.Error.Code = "canceled"
		}
	}
	send(frame)
}

// expireResults requires mu. Periodic cleanup also frees abandoned queries
// when the client stays idle and sends no further requests.
func (m *indexService) expireResults(now time.Time) {
	for id, v := range m.results {
		if now.Sub(v.used) > 10*time.Minute {
			delete(m.results, id)
		}
	}
}
