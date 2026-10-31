package engine

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Entry is a file or directory found under a root.
type Entry struct {
	Path    string // slash-separated, relative to the root
	IsDir   bool
	Size    int64
	ModTime time.Time
	Perm    fs.FileMode
	// Link is the target of a symbolic link, which is copied as a link.
	Link    string
	Special string // why it cannot be copied (a device…), or ""
}

// kind tells files, directories and symbolic links apart.
func (e *Entry) kind() int {
	switch {
	case e.IsDir:
		return 1
	case e.Link != "":
		return 2
	}
	return 0
}

// Tree is what a scan found under a root.
type Tree struct {
	Entries    []Entry  // each directory before its contents
	Unreadable []string // directories whose contents could not be listed
	Temps      []string // temporary files left by an interrupted copy
	Errors     []string
}

// tempName matches the temporary files copies write before renaming them.
var tempName = regexp.MustCompile(`^\.filesync-[0-9a-f]{8}\.tmp$`)

// scan lists root, leaving out what f excludes. onEntry is called for every
// entry, for progress.
func scan(ctx context.Context, root string, f *Filter, onEntry func()) (*Tree, error) {
	t := &Tree{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p == root {
			if err != nil {
				return err
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if err != nil {
			if d != nil && d.IsDir() {
				// The directory itself was listed by its parent, but not its
				// contents.
				t.Unreadable = append(t.Unreadable, rel)
			}
			t.Errors = append(t.Errors, rel+": "+err.Error())
			return nil
		}
		if onEntry != nil {
			onEntry()
		}
		isDir := d.IsDir()
		if !isDir && tempName.MatchString(d.Name()) {
			t.Temps = append(t.Temps, rel)
			return nil
		}
		if f.Excluded(rel, isDir) {
			if isDir {
				return fs.SkipDir
			}
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			if errors.Is(ierr, fs.ErrNotExist) {
				return nil // gone since it was listed
			}
			t.Errors = append(t.Errors, rel+": "+ierr.Error())
			if isDir {
				t.Unreadable = append(t.Unreadable, rel)
				return fs.SkipDir
			}
			return nil
		}
		e := Entry{Path: rel, IsDir: isDir, Size: info.Size(), ModTime: info.ModTime(), Perm: info.Mode().Perm()}
		switch mode := info.Mode(); {
		case mode&fs.ModeSymlink != 0:
			e.Size = 0
			if e.Link, ierr = os.Readlink(p); ierr != nil || e.Link == "" {
				e.Special = "无法读取的符号链接"
			}
		case mode&fs.ModeIrregular != 0:
			e.Special = "特殊文件（如 Windows 的连接点）"
		case !mode.IsRegular() && !isDir:
			e.Special = "设备、管道或套接字等特殊文件"
		}
		if e.Special != "" {
			e.IsDir = false
		}
		t.Entries = append(t.Entries, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// underAny reports whether rel is one of dirs or inside one of them, with
// paths compared by key.
func underAny(rel string, dirs map[string]bool, key func(string) string) bool {
	for p := rel; p != "." && p != "/" && p != ""; p = path.Dir(p) {
		if dirs[key(p)] {
			return true
		}
	}
	return false
}

// windowsNameProblem returns why name cannot be a file name on Windows or an
// SMB share, or "".
func windowsNameProblem(name string) string {
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(`<>:"|?*\`, r) {
			return "文件名含有 Windows 不允许的字符"
		}
	}
	if strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
		return "文件名以空格或句点结尾，Windows 不允许"
	}
	stem := strings.ToUpper(name)
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	stem = strings.TrimRight(stem, " ")
	switch stem {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return "文件名是 Windows 的保留名称"
	}
	return ""
}
