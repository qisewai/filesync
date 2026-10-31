package index

import (
	"slices"
	"strings"
	"testing"
)

func TestBlockedNetworkMounts(t *testing.T) {
	input := `1 0 8:1 / / rw - ext4 /dev/sda rw
2 1 0:2 / /mnt/NAS\040data rw - cifs //host/data rw
3 1 0:3 / /home/me/ssh rw - fuse.sshfs host:path rw
4 1 0:4 / /mnt/nfs rw - nfs4 host:path rw
5 1 0:5 / /mnt/encrypted rw - fuse.gocryptfs encrypted rw
6 1 0:6 / /mnt/ntfs rw - fuse.ntfs-3g /dev/sdb rw
invalid row
`
	if got := blockedMounts(strings.NewReader(input)); !slices.Equal(got, []string{"/mnt/NAS data", "/home/me/ssh", "/mnt/nfs"}) {
		t.Fatalf("mounts %q", got)
	}
}
