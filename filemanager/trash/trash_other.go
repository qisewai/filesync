//go:build !linux && !windows

package trash

import "errors"

func move(string) error { return errors.New("这个系统上还不支持移入回收站") }
