package foundation_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"unicode"
	"unicode/utf8"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/foundationtest"
	"go.krak3n.dev/foundation/watch"
)

// f.Watch(trigger) starts a watcher as a parallel child of f: it asks the trigger
// to watch, returns once that has begun (so a change right after is never
// missed), and from then on turns what the trigger says into the program's own
// calls: a Restart event is f.Restart, a Shutdown event is f.Shutdown. The watcher
// is a function like any other, so it outlives the function that started it, is
// stopped in the usual reverse order (a watcher started first stops last, and the
// trigger's context is cancelled when its turn comes), and what goes wrong at
// start fails the program.
//
// What it says is advice, so it is never trusted: the reason is escaped and cut
// short, and is never the message of a log line, since it may come from a file
// name that anyone who can write the directory chose. It starts nothing in a dry
// run, help or candidate, from an instance that has been replaced, or once the
// program is shutting down; and there are at most 64 watchers in an instance.

// counting is a trigger that does nothing but count how many times it was started,
// which is how the tests below see that a watcher was, or was not, started.
type counting struct{ n atomic.Int32 }

func (c *counting) Watch(ctx context.Context) (<-chan watch.Event, error) {
	c.n.Add(1)

	ch := make(chan watch.Event)

	go func() {
		<-ctx.Done()
		close(ch)
	}()

	return ch, nil
}

func (c *counting) started() int { return int(c.n.Load()) }

// emits is a trigger that says evs as soon as it is first started, and then
// nothing, written as someone outside the framework would write one.
func emits(evs ...watch.Event) watch.Trigger {
	var once atomic.Bool

	return watch.TriggerFunc(func(ctx context.Context) (<-chan watch.Event, error) {
		ch := make(chan watch.Event, len(evs))

		if once.CompareAndSwap(false, true) {
			for _, ev := range evs {
				ch <- ev
			}
		}

		go func() {
			<-ctx.Done()
			close(ch)
		}()

		return ch, nil
	})
}

func TestARestartEventRestartsTheProgram(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		trigger, fire := watch.Manual()

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			f.Watch(trigger)

			if n > 1 {
				f.Shutdown("done")

				return
			}

			// Straight after Watch returns: the watch has begun, so this is seen.
			fire(watch.Restart, "config changed")

			until(f)
		}), quick(foundation.RestartPolicy{}))

		if got := p.instances(); got != 2 {
			t.Fatalf("%d instances ran, want 2", got)
		}

		assertExitCodes(t, exit)

		restarting := entriesNamed(recorder, "restarting")
		if len(restarting) != 1 || restarting[0].attrs["reason"] != "config changed" {
			t.Errorf("want one restarting entry with the trigger's reason, got %+v", restarting)
		}
	})
}

// A shutdown event is a shutdown, not a failure: the program ends, nothing
// restarts, and the exit status is 0. It says who asked, as an attribute.
func TestAShutdownEventShutsTheProgramDown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		trigger, fire := watch.Manual()

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			f.Watch(trigger)

			fire(watch.Shutdown, "asked to stop")

			until(f)
		}), quick(foundation.RestartPolicy{}))

		if got := p.instances(); got != 1 {
			t.Fatalf("%d instances ran, want 1", got)
		}

		assertExitCodes(t, exit)

		got := entriesNamed(recorder, "shutdown requested by a watch")
		if len(got) != 1 || got[0].attrs["reason"] != "asked to stop" {
			t.Errorf("want one entry with the reason as an attribute, got %+v", got)
		}

		// A watch that ends because the program is shutting down is not news.
		if n := len(entriesNamed(recorder, "watch ended")); n != 0 {
			t.Errorf("%d warnings for a watch that ended with the program", n)
		}
	})
}

