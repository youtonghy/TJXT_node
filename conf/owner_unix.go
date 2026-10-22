//go:build unix

package conf

import (
	"os"
	"syscall"
)

// keepOwner gives f the owner of the file it replaces. Only root can do that
// for a foreign owner; otherwise the file keeps the caller as owner.
func keepOwner(f *os.File, old os.FileInfo) {
	st, ok := old.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	_ = f.Chown(int(st.Uid), int(st.Gid))
}
