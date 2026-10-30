package main

import (
	"context"
	"errors"
	"slices"
	"strings"

	localindex "filemanager/index"
)

// UI state stays on the UI thread; filesystem reconciliation runs in one
// background batch per index. Finishes arriving mid-batch queue the next batch.
func (a *app) connectLocalOperationLog(log *localindex.OperationLog) {
	log.SetOnChange(func(paths []string) {
		a.post(func() {
			if a.closed {
				return
			}
			a.ensureLocalIndex()
			a.localIdx.livePaths = append(a.localIdx.livePaths, paths...)
			a.startLiveIndex()
		})
	})
	a.ensureLocalIndex()
	a.localIdx.liveReplay = true
	a.startLiveIndex()
}

func (a *app) startLiveIndex() {
	m := a.localIdx
	if a.closed || m == nil || m.loading || m.running || m.liveRunning {
		return
	}
	if !m.liveReplay && len(m.livePaths) == 0 {
		return
	}
	log := a.localOpLog
	if log == nil || log.RecoveryNeeded() {
		return
	}
	paths, replay := m.livePaths, m.liveReplay
	m.livePaths, m.liveReplay = nil, false
	m.liveRunning = true
	ctx, cancel := context.WithCancel(context.Background())
	m.liveCancel = cancel
	x, filename := m.index, a.localCachePath
	opts := localindex.ScanOptions{Roots: slices.Clone(m.settings.Roots), Excludes: slices.Clone(m.settings.Excludes)}
	go func() {
		defer cancel()
		opts.Skip = indexSkipPaths(opts.Roots)
		var err error
		if replay {
			var previous []string
			var mismatch bool
			previous, mismatch, err = log.ReplayPaths(ctx, filename, opts)
			paths = append(paths, previous...)
			paths = append(paths, x.RefreshPaths()...)
			if mismatch {
				paths = append(paths, opts.Roots...)
			}
		}
		if err == nil {
			err = x.Reconcile(ctx, opts, paths)
		}

		a.post(func() {
			if a.closed || a.localIdx != m || m.index != x {
				return
			}
			m.liveRunning = false
			m.liveCancel = nil
			if err != nil {
				// Keep retry information, but don't spin on unreadable directories.
				m.livePaths = append(m.livePaths, paths...)
				m.liveReplay = m.liveReplay || replay
				if !errors.Is(err, context.Canceled) {
					m.err = "实时索引更新失败，可重试更新索引：" + err.Error()
				}
				return
			}
			status := x.Status()
			m.status = status
			if !m.running && !m.loading {
				if strings.HasPrefix(m.err, "实时索引更新失败") {
					m.err = ""
				}
				if status.Delta {
					m.note = "已合并本机文件操作变更"
				}
				a.indexChanged()
			}
			a.startLiveIndex()
		})
	}()
}
