package main

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"time"

	"filesync/fileindex"
)

// Immutable task parameters persisted with history; sessions are always read
// from current settings at retry time. Address prevents retrying on a different
// NAS after the user edits a server entry.
type downloadResume struct {
	Addr    string `json:"addr"`
	Root    int64  `json:"root"`
	Path    string `json:"path"`
	Partial string `json:"partial"`
}

func (a *app) downloadDestinationBusy(dest string) bool {
	for _, tr := range a.transfers {
		if tr.running() && tr.localFile != "" && filepath.Clean(tr.localFile) == filepath.Clean(dest) {
			a.errorMsg = "此文件已有下载任务，请先等待完成或取消"
			return true
		}
	}
	return false
}

func (a *app) startResumableDownload(c *fileindex.Client, server string, entry fileindex.Entry, dir string) {
	dest, err := filepath.Abs(filepath.Join(dir, entry.Name))
	if err != nil {
		a.errorMsg = err.Error()
		return
	}
	if a.downloadDestinationBusy(dest) {
		return
	}
	tr := a.addTransfer("下载", entry.Name+" → "+dir)
	tr.server, tr.localFile, tr.localDir, tr.folderReady = server, dest, filepath.Dir(dest), true
	tr.total = entry.Size
	tr.download = &downloadResume{Addr: c.Addr, Root: entry.Root, Path: entry.Path,
		Partial: filepath.Join(filepath.Dir(dest), ".filesync-download-"+rand.Text()+".part")}
	a.runResumableDownload(c, tr)
}

func (a *app) resumeDownload(tr *transfer) {
	if tr.download == nil || tr.running() || tr.state == "done" || a.closed || a.quitWhenDone {
		return
	}
	c, ok := a.serverClient(tr.server)
	if !ok {
		a.errorMsg = "下载来源服务器已删除，请重新选择文件下载"
		return
	}
	if c.Addr != tr.download.Addr {
		a.errorMsg = "下载来源服务器地址已改变，请重新选择文件下载"
		return
	}
	if c.Token == "" {
		a.errorMsg = "请先登录来源服务器，再继续下载"
		return
	}
	start := func() {
		if !tr.running() && !a.closed && !a.quitWhenDone && !a.downloadDestinationBusy(tr.localFile) {
			a.runResumableDownload(c, tr)
		}
	}
	if _, err := os.Lstat(tr.localFile); err == nil {
		a.ask("替换本机同名的文件？", "下载完成后将替换 "+tr.localFile+"。", "替换", start)
	} else if os.IsNotExist(err) {
		start()
	} else {
		a.errorMsg = err.Error()
	}
}

func (a *app) runResumableDownload(c *fileindex.Client, tr *transfer) {
	operationLog, err := a.operationLog()
	if err != nil {
		a.finishTransfer(tr, err, nil)
		return
	}
	guardedClient := *c
	guardedClient.BeforeLocalWrite = operationLog.CheckWritePath
	c = &guardedClient
	ctx, cancel := context.WithCancel(context.Background())
	tr.state, tr.err, tr.note, tr.current = "running", "", "", ""
	tr.errors, tr.finished, tr.started = nil, time.Time{}, time.Now()
	tr.resumed = tr.done
	tr.cancel = cancel
	download, dest := *tr.download, tr.localFile
	first, lastSaved := true, time.Now()
	a.saveTransferHistory() // parameters must survive an exit before first byte
	history := a.transferHistory
	go func() {
		defer cancel()
		if history != nil {
			if err := history.flush(); err != nil {
				a.post(func() { a.finishTransfer(tr, err, nil) })
				return
			}
		}
		opErr, logErr := loggedLocalOperation(operationLog, "download", "", dest, func() error {
			return c.DownloadFileResumable(ctx, download.Root, download.Path, dest, download.Partial, func(p fileindex.Progress) {
				a.post(func() {
					if first {
						tr.resumed, first = p.Done, false
					}
					tr.done, tr.total, tr.current = p.Done, p.Total, p.Current
					if time.Since(lastSaved) >= time.Second {
						a.saveTransferHistory()
						lastSaved = time.Now()
					}
				})
			})
		})
		var failed []string
		if logErr != nil {
			failed = append(failed, logErr.Error())
		}
		a.post(func() { a.finishTransfer(tr, opErr, failed) })
	}()
}
