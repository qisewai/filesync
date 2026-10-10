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

// entry is a file or directory found under a root.
type entry struct {
	rel     string // slash-separated, relative to the root
	isDir   bool
	size    int64
	modTime time.Time
	perm    fs.FileMode
	special string // why it cannot be copied (a symbolic link…), or ""
}

// tree is what a scan found under a root.
type tree struct {
	entries    []entry  // in lexical order, each directory before its contents
	unreadable []string // directories whose contents could not be listed
	temps      []string // temporary files left by an interrupted copy
	errors     []string
}

// tempName matches the temporary files copies write before renaming them.
var tempName = regexp.MustCompile(`^\.filesync-[0-9a-f]{8}\.tmp$`)

// scan lists root, leaving out what f excludes. onEntry is called for every
// entry, for progress.
func scan(ctx context.Context, root string, f *Filter, onEntry func()) (*tree, error) {
	t := &tree{}
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
				t.unreadable = append(t.unreadable, rel)
			}
			t.errors = append(t.errors, rel+": "+err.Error())
			return nil
		}
		if onEntry != nil {
			onEntry()
		}
		isDir := d.IsDir()
		if !isDir && tempName.MatchString(d.Name()) {
			t.temps = append(t.temps, rel)
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
			t.errors = append(t.errors, rel+": "+ierr.Error())
			if isDir {
				t.unreadable = append(t.unreadable, rel)
				return fs.SkipDir
			}
			return nil
		}
		e := entry{rel: rel, isDir: isDir, size: info.Size(), modTime: info.ModTime(), perm: info.Mode().Perm()}
		switch mode := info.Mode(); {
		case mode&fs.ModeSymlink != 0:
			e.special = "符号链接"
		case mode&fs.ModeIrregular != 0:
			e.special = "特殊文件（如 Windows 的连接点）"
		case !mode.IsRegular() && !isDir:
			e.special = "设备、管道或套接字等特殊文件"
		}
		if e.special != "" {
			e.isDir = false
		}
		t.entries = append(t.entries, e)
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

// caseInsensitive reports whether the file system at dir, or at its nearest
// existing ancestor, folds the case of names, as NTFS, FAT and SMB shares
// do.
func caseInsensitive(dir string) bool {
	for {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
	// Ask an existing name in the other case, which changes nothing.
	if des, err := os.ReadDir(dir); err == nil {
		for _, de := range des {
			name := de.Name()
			other := swapCase(name)
			if other == name {
				continue
			}
			a, err1 := os.Lstat(filepath.Join(dir, name))
			b, err2 := os.Lstat(filepath.Join(dir, other))
			if err1 != nil {
				continue
			}
			if err2 != nil {
				return false
			}
			return os.SameFile(a, b)
		}
	}
	// Else write a file to ask.
	f, err := os.CreateTemp(dir, ".filesync-case-")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	defer os.Remove(name)
	base := filepath.Base(name)
	_, err = os.Lstat(filepath.Join(dir, strings.ToUpper(base)))
	return err == nil
}

func swapCase(s string) string {
	if u := strings.ToUpper(s); u != s {
		return u
	}
	return strings.ToLower(s)
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
