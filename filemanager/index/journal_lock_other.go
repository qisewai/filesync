//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package index

import (
	"errors"
	"os"
)

func lockJournal(*os.File) error {
	return errors.New("此系统尚不支持安全的索引日志文件锁")
}

func syncJournalDirectory(string) error {
	return errors.New("此系统尚不支持安全的索引日志目录同步")
}
