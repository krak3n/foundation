package interrupt_test

import (
	"bufio"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"reflect"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/foundationtest"
	"go.krak3n.dev/foundation/interrupt"
	"go.krak3n.dev/foundation/watch"
)

// sendSignal sends a signal to the current process after a short delay to
// allow the signal handler to be registered.
func sendSignal(t *testing.T, sig syscall.Signal) {
	t.Helper()

	time.Sleep(20 * time.Millisecond)

	if err := syscall.Kill(syscall.Getpid(), sig); err != nil {
		t.Fatalf("failed to send signal %v: %v", sig, err)
	}
}

func TestSignalFuncTriggersShutdown(t *testing.T) {
	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			f.Run("signals", interrupt.SignalFunc(interrupt.SetSignals(interrupt.Shutdown, syscall.SIGUSR1)))

			f.Run("trigger", func(f *foundation.F) {
				f.Parallel()

				sendSignal(t, syscall.SIGUSR1)
			})
		})
	}()

	select {
	case <-done:
		// Run completed — shutdown was triggered by the signal
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for foundation.Run to complete after signal")
	}
}

func TestSignalFuncExitsOnContextDone(t *testing.T) {
	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			f.Run("signals", interrupt.SignalFunc(interrupt.SetSignals(interrupt.Shutdown, syscall.SIGUSR1)))

			// Trigger shutdown without a signal — signals func should exit
			// via the <-f.Context().Done() path
			f.Run("trigger", func(f *foundation.F) {
				f.Shutdown("manual shutdown")
			})
		})
	}()

	select {
	case <-done:
		// Run completed — signals exited cleanly on context cancellation
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for foundation.Run to complete after manual shutdown")
	}
}

func TestSignalFuncLogsShutdown(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			f.Run("signals", interrupt.SignalFunc(interrupt.SetSignals(interrupt.Shutdown, syscall.SIGUSR1)))

			f.Run("trigger", func(f *foundation.F) {
				f.Parallel()

				sendSignal(t, syscall.SIGUSR1)
			})
		})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for foundation.Run to complete")
	}

	entries := recorder.Entries()

	var found bool

	for _, e := range entries {
		if e.Record.Level != slog.LevelInfo || e.Record.Message != "shutdown requested by a watch" {
			continue
		}

		var reason string

		e.Record.Attrs(func(a slog.Attr) bool {
			if a.Key == "reason" {
				reason = a.Value.String()
			}

			return true
		})

		if reason == "signal: user defined signal 1" {
			found = true

			break
		}
	}

	if !found {
		t.Fatal("expected INFO log \"shutdown requested by a watch\" with reason \"signal: user defined signal 1\"")
	}
}

func TestSignalsConvenienceFunction(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			interrupt.Signals(f, interrupt.SetSignals(interrupt.Shutdown, syscall.SIGUSR1))

			f.Run("trigger", func(f *foundation.F) {
				f.Parallel()

				sendSignal(t, syscall.SIGUSR1)
			})
		})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for foundation.Run to complete")
	}

	entries := recorder.Entries()

	var found bool

	for _, e := range entries {
		if e.Record.Message != "shutdown requested by a watch" {
			continue
		}

		// The "fn" attr is set via f.Log().With("fn", path), which creates
		// a child handler via WithAttrs. Check the handler-level attrs. The signal
		// is watched by a function of its own, a child of "signals".
		for _, a := range e.Attrs {
			if a.Key == "fn" && a.Value.String() == "test/signals/watch" {
				found = true

				break
			}
		}

		if found {
			break
		}
	}

	if !found {
		t.Fatal("expected log with fn attr \"test/signals/watch\"")
	}
}

func TestSignalFuncCleanupCompletes(t *testing.T) {
	// Verify that foundation.Run completes cleanly after signal-triggered
	// shutdown. This implicitly confirms that cleanup (signal.Stop) ran and
	// the goroutine exited without hanging.
	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			f.Run("signals", interrupt.SignalFunc(interrupt.SetSignals(interrupt.Shutdown, syscall.SIGUSR1)))

			f.Run("trigger", func(f *foundation.F) {
				f.Parallel()

				sendSignal(t, syscall.SIGUSR1)
			})
		})
	}()

	select {
	case <-done:
		// Clean completion — cleanup ran, no hanging goroutines
	case <-time.After(2 * time.Second):
		t.Fatal("timed out — signal handler likely did not clean up properly")
	}
}

