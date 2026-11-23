//go:build unix

package interrupt_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.krak3n.dev/foundation/interrupt"
	"go.krak3n.dev/foundation/watch"
)

// These send real signals to the test process. Each test holds a handler of its own
// for the signals it sends, so that if the watcher is broken the process is not
// the thing that is terminated.

func guard(t *testing.T, sigs ...os.Signal) {
	t.Helper()

	ch := make(chan os.Signal, 64)

	signal.Notify(ch, sigs...)

	t.Cleanup(func() { signal.Stop(ch) })
}

func send(t *testing.T, sig syscall.Signal) {
	t.Helper()

	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		t.Fatal(err)
	}
}

func receive(t *testing.T, events <-chan watch.Event) watch.Event {
	t.Helper()

	select {
	case ev, ok := <-events:
		if !ok {
			t.Fatal("the channel was closed")
		}

		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}

	return watch.Event{}
}

func waitClosed(t *testing.T, events <-chan watch.Event) {
	t.Helper()

	select {
	case ev, ok := <-events:
		if ok {
			t.Fatalf("got %+v, want the channel closed", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the channel was not closed")
	}
}

func TestAHangupIsARestart(t *testing.T) {
	guard(t, syscall.SIGHUP)

	events, cancel := start(t, interrupt.NewTrigger(watch.Restart, syscall.SIGHUP))
	defer cancel()

	send(t, syscall.SIGHUP)

	ev := receive(t, events)

	if ev.Kind != watch.Restart || !strings.Contains(ev.Reason, "hangup") {
		t.Fatalf("got %+v", ev)
	}

	plain(t, ev)

	// A restart is not the end: another signal is another event.
	send(t, syscall.SIGHUP)

	if ev := receive(t, events); ev.Kind != watch.Restart {
		t.Fatalf("got %+v", ev)
	}
}

func TestATerminateIsAShutdownAndTheEnd(t *testing.T) {
	guard(t, syscall.SIGTERM)

	events, cancel := start(t, interrupt.NewTrigger(watch.Shutdown, syscall.SIGINT, syscall.SIGTERM))
	defer cancel()

	send(t, syscall.SIGTERM)

	ev := receive(t, events)

	if ev.Kind != watch.Shutdown || !strings.Contains(ev.Reason, "terminated") {
		t.Fatalf("got %+v", ev)
	}

	waitClosed(t, events)
}

func TestAnyMapsSignalsToWhatEachMeans(t *testing.T) {
	guard(t, syscall.SIGHUP, syscall.SIGTERM)

	events, cancel := start(t, watch.Any(
		interrupt.NewTrigger(watch.Restart, syscall.SIGHUP),
		interrupt.NewTrigger(watch.Shutdown, syscall.SIGINT, syscall.SIGTERM),
	))
	defer cancel()

	send(t, syscall.SIGHUP)

	if ev := receive(t, events); ev.Kind != watch.Restart {
		t.Fatalf("got %+v", ev)
	}

	send(t, syscall.SIGTERM)

	if ev := receive(t, events); ev.Kind != watch.Shutdown {
		t.Fatalf("got %+v", ev)
	}

	waitClosed(t, events)
}

func TestCancellingReleasesTheSignals(t *testing.T) {
	guard(t, syscall.SIGHUP)

	events, cancel := start(t, interrupt.NewTrigger(watch.Restart, syscall.SIGHUP))

	cancel()
	waitClosed(t, events)

	send(t, syscall.SIGHUP)

	select {
	case ev, ok := <-events:
		if ok {
			t.Fatalf("got %+v after the watch ended", ev)
		}
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSignalsRefusesWhatCannotBeStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := interrupt.NewTrigger(watch.Shutdown, syscall.SIGSTOP).Watch(ctx); err == nil {
		t.Fatal("SIGSTOP can't be caught, so asking for it is a mistake")
	}
}

// --- the second signal ---

// A shutdown gives the signals back. That is what lets a second Ctrl-C force quit a
// slow shutdown: the first asks politely, and with no handler left the second gets
// the default action. This is checked in a process of its own, which is the only
// place it can be seen.

const triggerHelperEnv = "INTERRUPT_TRIGGER_TEST_HELPER"

func TestTriggerHelperProcess(t *testing.T) {
	if os.Getenv(triggerHelperEnv) != "1" {
		t.Skip("only used by TestASecondSignalGetsTheDefaultActionAfterAShutdown")
	}

	events, err := interrupt.NewTrigger(watch.Shutdown, syscall.SIGINT, syscall.SIGTERM).Watch(context.Background())
	if err != nil {
		fmt.Println("error", err)
		os.Exit(2)
	}

	fmt.Println("ready")

	ev := <-events

	fmt.Println("shutdown", ev.Kind == watch.Shutdown)

	// Nothing is listening now. Block, as a slow shutdown would, until a signal
	// ends the process: if one never does the parent kills it.
	time.Sleep(time.Minute)

	os.Exit(3)
}

func TestASecondSignalGetsTheDefaultActionAfterAShutdown(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestTriggerHelperProcess$")
	cmd.Env = append(os.Environ(), triggerHelperEnv+"=1")

	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = cmd.Process.Kill() })

	lines := bufio.NewScanner(out)

	next := func(want string) {
		t.Helper()

		done := make(chan bool, 1)

		go func() { done <- lines.Scan() }()

		select {
		case ok := <-done:
			if !ok || lines.Text() != want {
				t.Fatalf("got %q (%v), want %q", lines.Text(), lines.Err(), want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}

	next("ready")

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	next("shutdown true")

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	waited := make(chan error, 1)

	go func() { waited <- cmd.Wait() }()

	select {
	case err := <-waited:
		var exit *exec.ExitError

		if !errors.As(err, &exit) {
			t.Fatalf("the process ended with %v, want it killed by the signal", err)
		}

		status, ok := exit.Sys().(syscall.WaitStatus)
		if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
			t.Fatalf("the process ended with %v, want it killed by SIGTERM", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the second signal did nothing: the watcher kept the signals after a shutdown")
	}
}
