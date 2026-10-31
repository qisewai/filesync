package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"filemanager/api"
	"filemanager/index"
	"filemanager/trash"
)

type mutationWriter struct {
	log      *index.Journal
	filename string
	gate     chan struct{}
}

type mutationRecord struct {
	Phase      string               `json:"phase"`
	ID         uint64               `json:"id,omitempty"`
	Request    *api.MutationRequest `json:"request,omitempty"`
	Error      string               `json:"error,omitempty"`
	SourcePath []byte               `json:"source_path,omitempty"`
	TargetPath []byte               `json:"target_path,omitempty"`
}

// EnableWrites must be called before serving. A durable independent journal is
// mandatory: index rebuilds must never discard filesystem operation evidence.
// One backend owns this journal; a second process fails rather than sharing it.
func (s *Service) EnableWrites(filename string) error {
	if s.writer != nil {
		return errors.New("writes already enabled")
	}
	abs, err := filepath.Abs(filename)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return err
	}
	abs = filepath.Join(parent, filepath.Base(abs))
	j, err := index.OpenJournal(abs, sha256.Sum256([]byte("filemanager-mutations-v1")))
	if err != nil {
		return err
	}
	s.writer = &mutationWriter{log: j, filename: abs, gate: make(chan struct{}, 1)}
	return nil
}

func (s *Service) checkMutationPath(root openedRoot, p string) error {
	if p == "." || root.config.ReadOnly || s.hiddenMutation(root, p) {
		return &api.Error{Code: "permission_denied", Message: "root, read-only or protected path"}
	}
	actual := filepath.Join(root.config.Path, p)
	for _, protected := range []string{s.writer.filename, s.writer.filename + ".lock"} {
		rel, err := filepath.Rel(actual, protected)
		if err == nil && filepath.IsLocal(rel) {
			return &api.Error{Code: "permission_denied", Message: "operation would modify backend journal or its parent"}
		}
		a, ae := os.Lstat(actual)
		b, be := os.Stat(protected)
		if ae == nil && be == nil && os.SameFile(a, b) {
			return &api.Error{Code: "permission_denied", Message: "operation targets backend journal"}
		}
	}
	return nil
}

// Resolve aliases before opening the parent. The final mutation uses only
// pinned root handles, never an absolute user-supplied path.
func (s *Service) writeLocation(loc api.Location) (openedRoot, string, error) {
	root, p, err := s.resolve(loc)
	if err != nil {
		return root, p, err
	}
	if err = s.checkMutationPath(root, p); err != nil {
		return root, p, err
	}
	p, err = readPath(root, p, false)
	if err == nil {
		err = s.checkMutationPath(root, p)
	}
	return root, p, err
}