// --- Integration tests for signal options ---

func TestWithSignalsAddsCustomSignal(t *testing.T) {
	// WithSignals adds SIGUSR1 to the defaults (SIGINT, SIGTERM).
	// Sending SIGUSR1 should trigger shutdown, proving it was added.
	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			f.Run("signals", interrupt.SignalFunc(interrupt.WithSignals(interrupt.Shutdown, syscall.SIGUSR1)))

			f.Run("trigger", func(f *foundation.F) {
				f.Parallel()

				sendSignal(t, syscall.SIGUSR1)
			})
		})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected SIGUSR1 to trigger shutdown after WithSignals")
	}
}

func TestSetSignalsReplacesDefaults(t *testing.T) {
	// SetSignals replaces defaults with only SIGUSR1.
	// Sending SIGUSR1 should trigger shutdown.
	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			f.Run("signals", interrupt.SignalFunc(interrupt.SetSignals(interrupt.Shutdown, syscall.SIGUSR1)))

			f.Run("trigger", func(f *foundation.F) {
				f.Parallel()

				sendSignal(t, syscall.SIGUSR1)
			})
		})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected SIGUSR1 to trigger shutdown after SetSignals")
	}
}

func TestWithoutSignalsRemovesSignal(t *testing.T) {
	// WithoutSignals removes SIGINT, keeping only SIGTERM from defaults.
	// We use SetSignals to set SIGUSR1 and SIGUSR2, then WithoutSignals to
	// remove SIGUSR1. Sending SIGUSR2 should still trigger shutdown.
	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			f.Run("signals", interrupt.SignalFunc(
				interrupt.SetSignals(interrupt.Shutdown, syscall.SIGUSR1, syscall.SIGUSR2),
				interrupt.WithoutSignals(interrupt.Shutdown, syscall.SIGUSR1),
			))

			f.Run("trigger", func(f *foundation.F) {
				f.Parallel()

				sendSignal(t, syscall.SIGUSR2)
			})
		})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected SIGUSR2 to trigger shutdown after WithoutSignals removed SIGUSR1")
	}
}

func TestOptionsSkipsNil(t *testing.T) {
	// Passing nil options, alone or in an Options, should not panic, and the
	// options around them are applied in order.
	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			f.Run("signals", interrupt.SignalFunc(
				nil,
				interrupt.Options{nil, interrupt.SetSignals(interrupt.Shutdown, syscall.SIGUSR1), nil},
				nil,
			))

			f.Run("trigger", func(f *foundation.F) {
				f.Parallel()

				sendSignal(t, syscall.SIGUSR1)
			})
		})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected shutdown to complete with nil options in the mix")
	}
}

func TestTheKindsAreTheWatchKinds(t *testing.T) {
	if interrupt.Shutdown != watch.Shutdown || interrupt.Restart != watch.Restart {
		t.Fatal("interrupt.Shutdown and interrupt.Restart must be the watch kinds")
	}
}

// Shutdown has defaults, restart has none: SIGHUP is also what a closing terminal
// sends, and a program that was never written to run its root function twice
// would restart in a dead terminal instead of exiting.
func TestDefaults(t *testing.T) {
	shutdown, restart := interrupt.Resolve()

	if want := []os.Signal{syscall.SIGINT, syscall.SIGTERM}; !reflect.DeepEqual(shutdown, want) {
		t.Errorf("shutdown = %v, want %v", shutdown, want)
	}

	if len(restart) != 0 {
		t.Errorf("restart = %v, want none", restart)
	}
}

