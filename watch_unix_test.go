//go:build unix

package foundation_test

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/interrupt"
)

// With the real signals, on the real clock: a program that restarts on SIGHUP, as
// daemons are used to, and shuts down on another signal. The signal is sent to the
// test process itself, after the watch has begun, so it is the watcher that
// handles it.

// fast is a policy for a test on the real clock: restarts that begin within a few
// milliseconds.
var fast = foundation.WithRestartPolicy(foundation.RestartPolicy{
	Debounce:    time.Millisecond,
	MaxDebounce: 10 * time.Millisecond,
	MinInterval: time.Millisecond,
})

// runWithin runs the program and fails the test if it hasn't returned in time.
func runWithin(t *testing.T, limit time.Duration, fn foundation.Func, opts ...foundation.Option) {
	t.Helper()

	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("svc", fn, opts...)
	}()

	select {
	case <-done:
	case <-time.After(limit):
		t.Fatal("the program did not finish")
	}
}

func TestARealSIGHUPRestartsTheProgram(t *testing.T) {
	exit := installExit(t)

	var p program

	runWithin(t, 20*time.Second, p.fn(func(f *foundation.F, n int) {
		interrupt.Signals(f, interrupt.WithRestartSignals(syscall.SIGHUP))

		if n > 1 {
			f.Shutdown("done")

			return
		}

		if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
			f.Fail(err)
		}

		until(f)
	}), fast)

	if got := p.instances(); got != 2 {
		t.Errorf("%d instances ran, want 2", got)
	}

	assertExitCodes(t, exit)
}

func TestARealSignalCanShutTheProgramDown(t *testing.T) {
	exit := installExit(t)

	var p program

	runWithin(t, 20*time.Second, p.fn(func(f *foundation.F, n int) {
		interrupt.Signals(f, interrupt.SetShutdownSignals(syscall.SIGHUP))

		if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
			f.Fail(err)
		}

		until(f)
	}), fast)

	if got := p.instances(); got != 1 {
		t.Errorf("%d instances ran, want 1", got)
	}

	assertExitCodes(t, exit)
}

// A restart has a gap: the old instance's watcher has stopped, and so has given its
// signals back, and the new one has not started. A SIGHUP in it would have ended the
// process, which is the one signal that is meant to reload it. Here the gap is made
// long on purpose, by a cleanup that sends the signal and then takes its time, and
// is seen in a process of its own, as what a signal does to a process can only be.
// The parent catches SIGHUP first, as in the tests of package interrupt: a process can
// be started with it ignored, which exec passes on, and which would hide that the
// default action is what is being avoided.

const helperEnv = "FOUNDATION_TEST_HELPER"

func TestRestartGapHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "restart-gap" {
		t.Skip("only used by TestASIGHUPInTheGapBetweenTwoInstancesIsSurvived")
	}

	var instances atomic.Int32

	foundation.Run("svc", func(f *foundation.F) {
		// A candidate is a dry run, and is not an instance.
		if f.DryRun() {
			return
		}

		n := instances.Add(1)

		// The watcher is started first, so it is stopped last, and then this cleanup
		// runs: with nothing watching.
		interrupt.Signals(f, interrupt.WithRestartSignals(syscall.SIGHUP))

		if n > 1 {
			f.Shutdown("done")

			return
		}

		f.Cleanup(func() {
			// Long enough for the watch to have given its signal back (it does so
			// in a goroutine of its own, as the context is done), so that the signal
			// is sent in the gap and not into the last of the old watch.
			time.Sleep(200 * time.Millisecond)

			_ = syscall.Kill(os.Getpid(), syscall.SIGHUP)

			time.Sleep(500 * time.Millisecond)
		})

		// The first asks for the restart.
		if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
			f.Fail(err)
		}

		until(f)
	}, fast)

	fmt.Println("instances", instances.Load())

	os.Exit(0)
}

func TestASIGHUPInTheGapBetweenTwoInstancesIsSurvived(t *testing.T) {
	ch := make(chan os.Signal, 1)

	signal.Notify(ch, syscall.SIGHUP)

	t.Cleanup(func() { signal.Stop(ch) })

	cmd := exec.Command(os.Args[0], "-test.run=^TestRestartGapHelperProcess$")
	cmd.Env = append(os.Environ(), helperEnv+"=restart-gap")

	done := make(chan struct{})

	var (
		out []byte
		err error
	)

	go func() {
		defer close(done)

		out, err = cmd.CombinedOutput()
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()

		t.Fatal("the program did not finish")
	}

	if err != nil {
		t.Fatalf("the program ended with %v, want it to survive the signal in the gap: %s", err, out)
	}

	if !strings.Contains(string(out), "instances 2") {
		t.Errorf("the program printed %q, want two instances", out)
	}
}
