// Package places turns the URLs of network locations into directories the
// backup can read.
//
// On KDE, Dolphin opens a share such as smb://user@host/share through
// kio-fuse, at a directory like /run/user/1000/kio-fuse-AbCdEf/smb/… whose
// name changes with every session. Tasks keep the URL instead, and Resolve
// asks kio-fuse for the directory each time a backup starts.
package places

import (
	"context"
	"net/url"
	"strings"
)

// remoteSchemes are the URLs kio-fuse can mount.
var remoteSchemes = []string{"smb", "sftp", "ftp", "ftps", "webdav", "webdavs", "fish", "nfs", "mtp"}

// IsURL reports whether p is a URL to resolve rather than a directory.
func IsURL(p string) bool {
	scheme, rest, ok := strings.Cut(p, "://")
	if !ok || rest == "" {
		return false
	}
	for _, s := range remoteSchemes {
		if strings.EqualFold(scheme, s) {
			return true
		}
	}
	return false
}

// Resolve returns the directory to read for p: p itself, unless it is a URL,
// which is mounted.
func Resolve(ctx context.Context, p string) (string, error) {
	if !IsURL(p) {
		return p, nil
	}
	return mountURL(ctx, encodeURL(p))
}

// Stable returns what a task should keep for the directory dir: the URL of
// a kio-fuse directory, whose path changes with the session, else dir.
func Stable(ctx context.Context, dir string) string {
	if u, ok := remoteURL(ctx, dir); ok {
		return decodeURL(u)
	}
	return dir
}

// encodeURL escapes the path of a URL kept readable, as
// smb://user@host/王宁/照片 #1.
func encodeURL(s string) string {
	scheme, rest, _ := strings.Cut(s, "://")
	host, path, _ := strings.Cut(rest, "/")
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return scheme + "://" + host + "/" + strings.Join(parts, "/")
}

// decodeURL unescapes the path of a URL, for people to read.
func decodeURL(s string) string {
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok {
		return s
	}
	host, path, _ := strings.Cut(rest, "/")
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if d, err := url.PathUnescape(p); err == nil {
			parts[i] = d
		}
	}
	return strings.TrimRight(scheme+"://"+host+"/"+strings.Join(parts, "/"), "/")
}
