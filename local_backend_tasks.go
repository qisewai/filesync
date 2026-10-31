package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"filemanager/localpath"
	"fmt"
	"time"

	"filemanager/api"
)

func backendLocation(p string) api.Location {
	return localpath.Location(p)
}

// Keep the index bridge's intent open until the backend reports a terminal
// outcome. Canceling a UI context sends cancellation, then waits for cleanup.
func (a *app) backendTransfer(ctx context.Context, from, to string, move, replace bool, cache string, progress func(int64, string), estimate func(int64)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.backend == nil {
		return errors.New("本机后台不可用")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	q := api.TransferRequest{ID: hex.EncodeToString(id[:]), Source: backendLocation(from), Target: backendLocation(to), Move: move, FileConflict: "fail", DirectoryConflict: "fail"}
	if replace {
		q.FileConflict = "replace"
		q.DirectoryConflict = "merge"
	}
	if cache != "" {
		for _, p := range []string{cache, cache + ".operations", cache + ".operations.lock"} {
			q.Protected = append(q.Protected, backendLocation(p))
		}
	}
	callCtx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	state, err := a.backend.SubmitTransfer(callCtx, q)
	cancel()
	if err != nil {
		cleanup, stop := context.WithTimeout(context.Background(), requestTimeout)
		a.backend.CancelTask(cleanup, q.ID)
		stop()
		return fmt.Errorf("提交后台任务失败，请刷新检查文件状态（任务 %s）：%w", q.ID, err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var previous, total int64
	canceled := false
	for {
		estimate(state.Total - total)
		total = state.Total
		progress(state.Done-previous, state.Current)
		previous = state.Done
		if state.Finished() {
			switch state.State {
			case "done":
				return nil
			case "canceled":
				return context.Canceled
			default:
				return fmt.Errorf("后台任务 %s：%s", state.ID, state.Error)
			}
		}
		if !canceled && ctx.Err() != nil {
			callCtx, cancel = context.WithTimeout(context.Background(), requestTimeout)
			state, err = a.backend.CancelTask(callCtx, q.ID)
			cancel()
			if err != nil {
				return fmt.Errorf("取消后台任务失败，请检查任务 %s：%w", q.ID, err)
			}
			canceled = true
			continue
		}
		if canceled {
			<-ticker.C
		} else {
			select {
			case <-ticker.C:
			case <-ctx.Done():
				continue
			}
		}
		callCtx, cancel = context.WithTimeout(context.Background(), requestTimeout)
		state, err = a.backend.Task(callCtx, q.ID)
		cancel()
		if err != nil {
			cleanup, stop := context.WithTimeout(context.Background(), requestTimeout)
			a.backend.CancelTask(cleanup, q.ID)
			stop()
			return fmt.Errorf("无法查询后台任务 %s，请刷新检查文件状态：%w", q.ID, err)
		}
	}
}