// A trigger of someone else's can say anything. Whatever it says, the reason is
// escaped and bounded wherever it is logged, and is never the message of a line.
func TestTheReasonFromATriggerIsLoggedSafely(t *testing.T) {
	reasons := map[string]string{
		"newline forging a line": "changed\n{\"level\":\"ERROR\",\"msg\":\"forged\"}",
		"escape sequence":        "changed \x1b[2J\x1b]0;owned\x07",
		"NUL":                    "a\x00b",
		"carriage return":        "ok\rERROR forged",
		"invalid UTF-8":          "a\xff\xfeb",
		"a megabyte":             strings.Repeat("x", 1<<20),
		"empty":                  "",
	}

	safe := func(t *testing.T, got string) {
		t.Helper()

		if len(got) > 1024 {
			t.Errorf("the reason is %d bytes in the log", len(got))
		}

		if !utf8.ValidString(got) {
			t.Errorf("the logged reason is not valid UTF-8: %q", got)
		}

		for _, r := range got {
			if unicode.IsControl(r) {
				t.Errorf("the logged reason contains %q", r)
			}
		}
	}

	for name, reason := range reasons {
		t.Run("restart: "+name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				installExit(t)

				recorder := foundationtest.NewLogRecorder()
				recorder.Install(t)

				trigger := emits(watch.Event{Kind: watch.Restart, Reason: reason})

				var p program

				foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
					f.Watch(trigger)

					if n > 1 {
						f.Shutdown("done")

						return
					}

					until(f)
				}), quick(foundation.RestartPolicy{}))

				restarting := entriesNamed(recorder, "restarting")
				if len(restarting) != 1 {
					t.Fatalf("%d restarting entries, want 1: the reason must not matter", len(restarting))
				}

				safe(t, restarting[0].attrs["reason"])
			})
		})

		t.Run("shutdown: "+name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				installExit(t)

				recorder := foundationtest.NewLogRecorder()
				recorder.Install(t)

				trigger := emits(watch.Event{Kind: watch.Shutdown, Reason: reason})

				var p program

				foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
					f.Watch(trigger)

					until(f)
				}), quick(foundation.RestartPolicy{}))

				got := entriesNamed(recorder, "shutdown requested by a watch")
				if len(got) != 1 {
					t.Fatalf("%d shutdown entries, want 1: the message is fixed, whatever the reason", len(got))
				}

				safe(t, got[0].attrs["reason"])
			})
		})
	}
}

// An event that is not a kind is dropped, as everywhere in the framework, and what
// the trigger says after it still gets through.
func TestAnEventThatIsNotAKindIsDropped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		trigger := emits(
			watch.Event{Kind: 0, Reason: "no kind"},
			watch.Event{Kind: 99, Reason: "another"},
			watch.Event{Kind: -1, Reason: "negative"},
		)

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			f.Watch(trigger)

			time.Sleep(time.Minute)

			if err := f.Context().Err(); err != nil {
				t.Errorf("an event that is not a kind stopped the program: %v", err)
			}

			f.Shutdown("done")
		}), quick(foundation.RestartPolicy{}))

		if got := p.instances(); got != 1 {
			t.Errorf("%d instances ran, want 1", got)
		}

		assertExitCodes(t, exit)
	})
}

// A trigger that never stops saying restart is bounded by the restart policy as
// any other caller: 100,000 events at once are one restart, and nothing is left
// behind (the bubble ends only when every goroutine has).
func TestAFloodOfEventsIsOneRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		flood := watch.TriggerFunc(func(ctx context.Context) (<-chan watch.Event, error) {
			ch := make(chan watch.Event)

			go func() {
				defer close(ch)

				for range 100_000 {
					select {
					case ch <- watch.Event{Kind: watch.Restart, Reason: "flood"}:
					case <-ctx.Done():
						return
					}
				}
			}()

			return ch, nil
		})

		var (
			p        program
			flooding atomic.Bool
		)

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			// Only the first instance is flooded.
			if flooding.CompareAndSwap(false, true) {
				f.Watch(flood)

				until(f)

				return
			}

			f.Shutdown("done")
		}), quick(foundation.RestartPolicy{}))

		if got := p.instances(); got != 2 {
			t.Errorf("%d instances ran after 100,000 events, want 2", got)
		}

		assertExitCodes(t, exit)
	})
}

// Watch returns once the trigger has begun to watch, which is what lets the
// function that starts it start what it watches: nothing that happens after is
// missed.
func TestWatchReturnsOnceTheTriggerHasBegun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		var begun atomic.Bool

		trigger := watch.TriggerFunc(func(ctx context.Context) (<-chan watch.Event, error) {
			// Taking the baseline takes a while.
			time.Sleep(time.Second)

			begun.Store(true)

			ch := make(chan watch.Event)

			go func() {
				<-ctx.Done()
				close(ch)
			}()

			return ch, nil
		})

		var (
			p       program
			ready   bool
			elapsed time.Duration
		)

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			start := time.Now()

			f.Watch(trigger)

			ready, elapsed = begun.Load(), time.Since(start)

			f.Shutdown("done")
		}))

		if !ready || elapsed != time.Second {
			t.Errorf("Watch returned after %v with the trigger begun = %v, want 1s and true", elapsed, ready)
		}
	})
}

