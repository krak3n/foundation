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
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.krak3n.dev/foundation/interrupt"
	"go.krak3n.dev/foundation/watch"
)

// A watch that restarts on a signal is stopped, and started again, around every
// restart. Between the two nothing handles the signal, and for SIGHUP the default
// action is to end the process: a program that reloads on SIGHUP would be killed by
// one that arrives at the wrong moment. So a Restart watch that has ended keeps
// handling its signals, discarding them, for a grace period, and then gives them
// back. Nothing else changes: a Shutdown watch gives its signals back at once (a
// second Ctrl-C must force quit), and a watch started during the grace still gets
// the signal, since the signal goes to every handler.
//
// What a signal does to a process can only be seen in a process of its own, so each
// of these runs a scenario in a child (the test binary itself) and looks at what it
// printed and how it ended. The child shortens the grace, with the test hook, to
// 600ms.
//
// The child must start with these signals at their default, which a Go program is
// not if it was started with SIGHUP ignored (nohup, a background job, some CI
// shells): an ignored signal stays ignored through exec, and giving it back would
// then mean ignoring it, hiding what these look for. Catching it in the parent
// first (signal.Notify) is what makes it the default in the child: a caught signal
// is reset by exec, an ignored one is not, and Notify stops ignoring it.

const (
	scenarioEnv = "INTERRUPT_TRIGGER_SCENARIO"
	childGrace  = 600 * time.Millisecond
)

// selfSignal sends sig to this process.
func selfSignal(sig syscall.Signal) {
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		fmt.Println("error", err)
		os.Exit(2)
	}
}

// begin starts a watch in the child, and returns it with a function that ends it
// and reports how long the channel took to close.
func begin(kind watch.Kind, sigs ...os.Signal) (events <-chan watch.Event, end func() time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())

	events, err := interrupt.NewTrigger(kind, sigs...).Watch(ctx)
	if err != nil {
		fmt.Println("error", err)
		os.Exit(2)
	}

	return events, func() time.Duration {
		start := time.Now()

		cancel()

		for {
			select {
			case _, ok := <-events:
				if !ok {
					return time.Since(start)
				}
			case <-time.After(10 * time.Second):
				fmt.Println("error: the channel was not closed")
				os.Exit(2)
			}
		}
	}
}

// ended ends a watch, and says so if the channel was held up: it closes at once,
// whatever the grace.
func ended(end func() time.Duration) {
	if took := end(); took > 200*time.Millisecond {
		fmt.Println("slow close", took)
	}
}

