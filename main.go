// FileSync backs up directories of this computer to a NAS, through the
// fileindex server running there, in two modes: incremental, which adds and
// updates files, and mirror, which also moves to the server's trash what
// the source no longer has.
package main

import (
	"log"
	"os"
	"strings"

	"filesync/config"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func main() {
	// On KDE, GTK's own file dialog knows nothing of the Places of Dolphin,
	// where the network shares are; the desktop portal shows KDE's dialog,
	// which has them, and opens a share chosen there through kio-fuse.
	if strings.Contains(os.Getenv("XDG_CURRENT_DESKTOP"), "KDE") && os.Getenv("GTK_USE_PORTAL") == "" {
		os.Setenv("GTK_USE_PORTAL", "1")
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

	mygo.App.WhenReady(func() {
		var win *mygo.Window
		win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "FileSync 文件备份",
			Width:     980,
			Height:    680,
			MinWidth:  720,
			MinHeight: 480,
			StateKey:  "main",
			Content:   ui.View(a.view),
		})
		a.post = win.Update
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
		a.keepAwake = func(reason string) func() { return mygo.Power.KeepAwake(reason, false) }
		a.closeWindow = func() {
			a.quitWhenDone, a.job = false, nil
			win.Close()
		}
		win.OnClose(func(e *mygo.CloseEvent) {
			if !a.requestClose() {
				e.PreventDefault()
				win.Invalidate()
			}
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
