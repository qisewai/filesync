package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"filemanager/api"
	"filemanager/engine"
	fileindex "filemanager/remoteclient"
)

type backupPlan struct {
	plan    *engine.Plan
	used    time.Time
	started bool
	outcome *api.BackupOutcome
	failure *api.Error
}
type backupService struct {
	mu    sync.Mutex
	plans map[string]*backupPlan
}

func (s *Service) EnableLocalBackup() error {
	if s.writer == nil || !s.unrestrictedLocal() {
		return errors.New("backup requires unrestricted local backend")
	}
	s.backups = &backupService{plans: map[string]*backupPlan{}}
	return nil
}
func (s *Service) backupCommand(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		fail(w, &api.Error{Code: "unsupported", Message: "backup not enabled"})
		return
	}
	var q api.BackupCommand
	if !decode(w, r, &q) {
		return
	}
	if q.Action != "prepare" && q.Action != "execute" {
		fail(w, &api.Error{Code: "invalid_request", Message: "unknown backup command"})
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	var sendMu sync.Mutex
	send := func(f api.BackupFrame) {
		sendMu.Lock()
		defer sendMu.Unlock()
		enc.Encode(f)
		http.NewResponseController(w).Flush()
	}
	done := api.BackupFrame{Done: true}
	var err error
	if q.Action == "prepare" {
		spec := q.Spec
		loc := fileindex.Location{Addr: spec.Address, Root: spec.Root, Path: spec.Path}
		var target engine.Target = fileindex.NewTarget(loc, spec.Token)
		if spec.Mode == engine.Archive {
			target = fileindex.NewArchive(loc, spec.Token, spec.ArchiveName, spec.ArchivePrefix, spec.Compress)
		}
		var plan *engine.Plan
		plan, err = engine.Prepare(r.Context(), engine.Options{Source: string(spec.Source), Mode: spec.Mode, Excludes: spec.Excludes, Only: spec.Only, Dest: target}, func(p engine.ScanProgress) { send(api.BackupFrame{Scan: &p}) })
		if err == nil {
			var id [16]byte
			_, err = rand.Read(id[:])
			if err == nil {
				m := s.backups
				m.mu.Lock()
				for key, v := range m.plans {
					if (!v.started || v.outcome != nil || v.failure != nil) && time.Since(v.used) > 30*time.Minute {
						delete(m.plans, key)
					}
				}
				if len(m.plans) >= 64 {
					var oldest string
					var used time.Time
					for key, v := range m.plans {
						if (!v.started || v.outcome != nil || v.failure != nil) && (oldest == "" || v.used.Before(used)) {
							oldest, used = key, v.used
						}
					}
					if oldest != "" {
						delete(m.plans, oldest)
					}
				}
				if len(m.plans) >= 64 {
					err = errors.New("too many active backup plans")
				} else {
					done.ID = hex.EncodeToString(id[:])
					m.plans[done.ID] = &backupPlan{plan: plan, used: time.Now()}
					view := *plan
					view.Options = plan.Options
					view.Options.Dest = nil
					view.Options.Source = plan.Source()
					done.Plan = &view
				}
				m.mu.Unlock()
			}
		}
	} else {
		m := s.backups
		m.mu.Lock()
		p := m.plans[q.ID]
		switch {
		case p == nil:
			err = &api.Error{Code: "plan_expired", Message: "backup preview expired; prepare again"}
		case p.failure != nil:
			err = p.failure
		case p.outcome != nil:
			done.Outcome = p.outcome
		case p.started:
			err = &api.Error{Code: "conflict", Message: "backup already started; do not submit again"}
		default:
			p.started = true
		}
		m.mu.Unlock()
		if err == nil && done.Outcome == nil {
			ctx := r.Context()
			begun := false
			err = func() error {
				select {
				case s.writer.gate <- struct{}{}:
					defer func() { <-s.writer.gate }()
				case <-ctx.Done():
					return ctx.Err()
				}
				record := struct{ Phase, ID, Source string }{"backup_begin", q.ID, p.plan.Source()}
				b, _ := json.Marshal(record)
				if _, err := s.writer.log.Append(b); err != nil {
					return err
				}
				begun = true
				result := engine.Execute(ctx, p.plan, engine.Hooks{Progress: func(v engine.Progress) { send(api.BackupFrame{Progress: &v}) }, Done: func(a engine.Action, e error) {
					f := api.BackupFrame{Action: &a}
					if e != nil {
						f.ActionError = e.Error()
					}
					send(f)
				}})
				if archive, ok := p.plan.Options.Dest.(*fileindex.Archive); ok {
					result.SavedPath = archive.Saved()
				}
				outcome := api.EncodeBackupOutcome(result)
				b, _ = json.Marshal(struct {
					Phase, ID string
					Outcome   *api.BackupOutcome
				}{"backup_end", q.ID, outcome})
				if _, err := s.writer.log.Append(b); err != nil {
					return &api.Error{Code: "outcome_unknown", Message: err.Error()}
				}
				m.mu.Lock()
				p.outcome = outcome
				p.plan = nil
				m.mu.Unlock()
				done.Outcome = outcome
				return nil
			}()
			if err != nil {
				m.mu.Lock()
				if !begun {
					p.started = false
				} else {
					p.failure = &api.Error{Code: "outcome_unknown", Message: err.Error()}
					p.plan = nil
				}
				p.used = time.Now()
				m.mu.Unlock()
			}
		}
	}
	if err != nil {
		done.Error = &api.Error{Code: "io", Message: err.Error()}
		var known *api.Error
		if errors.As(err, &known) {
			done.Error = known
		}
		if errors.Is(err, fileindex.ErrAuth) {
			done.Error.Code = "authentication_required"
		}
		if errors.Is(err, context.Canceled) {
			done.Error.Code = "canceled"
		}
	}
	send(done)
}
