package main

import (
	"fmt"
	"path/filepath"

	"filesync/localindex"
)

// operationLog is called on the UI thread; background tasks receive the
// returned concurrency-safe handle. An unset cache path is used by tests and
// by installations where no local index location is available.
func (a *app) operationLog() (*localindex.OperationLog, error) {
	if a.localOpLogLoading {
		return nil, fmt.Errorf("正在校验本机文件操作日志，请稍后重试")
	}
	if a.closed {
		return nil, fmt.Errorf("程序正在退出")
	}
	if a.localOpLogErr != nil {
		err := a.localOpLogErr
		if a.localOpLogRequired {
			a.initializeOperationLog()
		}
		return nil, err
	}
	if a.localCachePath == "" {
		if a.localOpLogRequired {
			return nil, fmt.Errorf("无法确定本机文件操作日志位置")
		}
		return nil, nil
	}
	if a.localOpLog != nil {
		return a.localOpLog, nil
	}
	if a.closed {
		return nil, fmt.Errorf("程序正在退出")
	}
	l, err := localindex.OpenOperationLog(a.localCachePath + ".operations")
	if err != nil {
		return nil, fmt.Errorf("无法打开本机文件操作日志: %w", err)
	}
	a.localOpLog = l
	a.connectLocalOperationLog(l)
	return l, nil
}

// loggedLocalOperation writes intent before changing disk, and writes the
// outcome afterwards. A failed or interrupted operation may be partial. The
// second error means the filesystem was already touched, but the outcome
// could not be recorded; its unfinished intent remains for reconciliation.
func loggedLocalOperation(log *localindex.OperationLog, action, source, target string, fn func() error) (opErr, logErr error) {
	if log == nil {
		return fn(), nil
	}
	abs := func(p string) (string, error) {
		if p == "" {
			return "", nil
		}
		return filepath.Abs(p)
	}
	var err error
	if source, err = abs(source); err != nil {
		return err, nil
	}
	if target, err = abs(target); err != nil {
		return err, nil
	}
	id, err := log.Begin(action, source, target)
	if err != nil {
		return fmt.Errorf("无法记录文件操作，未执行: %w", err), nil
	}
	opErr = fn()
	if err := log.Finish(id, opErr); err != nil {
		logErr = fmt.Errorf("文件操作已执行，但日志结果写入失败；请检查文件并更新本机索引: %w", err)
	}
	return opErr, logErr
}

// Startup replay runs off the UI thread, before any operation can get a handle.
func (a *app) initializeOperationLog() {
	if a.localCachePath == "" || a.localOpLog != nil || a.localOpLogLoading {
		return
	}
	a.localOpLogLoading = true
	filename := a.localCachePath + ".operations"
	go func() {
		l, err := localindex.OpenRecoverableOperationLog(filename)
		a.post(func() {
			a.localOpLogLoading = false
			if a.closed {
				if l != nil {
					l.Close()
				}
				return
			}
			a.localOpLog, a.localOpLogErr = l, err
			if l != nil {
				a.connectLocalOperationLog(l)
			}
			if err != nil {
				a.errorMsg = "无法打开本机文件操作日志：" + err.Error()
			} else if l.RecoveryNeeded() {
				a.errorMsg = "文件操作日志需要恢复，请在本机索引中选择全部重建；原日志将保留副本"
			}
		})
	}()
}
