package foundation

import (
	"log/slog"
	"slices"
	"time"
)

// shutdown is a helper that performs the actual shutdown logic for both [F.Shutdown] and [F.Fail].
// It ensures that the shutdown logic is only executed once, even if both methods are called.
// It reports whether this call was the one that started the shutdown.
//
// A shutdown is the process's: it is final, so no new instance starts after it
// (see [F.Restart]), and it applies to the current instance, which is not f's if
// f belongs to one that has since been replaced.
func (f *F) shutdown(level slog.Level, msg string, attrs ...slog.Attr) (first bool) {
	p := f.root.proc

	// Final is set before the current instance is looked up, and a new instance is
	// registered under the same lock that checks it, so a shutdown can't fall
	// between the end of one instance and the start of the next.
	p.final.Store(true)

	// A restart that is waiting is given up, and Run, which may be waiting for it,
	// is woken.
	p.cancelPending()

	return p.target(f.root).begin(f, false, level, msg, attrs...)
}

// begin starts the stop sequence of the instance that root is the root of, once,
// and reports whether this call was the one that did. caller is the function that
// asked, for the log. restart says the stop is a restart, which only the first
// call can decide.
func (root *F) begin(caller *F, restart bool, level slog.Level, msg string, attrs ...slog.Attr) (first bool) {
	root.shutdownOnce.Do(func() {
		first = true

		if restart {
			root.restarting.Store(true)
		}

		caller.Log().LogAttrs(caller.Context(), level, msg, attrs...)

		root.stopping.Store(true)

		// The root is first in the stop order, so cancel it synchronously: once
		// Shutdown returns, the caller can rely on the root context being done.
		root.cancel()

		go func() {
			defer close(root.stopped)

			root.stop()
		}()
	})

	return first
}

// stop runs the ordered shutdown sequence for f and everything beneath it:
//
//  1. f's context is cancelled so its function can begin shutting down (for
//     example by stopping a server).
//  2. f's function is given the chance to return. This is skipped if the
//     function is blocked in a sequential child, since stopping that child is
//     what releases it.
//  3. f's live children are stopped in reverse start order, each one finishing
//     (or timing out) before the next is signalled.
//  4. f's cleanups run and f finishes.
//
// The timeout of f is for what f does itself: steps 2 and 4 are each given it, and
// step 3 is as long as its children take, each under its own timeout. A step that
// takes longer is abandoned with a warning and the sequence continues without it.
func (f *F) stop() {
	f.cancel()

	timer := time.NewTimer(f.Timeout())
	defer timer.Stop()

	if !f.blockedOnChild() {
		if !f.await(f.bodyDone, timer.C) {
			return
		}
	}

	for c := f.nextToStop(); c != nil; c = f.nextToStop() {
		c.stop()
	}

	// What the children took to stop is theirs, each bounded by its own timeout, and
	// is not taken out of f's: f's cleanups get its whole timeout, from now.
	cleanups := time.NewTimer(f.Timeout())
	defer cleanups.Stop()

	f.await(f.finished, cleanups.C)
}

// await waits for ch to be closed. It reports false, after abandoning f, if
// the timeout fires first.
func (f *F) await(ch <-chan struct{}, timeout <-chan time.Time) bool {
	// Prefer completion over the timeout if both are ready.
	select {
	case <-ch:
		return true
	default:
	}

	select {
	case <-ch:
		return true
	case <-timeout:
		f.abandonOnce.Do(func() {
			close(f.abandoned)

			// Something may still be running, which a restart must not start
			// a second copy of.
			f.root.hung.Store(true)

			// Nothing will stop f's descendants in order now, so cancel them
			// all rather than leave them running with live contexts.
			f.cancelDescendants()

			f.Log().WarnContext(f.Context(), "function did not exit within shutdown timeout",
				slog.Duration("timeout", f.Timeout()),
			)
		})

		return false
	}
}

// cancelDescendants cancels the context of every live descendant of f.
func (f *F) cancelDescendants() {
	f.mu.Lock()
	children := slices.Clone(f.children)
	f.mu.Unlock()

	for _, c := range children {
		c.cancel()
		c.cancelDescendants()
	}
}

// nextToStop returns the most recently started live child that has not yet
// been stopped, or nil when there are none left, after which no further
// children can be started.
func (f *F) nextToStop() *F {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, c := range slices.Backward(f.children) {
		if !c.stopRequested {
			c.stopRequested = true

			return c
		}
	}

	f.closed = true

	return nil
}

// blockedOnChild reports whether f's function is currently waiting in
// [F.Run] on a sequential child, i.e. one that has not called [F.Parallel].
func (f *F) blockedOnChild() bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, c := range f.children {
		select {
		case <-c.parallelCh:
			continue
		case <-c.finished:
			continue
		default:
			return true
		}
	}

	return false
}
