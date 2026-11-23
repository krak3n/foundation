package watch_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	"go.krak3n.dev/foundation/watch"
)

// Any is every trigger in it as one: what any of them says is said, the strongest
// of what is waiting wins, a shutdown ends the lot, and none of it outlives its
// context.

// tracked is a trigger written the way someone outside the package would write
// it, that says when it was started and whether it was told to stop.
type tracked struct {
	started   bool
	cancelled bool
	out       chan watch.Event
	once      sync.Once
}

// end closes what the source says, once.
func (tr *tracked) end() { tr.once.Do(func() { close(tr.out) }) }

func newTracked() *tracked { return &tracked{out: make(chan watch.Event, 1)} }

func (tr *tracked) Watch(ctx context.Context) (<-chan watch.Event, error) {
	tr.started = true

	go func() {
		<-ctx.Done()

		tr.cancelled = true

		tr.end()
	}()

	return tr.out, nil
}

func TestAnyDeliversWhatAnySourceSays(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, fireA := watch.Manual()
		b, fireB := watch.Manual()

		events, cancel := start(t, watch.Any(a, b))
		defer cancel()

		fireA(watch.Restart, "from a")
		synctest.Wait()

		if got, _ := drain(events); len(got) != 1 || got[0].Reason != "from a" {
			t.Fatalf("got %+v", got)
		}

		fireB(watch.Restart, "from b")
		synctest.Wait()

		if got, _ := drain(events); len(got) != 1 || got[0].Reason != "from b" {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestAnyDeliversTheStrongestOfWhatIsWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, fireA := watch.Manual()
		b, fireB := watch.Manual()

		events, cancel := start(t, watch.Any(a, b))
		defer cancel()

		fireA(watch.Restart, "a")
		fireB(watch.Shutdown, "b")
		fireA(watch.Restart, "a again")
		synctest.Wait()

		got, closed := drain(events)

		if len(got) != 1 || got[0].Kind != watch.Shutdown || !closed {
			t.Fatalf("got %v, closed %v, want a single shutdown", kinds(got), closed)
		}
	})
}

// A shutdown from any source ends the whole: the others are told to stop watching, and
// nothing follows it.
func TestAShutdownFromOneSourceEndsAnyAndStopsTheRest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		other := newTracked()
		stopper, fire := watch.Manual()

		events, cancel := start(t, watch.Any(other, stopper))
		defer cancel()

		fire(watch.Shutdown, "now")
		synctest.Wait()

		got, closed := drain(events)

		if len(got) != 1 || got[0].Kind != watch.Shutdown || !closed {
			t.Fatalf("got %+v, closed %v", got, closed)
		}

		if !other.cancelled {
			t.Error("the other source should have been told to stop")
		}

		synctest.Wait()

		if got, _ := drain(events); len(got) != 0 {
			t.Fatalf("got %+v after the shutdown", got)
		}
	})
}

// A source that fails to start fails the whole, and those that had started are
// stopped: a half started watch would hold goroutines and handlers with nothing
// to read them.
func TestAnyThatFailsToStartStopsWhatItStarted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first := newTracked()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		events, err := watch.Any(first, watch.Files(nil)).Watch(ctx)
		if err == nil {
			t.Fatal("expected an error")
		}

		if events != nil {
			t.Error("a watch that failed to start should return no channel")
		}

		synctest.Wait()

		if !first.started || !first.cancelled {
			t.Errorf("started %v, cancelled %v: what had started should have been stopped", first.started, first.cancelled)
		}
	})
}

func TestAnyRefusesWhatIsNotAWatch(t *testing.T) {
	var nothing watch.Trigger

	many := make([]watch.Trigger, watch.MaxTriggers+1)
	for i := range many {
		tr, _ := watch.Manual()
		many[i] = tr
	}

	failing := watch.TriggerFunc(func(context.Context) (<-chan watch.Event, error) {
		return nil, errors.New("no")
	})

	noChannel := watch.TriggerFunc(func(context.Context) (<-chan watch.Event, error) {
		return nil, nil
	})

	m, _ := watch.Manual()

	var nilTracked *tracked

	tests := map[string]struct {
		trigger watch.Trigger
		ok      bool
	}{
		"no triggers":                   {watch.Any(), false},
		"a nil trigger":                 {watch.Any(m, nothing), false},
		"a nil pointer as a trigger":    {watch.Any(m, nilTracked), false},
		"one more than the most":        {watch.Any(many...), false},
		"the most":                      {watch.Any(many[:watch.MaxTriggers]...), true},
		"a trigger that fails":          {watch.Any(m, failing), false},
		"a trigger with no channel":     {watch.Any(m, noChannel), false},
		"any in any":                    {watch.Any(watch.Any(m), watch.Any(m)), true},
		"a function as a trigger":       {watch.Any(watch.TriggerFunc(newTracked().Watch)), true},
		"a trigger function that's nil": {watch.Any(watch.TriggerFunc(nil)), false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				_, err := tt.trigger.Watch(ctx)

				if tt.ok != (err == nil) {
					t.Fatalf("Watch error = %v, want ok %v", err, tt.ok)
				}
			})
		})
	}
}

// A kind that is not one, from a trigger written by someone else, is dropped, and
// what else the trigger says still gets through.
func TestAnyDropsAnInvalidKind(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newTracked()

		events, cancel := start(t, watch.Any(source))
		defer cancel()

		source.out <- watch.Event{Kind: 0, Reason: "no kind"}
		synctest.Wait()

		source.out <- watch.Event{Kind: 99, Reason: "another"}
		synctest.Wait()

		if got, closed := drain(events); len(got) != 0 || closed {
			t.Fatalf("got %+v, closed %v", got, closed)
		}

		source.out <- watch.Event{Kind: watch.Restart, Reason: "real"}
		synctest.Wait()

		if got, _ := drain(events); len(got) != 1 || got[0].Reason != "real" {
			t.Fatalf("got %+v", got)
		}
	})
}

// A source that has ended without a shutdown doesn't end the rest: Any is over when
// all of them are, or when there is a shutdown.
func TestAnyKeepsGoingWhenOneSourceEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ended := watch.TriggerFunc(func(context.Context) (<-chan watch.Event, error) {
			ch := make(chan watch.Event)

			close(ch)

			return ch, nil
		})

		live, fire := watch.Manual()

		events, cancel := start(t, watch.Any(ended, live))
		defer cancel()

		synctest.Wait()

		if got, closed := drain(events); len(got) != 0 || closed {
			t.Fatalf("got %+v, closed %v", got, closed)
		}

		fire(watch.Restart, "still here")
		synctest.Wait()

		if got, _ := drain(events); len(got) != 1 {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestAnyEndsWhenEverySourceHas(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first, second := newTracked(), newTracked()

		events, cancel := start(t, watch.Any(first, second))

		first.end()
		synctest.Wait()

		if _, closed := drain(events); closed {
			t.Fatal("one source ending is not the end")
		}

		cancel()
		synctest.Wait()

		if _, closed := drain(events); !closed {
			t.Fatal("the channel should be closed once the context is done")
		}

		if !second.cancelled {
			t.Error("the source should have been told to stop")
		}
	})
}
