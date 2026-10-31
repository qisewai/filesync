package index

import (
	"bufio"
	"io"
	"os"
	"strings"
)

func isLink(st os.FileInfo) bool   { return st.Mode()&os.ModeSymlink != 0 }
func isHidden(st os.FileInfo) bool { return false }

// BlockedMounts is refreshed only when the user requests a scan, including
// nested network mounts below an otherwise local indexing root.
func BlockedMounts() []string {
	var out []string
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return out
	}
	defer f.Close()
	return blockedMounts(f)
}
func blockedMounts(r io.Reader) []string {
	var out []string
	s := bufio.NewScanner(r)
	for s.Scan() {
		left, right, ok := strings.Cut(s.Text(), " - ")
		if !ok {
			continue
		}
		fields, fs := strings.Fields(left), strings.Fields(right)
		if len(fields) < 5 || len(fs) < 1 {
			continue
		}
		kind := fs[0]
		network := kind == "cifs" || kind == "smb3" || strings.HasPrefix(kind, "nfs") || kind == "davfs" || kind == "autofs"
		switch kind {
		case "fuse.sshfs", "fuse.rclone", "fuse.gvfsd-fuse", "fuse.kio-fuse", "fuse.davfs", "fuse.smbnetfs", "fuse.curlftpfs", "fuse.afpfs", "fuse.s3fs", "fuse.gcsfuse", "fuse.goofys", "fuse.juicefs":
			network = true
		}
		if network {
			out = append(out, strings.NewReplacer(`\040`, " ", `\011`, "\t", `\134`, `\`).Replace(fields[4]))
		}
	}
	return out
}