// Each helper means exactly its general form, and the options apply in order.
func TestHelpersAreTheGeneralOptions(t *testing.T) {
	tests := map[string]struct {
		helper, general interrupt.Option
	}{
		"WithShutdownSignals": {
			interrupt.WithShutdownSignals(syscall.SIGUSR1, syscall.SIGUSR2),
			interrupt.WithSignals(interrupt.Shutdown, syscall.SIGUSR1, syscall.SIGUSR2),
		},
		"WithRestartSignals": {
			interrupt.WithRestartSignals(syscall.SIGHUP, syscall.SIGUSR1),
			interrupt.WithSignals(interrupt.Restart, syscall.SIGHUP, syscall.SIGUSR1),
		},
		"SetShutdownSignals": {
			interrupt.SetShutdownSignals(syscall.SIGUSR1),
			interrupt.SetSignals(interrupt.Shutdown, syscall.SIGUSR1),
		},
		"SetRestartSignals": {
			interrupt.SetRestartSignals(syscall.SIGHUP),
			interrupt.SetSignals(interrupt.Restart, syscall.SIGHUP),
		},
		"SetShutdownSignals with none": {
			interrupt.SetShutdownSignals(),
			interrupt.SetSignals(interrupt.Shutdown),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			hs, hr := interrupt.Resolve(tt.helper)
			gs, gr := interrupt.Resolve(tt.general)

			if !reflect.DeepEqual(hs, gs) || !reflect.DeepEqual(hr, gr) {
				t.Errorf("helper = %v, %v; general = %v, %v", hs, hr, gs, gr)
			}
		})
	}
}

func TestOptionsPerKind(t *testing.T) {
	tests := map[string]struct {
		opts     []interrupt.Option
		shutdown []os.Signal
		restart  []os.Signal
	}{
		"restart added, shutdown defaults kept": {
			[]interrupt.Option{interrupt.WithRestartSignals(syscall.SIGHUP)},
			[]os.Signal{syscall.SIGINT, syscall.SIGTERM},
			[]os.Signal{syscall.SIGHUP},
		},
		"shutdown added after the defaults": {
			[]interrupt.Option{interrupt.WithShutdownSignals(syscall.SIGUSR2)},
			[]os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR2},
			nil,
		},
		"set shutdown replaces the defaults": {
			[]interrupt.Option{interrupt.SetShutdownSignals(syscall.SIGTERM)},
			[]os.Signal{syscall.SIGTERM},
			nil,
		},
		"set restart leaves shutdown alone": {
			[]interrupt.Option{interrupt.SetRestartSignals(syscall.SIGHUP, syscall.SIGUSR1)},
			[]os.Signal{syscall.SIGINT, syscall.SIGTERM},
			[]os.Signal{syscall.SIGHUP, syscall.SIGUSR1},
		},
		"set restart replaces restart only": {
			[]interrupt.Option{
				interrupt.WithRestartSignals(syscall.SIGHUP),
				interrupt.SetRestartSignals(syscall.SIGUSR1),
			},
			[]os.Signal{syscall.SIGINT, syscall.SIGTERM},
			[]os.Signal{syscall.SIGUSR1},
		},
		"without removes from its own kind only": {
			[]interrupt.Option{
				interrupt.WithRestartSignals(syscall.SIGHUP),
				interrupt.WithoutSignals(interrupt.Shutdown, syscall.SIGHUP, syscall.SIGINT),
			},
			[]os.Signal{syscall.SIGTERM},
			[]os.Signal{syscall.SIGHUP},
		},
		"without for restart": {
			[]interrupt.Option{
				interrupt.WithRestartSignals(syscall.SIGHUP, syscall.SIGUSR1),
				interrupt.WithoutSignals(interrupt.Restart, syscall.SIGHUP),
			},
			[]os.Signal{syscall.SIGINT, syscall.SIGTERM},
			[]os.Signal{syscall.SIGUSR1},
		},
		"nil options are skipped": {
			[]interrupt.Option{nil, interrupt.Options{nil, interrupt.WithRestartSignals(syscall.SIGHUP)}, nil},
			[]os.Signal{syscall.SIGINT, syscall.SIGTERM},
			[]os.Signal{syscall.SIGHUP},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			shutdown, restart := interrupt.Resolve(tt.opts...)

			if !reflect.DeepEqual(shutdown, tt.shutdown) && !(len(shutdown) == 0 && len(tt.shutdown) == 0) {
				t.Errorf("shutdown = %v, want %v", shutdown, tt.shutdown)
			}

			if !reflect.DeepEqual(restart, tt.restart) && !(len(restart) == 0 && len(tt.restart) == 0) {
				t.Errorf("restart = %v, want %v", restart, tt.restart)
			}
		})
	}
}