var scenarios = map[string]func(){
	// The signal is survived within the grace and ends the process after it.
	"swallow": func() {
		_, end := begin(watch.Restart, syscall.SIGHUP)
		ended(end)

		selfSignal(syscall.SIGHUP)
		time.Sleep(100 * time.Millisecond)

		fmt.Println("survived in grace")

		time.Sleep(childGrace + 300*time.Millisecond)

		selfSignal(syscall.SIGHUP)
		time.Sleep(time.Second)

		fmt.Println("alive after grace")
	},

	// A second watch ending moves the end of the grace on, so it is not the first
	// one's that counts.
	"extend": func() {
		_, end := begin(watch.Restart, syscall.SIGHUP)
		ended(end)

		time.Sleep(350 * time.Millisecond)

		_, end = begin(watch.Restart, syscall.SIGHUP)
		ended(end)

		// 700ms in: past the first grace (600ms), inside the second (950ms).
		time.Sleep(350 * time.Millisecond)

		selfSignal(syscall.SIGHUP)
		time.Sleep(100 * time.Millisecond)

		fmt.Println("survived extended")

		time.Sleep(600 * time.Millisecond)

		// 1.4s in: past both.
		selfSignal(syscall.SIGHUP)
		time.Sleep(time.Second)

		fmt.Println("alive after grace")
	},

	// A watch that shuts down gives its signals back when it ends, with no grace.
	"shutdown": func() {
		_, end := begin(watch.Shutdown, syscall.SIGTERM)
		ended(end)

		selfSignal(syscall.SIGTERM)
		time.Sleep(time.Second)

		fmt.Println("alive")
	},

	// A watch started while another is in its grace is the one that gets the signal.
	"handover": func() {
		_, end := begin(watch.Restart, syscall.SIGHUP)
		ended(end)

		events, end := begin(watch.Restart, syscall.SIGHUP)

		selfSignal(syscall.SIGHUP)

		select {
		case ev := <-events:
			fmt.Println("event received", ev.Kind == watch.Restart)
		case <-time.After(5 * time.Second):
			fmt.Println("no event")
		}

		ended(end)

		time.Sleep(childGrace + 300*time.Millisecond)

		fmt.Println("done")
	},

	// However many watches come and go, what is kept is bounded, and it is gone
	// once the grace is over.
	"bound": func() {
		// Start the machinery of the signal package, so it is not counted.
		quiet := make(chan os.Signal, 1)

		signal.Notify(quiet, syscall.SIGWINCH)

		time.Sleep(50 * time.Millisecond)

		base := runtime.NumGoroutine()

		for range 1000 {
			_, end := begin(watch.Restart, syscall.SIGHUP)
			ended(end)
		}

		time.Sleep(50 * time.Millisecond)

		fmt.Println("during", runtime.NumGoroutine()-base)

		time.Sleep(childGrace + 400*time.Millisecond)

		fmt.Println("after", runtime.NumGoroutine()-base)

		// The signals have been given back: this one ends the process.
		selfSignal(syscall.SIGHUP)
		time.Sleep(time.Second)

		fmt.Println("alive after grace")
	},

	// A flood of the signal in the grace is taken, discarded and survived.
	"flood": func() {
		_, end := begin(watch.Restart, syscall.SIGHUP)
		ended(end)

		for range 10_000 {
			selfSignal(syscall.SIGHUP)
		}

		time.Sleep(100 * time.Millisecond)

		fmt.Println("survived flood")

		time.Sleep(childGrace + 300*time.Millisecond)

		selfSignal(syscall.SIGHUP)
		time.Sleep(time.Second)

		fmt.Println("alive after grace")
	},
}

// TestGraceHelperProcess is the child: it runs the scenario it is asked for.
func TestGraceHelperProcess(t *testing.T) {
	name := os.Getenv(scenarioEnv)
	if name == "" {
		t.Skip("only used by the tests that run a scenario in a process of its own")
	}

	run, ok := scenarios[name]
	if !ok {
		fmt.Println("error: no scenario", name)
		os.Exit(2)
	}

	interrupt.SetSignalsGrace(childGrace)

	run()

	os.Exit(0)
}

// outcome is what a scenario printed and how its process ended.
type outcome struct {
	lines    []string
	exitCode int
	signal   syscall.Signal // the signal that ended it, or 0
}

func (o outcome) printed(line string) bool {
	for _, l := range o.lines {
		if l == line {
			return true
		}
	}

	return false
}

func (o outcome) printedPrefix(prefix string) (string, bool) {
	for _, l := range o.lines {
		if strings.HasPrefix(l, prefix) {
			return l, true
		}
	}

	return "", false
}

// defaults makes the signals the scenarios use the default in processes started
// from here, whatever this one was started with.
func defaults(t *testing.T) {
	t.Helper()

	ch := make(chan os.Signal, 64)

	signal.Notify(ch, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)

	t.Cleanup(func() { signal.Stop(ch) })
}

