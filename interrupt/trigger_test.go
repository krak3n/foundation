package interrupt_test

import (
	"context"
	"syscall"
	"testing"
	"unicode/utf8"

	"go.krak3n.dev/foundation/interrupt"
	"go.krak3n.dev/foundation/watch"
)

// What can be asked for is decided when the watch starts, and a mistake is an
// error then, not a trigger that never fires.
func TestTheTriggerRefusesWhatIsNotAWatch(t *testing.T) {
	tests := map[string]struct {
		trigger watch.Trigger
		ok      bool
	}{
		"restart on hangup":                   {interrupt.NewTrigger(watch.Restart, syscall.SIGHUP), true},
		"shutdown on interrupt and terminate": {interrupt.NewTrigger(watch.Shutdown, syscall.SIGINT, syscall.SIGTERM), true},
		"shutdown on hangup":                  {interrupt.NewTrigger(watch.Shutdown, syscall.SIGHUP), true},
		"the same signal twice":               {interrupt.NewTrigger(watch.Restart, syscall.SIGHUP, syscall.SIGHUP), true},
		// No signal is special to the trigger: that SIGINT and SIGTERM shut a program
		// down by default, and that a signal can't be in both kinds, are rules of
		// interrupt.Signals, which knows what it listens for.
		"restart on interrupt":          {interrupt.NewTrigger(watch.Restart, syscall.SIGINT), true},
		"restart on terminate":          {interrupt.NewTrigger(watch.Restart, syscall.SIGTERM), true},
		"restart among others":          {interrupt.NewTrigger(watch.Restart, syscall.SIGHUP, syscall.SIGTERM), true},
		"restart on nothing":            {interrupt.NewTrigger(watch.Restart), false},
		"shutdown on nothing":           {interrupt.NewTrigger(watch.Shutdown), false},
		"no kind":                       {interrupt.NewTrigger(0, syscall.SIGHUP), false},
		"a kind that is not one":        {interrupt.NewTrigger(99, syscall.SIGHUP), false},
		"a nil signal":                  {interrupt.NewTrigger(watch.Restart, nil), false},
		"a signal that can't be caught": {interrupt.NewTrigger(watch.Shutdown, syscall.SIGKILL), false},
		"a signal of another kind":      {interrupt.NewTrigger(watch.Restart, customSignal("custom")), false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			events, err := tt.trigger.Watch(ctx)

			if tt.ok != (err == nil) {
				t.Fatalf("Watch error = %v, want ok %v", err, tt.ok)
			}

			if err != nil && events != nil {
				t.Error("a watch that failed to start should return no channel")
			}

			if err != nil && len(err.Error()) > 300 {
				t.Errorf("an error of %d bytes", len(err.Error()))
			}
		})
	}
}

// start watches with tr until the returned function is called, and fails the test
// if the watch can't start.
func start(t *testing.T, tr watch.Trigger) (<-chan watch.Event, context.CancelFunc) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	events, err := tr.Watch(ctx)
	if err != nil {
		cancel()
		t.Fatalf("Watch: %v", err)
	}

	return events, cancel
}

// plain fails the test if a reason is not what a log can take.
func plain(t *testing.T, ev watch.Event) {
	t.Helper()

	if len(ev.Reason) > watch.MaxReason {
		t.Errorf("a reason of %d bytes, the limit is %d", len(ev.Reason), watch.MaxReason)
	}

	if !utf8.ValidString(ev.Reason) {
		t.Errorf("reason %q is not UTF-8", ev.Reason)
	}

	for _, r := range ev.Reason {
		if r < 0x20 || r == 0x7f {
			t.Errorf("reason %q has a control character", ev.Reason)

			break
		}
	}
}