// A Set option keeps its own copy, so that the caller changing the slice it passed
// in later can't change which signals are listened for.
func TestSetKeepsItsOwnCopy(t *testing.T) {
	sigs := []os.Signal{syscall.SIGHUP}
	opt := interrupt.SetRestartSignals(sigs...)

	sigs[0] = syscall.SIGKILL

	if _, restart := interrupt.Resolve(opt); !reflect.DeepEqual(restart, []os.Signal{syscall.SIGHUP}) {
		t.Errorf("restart = %v, want [hangup]", restart)
	}

	// Applying the same option twice, as options are for every instance of a
	// restarting program, gives the same result and shares nothing.
	a, _ := interrupt.Resolve(interrupt.SetShutdownSignals(syscall.SIGUSR1))
	a[0] = syscall.SIGUSR2

	if b, _ := interrupt.Resolve(interrupt.SetShutdownSignals(syscall.SIGUSR1)); len(b) != 1 {
		t.Errorf("shutdown = %v, a later resolve saw an earlier one's change", b)
	}
}

// What is asked for is checked when the function starts, and a mistake stops
// startup. A signal handler that quietly isn't what was meant is the kind of
// mistake that is found when the process is asked to stop and doesn't: with no
// signals at all, the handler used to be for every signal there is.

// customSignal is an os.Signal that is not a syscall.Signal.
type customSignal string

func (customSignal) Signal()          {}
func (s customSignal) String() string { return string(s) }

