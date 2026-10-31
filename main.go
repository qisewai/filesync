// FileSync backs up directories of this computer to a NAS, through the
// fileindex server running there, in two modes: incremental, which adds and
// updates files, and mirror, which also moves to the server's trash what
// the source no longer has.
package main

import (
	"context"
	"filemanager/localprocess"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	localindex "filemanager/index"
	"filesync/config"

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
	// Keep this session's helper even if the updater replaces the installed bundle.
	backendExe, cleanupHelper, helperErr := prepareCompanion()
	defer cleanupHelper()
	sessionCompanion = backendExe
	backendCtx, backendCancel := context.WithTimeout(context.Background(), 15*time.Second)
	var backend *localprocess.Process
	backendErr := helperErr
	if backendErr == nil {
		backend, backendErr = localprocess.StartWritable(backendCtx, backendExe, "/", filepath.Join(dir, "filemanager", "mutations.log"))
	}
	backendCancel()
	if backendErr != nil {
		a.backendErr = fmt.Errorf("本机后台启动失败，请检查配套 filemanager 程序：%w", backendErr)
		a.errorMsg = a.backendErr.Error()
	} else {
		a.backend = backend.Client
		defer backend.Close()
	}
	defer func() {
		a.stopBackground()
		if a.transferHistory != nil {
			if err := a.transferHistory.wait(); err != nil {
				log.Printf("保存传输记录失败：%v", err)
			}
		}
	}()
	a.localCachePath, err = localindex.DefaultSnapshotPath()
	a.localOpLogRequired = true
	if err != nil {
		a.errorMsg = "无法确定本机索引快照位置：" + err.Error()
	}
	// Relaunch runs inside MyGo's shutdown, before Run returns and defers run.
	mygo.App.OnQuit(func() {
		a.stopBackground()
		if a.transferHistory != nil {
			_ = a.transferHistory.wait()
		}
		if backend != nil {
			_ = backend.Close()
		}
		cleanupHelper()
	})
	a.useUpdater()
	a.plainText = x11TextScale()

	// Schemes go before the windows that load them.
	if err := mygo.Protocol.Handle(docScheme, &a.docs); err != nil {
		log.Printf("文档预览不可用：%v", err)
	}
	// Before the window: the compositor finds its icon by the entry as it
	// maps it.
	if err := registerDesktopEntry(); err != nil {
		log.Printf("登记桌面条目失败：%v", err)
	}
	mygo.App.WhenReady(func() {
		var win *mygo.Window
		opts := mygo.WindowOptions{
			Title:     "FileSync · fileindex 客户端",
			Width:     1180,
			Height:    760,
			MinWidth:  900,
			MinHeight: 480,
			StateKey:  "main",
			Content:   ui.View(a.view),
		}
		// The toolbar takes the place of the title bar, unless the settings
		// keep the title bar. On Windows the system's window controls go
		// over it. On Linux the window goes frameless, and the toolbar draws
		// the controls: KWin and other Wayland compositors decorate a window
		// whose title bar is only hidden, and draw no title bar only for one
		// that says it decorates itself.
		if !a.store.SystemTitleBar {
			a.hiddenTitleBar = true
			if runtime.GOOS == "linux" {
				opts.Frameless = true
				a.ownControls = true
			} else {
				opts.TitleBarStyle = mygo.TitleBarHidden
			}
		}
		win = mygo.NewWindow(opts)
		if err := win.SetIcon(appIcon); err != nil {
			log.Printf("设置窗口图标失败：%v", err)
		}
		// The buttons of the toolbar act once the frame they are clicked in
		// is drawn: MyGo's OpenGL drawing aborts the process when the window
		// hides while it draws a frame.
		a.minimizeWindow = func() { go win.Minimize() }
		a.maximizeWindow = func(on bool) {
			if on {
				go win.Maximize()
			} else {
				go win.Unmaximize()
			}
		}
		a.closeButton = func() { go win.Close() } // as the system's button: to the tray, or asking
		win.OnMaximize(func() { a.post(func() { a.maximized = true }) })
		win.OnUnmaximize(func() { a.post(func() { a.maximized = false }) })
		win.OnEnterFullScreen(func() { a.post(func() { a.maximized = true }) })
		win.OnLeaveFullScreen(func() { a.post(func() { a.maximized = false }) })
		// The tray follows the work posted, which goes on while the window
		// is hidden and draws no frames.
		a.post = func(fn func()) {
			win.Update(func() {
				fn()
				a.syncTray()
			})
		}
		a.initializeOperationLog()
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
			go win.Close() // after the frame, as the toolbar's buttons
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
