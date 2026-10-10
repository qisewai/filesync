//go:build !linux

package places

import (
	"context"
	"errors"
)

func mountURL(ctx context.Context, u string) (string, error) {
	return "", errors.New("这个系统不能直接打开 " + decodeURL(u) + "。请使用 \\\\服务器\\共享 这样的路径，或映射的网络驱动器")
}

func remoteURL(ctx context.Context, dir string) (string, bool) { return "", false }