func TestASignalSetThatIsNotOneStopsStartup(t *testing.T) {
	tests := map[string][]interrupt.Option{
		"no signals":                {interrupt.SetSignals(interrupt.Shutdown)},
		"every default removed":     {interrupt.WithoutSignals(interrupt.Shutdown, syscall.SIGINT, syscall.SIGTERM)},
		"a signal that is nil":      {interrupt.SetSignals(interrupt.Shutdown, nil)},
		"SIGKILL":                   {interrupt.SetSignals(interrupt.Shutdown, syscall.SIGKILL)},
		"SIGSTOP":                   {interrupt.SetSignals(interrupt.Shutdown, syscall.SIGSTOP)},
		"SIGKILL among the others":  {interrupt.WithSignals(interrupt.Shutdown, syscall.SIGKILL)},
		"a signal of another kind":  {interrupt.SetSignals(interrupt.Shutdown, customSignal("custom"))},
		"valid and not, in options": {interrupt.Options{interrupt.SetSignals(interrupt.Shutdown, syscall.SIGUSR1), interrupt.WithSignals(interrupt.Shutdown, syscall.SIGSTOP)}},

		// The same mistakes for restart signals, and the ones only they can make.
		"restart SIGKILL":                             {interrupt.WithRestartSignals(syscall.SIGKILL)},
		"restart SIGSTOP":                             {interrupt.SetRestartSignals(syscall.SIGSTOP)},
		"restart nil":                                 {interrupt.WithRestartSignals(nil)},
		"restart of another kind":                     {interrupt.WithRestartSignals(customSignal("custom"))},
		"restart on SIGINT":                           {interrupt.WithRestartSignals(syscall.SIGINT)},
		"restart on SIGTERM":                          {interrupt.WithRestartSignals(syscall.SIGTERM)},
		"restart on SIGINT by kind":                   {interrupt.WithSignals(interrupt.Restart, syscall.SIGINT)},
		"a signal in both kinds":                      {interrupt.WithShutdownSignals(syscall.SIGUSR1), interrupt.WithRestartSignals(syscall.SIGUSR1)},
		"a signal in both, restart first":             {interrupt.WithRestartSignals(syscall.SIGUSR1), interrupt.WithShutdownSignals(syscall.SIGUSR1)},
		"a shutdown default in restart":               {interrupt.SetRestartSignals(syscall.SIGTERM)},
		"a kind that is not one":                      {interrupt.WithSignals(interrupt.Kind(0), syscall.SIGUSR1)},
		"a kind that is out of range":                 {interrupt.SetSignals(interrupt.Kind(99), syscall.SIGUSR1)},
		"valid restart beside a bad one":              {interrupt.WithRestartSignals(syscall.SIGUSR1, syscall.SIGKILL)},
		"restart on a signal that shutdown keeps":     {interrupt.SetShutdownSignals(syscall.SIGINT, syscall.SIGUSR1), interrupt.WithRestartSignals(syscall.SIGINT)},
		"restart on SIGTERM, no shutdown signals":     {interrupt.SetShutdownSignals(), interrupt.WithRestartSignals(syscall.SIGTERM)},
		"restart on SIGTERM, shutdown still has it":   {interrupt.SetShutdownSignals(syscall.SIGTERM), interrupt.WithRestartSignals(syscall.SIGTERM)},
		"restart on both, shutdown keeps one of them": {interrupt.SetShutdownSignals(syscall.SIGINT), interrupt.WithRestartSignals(syscall.SIGINT, syscall.SIGTERM)},
		"restart valid, shutdown none":                {interrupt.WithRestartSignals(syscall.SIGUSR1), interrupt.SetShutdownSignals()},
		"without, for a kind that is not one":         {interrupt.WithoutSignals(interrupt.Kind(0), syscall.SIGINT)},
	}

	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			recorder := foundationtest.NewLogRecorder()
			recorder.Install(t)

			started := make(chan struct{}, 1)

			done := make(chan struct{})

			go func() {
				defer close(done)

				foundation.Run("test", func(f *foundation.F) {
					f.Run("signals", interrupt.SignalFunc(opts...))

					// Nothing is started after a mistake.
					f.Run("after", func(*foundation.F) { started <- struct{}{} })
				})
			}()

			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("the run did not end")
			}

			if codes := exit.Codes(); len(codes) != 1 || codes[0] != 1 {
				t.Fatalf("exit codes = %v, want [1]", codes)
			}

			select {
			case <-started:
				t.Error("a function was started after the signals were refused")
			default:
			}

			failed := 0

			for _, e := range recorder.Entries() {
				if e.Record.Message != "shutting down due to failure" {
					continue
				}

				failed++

				e.Record.Attrs(func(a slog.Attr) bool {
					if a.Key != "err" {
						return true
					}

					msg := a.Value.String()

					if len(msg) > 300 {
						t.Errorf("an error of %d bytes", len(msg))
					}

					for _, r := range msg {
						if unicode.IsControl(r) {
							t.Errorf("the error contains %q", r)
						}
					}

					return true
				})
			}

			if failed != 1 {
				t.Errorf("%d failures logged, want 1", failed)
			}
		})
	}
}

// Only the signals asked for stop the program. Another one that arrives is not a
// request to stop, which a handler for every signal would have taken it for.
func TestOnlyTheSignalsAskedForStopTheProgram(t *testing.T) {
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)

		foundation.Run("test", func(f *foundation.F) {
			f.Run("signals", interrupt.SignalFunc(interrupt.SetSignals(interrupt.Shutdown, syscall.SIGUSR1)))

			f.Run("trigger", func(f *foundation.F) {
				f.Parallel()

				// SIGUSR2 is not asked for.
				sendSignal(t, syscall.SIGUSR2)

				select {
				case <-f.Context().Done():
					t.Error("a signal that was not asked for stopped the program")

					return
				case <-time.After(300 * time.Millisecond):
				}

				sendSignal(t, syscall.SIGUSR1)
			})
		})
	}()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the signal that was asked for did not stop the program")
	}
}

