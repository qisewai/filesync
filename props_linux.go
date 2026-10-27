package main

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
	"time"
)

// statMore reads what os.FileInfo leaves to the platform.
func statMore(fi os.FileInfo) (x fileStatMore) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	x.atime = time.Unix(st.Atim.Sec, st.Atim.Nsec)
	x.ctime = time.Unix(st.Ctim.Sec, st.Ctim.Nsec)
	uid, gid := strconv.FormatUint(uint64(st.Uid), 10), strconv.FormatUint(uint64(st.Gid), 10)
	owner, group := uid, gid
	if u, err := user.LookupId(uid); err == nil {
		owner = u.Username
	}
	if g, err := user.LookupGroupId(gid); err == nil {
		group = g.Name
	}
	x.owner = owner + ":" + group
	return
}
