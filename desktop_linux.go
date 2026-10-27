package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Wayland compositors show the icon of a window as its desktop entry
// gives it, found by the window's app_id, "filesync": GTK 3 does not hand
// them the window's own. The Debian package and install.sh register the
// entry; run as it was built, unpacked or copied, the app registers one of
// its own in ~/.local/share, running it where it is, so that its windows
// and the applications menu show its icon. An entry the app did not write
// is left alone, as is a path a desktop entry cannot hold.

// desktopMark marks the entries the app wrote, which it may write again as
// it moves.
const desktopMark = "X-FileSync-Registered=true"

func registerDesktopEntry() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	// Not the binaries of go run and go test.
	if strings.Contains(exe, string(filepath.Separator)+"go-build") || strings.HasSuffix(exe, ".test") {
		return nil
	}
	return registerDesktopEntryOf(exe)
}

// registerDesktopEntryOf registers the entry of the app at exe.
func registerDesktopEntryOf(exe string) error {
	if strings.ContainsAny(exe, "\"`$\\\n%") {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	dataDirs := os.Getenv("XDG_DATA_DIRS")
	if dataDirs == "" {
		dataDirs = "/usr/local/share:/usr/share"
	}
	entry := filepath.Join(dataHome, "applications", "filesync.desktop")
	// Installed: the entry of the package or of install.sh stays.
	for _, dir := range append([]string{dataHome}, filepath.SplitList(dataDirs)...) {
		b, err := os.ReadFile(filepath.Join(dir, "applications", "filesync.desktop"))
		if err == nil && !bytes.Contains(b, []byte(desktopMark)) {
			return nil
		}
	}
	// The icon: beside the app as mygo build leaves it, else a copy.
	icon := filepath.Join(filepath.Dir(exe), "filesync.png")
	if _, err := os.Stat(icon); err != nil {
		icon = filepath.Join(dataHome, "filesync", "filesync.png")
		if old, err := os.ReadFile(icon); err != nil || !bytes.Equal(old, appIcon) {
			if err := os.MkdirAll(filepath.Dir(icon), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(icon, appIcon, 0o644); err != nil {
				return err
			}
		}
	}
	want := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=FileSync
Comment=fileindex 客户端：浏览、搜索和管理 NAS 上的文件
Exec="%s"
Icon=%s
Categories=Utility;
Terminal=false
StartupWMClass=filesync
%s
`, exe, icon, desktopMark)
	if old, err := os.ReadFile(entry); err == nil && string(old) == want {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		return err
	}
	tmp := entry + ".tmp"
	if err := os.WriteFile(tmp, []byte(want), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, entry)
}
