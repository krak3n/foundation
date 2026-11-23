package watch_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"go.krak3n.dev/foundation/watch"
)

// An Emitter is what a trigger delivers on: it holds at most one event, so a
// trigger that posts as fast as it likes costs one event of memory and no
// goroutine, and it has the rules every trigger shares. A trigger written outside
// this package gets them from here instead of writing them again.

func take(t *testing.T, e *watch.Emitter) (watch.Event, bool) {
	t.Helper()

	select {
	case ev, ok := <-e.Events():
		return ev, ok
	case <-time.After(5 * time.Second):
		t.Fatal("no event, and the channel was not closed")
	}

	return watch.Event{}, false
}

func pending(e *watch.Emitter) bool {
	select {
	case _, ok := <-e.Events():
		return ok
	default:
		return false
	}
}

func TestAnEmitterDeliversWhatIsPosted(t *testing.T) {
	e := watch.NewEmitter(nil)

	e.Post(watch.Event{Kind: watch.Restart, Reason: "changed"})

	ev, ok := take(t, e)
	if !ok || ev.Kind != watch.Restart || ev.Reason != "changed" {
		t.Fatalf("got %+v, %v", ev, ok)
	}

	first, second := e.Events(), e.Events()

	if first != second {
		t.Error("Events should be the same channel every time")
	}
}

func TestASecondRestartWhileOneWaitsIsDropped(t *testing.T) {
	e := watch.NewEmitter(nil)

	e.Post(watch.Event{Kind: watch.Restart, Reason: "first"})
	e.Post(watch.Event{Kind: watch.Restart, Reason: "second"})

	ev, _ := take(t, e)
	if ev.Reason != "first" {
		t.Errorf("got %q, want the one that was waiting", ev.Reason)
	}

	if pending(e) {
		t.Error("the dropped restart was delivered")
	}
}

func TestAShutdownReplacesAWaitingRestartAndIsTheLast(t *testing.T) {
	e := watch.NewEmitter(nil)

	e.Post(watch.Event{Kind: watch.Restart, Reason: "r"})
	e.Post(watch.Event{Kind: watch.Shutdown, Reason: "s"})
	e.Post(watch.Event{Kind: watch.Restart, Reason: "after"})
	e.Post(watch.Event{Kind: watch.Shutdown, Reason: "after"})

	ev, ok := take(t, e)
	if !ok || ev.Kind != watch.Shutdown || ev.Reason != "s" {
		t.Fatalf("got %+v, %v, want the shutdown", ev, ok)
	}

	if _, ok := take(t, e); ok {
		t.Error("something was delivered after a shutdown")
	}
}

func TestAnEmitterDropsWhatIsNotAKind(t *testing.T) {
	e := watch.NewEmitter(nil)

	e.Post(watch.Event{Kind: 0, Reason: "none"})
	e.Post(watch.Event{Kind: watch.Kind(99), Reason: "far"})
	e.Post(watch.Event{Kind: watch.Kind(-1), Reason: "negative"})

	if pending(e) {
		t.Fatal("an event that is not a kind was delivered")
	}

	// And it doesn't stop the emitter working.
	e.Post(watch.Event{Kind: watch.Restart})

	if ev, ok := take(t, e); !ok || ev.Kind != watch.Restart {
		t.Fatalf("got %+v, %v", ev, ok)
	}
}

func TestAnEmitterBoundsAndCleansTheReason(t *testing.T) {
	for name, reason := range map[string]string{
		"too long":       string(make([]byte, 10*watch.MaxReason)),
		"not UTF-8":      "a\xff\xfeb",
		"cut in a rune":  repeat("é", watch.MaxReason),
		"a long nothing": "",
	} {
		t.Run(name, func(t *testing.T) {
			e := watch.NewEmitter(nil)

			e.Post(watch.Event{Kind: watch.Restart, Reason: reason})

			ev, _ := take(t, e)

			if len(ev.Reason) > watch.MaxReason {
				t.Errorf("a reason of %d bytes", len(ev.Reason))
			}

			if !utf8.ValidString(ev.Reason) {
				t.Errorf("reason %q is not UTF-8", ev.Reason)
			}
		})
	}
}

func TestEndClosesTheChannelAndAfterThatNothingHappens(t *testing.T) {
	var released atomic.Int32

	e := watch.NewEmitter(func() { released.Add(1) })

	e.End()
	e.End()
	e.Post(watch.Event{Kind: watch.Restart})
	e.Post(watch.Event{Kind: watch.Shutdown})

	if _, ok := take(t, e); ok {
		t.Fatal("something was delivered after the end")
	}

	if got := released.Load(); got != 1 {
		t.Errorf("released %d times, want once", got)
	}
}

func TestAShutdownReleasesOnceAndEndAfterItDoesNot(t *testing.T) {
	var released atomic.Int32

	e := watch.NewEmitter(func() { released.Add(1) })

	e.Post(watch.Event{Kind: watch.Shutdown})
	e.End()

	if got := released.Load(); got != 1 {
		t.Errorf("released %d times, want once", got)
	}
}

// The release function is called without the emitter's lock, so a trigger that
// unregisters itself from the release, and one that posts or ends from it, can't
// deadlock itself.
func TestTheReleaseMayUseTheEmitter(t *testing.T) {
	done := make(chan struct{})

	go func() {
		defer close(done)

		var e *watch.Emitter

		e = watch.NewEmitter(func() {
			e.Post(watch.Event{Kind: watch.Restart})
			e.End()
		})

		e.End()

		e = watch.NewEmitter(func() { e.End() })

		e.Post(watch.Event{Kind: watch.Shutdown})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the release deadlocked on the emitter")
	}
}

// Many goroutines posting and one ending: no send on a closed channel, no race,
// and what is left is closed.
func TestAnEmitterIsSafeForConcurrentUse(t *testing.T) {
	for range 50 {
		e := watch.NewEmitter(nil)

		var wg sync.WaitGroup

		for i := range 8 {
			wg.Go(func() {
				for range 200 {
					kind := watch.Restart
					if i == 0 {
						kind = watch.Shutdown
					}

					e.Post(watch.Event{Kind: kind, Reason: "x"})
				}
			})
		}

		wg.Go(e.End)

		wg.Wait()

		for pending(e) {
		}

		if _, ok := <-e.Events(); ok {
			t.Fatal("the channel is not closed")
		}
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)

	for range n {
		out = append(out, s...)
	}

	return string(out)
}
