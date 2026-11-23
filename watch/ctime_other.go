//go:build !(darwin || freebsd || netbsd || linux || openbsd || dragonfly || solaris)

package watch

import "io/fs"

// ctimeOf is the time the status of the file last changed, which this system
// doesn't say.
func ctimeOf(fs.FileInfo) int64 { return 0 }
