// This example shuts down on operating system signals.
//
//	go run ./examples/interrupt
//
// Press Ctrl-C (SIGINT), or from another terminal send SIGTERM to shut down, or
// SIGHUP to restart:
//
//	kill -TERM <pid>
//	kill -HUP <pid>
//
// The first signal starts a graceful shutdown: every function is stopped in the
// reverse of the order it started, each given time to finish, and the process
// ends with status 0. Here the worker takes ten seconds to wind down, so there is
// time to press Ctrl-C a second time, which force quits: the first signal gives
// the signals back to the system, so the second gets their default action and
// ends the process at once. That is what makes a shutdown that has got stuck
// something you can always get out of.
//
// interrupt.Signals shuts down on SIGINT and SIGTERM unless told otherwise.
// More can be added with interrupt.WithShutdownSignals. Here SIGHUP is made a
// restart with interrupt.WithRestartSignals: the program starts again
// instead of ending (examples/restart). There are no restart signals by default,
// as a terminal that closes sends SIGHUP too. Each has a Set form that replaces
// the list, and interrupt.WithoutSignals(kind, ...) removes from it; the general
// forms (interrupt.WithSignals(interrupt.Restart, ...)) take the kind. A list that
// can't work (no shutdown signals, SIGKILL or SIGSTOP, which can't be caught, a
// signal in both kinds, which includes a restart on SIGINT or SIGTERM while they
// still shut down) stops the program
// starting rather than leaving a program that can't be asked to stop.
//
// It is started first so that it is stopped last, which keeps it handling
// signals for as long as anything is still stopping. It skips itself in a dry
// run (examples/dry-run), as it must not install a handler for one.
package main

import (
	"syscall"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/interrupt"
)

func main() {
	foundation.Run("interrupt", func(f *foundation.F) {
		interrupt.Signals(f, interrupt.WithRestartSignals(syscall.SIGHUP))

		f.Run("worker", func(f *foundation.F) {
			// A restart tries the program as a dry run first (examples/restart), and
			// something that starts work skips it then.
			if f.DryRun() {
				f.Skip("dry run")
			}

			// Long enough for this function to finish what it is doing, and for the
			// second Ctrl-C.
			f.SetTimeout(30 * time.Second)

			f.Cleanup(func() {
				f.Log().Info("stopped")
			})

			f.Log().Info("running: press Ctrl-C to stop it, and again during the shutdown to force quit")

			// Release the parent once set up, so the rest of the program can start.
			f.Parallel()

			<-f.Context().Done()

			f.Log().Info("stopping: this takes ten seconds")

			time.Sleep(10 * time.Second)
		})
	})
}
