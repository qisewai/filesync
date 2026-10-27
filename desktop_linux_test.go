package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Run where it was built, the app registers its entry, with its icon,
// and again as it moves; an entry it did not write stays.
func TestRegisterDesktopEntry(t *testing.T) {
	data, system := t.TempDir(), t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_DATA_DIRS", system)
	entry := filepath.Join(data, "applications", "filesync.desktop")

	built := filepath.Join(t.TempDir(), "linux-amd64")
	os.MkdirAll(built, 0o755)
	os.WriteFile(filepath.Join(built, "filesync.png"), appIcon, 0o644)
	if err := registerDesktopEntryOf(filepath.Join(built, "filesync")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(entry)
	if !strings.Contains(string(b), `Exec="`+built+`/filesync"`) || !strings.Contains(string(b), "Icon="+built+"/filesync.png") {
		t.Fatalf("entry %s", b)
	}

	// Moved, without its icon beside it: a copy of the icon.
	moved := filepath.Join(t.TempDir(), "filesync")
	if err := registerDesktopEntryOf(moved); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(entry)
	icon := filepath.Join(data, "filesync", "filesync.png")
	if !strings.Contains(string(b), `Exec="`+moved+`"`) || !strings.Contains(string(b), "Icon="+icon) {
		t.Fatalf("entry %s", b)
	}
	if got, _ := os.ReadFile(icon); !bytes.Equal(got, appIcon) {
		t.Fatal("icon not copied")
	}

	// Installed by the package: left alone, and not shadowed.
	os.Remove(entry)
	os.MkdirAll(filepath.Join(system, "applications"), 0o755)
	os.WriteFile(filepath.Join(system, "applications", "filesync.desktop"), []byte("[Desktop Entry]\nExec=filesync\n"), 0o644)
	if err := registerDesktopEntryOf(moved); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(entry); err == nil {
		t.Fatal("registered over the installed entry")
	}

	// A path a desktop entry cannot hold.
	os.Remove(filepath.Join(system, "applications", "filesync.desktop"))
	if err := registerDesktopEntryOf(filepath.Join(t.TempDir(), `a"b`, "filesync")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(entry); err == nil {
		t.Fatal("registered a path with a quote")
	}
}