func (s *Service) mutate(w http.ResponseWriter, req *http.Request) {
	if s.writer == nil {
		fail(w, &api.Error{Code: "unsupported", Message: "mutations are not enabled"})
		return
	}
	var q api.MutationRequest
	if !decode(w, req, &q) {
		return
	}
	switch q.Action {
	case "mkdir":
		if q.Source.Root != "" || q.Source.NativePath() != "" {
			fail(w, &api.Error{Code: "invalid_request", Message: "mkdir accepts only target"})
			return
		}
	case "trash":
		if !s.desktopTrash {
			fail(w, &api.Error{Code: "unsupported", Message: "desktop trash is not enabled"})
			return
		}
		fallthrough
	case "delete":
		if q.Target.Root != "" || q.Target.NativePath() != "" {
			fail(w, &api.Error{Code: "invalid_request", Message: "delete accepts only source"})
			return
		}
	case "rename":
		if q.Source.Root != q.Target.Root {
			fail(w, &api.Error{Code: "invalid_request", Message: "rename requires the same root"})
			return
		}
	default:
		fail(w, &api.Error{Code: "invalid_request", Message: "unknown mutation action"})
		return
	}
	ctx := req.Context()
	select {
	case s.writer.gate <- struct{}{}:
		defer func() { <-s.writer.gate }()
	case <-ctx.Done():
		fail(w, ctx.Err())
		return
	}
	var src, dst openedRoot
	var from, to string
	var err error
	if q.Action != "mkdir" {
		src, from, err = s.writeLocation(q.Source)
	}
	if err == nil && q.Action != "delete" && q.Action != "trash" {
		dst, to, err = s.writeLocation(q.Target)
	}
	if err != nil {
		fail(w, err)
		return
	}
	if err = ctx.Err(); err != nil {
		fail(w, err)
		return
	}
	q.Source = q.Source.Wire()
	q.Target = q.Target.Wire()
	intent := mutationRecord{Phase: "begin", Request: &q}
	if from != "" {
		intent.SourcePath = []byte(filepath.Join(src.config.Path, from))
	}
	if to != "" {
		intent.TargetPath = []byte(filepath.Join(dst.config.Path, to))
	}
	data, err := json.Marshal(intent)
	if err != nil {
		fail(w, err)
		return
	}
	id, err := s.writer.log.Append(data)
	if err != nil {
		fail(w, fmt.Errorf("operation not executed: %w", err))
		return
	}
	defer func() {
		var paths []string
		if from != "" {
			paths = append(paths, filepath.Join(src.config.Path, from))
		}
		if to != "" {
			paths = append(paths, filepath.Join(dst.config.Path, to))
		}
		s.notifyIndex(paths...)
	}()
	var changed []api.Location
	if from != "" {
		changed = append(changed, api.Location{Root: src.config.ID, Path: filepath.ToSlash(from)})
	}
	if to != "" {
		changed = append(changed, api.Location{Root: dst.config.ID, Path: filepath.ToSlash(to)})
	}
	if s.BeforeChange != nil {
		done, hookErr := s.BeforeChange(changed)
		if hookErr != nil {
			fail(w, hookErr)
			return
		}
		if done != nil {
			defer done()
		}
	}
	err = func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if q.Action == "mkdir" {
			if err := dst.handle.Mkdir(to, 0755); err != nil {
				return err
			}
			if s.Metadata != nil {
				f, err := dst.handle.Open(to)
				if err != nil {
					return err
				}
				defer f.Close()
				if err := f.Chmod(s.Metadata.DirMode); err != nil {
					return err
				}
				if s.Metadata.UID >= 0 {
					return f.Chown(s.Metadata.UID, s.Metadata.GID)
				}
			}
			return nil
		}
		if q.Action == "trash" {
			return trash.Move(filepath.Join(src.config.Path, from))
		}
		if q.Action == "delete" {
			return s.removeTree(ctx, src, from)
		}
		a, e := src.handle.Open(filepath.Dir(from))
		if e != nil {
			return e
		}
		defer a.Close()
		b, e := dst.handle.Open(filepath.Dir(to))
		if e != nil {
			return e
		}
		defer b.Close()
		return renameExclusive(a, filepath.Base(from), b, filepath.Base(to))
	}()
	outcome := mutationRecord{Phase: "done", ID: id}
	if err != nil {
		outcome.Phase = "failed"
		outcome.Error = err.Error()
	}
	data, _ = json.Marshal(outcome)
	if _, e := s.writer.log.Append(data); e != nil {
		fail(w, &api.Error{Code: "outcome_unknown", Message: fmt.Sprintf("operation %d may have changed files; recording outcome failed: %v", id, e)})
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	write(w, api.MutationResult{ID: id})
}

// Recursive removal checks cancellation between entries and never follows a
// leaf symlink. Pin each directory and check identity before descending.
func (s *Service) removeTree(ctx context.Context, root openedRoot, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkMutationPath(root, p); err != nil {
		return err
	}
	fi, err := root.handle.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return root.handle.Remove(p)
	}
	child, err := root.handle.OpenRoot(p)
	if err != nil {
		return err
	}
	defer child.Close()
	current, err := child.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(fi, current) {
		return errors.New("directory changed during deletion")
	}
	f, err := child.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	nested := openedRoot{config: root.config, handle: child}
	nested.config.Path = filepath.Join(root.config.Path, p)
	for {
		ds, e := f.ReadDir(128)
		for _, d := range ds {
			if err := s.removeTree(ctx, nested, d.Name()); err != nil {
				return err
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	latest, err := root.handle.Lstat(p)
	if err != nil {
		return err
	}
	if !os.SameFile(fi, latest) {
		return errors.New("directory changed during deletion")
	}
	return root.handle.Remove(p)
}

func (s *Service) hiddenMutation(root openedRoot, p string) bool {
	original := s.roots[root.config.ID].config.Path
	relative, err := filepath.Rel(original, filepath.Join(root.config.Path, p))
	return err != nil || s.hidden(root.config.ID, filepath.ToSlash(relative))
}

// EnableDesktopTrash is only valid for an explicitly unrestricted local root.
// Restricted and remote roots must implement their own managed trash policy.
func (s *Service) EnableDesktopTrash() error {
	if s.writer == nil {
		return errors.New("enable writes before desktop trash")
	}
	if !s.unrestrictedLocal() {
		return errors.New("desktop trash requires unrestricted writable filesystem roots")
	}
	s.desktopTrash = true
	return nil
}