// The watcher is a function in its own right: it is not stopped because the
// function that started it has returned.
func TestAWatcherOutlivesTheFunctionThatStartedIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		trigger, fire := watch.Manual()

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			f.Watch(trigger)

			if n > 1 {
				f.Shutdown("done")

				return
			}

			go func() {
				time.Sleep(5 * time.Second)

				fire(watch.Restart, "later")
			}()

			// The function is over, and the program carries on: the watcher is
			// what it is waiting for.
		}), quick(foundation.RestartPolicy{}))

		if got := p.instances(); got != 2 {
			t.Fatalf("%d instances ran, want 2: the watcher should have outlived the function", got)
		}
	})
}

// Each instance watches for itself: what was started with one instance ends with
// it, and the next one starts its own.
func TestEachInstanceStartsItsOwnWatcher(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		trigger, fire := watch.Manual()

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			f.Watch(trigger)

			if n == 3 {
				f.Shutdown("done")

				return
			}

			go func() {
				time.Sleep(5 * time.Second)

				fire(watch.Restart, "again")
			}()

			until(f)
		}), quick(foundation.RestartPolicy{}))

		if got := p.instances(); got != 3 {
			t.Fatalf("%d instances ran, want 3", got)
		}

		assertExitCodes(t, exit)
	})
}

// The watcher is stopped in the usual order: it was started first, so it is
// stopped last, after what was started after it, and the trigger is told to stop
// at its turn, not before.
func TestAWatcherIsStoppedInTheUsualOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		var p program

		trigger := watch.TriggerFunc(func(ctx context.Context) (<-chan watch.Event, error) {
			ch := make(chan watch.Event)

			go func() {
				<-ctx.Done()

				p.ev.add("watch:stopped")
				close(ch)
			}()

			return ch, nil
		})

		foundation.Run("svc", func(f *foundation.F) {
			f.Watch(trigger)

			stoppable(f, &p.ev, "a", func() { time.Sleep(time.Second) })
			stoppable(f, &p.ev, "b", func() { time.Sleep(time.Second) })

			f.Shutdown("done")
		})

		assertEvents(t, p.ev.get(), []string{
			"b:stop", "b:cleanup",
			"a:stop", "a:cleanup",
			"watch:stopped",
		})
	})
}

// A trigger that stops sending when nobody asked it to is a watch that has gone
// away, which is worth a line, once. The program carries on.
func TestAWatchThatEndsOnItsOwnIsWarnedOfOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		ended := watch.TriggerFunc(func(ctx context.Context) (<-chan watch.Event, error) {
			ch := make(chan watch.Event)

			close(ch)

			return ch, nil
		})

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			f.Watch(ended)

			time.Sleep(time.Minute)

			if err := f.Context().Err(); err != nil {
				t.Errorf("the program stopped when a watch ended: %v", err)
			}

			f.Shutdown("done")
		}))

		got := entriesNamed(recorder, "watch ended")
		if len(got) != 1 || got[0].level != slog.LevelWarn {
			t.Fatalf("want one warning, got %+v", got)
		}

		if got[0].attrs["fn"] != "svc/watch" {
			t.Errorf("the warning is from %q, want svc/watch", got[0].attrs["fn"])
		}

		assertExitCodes(t, exit)
	})
}

// What goes wrong when a watch is asked for stops startup: a program that thinks
// it is watching and isn't is worse than one that doesn't start.
func TestAWatchThatCannotStartFailsTheProgram(t *testing.T) {
	var nilTrigger watch.Trigger

	var typedNil *counting

	tests := map[string]watch.Trigger{
		"an error": watch.TriggerFunc(func(context.Context) (<-chan watch.Event, error) {
			return nil, errors.New("boom")
		}),
		"an error with control characters": watch.TriggerFunc(func(context.Context) (<-chan watch.Event, error) {
			return nil, errors.New("bad\nlevel=ERROR msg=forged \x1b[2J")
		}),
		"no channel": watch.TriggerFunc(func(context.Context) (<-chan watch.Event, error) {
			return nil, nil
		}),
		"a nil trigger":          nilTrigger,
		"a nil pointer":          typedNil,
		"a nil trigger function": watch.TriggerFunc(nil),
	}

	for name, trigger := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				exit := installExit(t)

				recorder := foundationtest.NewLogRecorder()
				recorder.Install(t)

				var (
					p     program
					after atomic.Bool
				)

				foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
					f.Watch(trigger)

					f.Run("after", func(*foundation.F) { after.Store(true) })
				}))

				assertExitCodes(t, exit, 1)

				if after.Load() {
					t.Error("the program went on to start things after a watch that could not start")
				}

				for _, l := range loggedEntries(recorder) {
					for k, v := range l.attrs {
						for _, r := range v {
							if unicode.IsControl(r) {
								t.Errorf("%q: attribute %s contains %q", l.msg, k, r)
							}
						}
					}
				}
			})
		})
	}
}

