package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// History contains data only: never callbacks, session tokens or proxy URLs.
type transferRecord struct {
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	State       string    `json:"state"`
	Error       string    `json:"error,omitempty"`
	Note        string    `json:"note,omitempty"`
	Errors      []string  `json:"errors,omitempty"`
	Started     time.Time `json:"started"`
	Finished    time.Time `json:"finished,omitempty"`
	Done        int64     `json:"done,omitempty"`
	Total       int64     `json:"total,omitempty"`
	Files       int       `json:"files,omitempty"`
	Directory   string    `json:"directory,omitempty"`
	File        string    `json:"file,omitempty"`
	FolderReady bool      `json:"folder_ready,omitempty"`
	Server      string    `json:"server,omitempty"`
	Peer        string    `json:"peer,omitempty"`
	Task        int64     `json:"task,omitempty"`
	ScanRoot    int64     `json:"scan_root,omitempty"`
	ScanJob     int64     `json:"scan_job,omitempty"`
}
type transferHistoryFile struct {
	Version int              `json:"version"`
	Items   []transferRecord `json:"items"`
}

func (a *app) transferRecords() []transferRecord {
	records := make([]transferRecord, 0, len(a.transfers))
	for _, tr := range a.transfers {
		records = append(records, transferRecord{Kind: tr.kind, Title: tr.title, State: tr.state, Error: tr.err, Note: tr.note, Errors: slices.Clone(tr.errors), Started: tr.started, Finished: tr.finished, Done: tr.done, Total: tr.total, Files: tr.files, Directory: tr.localDir, File: tr.localFile, FolderReady: tr.folderReady, Server: tr.server, Peer: tr.peer, Task: tr.task, ScanRoot: tr.scanRoot, ScanJob: tr.scanJob})
	}
	return records
}
func (a *app) loadTransferHistory() {
	if a.store.Dir == "" {
		return
	}
	filename := filepath.Join(a.store.Dir, "transfers.json")
	b, err := os.ReadFile(filename)
	var data transferHistoryFile
	if err == nil {
		err = json.Unmarshal(b, &data)
		if err == nil && data.Version != 1 {
			err = fmt.Errorf("不支持的传输记录版本 %d", data.Version)
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// Keep the original bytes before allowing a new history to be written.
		backup := filename + ".broken-" + time.Now().Format("20060102-150405.000000000")
		info, statErr := os.Lstat(filename)
		if statErr != nil || !info.Mode().IsRegular() {
			a.errorMsg = "无法读取传输记录，原文件保留，历史保存暂停：" + err.Error()
			return
		}
		if moveErr := os.Rename(filename, backup); moveErr != nil {
			a.errorMsg = "无法读取传输记录，原文件保留，历史保存暂停：" + err.Error()
			return
		}
		a.errorMsg = "无法读取传输记录，原文件已保留为 " + filepath.Base(backup) + "：" + err.Error()
		data.Items = nil
	}
	for _, r := range data.Items {
		a.transferSeq++
		tr := &transfer{id: a.transferSeq, kind: r.Kind, title: r.Title, state: r.State, err: r.Error, note: r.Note, errors: r.Errors, started: r.Started, finished: r.Finished, done: r.Done, total: r.Total, files: r.Files, localDir: r.Directory, localFile: r.File, folderReady: r.FolderReady, server: r.Server, peer: r.Peer, task: r.Task, scanRoot: r.ScanRoot, scanJob: r.ScanJob}
		switch tr.state {
		case "done", "failed", "canceled", "interrupted":
		default:
			tr.state = "interrupted"
			tr.err = "客户端上次退出时未完成，未自动继续"
			if tr.task > 0 || tr.scanJob > 0 {
				tr.err = "客户端上次退出时未结束；服务器任务可能仍在运行"
			}
		}
		a.transfers = append(a.transfers, tr)
	}
	a.transferHistory = newTransferHistoryWriter(filename)
}
func (a *app) saveTransferHistory() {
	if a.transferHistory == nil {
		return
	}
	// Capture an immutable snapshot and the initialized UI dispatcher. The
	// writer never touches app state or a live transfer from its goroutine.
	post := a.post
	a.transferHistory.enqueue(a.transferRecords(), func(err error) {
		post(func() {
			if !a.closed {
				a.errorMsg = "无法保存传输记录：" + err.Error()
			}
		})
	})
}

type historySnapshot struct {
	records    []transferRecord
	generation uint64
	report     func(error)
}
type transferHistoryWriter struct {
	filename          string
	mu                sync.Mutex
	cond              *sync.Cond
	pending           *historySnapshot
	generation, saved uint64
	err               error
	closed            bool
	done              chan struct{}
}

func newTransferHistoryWriter(filename string) *transferHistoryWriter {
	w := &transferHistoryWriter{filename: filename, done: make(chan struct{})}
	w.cond = sync.NewCond(&w.mu)
	go w.run()
	return w
}
func (w *transferHistoryWriter) enqueue(records []transferRecord, report func(error)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.generation++
	w.pending = &historySnapshot{records: records, generation: w.generation, report: report}
	w.cond.Signal()
}
func (w *transferHistoryWriter) run() {
	defer close(w.done)
	for {
		w.mu.Lock()
		for w.pending == nil && !w.closed {
			w.cond.Wait()
		}
		if w.pending == nil {
			w.mu.Unlock()
			return
		}
		snapshot := w.pending
		w.pending = nil
		w.mu.Unlock()
		err := writeTransferHistory(w.filename, snapshot.records)
		w.mu.Lock()
		w.saved, w.err = snapshot.generation, err
		w.cond.Broadcast()
		w.mu.Unlock()
		if err != nil && snapshot.report != nil {
			snapshot.report(err)
		}
	}
}

// flush is used outside UI callbacks to verify a completed write, or at
// process exit after the event loop stops. New snapshots can supersede old ones.
func (w *transferHistoryWriter) flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	target := w.generation
	for w.saved < target {
		w.cond.Wait()
	}
	return w.err
}
func (w *transferHistoryWriter) close() {
	w.mu.Lock()
	w.closed = true
	w.cond.Broadcast()
	w.mu.Unlock()
}
func (w *transferHistoryWriter) wait() error {
	w.close()
	<-w.done
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}
func writeTransferHistory(filename string, records []transferRecord) error {
	if records == nil {
		records = []transferRecord{}
	}
	b, err := json.MarshalIndent(transferHistoryFile{Version: 1, Items: records}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".transfers-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { f.Close(); os.Remove(tmp) }()
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filename)
}