// In a dry run no signal handler is installed, and the function ends at once
// instead of waiting for a signal that is never going to be handled.
func TestSignalsSkipInADryRun(t *testing.T) {
	foundationtest.NewExitRecorder().Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			interrupt.Signals(f)
		}, foundation.WithDryRun(true))
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the dry run did not finish")
	}

	var skipped bool

	for _, e := range recorder.Entries() {
		if e.Record.Message != "skipped" {
			continue
		}

		for _, a := range e.Attrs {
			if a.Key == "fn" && a.Value.String() == "test/signals" {
				skipped = true
			}
		}
	}

	if !skipped {
		t.Fatal("expected the signals function to say it was skipped")
	}
}

// Neither watcher is started in a dry run, so it installs no handler of either
// kind, and one that would be refused is not found (as for any watch).
func TestSignalsOfBothKindsSkipInADryRun(t *testing.T) {
	foundationtest.NewExitRecorder().Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			interrupt.Signals(f, interrupt.WithRestartSignals(syscall.SIGUSR1))
		}, foundation.WithDryRun(true))
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the dry run did not finish")
	}

	for _, e := range recorder.Entries() {
		for _, a := range e.Attrs {
			if a.Key == "fn" && (a.Value.String() == "test/signals/watch") {
				t.Errorf("a watcher ran in a dry run: %q", e.Record.Message)
			}
		}
	}
}

// interrupt.Signals is what asks for a shutdown, and a program that restarts runs
// its root function again, so it is there for every instance: it is a watcher like
// any other, stopped with its instance and started with the next, and what it
// asks for is a shutdown, which always wins over a restart.

// fast keeps the restarts of a test to a few milliseconds.
var fast = foundation.WithRestartPolicy(foundation.RestartPolicy{
	Debounce:    time.Millisecond,
	MaxDebounce: 10 * time.Millisecond,
	MinInterval: time.Millisecond,
})

func TestAProgramThatRestartsStillShutsDownOnASignal(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var instances atomic.Int32

	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			interrupt.Signals(f, interrupt.SetSignals(interrupt.Shutdown, syscall.SIGUSR1))

			// A candidate is a dry run, and is not an instance.
			if f.DryRun() {
				return
			}

			if instances.Add(1) == 1 {
				f.Restart("again")

				return
			}

			// The second instance has its own handler, which this reaches.
			f.Run("trigger", func(f *foundation.F) {
				f.Parallel()

				sendSignal(t, syscall.SIGUSR1)
			})
		}, fast)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the program did not shut down on the signal after a restart")
	}

	if got := instances.Load(); got != 2 {
		t.Errorf("%d instances ran, want 2", got)
	}

	if codes := exit.Codes(); len(codes) != 0 {
		t.Errorf("exit codes = %v, want none: a shutdown is not a failure", codes)
	}

	// The candidate did not install a handler: the signals function skipped.
	skipped := 0

	for _, e := range recorder.Entries() {
		if e.Record.Message != "skipped" {
			continue
		}

		var fn, candidate string

		for _, a := range e.Attrs {
			switch a.Key {
			case "fn":
				fn = a.Value.String()
			case "candidate":
				candidate = a.Value.String()
			}
		}

		if fn == "test/signals" && candidate == "true" {
			skipped++
		}
	}

	if skipped != 1 {
		t.Errorf("the candidate skipped the signals function %d times, want 1", skipped)
	}
}

// A signal that arrives while the old instance is being stopped is a shutdown, and
// wins: the signals function was started first, so it is stopped last and is still
// handling signals while the rest stops. No new instance starts.
func TestASignalDuringARestartShutsTheProgramDown(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	var instances atomic.Int32

	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			interrupt.Signals(f, interrupt.SetSignals(interrupt.Shutdown, syscall.SIGUSR1))

			if f.DryRun() {
				return
			}

			instances.Add(1)

			f.Run("slow", func(f *foundation.F) {
				f.Parallel()

				<-f.Context().Done()

				// Stopping takes a while, and the signal comes in the middle of it.
				time.Sleep(500 * time.Millisecond)
			})

			go func() {
				time.Sleep(50 * time.Millisecond)

				_ = syscall.Kill(syscall.Getpid(), syscall.SIGUSR1)
			}()

			f.Restart("now")
		}, fast)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the program did not end")
	}

	if got := instances.Load(); got != 1 {
		t.Errorf("%d instances ran, want 1: the shutdown should have won the restart", got)
	}

	if codes := exit.Codes(); len(codes) != 0 {
		t.Errorf("exit codes = %v, want none", codes)
	}
}

