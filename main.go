// FileSync backs up directories of this computer to a NAS, through the
// fileindex server running there, in two modes: incremental, which adds and
// updates files, and mirror, which also moves to the server's trash what
// the source no longer has.
package main

import (
	"log"
	"os"
	"runtime"
	"strings"

	"filesync/config"
	"filesync/localindex"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// x11TextScale reports whether the desktop's text scale is to be left out.
// KDE on X11, scaled by a fraction, as 125%, tells GTK apps, which scale
// only by whole numbers there, to draw text that much larger instead; mygo
// then enlarges the usual text alone, not the sizes set nor the rest, which
// shows out of proportion. Left out, all shows as at 100%.
// FILESYNC_TEXT_SCALE=1 keeps it.
func x11TextScale() bool {
	x11 := os.Getenv("GDK_BACKEND") == "x11" || os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("DISPLAY") != ""
	return runtime.GOOS == "linux" && x11 && strings.Contains(os.Getenv("XDG_CURRENT_DESKTOP"), "KDE") && os.Getenv("FILESYNC_TEXT_SCALE") == ""
}

func main() {
	// On KDE, GTK's own file dialog knows nothing of the Places of Dolphin,
	// where the network shares are; the desktop portal shows KDE's dialog,
	// which has them, and opens a share chosen there through kio-fuse.
	if strings.Contains(os.Getenv("XDG_CURRENT_DESKTOP"), "KDE") && os.Getenv("GTK_USE_PORTAL") == "" {
		os.Setenv("GTK_USE_PORTAL", "1")
	}

	// On a Wayland desktop scaled by a fraction, as 125%, GTK on X11 (through
	// XWayland) draws at a whole scale of 1, and everything shows small;
	// on Wayland itself it draws at 2 and the compositor scales that to
	// 1.25. GDK_BACKEND=x11 is often inherited from the program FileSync
	// was started from: FILESYNC_X11=1 keeps it.
	if os.Getenv("WAYLAND_DISPLAY") != "" && os.Getenv("GDK_BACKEND") == "x11" && os.Getenv("FILESYNC_X11") == "" {
		os.Unsetenv("GDK_BACKEND")
	}

	dir, err := config.DefaultDir()
	if err != nil {
		log.Fatal(err)
	}
	store, err := config.Load(dir)
	if err != nil {
		log.Fatalf("无法读取设置 %s: %v", dir, err)
	}
	a := newApp(store)
	defer func() {
		a.stopBackground()
		if a.transferHistory != nil {
			if err := a.transferHistory.wait(); err != nil {
				log.Printf("保存传输记录失败：%v", err)
			}
		}
	}()
	a.localCachePath, err = localindex.DefaultSnapshotPath()
	if err != nil {
		a.errorMsg = "无法确定本机索引快照位置：" + err.Error()
	}
	a.plainText = x11TextScale()

	// Schemes go before the windows that load them.
	if err := mygo.Protocol.Handle(docScheme, &a.docs); err != nil {
		log.Printf("文档预览不可用：%v", err)
	}
	mygo.App.WhenReady(func() {
		var win *mygo.Window
		win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "FileSync · fileindex 客户端",
			Width:     1180,
			Height:    760,
			MinWidth:  900,
			MinHeight: 480,
			StateKey:  "main",
			Content:   ui.View(a.view),
		})
		// The tray follows the work posted, which goes on while the window
		// is hidden and draws no frames.
		a.post = func(fn func()) {
			win.Update(func() {
				fn()
				a.syncTray()
			})
		}
		a.ensureLocalIndex() // load only the persisted snapshot, never scan at startup
		a.pickDir = func(title, start string) (string, error) {
			paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
				Parent:      win,
				Title:       title,
				DefaultPath: start,
				Directory:   true,
			})
			if err != nil || len(paths) == 0 {
				return "", err
			}
			return paths[0], nil
		}
		a.pickFiles = func(title string) ([]string, error) {
			return mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win, Title: title, Multiple: true})
		}
		a.openPath = func(path string) {
			if err := mygo.Shell.OpenPath(path); err != nil {
				win.Update(func() { a.errorMsg = "无法打开 " + path + ": " + err.Error() })
			}
		}
		a.openURL = func(u string) {
			if err := mygo.Shell.OpenExternal(u); err != nil {
				win.Update(func() { a.errorMsg = "无法打开 " + u + ": " + err.Error() })
			}
		}
		a.newDocView = func() (ui.NativeWebView, func(string), error) {
			v, err := win.NewWebView(mygo.WebViewOptions{BackgroundColor: "#ffffff"})
			if err != nil {
				return nil, nil, err
			}
			// The page stays on its document: a link followed opens in
			// the browser instead.
			v.Page().OnWillNavigate(func(e *mygo.NavigateEvent) {
				if docNavigable(e.URL) {
					return
				}
				e.PreventDefault()
				if e.UserInitiated && (strings.HasPrefix(e.URL, "https://") || strings.HasPrefix(e.URL, "http://")) {
					a.openURL(e.URL)
				}
			})
			return v, func(u string) { v.Page().LoadURL(u) }, nil
		}
		a.keepAwake = func(reason string) func() { return mygo.Power.KeepAwake(reason, false) }
		quitting := false // the window closes to quit, not to the tray
		a.closeWindow = func() {
			a.quitWhenDone, a.job = false, nil
			quitting = true
			win.Close()
		}
		// Quitting, as the session ends or on SIGTERM, closes the window
		// rather than hiding it.
		mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { quitting = true })
		inTray := setUpTray(a, win, &quitting)
		hintShown := false
		win.OnClose(func(e *mygo.CloseEvent) {
			if inTray && !quitting && !a.store.QuitOnClose {
				e.PreventDefault()
				win.Hide()
				if !hintShown {
					hintShown = true
					showNotice(win, "FileSync 仍在后台运行", "可以从系统托盘的图标重新打开窗口或退出。")
				}
				return
			}
			quitting = false
			if !a.requestClose() {
				e.PreventDefault()
				win.Show()
				win.Invalidate()
			}
		})
	})
	if err := mygo.App.Run(); err != nil {
		a.stopBackground()
		if a.transferHistory != nil {
			a.transferHistory.wait()
		}
		log.Fatal(err)
	}
}
