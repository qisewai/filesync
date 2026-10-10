package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"filesync/config"
	"filesync/engine"
)

// runLog is the text log of a backup, one line for each change.
type runLog struct {
	path string
	f    *os.File
	w    *bufio.Writer
}

func openRunLog(dir string, task *config.Task, p *engine.Plan) (*runLog, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("%s-%s.log", task.ID, time.Now().Format("20060102-150405"))
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	l := &runLog{path: path, f: f, w: bufio.NewWriter(f)}
	s := p.Summary
	fmt.Fprintf(l.w, "任务: %s\n开始: %s\n模式: %s\n源目录: %s\n目标目录: %s\n",
		task.Name, time.Now().Format("2006-01-02 15:04:05"), task.Mode, task.Source, task.Target)
	fmt.Fprintf(l.w, "计划: 新增 %d，更新 %d，新建目录 %d，删除 %d，删除目录 %d，跳过 %d，共需复制 %s\n",
		s.Create, s.Update, s.Mkdir, s.Delete, s.Rmdir, s.Skip, formatBytes(s.CopyBytes))
	for _, w := range p.Warnings {
		fmt.Fprintf(l.w, "注意: %s\n", w)
	}
	for _, a := range p.Actions {
		if a.Kind == engine.ActSkip {
			fmt.Fprintf(l.w, "[跳过] %s（%s）\n", a.Path, a.Reason)
		}
	}
	fmt.Fprintln(l.w)
	return l, nil
}

func (l *runLog) action(a engine.Action, err error) {
	if err != nil {
		fmt.Fprintf(l.w, "[错误] %s %s: %v\n", a.Kind, a.Path, err)
		return
	}
	fmt.Fprintf(l.w, "[%s] %s\n", a.Kind, a.Path)
}

func (l *runLog) finish(r *engine.Result) error {
	fmt.Fprintln(l.w)
	switch {
	case r.Fatal != nil:
		fmt.Fprintf(l.w, "失败: %v\n", r.Fatal)
	case r.Canceled:
		fmt.Fprintln(l.w, "已被用户停止")
	}
	fmt.Fprintf(l.w, "结束: %s，用时 %s\n", r.Finished.Format("2006-01-02 15:04:05"), formatDuration(r.Finished.Sub(r.Started).Seconds()))
	fmt.Fprintf(l.w, "结果: 新增 %d，更新 %d，新建目录 %d，删除 %d，删除目录 %d，清理临时文件 %d，复制 %s，错误 %d\n",
		r.Created, r.Updated, r.Mkdirs, r.Deleted, r.Rmdirs, r.Cleaned, formatBytes(r.Bytes), len(r.Errors))
	if r.TimesLost > 0 {
		fmt.Fprintf(l.w, "注意: 目标没有保留 %d 个文件的修改时间\n", r.TimesLost)
	}
	err := l.w.Flush()
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	return err
}