// A nil trigger is a bug in the program, so a dry run, which is how it is found
// before a deploy, fails on it too, though it starts nothing.
func TestANilTriggerFailsEvenInADryRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		var nilTrigger watch.Trigger

		p := program{countDryRuns: true}

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			f.Watch(nilTrigger)
		}), foundation.WithDryRun(true))

		assertExitCodes(t, exit, 1)
	})
}

// Where a watcher is not started, and a trigger is not asked to watch: a dry run
// starts nothing (so a trigger that could not start is not found by one, which is
// accepted), nor does help, nor a candidate, nor an instance that has been
// replaced, nor a program that is already shutting down.
func TestAWatcherIsNotStartedWhereNothingStarts(t *testing.T) {
	t.Run("a dry run", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			exit := installExit(t)

			var c counting

			p := program{countDryRuns: true}

			foundation.Run("svc", p.fn(func(f *foundation.F, n int) { f.Watch(&c) }), foundation.WithDryRun(true))

			if c.started() != 0 {
				t.Error("a trigger was started in a dry run")
			}

			assertExitCodes(t, exit)
		})
	})

	t.Run("--dry-run", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			exit := installExit(t)

			var c counting

			p := program{countDryRuns: true}

			foundation.Run("svc", p.fn(func(f *foundation.F, n int) { f.Watch(&c) }),
				foundation.WithArgs([]string{"--dry-run"}))

			if c.started() != 0 {
				t.Error("a trigger was started with --dry-run")
			}

			assertExitCodes(t, exit)
		})
	})

	t.Run("help", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			exit := installExit(t)

			var c counting

			p := program{countDryRuns: true}

			foundation.Run("svc", p.fn(func(f *foundation.F, n int) { f.Watch(&c) }),
				foundation.WithArgs([]string{"--help"}), foundation.WithHelpOutput(io.Discard))

			if c.started() != 0 {
				t.Error("a trigger was started for help")
			}

			assertExitCodes(t, exit)
		})
	})

	t.Run("a candidate", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			installExit(t)

			var c counting

			var p program

			p.onCandidate = func(f *foundation.F, k int) { f.Watch(&c) }

			foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
				f.Watch(&c)

				if n > 1 {
					f.Shutdown("done")

					return
				}

				f.Restart("edit")

				until(f)
			}), quick(foundation.RestartPolicy{}))

			if p.candidates.Load() != 1 {
				t.Fatalf("%d candidates ran, want 1", p.candidates.Load())
			}

			// The two instances watched, and the candidate between them did not.
			if got := c.started(); got != 2 {
				t.Errorf("a trigger was started %d times, want 2 (one for each instance)", got)
			}
		})
	})

	t.Run("an instance that has been replaced", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			installExit(t)

			var (
				c     counting
				p     program
				first *foundation.F
			)

			foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
				switch n {
				case 1:
					first = f

					f.Restart("now")

					until(f)
				case 2:
					first.Watch(&c)

					f.Shutdown("done")
				}
			}), quick(foundation.RestartPolicy{}))

			if c.started() != 0 {
				t.Error("a trigger was started from an instance that had been replaced")
			}
		})
	})

	t.Run("a program that is shutting down", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			exit := installExit(t)

			var (
				c counting
				p program
			)

			foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
				f.Shutdown("bye")

				f.Watch(&c)
			}))

			if c.started() != 0 {
				t.Error("a trigger was started after a shutdown")
			}

			assertExitCodes(t, exit)
		})
	})
}