// A restart signal restarts the program and a shutdown signal ends it, through the
// same function: the second instance has a handler of its own for both.
func TestARestartSignalRestartsAndAShutdownSignalEnds(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var instances atomic.Int32

	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			interrupt.Signals(f,
				interrupt.SetShutdownSignals(syscall.SIGUSR2),
				interrupt.WithRestartSignals(syscall.SIGUSR1),
			)

			// A candidate is a dry run, and is not an instance.
			if f.DryRun() {
				return
			}

			n := instances.Add(1)

			f.Run("trigger", func(f *foundation.F) {
				f.Parallel()

				if n == 1 {
					sendSignal(t, syscall.SIGUSR1)

					return
				}

				sendSignal(t, syscall.SIGUSR2)
			})
		}, fast)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the program did not restart and then shut down")
	}

	if got := instances.Load(); got != 2 {
		t.Errorf("%d instances ran, want 2", got)
	}

	if codes := exit.Codes(); len(codes) != 0 {
		t.Errorf("exit codes = %v, want none", codes)
	}

	var restarted bool

	for _, e := range recorder.Entries() {
		if e.Record.Message != "restarting" {
			continue
		}

		for _, a := range e.Attrs {
			if a.Value.String() == "signal: user defined signal 1" {
				restarted = true
			}
		}

		e.Record.Attrs(func(a slog.Attr) bool {
			if a.Value.String() == "signal: user defined signal 1" {
				restarted = true
			}

			return true
		})
	}

	if !restarted {
		t.Error("expected the restart to name the signal as its reason")
	}
}

// With no restart signals asked for, a signal that is only a restart signal
// elsewhere does nothing: the default is shutdown on SIGINT and SIGTERM alone.
func TestWithoutRestartSignalsASignalDoesNotRestart(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	var instances atomic.Int32

	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			interrupt.Signals(f, interrupt.SetShutdownSignals(syscall.SIGUSR2))

			if f.DryRun() {
				return
			}

			instances.Add(1)

			f.Run("trigger", func(f *foundation.F) {
				f.Parallel()

				// SIGUSR1 is nobody's request here.
				sendSignal(t, syscall.SIGUSR1)

				time.Sleep(300 * time.Millisecond)

				sendSignal(t, syscall.SIGUSR2)
			})
		}, fast)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the program did not shut down")
	}

	if got := instances.Load(); got != 1 {
		t.Errorf("%d instances ran, want 1: nothing asked for a restart", got)
	}
}

// A restart signal that arrives while the old instance is being stopped (the gap
// before the next handler exists) is survived, not the default action: SIGHUP
// ends a process that does not handle it. SIGHUP is used because only it, SIGINT
// and SIGTERM show the difference, and the parent catches it first so that a test
// binary started with it ignored is not mistaken for handling it.
func TestARestartSignalInTheGapIsSurvived(t *testing.T) {
	signal.Notify(make(chan os.Signal, 8), syscall.SIGHUP)

	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	var instances atomic.Int32

	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			interrupt.Signals(f,
				interrupt.SetShutdownSignals(syscall.SIGUSR2),
				interrupt.WithRestartSignals(syscall.SIGHUP),
			)

			if f.DryRun() {
				return
			}

			n := instances.Add(1)

			if n == 1 {
				f.Run("slow", func(f *foundation.F) {
					f.Parallel()

					<-f.Context().Done()

					// Stopping takes a while, and the signal comes in the middle.
					time.Sleep(300 * time.Millisecond)
				})

				go func() {
					time.Sleep(50 * time.Millisecond)

					_ = syscall.Kill(syscall.Getpid(), syscall.SIGHUP)
				}()

				f.Restart("now")

				return
			}

			f.Run("trigger", func(f *foundation.F) {
				f.Parallel()

				sendSignal(t, syscall.SIGUSR2)
			})
		}, fast)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the program did not end")
	}

	if codes := exit.Codes(); len(codes) != 0 {
		t.Errorf("exit codes = %v, want none", codes)
	}
}

