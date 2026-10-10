package places

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	kioFuseService   = "org.kde.KIOFuse"
	kioFusePath      = "/org/kde/KIOFuse"
	kioFuseInterface = "org.kde.KIOFuse.VFS"
)

// mountURL asks kio-fuse for the directory of a URL, mounting it. It may
// take a while, and KDE may ask for the password.
func mountURL(ctx context.Context, u string) (string, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return "", fmt.Errorf("无法连接桌面会话总线，不能打开 %s: %w", u, err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var dir string
	err = conn.Object(kioFuseService, kioFusePath).CallWithContext(ctx, kioFuseInterface+".mountUrl", 0, u).Store(&dir)
	if err != nil {
		var derr dbus.Error
		if errors.As(err, &derr) && strings.Contains(derr.Name, "ServiceUnknown") {
			return "", fmt.Errorf("打开 %s 需要 KDE 的 kio-fuse，请先安装 kio-fuse 软件包", decodeURL(u))
		}
		return "", fmt.Errorf("无法打开 %s: %w", decodeURL(u), err)
	}
	return dir, nil
}

// remoteURL returns the URL of a directory of kio-fuse.
func remoteURL(ctx context.Context, dir string) (string, bool) {
	if !isKioFuse(dir) {
		return "", false
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return "", false
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var u string
	if conn.Object(kioFuseService, kioFusePath).CallWithContext(ctx, kioFuseInterface+".remoteUrl", 0, dir).Store(&u) != nil || u == "" {
		return "", false
	}
	return u, true
}

func runtimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return d
	}
	return fmt.Sprintf("/run/user/%d", os.Getuid())
}

func isKioFuse(dir string) bool {
	return strings.HasPrefix(dir, filepath.Join(runtimeDir(), "kio-fuse-"))
}
