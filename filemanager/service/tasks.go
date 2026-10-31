package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	"filemanager/api"
	fmfs "filemanager/fs"
)

type transferTask struct {
	state   api.Task
	request api.TransferRequest
	cancel  context.CancelFunc
}
type taskManager struct {
	mu       sync.Mutex
	tasks    map[string]*transferTask
	stopping bool
	wg       sync.WaitGroup
}
type taskRecord struct {
	Phase   string               `json:"phase"`
	Request *api.TransferRequest `json:"transfer,omitempty"`
	Task    *api.Task            `json:"task,omitempty"`
}

// EnableLocalTransfers exposes trusted local primitives only for an unrestricted
// desktop root. FileIndex's restricted-root adapter must not enable this.
func (s *Service) EnableLocalTransfers() error { return s.EnableTransfers() }

// EnableTransfers uses root-confined filesystem operations for local and remote roots.
func (s *Service) EnableTransfers() error {
	if s.writer == nil {
		return errors.New("transfers require durable journal")
	}

	if s.tasks != nil {
		return errors.New("tasks already enabled")
	}
	m := &taskManager{tasks: map[string]*transferTask{}}
	err := s.writer.log.Replay(func(_ uint64, b []byte) error {
		var r taskRecord
		if err := json.Unmarshal(b, &r); err != nil {
			return err
		}
		switch r.Phase {
		case "task_begin":
			if r.Request == nil {
				return errors.New("invalid task intent")
			}
			if _, ok := m.tasks[r.Request.ID]; ok {
				return errors.New("duplicate task intent")
			}
			m.tasks[r.Request.ID] = &transferTask{request: *r.Request, state: api.Task{ID: r.Request.ID, State: "interrupted", Error: "backend stopped before a durable outcome; inspect partial files"}}
		case "task_end":
			if r.Task == nil || m.tasks[r.Task.ID] == nil || !r.Task.Finished() {
				return errors.New("invalid task outcome")
			}
			m.tasks[r.Task.ID].state = *r.Task
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.tasks = m
	return nil
}
func (s *Service) StopTasks() {
	if s.tasks == nil {
		return
	}
	m := s.tasks
	m.mu.Lock()
	m.stopping = true
	for _, t := range m.tasks {
		if t.cancel != nil {
			t.cancel()
		}
	}
	m.mu.Unlock()
	m.wg.Wait()
}
func (s *Service) submitTransfer(w http.ResponseWriter, r *http.Request) {
	if s.tasks == nil {
		fail(w, &api.Error{Code: "unsupported", Message: "local transfers not enabled"})
		return
	}
	var q api.TransferRequest
	if !decode(w, r, &q) {
		return
	}
	id, err := hex.DecodeString(q.ID)
	if err != nil || len(id) != 16 || (q.FileConflict != "fail" && q.FileConflict != "replace") || (q.DirectoryConflict != "fail" && q.DirectoryConflict != "merge") || len(q.Protected) > 16 {
		fail(w, &api.Error{Code: "invalid_request", Message: "invalid task id or conflict policies"})
		return
	}
	q.Source = q.Source.Wire()
	q.Target = q.Target.Wire()
	for i := range q.Protected {
		q.Protected[i] = q.Protected[i].Wire()
	}
	m := s.tasks
	m.mu.Lock()
	defer m.mu.Unlock()
	if old := m.tasks[q.ID]; old != nil {
		if !reflect.DeepEqual(old.request, q) {
			fail(w, &api.Error{Code: "conflict", Message: "task id already belongs to another request"})
			return
		}
		write(w, old.state)
		return
	}
	if m.stopping {
		fail(w, &api.Error{Code: "unavailable", Message: "backend stopping"})
		return
	}
	// Validate before acknowledging, then validate again in the worker.
	if _, _, err := s.transferPaths(q); err != nil {
		fail(w, err)
		return
	}
	if len(m.tasks) >= 10000 {
		fail(w, &api.Error{Code: "unavailable", Message: "task history limit reached; archival required"})
		return
	}
	b, err := json.Marshal(taskRecord{Phase: "task_begin", Request: &q})
	if err != nil {
		fail(w, err)
		return
	}
	if _, err = s.writer.log.Append(b); err != nil {
		fail(w, err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &transferTask{request: q, state: api.Task{ID: q.ID, State: "queued"}, cancel: cancel}
	m.tasks[q.ID] = t
	m.wg.Add(1)
	go s.runTransfer(ctx, t)
	write(w, t.state)
}
func (s *Service) getTask(w http.ResponseWriter, r *http.Request) {
	if s.tasks == nil {
		fail(w, &api.Error{Code: "unsupported", Message: "tasks not enabled"})
		return
	}
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	t := s.tasks.tasks[r.PathValue("id")]
	if t == nil {
		fail(w, os.ErrNotExist)
		return
	}
	write(w, t.state)
}
func (s *Service) cancelTask(w http.ResponseWriter, r *http.Request) {
	if s.tasks == nil {
		fail(w, &api.Error{Code: "unsupported", Message: "tasks not enabled"})
		return
	}
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	t := s.tasks.tasks[r.PathValue("id")]
	if t == nil {
		fail(w, os.ErrNotExist)
		return
	}
	if t.cancel != nil {
		t.cancel()
	}
	write(w, t.state)
}
func (s *Service) transferPaths(q api.TransferRequest) (string, string, error) {
	src, from, err := s.resolve(q.Source)
	if err == nil {
		from, err = readPath(src, from, false)
	}
	if err == nil && q.Move {
		err = s.checkMutationPath(src, from)
	}
	if err == nil && (from == "." || s.hidden(src.config.ID, filepath.ToSlash(from))) {
		err = errors.New("protected source root or entry")
	}
	if err != nil {
		return "", "", err
	}
	dst, to, err := s.writeLocation(q.Target)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(src.config.Path, from), filepath.Join(dst.config.Path, to), nil
}

// Resolve the existing prefix, including a leaf symlink, to protect journals
// even when a copied tree traverses an alias. This is trusted-local policy,
// not a replacement for confined root handles on a remote server.
func resolvePrefix(p string) (string, error) {
	real, err := filepath.EvalSymlinks(p)
	if err == nil {
		return real, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(p)
	if parent == p {
		return "", err
	}
	real, err = resolvePrefix(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(real, filepath.Base(p)), nil
}
func (s *Service) transferGuard(q api.TransferRequest) (func(string) error, error) {
	protected := []string{s.writer.filename, s.writer.filename + ".lock"}
	for _, loc := range q.Protected {
		root, p, err := s.resolve(loc)
		if err != nil {
			return nil, err
		}
		actual, err := resolvePrefix(filepath.Join(root.config.Path, p))
		if err != nil {
			return nil, err
		}
		protected = append(protected, actual)
	}
	return func(p string) error {
		actual, err := resolvePrefix(p)
		if err != nil {
			return err
		}
		for _, keep := range protected {
			rel, e := filepath.Rel(actual, keep)
			if e == nil && filepath.IsLocal(rel) {
				return fmt.Errorf("protected file or ancestor: %s", p)
			}
			a, ae := os.Stat(actual)
			b, be := os.Stat(keep)
			if ae == nil && be == nil && os.SameFile(a, b) {
				return fmt.Errorf("protected file alias: %s", p)
			}
		}
		return nil
	}, nil
}
func (s *Service) runTransfer(ctx context.Context, t *transferTask) {
	m := s.tasks
	defer m.wg.Done()
	defer t.cancel()
	err := func() error {
		select {
		case s.writer.gate <- struct{}{}:
			defer func() { <-s.writer.gate }()
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		m.mu.Lock()
		t.state.State = "running"
		m.mu.Unlock()
		from, to, err := s.transferPaths(t.request)
		if err != nil {
			return err
		}
		guard, err := s.transferGuard(t.request)
		if err != nil {
			return err
		}
		if err := guard(to); err != nil {
			return err
		}
		if t.request.Move {
			if err := guard(from); err != nil {
				return err
			}
		}
		sourceRoot := s.roots[t.request.Source.Root]
		targetRoot := s.roots[t.request.Target.Root]
		sourcePath, err := filepath.Rel(sourceRoot.config.Path, from)
		if err != nil {
			return err
		}
		targetPath, err := filepath.Rel(targetRoot.config.Path, to)
		if err != nil {
			return err
		}
		// Check resolved ancestry across different configured roots too.
		rel, e := filepath.Rel(from, to)
		if e == nil && filepath.IsLocal(rel) {
			return errors.New("cannot transfer into source")
		}
		defer func() {
			if t.request.Move {
				s.notifyIndex(from, to)
			} else {
				s.notifyIndex(to)
			}
		}()
		changed := []api.Location{{Root: targetRoot.config.ID, Path: filepath.ToSlash(targetPath)}}
		if t.request.Move {
			changed = append(changed, api.Location{Root: sourceRoot.config.ID, Path: filepath.ToSlash(sourcePath)})
		}
		if s.BeforeChange != nil {
			done, err := s.BeforeChange(changed)
			if err != nil {
				return err
			}
			if done != nil {
				defer done()
			}
		}
		return fmfs.RootTransfer(ctx, sourceRoot.handle, sourcePath, targetRoot.handle, targetPath, fmfs.RootTransferOptions{Metadata: s.Metadata,
			Move: t.request.Move, Policy: fmfs.TransferPolicy{ReplaceFiles: t.request.FileConflict == "replace", MergeDirectories: t.request.DirectoryConflict == "merge"},
			SourceGuard: func(p string) error {
				if s.hidden(sourceRoot.config.ID, filepath.ToSlash(p)) {
					return errors.New("protected source entry")
				}
				if t.request.Move {
					return guard(filepath.Join(sourceRoot.config.Path, p))
				}
				return nil
			},
			TargetGuard: func(p string) error {
				if s.hidden(targetRoot.config.ID, filepath.ToSlash(p)) {
					return errors.New("protected target entry")
				}
				return guard(filepath.Join(targetRoot.config.Path, p))
			},
			Total:    func(n int64) { m.mu.Lock(); t.state.Total = n; m.mu.Unlock() },
			Progress: func(n int64, p string) { m.mu.Lock(); t.state.Done += n; t.state.Current = p; m.mu.Unlock() },
		})
	}()
	m.mu.Lock()
	defer m.mu.Unlock()
	outcome := t.state
	outcome.State = "done"
	if err != nil {
		outcome.State = "failed"
		outcome.Error = err.Error()
		if errors.Is(err, context.Canceled) {
			outcome.State = "canceled"
		}
	}
	b, _ := json.Marshal(taskRecord{Phase: "task_end", Task: &outcome})
	if _, e := s.writer.log.Append(b); e != nil {
		outcome.State = "interrupted"
		outcome.Error = "filesystem may have changed; outcome journal failed: " + e.Error()
	}
	t.state = outcome
}
