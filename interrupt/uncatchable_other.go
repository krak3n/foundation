//go:build !unix

package interrupt

import "syscall"

// uncatchable reports whether a signal can't be caught, so that asking for it
// would never do anything.
func uncatchable(sig syscall.Signal) bool {
	return sig == syscall.SIGKILL
}
