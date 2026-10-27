//go:build !linux

package main

// registerDesktopEntry has nothing to do: the system takes the icon from
// the executable (Windows) or the app bundle (macOS).
func registerDesktopEntry() error { return nil }
