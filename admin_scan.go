package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"filesync/fileindex"
)

func (a *app) startScan(m *adminPage, r fileindex.Root, rebuild bool) {
	if a.hasScanTransfer(m.server, r.ID) {
		return
	}
	sv := a.store.Server(m.server)
	if sv == nil || sv.Token == "" {
		return
	}
	c := fileindex.New(sv.Addr, sv.Token)
	server := m.server
	kind := "扫描"
	if rebuild {
		kind = "校验"
	}
	tr := a.addTransfer(kind, r.Name)
	tr.server = server
	tr.scanRoot = r.ID
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	tr.cancel = cancel
	go func() {
		defer cancel()
		job, err := c.StartScan(ctx, r.ID, rebuild)
		a.post(func() {
			if err != nil {
				a.finishTransfer(tr, err, nil)
				if !a.sourceCurrent(server, c) {
					return
				}
				if errors.Is(err, fileindex.ErrAuth) {
					a.signedOut(sv)
					return
				}
				if a.admin == m {
					m.err = err.Error()
				}
				a.adminRefreshSoon(server)
				return
			}
			tr.after = func() {
				if !a.sourceCurrent(server, c) {
					return
				}
				a.invalidateTree(server, r.ID)
				if a.br != nil && a.br.server == server && a.br.root.ID == r.ID {
					a.reloadKeeping()
				}
				a.adminRefreshSoon(server)
			}
			a.trackScan(c, tr, job)
		})
	}()
}
func (a *app) applyScan(server string, j *fileindex.ScanJob, current string) {
	if j == nil {
		return
	}
	for i, r := range a.roots[server] {
		if r.ID != j.RootID {
			continue
		}
		// Ignore delayed events from an older job after a newer scan was observed.
		if r.Active != nil && r.Active.Job != nil && r.Active.Job.ID > j.ID || r.LastJob != nil && r.LastJob.ID > j.ID {
			break
		}
		if r.LastJob != nil && r.LastJob.ID == j.ID && r.LastJob.Finished() && !j.Finished() {
			break
		}
		if !j.Finished() && r.Active != nil && r.Active.Job != nil && r.Active.Job.ID == j.ID {
			if r.Active.Job.Scanned > j.Scanned {
				copy := *r.Active.Job
				j = &copy
			}
			if current == "" {
				current = r.Active.Current
			}
		}
		if j.Finished() {
			r.Active = nil
			r.LastJob = j
		} else {
			r.Active = &fileindex.ScanEvent{Job: j, Current: current}
		}
		a.roots[server][i] = r
		break
	}
	for _, tr := range a.transfers {
		if tr.server != server || tr.scanRoot != j.RootID || tr.scanJob != j.ID || !tr.running() {
			continue
		}
		a.applyScanTransfer(tr, j, current)
	}
	if j.Finished() {
		a.adminRefreshSoon(server)
	}
}
func (a *app) applyScanTransfer(tr *transfer, j *fileindex.ScanJob, current string) {
	if !tr.running() {
		return
	}
	tr.files = int(j.Scanned)
	tr.current = current
	if !j.Finished() {
		return
	}
	tr.note = fmt.Sprintf("扫描 %d · 新增 %d · 更新 %d · 删除 %d · 错误 %d", j.Scanned, j.Added, j.Updated, j.Deleted, j.Errors)
	var err error
	if j.Status == "canceled" {
		err = context.Canceled
	} else if j.Status == "failed" {
		message := j.Message
		if message == "" {
			message = "服务器扫描失败"
		}
		err = errors.New(message)
	}
	a.finishTransfer(tr, err, nil)
}
func (a *app) trackedScan(c *fileindex.Client, tr *transfer, j *fileindex.ScanJob) {
	if a.sourceCurrent(tr.server, c) {
		a.applyScan(tr.server, j, "")
	} else {
		// A task keeps its original client so it can still finish or be canceled
		// after the user edits a server. Its snapshots cannot update the new source.
		a.applyScanTransfer(tr, j, "")
	}
}

func (a *app) trackScan(c *fileindex.Client, tr *transfer, j *fileindex.ScanJob) {
	tr.scanJob = j.ID
	a.saveTransferHistory()
	a.trackedScan(c, tr, j)
	if !tr.running() {
		return
	}
	ctx, stop := context.WithCancel(context.Background())
	tr.stop = stop
	root, jobID := tr.scanRoot, tr.scanJob
	tr.cancel = func() {
		go func() {
			req, cancel := context.WithTimeout(ctx, requestTimeout)
			defer cancel()
			jobs, err := c.ScanJobs(req, root)
			var target *fileindex.ScanJob
			if err == nil {
				for i := range jobs {
					if jobs[i].ID == jobID {
						target = &jobs[i]
						break
					}
				}
				if target == nil {
					err = fmt.Errorf("原扫描已不在记录中，未取消其他扫描")
				} else if !target.Finished() {
					err = c.CancelScan(req, root)
				}
			}
			a.post(func() {
				if target != nil && target.Finished() {
					a.trackedScan(c, tr, target)
					return
				}
				if tr.running() && err != nil {
					tr.current = "取消失败：" + err.Error()
					if a.quitWhenDone {
						a.finishTransfer(tr, fmt.Errorf("无法取消服务器扫描，任务可能继续运行：%w", err), nil)
					}
				}
			})
		}()
	}
	if a.quitWhenDone {
		tr.cancel()
	}
	go func() {
		for {
			req, cancel := context.WithTimeout(ctx, requestTimeout)
			jobs, err := c.ScanJobs(req, root)
			cancel()
			if err == nil {
				a.post(func() {
					if a.closed {
						return
					}
					for i := range jobs {
						if jobs[i].ID == jobID {
							a.trackedScan(c, tr, &jobs[i])
							return
						}
					}
				})
			} else if errors.Is(err, fileindex.ErrAuth) {
				a.post(func() {
					if tr.running() {
						a.finishTransfer(tr, err, nil)
					}
				})
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}()
}
func (a *app) cancelRootScan(m *adminPage, r fileindex.Root) {
	if r.Active == nil || r.Active.Job == nil {
		return
	}
	for _, tr := range a.transfers {
		if tr.server == m.server && tr.scanRoot == r.ID && tr.scanJob == r.Active.Job.ID && tr.running() {
			tr.cancel()
			return
		}
	}
	sv := a.store.Server(m.server)
	if sv == nil {
		return
	}
	tr := a.addTransfer("扫描", r.Name)
	tr.server = m.server
	tr.scanRoot = r.ID
	a.trackScan(fileindex.New(sv.Addr, sv.Token), tr, r.Active.Job)
	if tr.running() && tr.cancel != nil {
		tr.cancel()
	}
}

func (a *app) hasScanTransfer(server string, root int64) bool {
	for _, tr := range a.transfers {
		if tr.server == server && tr.scanRoot == root && tr.running() {
			return true
		}
	}
	return false
}