// SIGINT and SIGTERM are only shutdown signals by default. Once shutdown is asked
// for on others, they are free, and a restart on one is a choice like any other.
// What is refused is the same signal in both kinds (see the validation table), not
// these two as such. The run does restart, and then ends on the shutdown signal.
func TestARestartMayUseSIGINTOrSIGTERMOnceShutdownIsElsewhere(t *testing.T) {
	tests := map[string]struct {
		shutdown []os.Signal
		restart  syscall.Signal
		stop     syscall.Signal
	}{
		"SIGTERM restarts, SIGUSR2 shuts down": {[]os.Signal{syscall.SIGUSR2}, syscall.SIGTERM, syscall.SIGUSR2},
		"SIGINT restarts, SIGUSR2 shuts down":  {[]os.Signal{syscall.SIGUSR2}, syscall.SIGINT, syscall.SIGUSR2},
		"SIGTERM restarts, SIGINT shuts down":  {[]os.Signal{syscall.SIGINT}, syscall.SIGTERM, syscall.SIGINT},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			var instances atomic.Int32

			done := make(chan struct{})

			go func() {
				defer close(done)

				foundation.Run("test", func(f *foundation.F) {
					interrupt.Signals(f,
						interrupt.SetShutdownSignals(tt.shutdown...),
						interrupt.WithRestartSignals(tt.restart),
					)

					if f.DryRun() {
						return
					}

					n := instances.Add(1)

					f.Run("trigger", func(f *foundation.F) {
						f.Parallel()

						if n == 1 {
							sendSignal(t, tt.restart)

							return
						}

						sendSignal(t, tt.stop)
					})
				}, fast)
			}()

			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the program did not restart and then shut down")
			}

			if got := instances.Load(); got != 2 {
				t.Errorf("%d instances ran, want 2", got)
			}

			if codes := exit.Codes(); len(codes) != 0 {
				t.Errorf("exit codes = %v, want none", codes)
			}
		})
	}
}

const helperEnv = "FOUNDATION_INTERRUPT_HELPER"

// TestHelperProcess is not a real test: it is the program the force quit test
// runs in a subprocess. It has a node that takes a very long time to stop, so
// the shutdown it triggers is still in progress when the second signal arrives.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("helper process for TestSecondSignalForceQuits")
	}

	foundation.Run("helper", func(f *foundation.F) {
		interrupt.Signals(f)

		f.Run("slow", func(f *foundation.F) {
			f.SetTimeout(time.Minute)
			f.Parallel()

			os.Stdout.WriteString("ready\n")

			<-f.Context().Done()

			os.Stdout.WriteString("stopping\n")

			// Stays busy long enough that only a forced exit can end the process.
			time.Sleep(time.Minute)
		})
	})
}

// During a slow shutdown the signals node is the last to stop, so it must give
// up handling signals as soon as the first arrives. Otherwise a second Ctrl-C
// is swallowed and the process can't be force quit.
func TestSecondSignalForceQuits(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), helperEnv+"=1")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = cmd.Process.Kill() })

	lines := bufio.NewScanner(stdout)

	expect := func(want string) {
		t.Helper()

		for lines.Scan() {
			if lines.Text() == want {
				return
			}
		}

		t.Fatalf("helper exited before printing %q", want)
	}

	expect("ready")

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}

	expect("stopping") // shutdown is now in progress, and stuck

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)

	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
		if status := cmd.ProcessState.Sys().(syscall.WaitStatus); !status.Signaled() || status.Signal() != syscall.SIGINT {
			t.Fatalf("expected the helper to be killed by SIGINT, got %v", cmd.ProcessState)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second signal was swallowed: process still running during shutdown")
	}
}
