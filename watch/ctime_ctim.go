//go:build linux || openbsd || dragonfly || solaris

package watch

import (
	"io/fs"
	"syscall"
)

// ctimeOf is the time the status of the file last changed, which a write can't set
// back the way it can the modification time, or 0 where the system doesn't say.
func ctimeOf(info fs.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Ctim.Nano()
	}

	return 0
}
