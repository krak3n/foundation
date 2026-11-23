package foundation_test

import (
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/foundationtest"
)

// events is a concurrency safe, ordered record of things that happened.
type events struct {
	mu sync.Mutex
	s  []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.s = append(e.s, s)
}

func (e *events) get() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return slices.Clone(e.s)
}

// stoppable registers a parallel child that records "<name>:stop" when its
// context is cancelled, then "<name>:cleanup" when it has fully exited.
func stoppable(f *foundation.F, ev *events, name string, onStop func()) {
	f.Run(name, func(f *foundation.F) {
		f.Cleanup(func() { ev.add(name + ":cleanup") })

		// Only release the parent once set up, so a Shutdown that follows
		// can't race with the registration above.
		f.Parallel()

		<-f.Context().Done()

		if onStop != nil {
			onStop()
		}

		ev.add(name + ":stop")
	})
}

func assertEvents(t *testing.T, got, want []string) {
	t.Helper()

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected event order\n got: %v\nwant: %v", got, want)
	}
}

func TestShutdownStopsParallelSiblingsInReverseOrder(t *testing.T) {
	var ev events

	foundation.Run("test", func(f *foundation.F) {
		stoppable(f, &ev, "a", nil)
		stoppable(f, &ev, "b", nil)
		stoppable(f, &ev, "c", nil)

		f.Shutdown("done")
	})

	assertEvents(t, ev.get(), []string{
		"c:stop", "c:cleanup",
		"b:stop", "b:cleanup",
		"a:stop", "a:cleanup",
	})
}

func TestShutdownWaitsForSiblingBeforeStoppingNext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var ev events

		foundation.Run("test", func(f *foundation.F) {
			stoppable(f, &ev, "a", nil)
			stoppable(f, &ev, "b", nil)

			// c is stopped first and takes a while to finish; b must not be
			// signalled until c has completely exited.
			stoppable(f, &ev, "c", func() { time.Sleep(100 * time.Millisecond) })

			f.Shutdown("done")
		})

		assertEvents(t, ev.get(), []string{
			"c:stop", "c:cleanup",
			"b:stop", "b:cleanup",
			"a:stop", "a:cleanup",
		})
	})
}

func TestShutdownNestedOrder(t *testing.T) {
	var ev events

	foundation.Run("test", func(f *foundation.F) {
		stoppable(f, &ev, "before", nil)

		f.Run("svc", func(f *foundation.F) {
			f.Cleanup(func() { ev.add("svc:cleanup") })

			stoppable(f, &ev, "x", nil)
			stoppable(f, &ev, "y", nil)

			f.Parallel()

			<-f.Context().Done()

			ev.add("svc:body-exit")
		})

		f.Shutdown("done")
	})

	// A node's own body is signalled first (so it can, for example, shut down
	// a server), then its children in reverse order, then its cleanups. Only
	// then does the next sibling (before) stop.
	assertEvents(t, ev.get(), []string{
		"svc:body-exit",
		"y:stop", "y:cleanup",
		"x:stop", "x:cleanup",
		"svc:cleanup",
		"before:stop", "before:cleanup",
	})
}

func TestShutdownTimeoutContinuesToNextSibling(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var ev events

	foundation.Run("test", func(f *foundation.F) {
		stoppable(f, &ev, "a", nil)

		f.Run("hung", func(f *foundation.F) {
			f.Parallel()
			f.SetTimeout(50 * time.Millisecond)

			// Ignores cancellation for the duration of the test.
			select {}
		})

		f.Shutdown("done")
	})

	// hung is stopped first, times out, and the sequence carries on to a.
	assertEvents(t, ev.get(), []string{"a:stop", "a:cleanup"})

	var warned bool

	for _, e := range recorder.Entries() {
		if e.Record.Level == slog.LevelWarn && e.Record.Message == "function did not exit within shutdown timeout" {
			warned = true
		}
	}

	if !warned {
		t.Fatal("expected a WARN log for the timed out function")
	}
}

func TestFailTriggersOrderedShutdown(t *testing.T) {
	installExit(t) // Fail ends the process unless the exit is recorded

	var ev events

	foundation.Run("test", func(f *foundation.F) {
		stoppable(f, &ev, "a", nil)
		stoppable(f, &ev, "b", nil)

		f.Run("failing", func(f *foundation.F) {
			f.Fail(errors.New("boom"))
		})
	})

	assertEvents(t, ev.get(), []string{
		"b:stop", "b:cleanup",
		"a:stop", "a:cleanup",
	})
}

func TestRunDuringShutdownIsNoop(t *testing.T) {
	var (
		ev   events
		aF   *foundation.F
		late = make(chan struct{}, 1)
	)

	foundation.Run("test", func(f *foundation.F) {
		f.Run("a", func(f *foundation.F) {
			aF = f

			f.Parallel() // publishes aF to the goroutine below

			<-f.Context().Done()
		})

		// b is stopped before a, so a's context is still live while b is
		// stopping. Starting new work under a at that point must not run: the
		// process is already shutting down.
		stoppable(f, &ev, "b", func() {
			aF.Run("late", func(f *foundation.F) {
				late <- struct{}{}
			})
		})

		f.Shutdown("done")
	})

	select {
	case <-late:
		t.Fatal("expected Run during shutdown not to execute the function")
	default:
	}
}

func TestShutdownOrderIsStable(t *testing.T) {
	want := []string{
		"d:stop", "d:cleanup",
		"c:stop", "c:cleanup",
		"b:stop", "b:cleanup",
		"a:stop", "a:cleanup",
	}

	for range 100 {
		var ev events

		foundation.Run("test", func(f *foundation.F) {
			for _, name := range []string{"a", "b", "c", "d"} {
				stoppable(f, &ev, name, nil)
			}

			f.Shutdown("done")
		})

		assertEvents(t, ev.get(), want)
	}
}

func TestAbandonedNodeCancelsDescendants(t *testing.T) {
	var (
		childCancelled      = make(chan struct{})
		grandchildCancelled = make(chan struct{})
	)

	foundation.Run("test", func(f *foundation.F) {
		f.Run("hung", func(f *foundation.F) {
			f.SetTimeout(50 * time.Millisecond)

			f.Run("child", func(f *foundation.F) {
				f.Run("grandchild", func(f *foundation.F) {
					f.Parallel()

					<-f.Context().Done()
					close(grandchildCancelled)
				})

				f.Parallel()

				<-f.Context().Done()
				close(childCancelled)
			})

			f.Parallel()

			// Ignores cancellation, so the stop sequence abandons it.
			select {}
		})

		f.Shutdown("done")
	})

	// Abandoning a node must not leave its descendants running with live
	// contexts while the rest of the process carries on shutting down.
	for name, ch := range map[string]chan struct{}{"child": childCancelled, "grandchild": grandchildCancelled} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("expected %s of an abandoned node to be cancelled", name)
		}
	}
}