// A watcher is a goroutine and whatever the trigger holds, so there is a limit,
// and it is on the instance: the next starts with none.
func TestAnInstanceHasAtMostSixtyFourWatchers(t *testing.T) {
	t.Run("sixty four", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			exit := installExit(t)

			trigger, _ := watch.Manual()

			var p program

			foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
				for range 64 {
					f.Watch(trigger)
				}

				if n == 1 {
					f.Restart("again")

					until(f)

					return
				}

				// The new instance started its own 64.
				f.Shutdown("done")
			}), quick(foundation.RestartPolicy{}))

			if got := p.instances(); got != 2 {
				t.Errorf("%d instances ran, want 2", got)
			}

			assertExitCodes(t, exit)
		})
	})

	t.Run("sixty five", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			exit := installExit(t)

			trigger, _ := watch.Manual()

			var p program

			foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
				for range 65 {
					f.Watch(trigger)
				}

				f.Shutdown("not reached")
			}))

			assertExitCodes(t, exit, 1)
		})
	})
}

// Events and a shutdown from outside, from many goroutines, in any order: Run
// returns, no more than one instance is ever running, and nothing is left behind.
// Run it with -race.
func TestEventsRacingAShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		for round := range 20 {
			trigger, fire := watch.Manual()

			var (
				p  program
				wg sync.WaitGroup
			)

			foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
				f.Watch(trigger)

				if n == 1 {
					for range 10 {
						wg.Go(func() {
							for range 50 {
								fire(watch.Restart, "busy")
								time.Sleep(time.Duration(round) * time.Millisecond)
							}
						})
					}

					wg.Go(func() {
						time.Sleep(time.Duration(round*40) * time.Millisecond)

						f.Shutdown("bye")
					})
				}

				until(f)
			}), quick(foundation.RestartPolicy{}))

			wg.Wait()

			if got := p.overlap(); got != 1 {
				t.Fatalf("round %d: %d instances were running at once", round, got)
			}
		}

		assertExitCodes(t, exit)
	})
}

// The case this is for: a program that restarts when its own configuration file
// changes. A good edit is applied, a bad one is rejected by the candidate and the
// service carries on with what it had, and the fix that follows is applied. The
// file is watched by the real trigger, polling on the fake clock, and read by the
// real provider.
func TestAProgramThatRestartsWhenItsConfigurationFileChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		path := filepath.Join(t.TempDir(), "svc.json")

		write := func(data string) {
			t.Helper()

			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		write(`{"svc": {"addr": ":1"}}`)

		var (
			p    program
			mu   sync.Mutex
			seen []string
			wg   sync.WaitGroup
		)

		app := func(f *foundation.F) {
			var cfg appConfig

			f.Load(&cfg)

			f.Watch(watch.Files([]string{path}))

			if f.DryRun() {
				f.Skip("dry run")
			}

			mu.Lock()
			seen = append(seen, cfg.Addr)
			mu.Unlock()
		}

		p.onCandidate = func(f *foundation.F, k int) { app(f) }

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			app(f)

			if n > 1 {
				f.Shutdown("done")

				return
			}

			wg.Go(func() {
				// Off the one second beat of the poll, so that a write is never
				// seen half made (truncated, not yet written), as it could be by a
				// real poll that happened to coincide: that would be two edits.
				time.Sleep(5*time.Second + 500*time.Millisecond)
				write(`{"svc": {"addr": ":2", "port": "not a number"}}`)

				// Long enough for the bad edit to have been found, tried and rejected.
				time.Sleep(25 * time.Second)

				if err := f.Context().Err(); err != nil {
					t.Errorf("a bad edit stopped the service: %v", err)
				}

				write(`{"svc": {"addr": ":2"}}`)
			})

			until(f)
		}),
			foundation.WithDocumentConfig(json.File(path)),
			foundation.WithStrictConfig(),
			quick(foundation.RestartPolicy{}),
		)

		wg.Wait()

		mu.Lock()
		defer mu.Unlock()

		if len(seen) != 2 || seen[0] != ":1" || seen[1] != ":2" {
			t.Fatalf("the instances read %v, want [:1 :2]", seen)
		}

		if got := len(entriesNamed(recorder, "restart rejected")); got != 1 {
			t.Errorf("%d rejections, want 1 (the bad edit)", got)
		}

		if got := len(entriesNamed(recorder, "restarting")); got != 1 {
			t.Errorf("%d restarts, want 1 (the fix)", got)
		}

		assertExitCodes(t, exit)
	})
}
