package foundation_test

import (
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/foundationtest"
)

// The shutdown timeout of a function is for what the function itself does: it
// returns within its timeout, and its own cleanups finish within it. The time its
// children take to stop is theirs, each bounded by its own timeout, and must not
// be taken out of the parent's: a client that is slow to go away would otherwise
// make a healthy parent look hung, and a hung function fails a restart.

const abandonedWarning = "function did not exit within shutdown timeout"

// slowChildren starts n parallel children that each take stopTime to stop once
// they are asked to, under their own, longer timeout.
func slowChildren(f *foundation.F, n int, stopTime time.Duration) {
	for range n {
		f.Run("child", func(c *foundation.F) {
			c.SetTimeout(time.Hour)
			c.Parallel()

			<-c.Context().Done()

			time.Sleep(stopTime)
		})
	}
}

// settle lets what is still running under the fake clock finish.
func settle() { time.Sleep(time.Minute) }

func abandoned(r *foundationtest.LogRecorder) int {
	n := 0

	for _, e := range r.Entries() {
		if e.Record.Level == slog.LevelWarn && e.Record.Message == abandonedWarning {
			n++
		}
	}

	return n
}

// The parent's timeout is 200ms and its children take 450ms between them to stop,
// then it has a cleanup to run. None of that is the parent's fault.
func TestAParentIsNotAbandonedBecauseItsChildrenWereSlowToStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		foundation.Run("svc", func(f *foundation.F) {
			f.Run("parent", func(p *foundation.F) {
				p.SetTimeout(200 * time.Millisecond)
				p.Cleanup(func() { time.Sleep(20 * time.Millisecond) })

				slowChildren(p, 3, 150*time.Millisecond)

				p.Parallel()

				<-p.Context().Done()
			})

			f.Shutdown("done")
		})

		// Anything that was abandoned is still running: let it finish, so that a
		// failure here is reported and not a bubble that ended with it blocked.
		settle()

		if n := abandoned(recorder); n != 0 {
			t.Errorf("%d functions were abandoned, want none", n)
		}

		assertExitCodes(t, exit)
	})
}

// The same, two levels down: each level has more to stop than its own timeout.
func TestSlowChildrenDoNotAbandonAnyLevelOfTheTree(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		foundation.Run("svc", func(f *foundation.F) {
			f.Run("grandparent", func(g *foundation.F) {
				g.SetTimeout(300 * time.Millisecond)
				g.Cleanup(func() { time.Sleep(20 * time.Millisecond) })

				g.Run("parent", func(p *foundation.F) {
					p.SetTimeout(200 * time.Millisecond)
					p.Cleanup(func() { time.Sleep(20 * time.Millisecond) })

					slowChildren(p, 3, 150*time.Millisecond)

					p.Parallel()

					<-p.Context().Done()
				})

				g.Parallel()

				<-g.Context().Done()
			})

			f.Shutdown("done")
		})

		// Anything that was abandoned is still running: let it finish, so that a
		// failure here is reported and not a bubble that ended with it blocked.
		settle()

		if n := abandoned(recorder); n != 0 {
			t.Errorf("%d functions were abandoned, want none", n)
		}

		assertExitCodes(t, exit)
	})
}

// An abandoned function fails a restart, so a parent that only had slow children
// must not stop the program from starting again.
func TestSlowChildrenDoNotStopARestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			f.Run("parent", func(c *foundation.F) {
				c.SetTimeout(200 * time.Millisecond)
				c.Cleanup(func() { time.Sleep(20 * time.Millisecond) })

				slowChildren(c, 3, 150*time.Millisecond)

				c.Parallel()

				<-c.Context().Done()
			})

			if n == 1 {
				f.Restart("now")

				return
			}

			f.Shutdown("done")
		}))

		// Anything that was abandoned is still running: let it finish, so that a
		// failure here is reported and not a bubble that ended with it blocked.
		settle()

		if got := p.instances(); got != 2 {
			t.Fatalf("%d instances ran, want 2", got)
		}

		if n := abandoned(recorder); n != 0 {
			t.Errorf("%d functions were abandoned, want none", n)
		}

		assertExitCodes(t, exit)
	})
}

// A cleanup that never returns is still bounded, and is given its whole timeout
// from when the children are done, not what was left of one that they used.
func TestACleanupThatNeverReturnsIsAbandonedOneTimeoutAfterTheChildren(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		hang := make(chan struct{})
		defer close(hang)

		var start time.Time

		foundation.Run("svc", func(f *foundation.F) {
			f.Run("parent", func(p *foundation.F) {
				p.SetTimeout(200 * time.Millisecond)
				p.Cleanup(func() { <-hang })

				slowChildren(p, 1, 150*time.Millisecond)

				p.Parallel()

				<-p.Context().Done()
			})

			start = time.Now()

			f.Shutdown("done")
		})

		// 150ms for the child, then the parent's own 200ms for its cleanup.
		if got, want := time.Since(start), 350*time.Millisecond; got != want {
			t.Errorf("the run took %v, want %v", got, want)
		}

		if n := abandoned(recorder); n != 1 {
			t.Errorf("%d functions were abandoned, want the one cleanup", n)
		}
	})
}

// A function that never returns is abandoned at its timeout, as before.
func TestABodyThatNeverReturnsIsAbandonedAtItsTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		hang := make(chan struct{})
		defer close(hang)

		var start time.Time

		foundation.Run("svc", func(f *foundation.F) {
			f.Run("stuck", func(s *foundation.F) {
				s.SetTimeout(200 * time.Millisecond)
				s.Parallel()

				<-hang
			})

			start = time.Now()

			f.Shutdown("done")
		})

		if got, want := time.Since(start), 200*time.Millisecond; got != want {
			t.Errorf("the run took %v, want %v", got, want)
		}

		if n := abandoned(recorder); n != 1 {
			t.Errorf("%d functions were abandoned, want 1", n)
		}
	})
}