func runScenario(t *testing.T, name string) outcome {
	t.Helper()

	defaults(t)

	cmd := exec.Command(os.Args[0], "-test.run=^TestGraceHelperProcess$")
	cmd.Env = append(os.Environ(), scenarioEnv+"="+name)

	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = cmd.Process.Kill() })

	var o outcome

	read := make(chan struct{})

	go func() {
		defer close(read)

		lines := bufio.NewScanner(out)

		for lines.Scan() {
			o.lines = append(o.lines, lines.Text())
		}
	}()

	waited := make(chan error, 1)

	go func() {
		<-read

		waited <- cmd.Wait()
	}()

	select {
	case err := <-waited:
		var exit *exec.ExitError

		switch {
		case err == nil:
		case errors.As(err, &exit):
			if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				o.signal = status.Signal()
			}

			o.exitCode = exit.ExitCode()
		default:
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("scenario %s did not finish; it printed %q", name, o.lines)
	}

	for _, l := range o.lines {
		if strings.HasPrefix(l, "error") || strings.HasPrefix(l, "slow close") || l == "no event" {
			t.Errorf("scenario %s: %q", name, l)
		}
	}

	return o
}

func TestARestartWatchKeepsItsSignalsForAGraceAfterItEnds(t *testing.T) {
	o := runScenario(t, "swallow")

	if !o.printed("survived in grace") {
		t.Errorf("the signal in the grace was not survived: %q, ended by %v", o.lines, o.signal)
	}

	if o.printed("alive after grace") || o.signal != syscall.SIGHUP {
		t.Errorf("the signal after the grace did not end the process: %q, ended by %v (exit %d)", o.lines, o.signal, o.exitCode)
	}
}

func TestAnotherWatchEndingExtendsTheGrace(t *testing.T) {
	o := runScenario(t, "extend")

	if !o.printed("survived extended") {
		t.Errorf("the signal after the first grace and inside the second was not survived: %q, ended by %v", o.lines, o.signal)
	}

	if o.printed("alive after grace") || o.signal != syscall.SIGHUP {
		t.Errorf("the signal after both did not end the process: %q, ended by %v", o.lines, o.signal)
	}
}

// A watch that shuts down is not given a grace: the signals are back with the
// system when it ends, so the next one ends the process.
func TestAShutdownWatchGivesItsSignalsBackAtOnceWhenItEnds(t *testing.T) {
	o := runScenario(t, "shutdown")

	if o.printed("alive") || o.signal != syscall.SIGTERM {
		t.Errorf("the signal did not end the process: %q, ended by %v (exit %d)", o.lines, o.signal, o.exitCode)
	}
}

func TestAWatchStartedDuringTheGraceStillGetsTheSignal(t *testing.T) {
	o := runScenario(t, "handover")

	if !o.printed("event received true") {
		t.Errorf("the new watch did not get the signal: %q", o.lines)
	}

	if !o.printed("done") || o.exitCode != 0 || o.signal != 0 {
		t.Errorf("the process did not end cleanly: %q, exit %d, signal %v", o.lines, o.exitCode, o.signal)
	}
}

// A program that restarts for ever starts and ends a watch for ever. What is kept
// is one handler for the signal, not one for each, and it goes when the grace does.
func TestTheGraceIsBoundedWhateverTheNumberOfWatches(t *testing.T) {
	o := runScenario(t, "bound")

	number := func(prefix string) int {
		t.Helper()

		line, ok := o.printedPrefix(prefix)
		if !ok {
			t.Fatalf("no %q line in %q", prefix, o.lines)
		}

		n, err := strconv.Atoi(strings.TrimPrefix(line, prefix+" "))
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}

		return n
	}

	if during := number("during"); during > 3 {
		t.Errorf("%d goroutines were left by 1000 watches that ended, want a handful at most", during)
	}

	if after := number("after"); after > 1 {
		t.Errorf("%d goroutines were left once the grace was over", after)
	}

	if o.printed("alive after grace") || o.signal != syscall.SIGHUP {
		t.Errorf("the signals were not given back after the grace: %q, ended by %v", o.lines, o.signal)
	}
}

func TestAFloodOfSignalsDuringTheGraceIsSurvived(t *testing.T) {
	o := runScenario(t, "flood")

	if !o.printed("survived flood") {
		t.Errorf("the flood was not survived: %q, ended by %v", o.lines, o.signal)
	}

	if o.printed("alive after grace") || o.signal != syscall.SIGHUP {
		t.Errorf("the signal after the grace did not end the process: %q, ended by %v", o.lines, o.signal)
	}
}
