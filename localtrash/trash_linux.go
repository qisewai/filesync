package localtrash

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// move follows the freedesktop.org trash specification: the home trash
// for what is on the file system of the home, else the trash at the top
// of the file system of p, as the file managers of the desktops do.
func move(p string) error {
	p, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	var st syscall.Stat_t
	if err := syscall.Lstat(p, &st); err != nil {
		return err
	}
	home, err := homeTrash()
	if err != nil {
		return err
	}
	if dev, err := device(filepath.Dir(home)); err == nil && dev == st.Dev {
		return trashIn(home, p, p)
	}
	top, err := topDir(p, st.Dev)
	if err != nil {
		return err
	}
	dir, err := topTrash(top)
	if err != nil {
		return fmt.Errorf("%s 所在的磁盘上没有可用的回收站: %w", p, err)
	}
	rel, err := filepath.Rel(top, p)
	if err != nil {
		return err
	}
	return trashIn(dir, p, rel)
}

// homeTrash is $XDG_DATA_HOME/Trash, made if missing.
func homeTrash() (string, error) {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		data = filepath.Join(home, ".local", "share")
	}
	dir := filepath.Join(data, "Trash")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func device(p string) (uint64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		return 0, err
	}
	return st.Dev, nil
}

// topDir is the top directory of the file system of p, of device dev.
func topDir(p string, dev uint64) (string, error) {
	dir := filepath.Dir(p)
	for {
		up := filepath.Dir(dir)
		if up == dir {
			return dir, nil
		}
		d, err := device(up)
		if err != nil {
			return "", err
		}
		if d != dev {
			return dir, nil
		}
		dir = up
	}
}

// topTrash is the trash of this user at the top directory of a file
// system: $top/.Trash/$uid when an administrator made $top/.Trash, a
// sticky directory and not a link, else $top/.Trash-$uid.
func topTrash(top string) (string, error) {
	uid := strconv.Itoa(os.Getuid())
	shared := filepath.Join(top, ".Trash")
	if fi, err := os.Lstat(shared); err == nil && fi.IsDir() && fi.Mode()&fs.ModeSticky != 0 {
		dir := filepath.Join(shared, uid)
		if err := os.MkdirAll(dir, 0o700); err == nil {
			return dir, nil
		}
	}
	dir := filepath.Join(top, ".Trash-"+uid)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	if fi, err := os.Lstat(dir); err != nil {
		return "", err
	} else if !fi.IsDir() {
		return "", fmt.Errorf("%s 不是文件夹", dir)
	}
	return dir, nil
}

// trashIn moves p into the trash dir, recording it as was, the path the
// trash restores it to.
func trashIn(dir, p, was string) error {
	files, info := filepath.Join(dir, "files"), filepath.Join(dir, "info")
	for _, d := range []string{files, info} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	base := filepath.Base(p)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		stem, ext = base, ""
	}
	record := "[Trash Info]\nPath=" + escape(was) + "\nDeletionDate=" + time.Now().Format("2006-01-02T15:04:05") + "\n"
	for n := 1; ; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s.%d%s", stem, n, ext)
		}
		// The record taken first claims the name.
		f, err := os.OpenFile(filepath.Join(info, name+".trashinfo"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		_, err = f.WriteString(record)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			if _, serr := os.Lstat(filepath.Join(files, name)); serr == nil {
				err = fs.ErrExist // left by another program
			}
		}
		if err == nil {
			err = os.Rename(p, filepath.Join(files, name))
		}
		if err != nil {
			os.Remove(filepath.Join(info, name+".trashinfo"))
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return err
		}
		return nil
	}
}

// escape writes a path as the trash records it, escaped as in a URL.
func escape(p string) string {
	parts := strings.Split(filepath.ToSlash(p), "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}
