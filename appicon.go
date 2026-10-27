package main

import _ "embed"

// appIcon is the app's icon, which the window shows in its title bar and
// taskbar button where the system takes it from the window (X11,
// Windows); Wayland takes it from the desktop entry (desktop_linux.go).
//
//go:embed resources/icon.png
var appIcon []byte
