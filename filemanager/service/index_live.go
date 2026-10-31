package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"

	"filemanager/api"
)

// Queue changes after filesystem execution, independently of the foreground
// journal bridge. One worker per session serializes against scans/snapshot IO.
func (s *Service) notifyIndex(paths ...string) {
	m := s.indexes
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping {
		return
	}
	for _, v := range m.sessions {
		v.mu.Lock()
		v.pending = append(v.pending, paths...)
		if !v.updating && len(v.options.Roots) > 0 && len(v.pending) > 0 {
			v.updating = true
			m.work.Add(1)
			go s.reconcileIndex(v)
		}
		v.mu.Unlock()
	}
}
func (s *Service) reconcileIndex(v *indexSession) {
	defer s.indexes.work.Done()
	for {
		v.mu.Lock()
		paths := v.pending
		v.pending = nil
		opts := v.options
		if len(paths) == 0 {
			v.updating = false
			v.mu.Unlock()
			return
		}
		v.mu.Unlock()
		v.writeMu.Lock()
		err := v.index.Reconcile(s.indexes.ctx, opts, paths)
		v.writeMu.Unlock()
		if err != nil {
			v.mu.Lock()
			v.pending = append(v.pending, paths...)
			v.updating = false
			v.mu.Unlock()
			return
		}
	}
}
func (s *Service) stopIndexes() {
	if s.indexes == nil {
		return
	}
	m := s.indexes
	m.mu.Lock()
	m.stopping = true
	m.cancel()
	m.mu.Unlock()
	m.work.Wait()
}
func (s *Service) replayIndexPaths(ctx context.Context) ([]string, error) {
	if s.writer == nil {
		return nil, nil
	}
	unique := map[string]bool{}
	add := func(p string) {
		if p != "" {
			unique[p] = true
		}
	}
	location := func(l api.Location) string {
		root, ok := s.roots[l.Root]
		if !ok {
			return ""
		}
		p, e := relative(l.NativePath())
		if e != nil {
			return ""
		}
		return filepath.Join(root.config.Path, p)
	}
	err := s.writer.log.Replay(func(_ uint64, b []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var record struct {
			Phase      string
			Request    json.RawMessage
			Transfer   json.RawMessage `json:"transfer"`
			SourcePath []byte          `json:"source_path"`
			TargetPath []byte          `json:"target_path"`
		}
		if err := json.Unmarshal(b, &record); err != nil {
			return err
		}
		switch record.Phase {
		case "begin":
			add(string(record.SourcePath))
			add(string(record.TargetPath))
		case "task_begin":
			var q api.TransferRequest
			if err := json.Unmarshal(record.Transfer, &q); err != nil {
				return err
			}
			if q.Move {
				add(location(q.Source))
			}
			add(location(q.Target))
		case "remote_begin":
			var q api.RemoteCommand
			if err := json.Unmarshal(record.Request, &q); err != nil {
				return err
			}
			if q.Kind == "download" || q.Kind == "download_file" || q.Kind == "resume" {
				add(string(q.Destination))
			}
		}
		return nil
	})
	var paths []string
	for p := range unique {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	return paths, err
}
