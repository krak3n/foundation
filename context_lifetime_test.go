package foundation_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"go.krak3n.dev/foundation"
)

// What a node's context means, as F.Context and F.Shutdown document it: it carries
// the parent's values but not its cancellation. In a shutdown it is cancelled when
// the node's turn in the stop order comes (the root's at once), and otherwise when
// the node has finished completely: its function, its parallel children and its
// cleanups.

// A shutdown cancels the root's context at once and each other node's only when its
// turn comes, so a node that is waiting for a later one to stop is still live.
func TestAShutdownCancelsEachNodesContextAtItsTurn(t *testing.T) {
	type result struct {
		rootDone       bool
		aLiveWhileB    bool
		aDoneAtItsTurn bool
	}

	var (
		res    result
		aCtx   context.Context
		bSeen  = make(chan struct{})
		gate   = make(chan struct{})
		checks = make(chan struct{})
	)

	foundation.Run("svc", func(f *foundation.F) {
		f.Run("a", func(a *foundation.F) {
			aCtx = a.Context()

			a.Parallel()

			<-a.Context().Done()
		})

		f.Run("b", func(b *foundation.F) {
			b.Parallel()

			<-b.Context().Done()

			close(bSeen)

			// b is the last started so the first stopped, and takes its time.
			<-gate
		})

		go func() {
			defer close(checks)

			<-bSeen

			// b has been told to stop, and a, which started first, has not.
			res.aLiveWhileB = aCtx.Err() == nil

			close(gate)
		}()

		f.Shutdown("now")

		// Cancelled synchronously: when Shutdown returns, the root's is done.
		res.rootDone = f.Context().Err() != nil
	})

	<-checks

	res.aDoneAtItsTurn = aCtx.Err() != nil

	if !res.rootDone {
		t.Error("the root's context was not done when Shutdown returned")
	}

	if !res.aLiveWhileB {
		t.Error("a's context was cancelled before its turn, while b was still stopping")
	}

	if !res.aDoneAtItsTurn {
		t.Error("a's context was never cancelled")
	}
}

// A function that returns while its parallel children carry on does not cancel its
// own context: the usual root function starts what it needs and returns, and what
// holds its context must not lose it until the program stops.
func TestAContextLivesWhileTheNodesParallelChildrenRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var live bool

		foundation.Run("svc", func(f *foundation.F) {
			f.Run("worker", func(w *foundation.F) {
				w.Parallel()

				<-w.Context().Done()
			})

			// The root function returns here, with the worker still running.
			time.AfterFunc(time.Minute, func() {
				live = f.Context().Err() == nil

				f.Shutdown("done")
			})
		})

		if !live {
			t.Error("the root's context was cancelled when its function returned, with a child still running")
		}
	})
}

// A function that returns by itself still has its context while its cleanups run:
// the node is not finished until they have, and is released after. (A cleanup
// after a shutdown sees a cancelled one, so it should not depend on either.)
func TestAContextIsReleasedAfterTheCleanupsOfAFunctionThatReturned(t *testing.T) {
	var (
		inCleanup context.Context
		duringErr error
	)

	foundation.Run("svc", func(f *foundation.F) {
		f.Run("child", func(c *foundation.F) {
			inCleanup = c.Context()

			c.Cleanup(func() { duringErr = c.Context().Err() })
		})
	})

	if duringErr != nil {
		t.Errorf("the context was already cancelled in the cleanup: %v", duringErr)
	}

	if inCleanup.Err() == nil {
		t.Error("the context was not released once the node had finished")
	}
}
