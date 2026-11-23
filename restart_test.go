package foundation_test

import (
	"context"
	"errors"
	"io"
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
	"go.krak3n.dev/foundation/health/monitor"
)

// f.Restart(reason) restarts the whole program, as f.Shutdown ends the whole
// program: from any function or goroutine, without halting the caller. Run stops
// the instance in the usual order (every function, then every cleanup) and only
// then runs the root function again, as a new instance with a tree, a context and
// a probe registry of its own. What belongs to the process (the options, the
// configuration providers, whether the run has failed) is kept.
//
// A real shutdown always wins: once a Shutdown or a failure has been asked for,
// Restart does nothing, and one that arrives while a restart is under way ends
// the program instead of starting another instance. Two instances never overlap,
// so if the old one can't be stopped (a function was abandoned) the run fails
// rather than start the new one beside it.

// program counts the instances of a root function that run, and how many ran at
// once, which must never be more than one.
type program struct {
	mu     sync.Mutex
	n      int
	active int
	most   int
	ev     events

	// A restart first runs the root function as a candidate, in a dry run, to see
	// whether the new instance would start. A candidate is not an instance: it is
	// counted in candidates, runs onCandidate (k is its number, from 1) if there is
	// one, and otherwise passes. countDryRuns is for the tests of a program that is
	// itself a dry run, where the root function is the instance.
	candidates   atomic.Int32
	onCandidate  func(f *foundation.F, k int)
	countDryRuns bool
}

// fn is the root function: it records the instance and calls body with its number,
// starting at 1. The instance is over when its cleanups have run, which is after
// every function it started has stopped.
func (p *program) fn(body func(f *foundation.F, n int)) foundation.Func {
	return func(f *foundation.F) {
		if f.DryRun() && !p.countDryRuns {
			k := int(p.candidates.Add(1))

			if p.onCandidate != nil {
				p.onCandidate(f, k)
			}

			return
		}

		p.mu.Lock()
		p.n++
		n := p.n
		p.active++
		p.most = max(p.most, p.active)
		p.mu.Unlock()

		p.ev.add(label(n, "start"))

		f.Cleanup(func() {
			p.ev.add(label(n, "end"))

			p.mu.Lock()
			p.active--
			p.mu.Unlock()
		})

		body(f, n)
	}
}

func (p *program) instances() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.n
}

func (p *program) overlap() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.most
}

func label(n int, what string) string {
	return string(rune('0'+n%10)) + ":" + what
}

// until returns when the instance has been asked to stop.
func until(f *foundation.F) { <-f.Context().Done() }

func TestRestartRunsTheRootFunctionAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			if n == 1 {
				f.Restart("config changed")

				return
			}

			f.Shutdown("done")
		}))

		if got := p.instances(); got != 2 {
			t.Fatalf("%d instances ran, want 2", got)
		}

		assertExitCodes(t, exit)
	})
}

// A program that never asks for a restart is what it was: one instance.
func TestWithoutARestartTheRootFunctionRunsOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {}))

		if got := p.instances(); got != 1 {
			t.Fatalf("%d instances ran, want 1", got)
		}

		assertExitCodes(t, exit)
	})
}

// Each instance starts as the first did: a live context, the same name and path
// (so the same configuration keys), and a probe registry of its own, so that a
// probe registered again is not a duplicate.
func TestEachInstanceStartsFresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		var p program

		const last = 3

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			if err := f.Context().Err(); err != nil {
				t.Errorf("instance %d started with a context that is done: %v", n, err)
			}

			if got := f.String(); got != "svc" {
				t.Errorf("instance %d is %q, want svc", n, got)
			}

			if f.DryRun() {
				t.Errorf("instance %d is a dry run", n)
			}

			f.Probes().Add(monitor.NewProbe("db", monitor.LivenessMode, alwaysHealthy{}))

			if n < last {
				f.Restart("again")

				return
			}

			f.Shutdown("done")
		}))

		if got := p.instances(); got != last {
			t.Fatalf("%d instances ran, want %d", got, last)
		}

		assertExitCodes(t, exit)
	})
}

type alwaysHealthy struct{}

func (alwaysHealthy) Status(context.Context) (monitor.Status, error) {
	return monitor.StatusHealthy, nil
}

// The old instance is stopped completely, in the usual order, before the new one
// starts: its functions in the reverse of the order they started, then its
// cleanups. At no time are two running.
func TestAnInstanceIsStoppedCompletelyBeforeTheNextStarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			f.Cleanup(func() { p.ev.add("1:cleanup") })

			stoppable(f, &p.ev, "a", func() { time.Sleep(time.Second) })
			stoppable(f, &p.ev, "b", func() { time.Sleep(time.Second) })

			f.Restart("now")
		}))

		assertEvents(t, p.ev.get(), []string{
			"1:start",
			"b:stop", "b:cleanup",
			"a:stop", "a:cleanup",
			"1:cleanup",
			"1:end",
			"2:start",
			"2:end",
		})

		if got := p.overlap(); got != 1 {
			t.Fatalf("%d instances were running at once", got)
		}
	})
}

// Restart asks, like Shutdown: the caller carries on, whichever function or
// goroutine it is.
func TestRestartDoesNotHaltTheCaller(t *testing.T) {
	where := map[string]func(f *foundation.F, after func()){
		"the root function": func(f *foundation.F, after func()) {
			f.Restart("x")
			after()
		},
		"a child": func(f *foundation.F, after func()) {
			f.Run("child", func(f *foundation.F) {
				f.Restart("x")
				after()
			})
		},
		"a goroutine": func(f *foundation.F, after func()) {
			done := make(chan struct{})

			go func() {
				defer close(done)

				f.Restart("x")
				after()
			}()

			<-done
		},
		"a cleanup of a child": func(f *foundation.F, after func()) {
			f.Run("child", func(f *foundation.F) {
				f.Cleanup(func() {
					f.Restart("x")
					after()
				})
			})
		},
	}

	for name, call := range where {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				installExit(t)

				var (
					p     program
					after atomic.Int32
				)

				foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
					if n > 1 {
						f.Shutdown("done")

						return
					}

					call(f, func() { after.Add(1) })
				}))

				if after.Load() != 1 {
					t.Error("the code after Restart did not run")
				}

				if got := p.instances(); got != 2 {
					t.Errorf("%d instances ran, want 2", got)
				}
			})
		})
	}
}

// Restart only asks: it returns while the old instance is still being stopped,
// so a function that is slow to stop can't hold up whoever asked.
func TestRestartDoesNotWaitForTheTeardown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		var (
			p       program
			elapsed time.Duration
		)

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			stoppable(f, &p.ev, "slow", func() { time.Sleep(10 * time.Second) })

			start := time.Now()

			f.Restart("now")

			elapsed = time.Since(start)
		}))

		if elapsed != 0 {
			t.Fatalf("Restart took %v", elapsed)
		}
	})
}

// A request while one is already being carried out is the same request: a flood
// from a watcher that fires over and over is one restart.
func TestManyRequestsDuringOneRestartAreOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			stoppable(f, &p.ev, "slow", func() { time.Sleep(time.Second) })

			var wg sync.WaitGroup

			for range 100 {
				wg.Go(func() {
					for range 100 {
						f.Restart("flood")
					}
				})
			}

			wg.Wait()
		}))

		if got := p.instances(); got != 2 {
			t.Fatalf("%d instances ran after 10,000 requests, want 2", got)
		}
	})
}

// A function of an old instance that still holds its f can't restart the new one:
// what it asks about is gone.
func TestARestartFromAnOldInstanceIsIgnored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		var (
			p     program
			first *foundation.F
		)

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			switch n {
			case 1:
				first = f

				f.Restart("now")
			case 2:
				first.Restart("stale")

				time.Sleep(time.Minute)

				if err := f.Context().Err(); err != nil {
					t.Errorf("a stale Restart stopped the running instance: %v", err)
				}

				f.Shutdown("done")
			}
		}))

		if got := p.instances(); got != 2 {
			t.Fatalf("%d instances ran, want 2", got)
		}
	})
}

// An old instance can't add to the new tree either: its root has stopped
// accepting functions, and the new one is not its to extend.
func TestAnOldInstanceCannotStartFunctions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		var (
			p     program
			first *foundation.F
			ran   atomic.Bool
		)

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			switch n {
			case 1:
				first = f

				f.Restart("now")
			case 2:
				first.Run("late", func(*foundation.F) { ran.Store(true) })

				f.Shutdown("done")
			}
		}))

		if ran.Load() {
			t.Error("a function was started on an instance that had stopped")
		}
	})
}

// A real shutdown is never overtaken by a restart: a Shutdown that arrives while
// the old instance is being stopped means no new one starts.
func TestAShutdownDuringARestartWins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			stoppable(f, &p.ev, "slow", func() { time.Sleep(5 * time.Second) })

			go func() {
				// The restart begins after the minimum interval, 1s, and takes 5s to stop.
				time.Sleep(3 * time.Second)

				f.Shutdown("sigterm")
			}()

			f.Restart("now")
		}))

		if got := p.instances(); got != 1 {
			t.Fatalf("%d instances ran, want 1: the shutdown should have won", got)
		}

		assertExitCodes(t, exit)
	})
}

func TestAFailureDuringARestartWins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			stoppable(f, &p.ev, "slow", func() { time.Sleep(5 * time.Second) })

			go func() {
				// The restart begins after the minimum interval, 1s, and takes 5s to stop.
				time.Sleep(3 * time.Second)

				f.Fail(errors.New("boom"))
			}()

			f.Restart("now")
		}))

		if got := p.instances(); got != 1 {
			t.Fatalf("%d instances ran, want 1: the failure should have won", got)
		}

		assertExitCodes(t, exit, 1)
	})
}

// Shutdown and Fail are the process's, whichever instance's f they are called on:
// the old instance's goroutine that was still holding it can still end the
// program, and a failure it reports still counts.
func TestAShutdownOrFailureFromAnOldInstanceStillEndsTheProgram(t *testing.T) {
	tests := map[string]struct {
		end  func(f *foundation.F)
		want []int
	}{
		"shutdown": {func(f *foundation.F) { f.Shutdown("late") }, nil},
		"fail":     {func(f *foundation.F) { f.Fail(errors.New("late")) }, []int{1}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				exit := installExit(t)

				var (
					p     program
					first *foundation.F
				)

				foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
					switch n {
					case 1:
						first = f

						f.Restart("now")
					case 2:
						go tt.end(first)

						until(f)
					}
				}))

				if got := p.instances(); got != 2 {
					t.Fatalf("%d instances ran, want 2", got)
				}

				assertExitCodes(t, exit, tt.want...)
			})
		})
	}
}

// Once the program is shutting down or has failed there is nothing to restart. The
// request is made from a cleanup, which still runs after Fail has ended the
// function.
func TestARestartAfterAShutdownOrFailureIsIgnored(t *testing.T) {
	tests := map[string]struct {
		end  func(f *foundation.F)
		want []int
	}{
		"shutdown": {func(f *foundation.F) { f.Shutdown("bye") }, nil},
		"fail":     {func(f *foundation.F) { f.Fail(errors.New("boom")) }, []int{1}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				exit := installExit(t)

				var p program

				foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
					f.Cleanup(func() { f.Restart("too late") })

					tt.end(f)
				}))

				if got := p.instances(); got != 1 {
					t.Fatalf("%d instances ran, want 1", got)
				}

				assertExitCodes(t, exit, tt.want...)
			})
		})
	}
}

// A new instance that fails ends the run with a failure, as it would have the
// first time: the process is what is failed, not the instance.
func TestAFailureInTheNewInstanceFailsTheRun(t *testing.T) {
	tests := map[string]func(f *foundation.F){
		"fail":  func(f *foundation.F) { f.Fail(errors.New("boom")) },
		"panic": func(f *foundation.F) { panic("boom") },
	}

	for name, fail := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				exit := installExit(t)

				var p program

				foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
					if n == 1 {
						f.Restart("now")

						return
					}

					fail(f)
				}))

				if got := p.instances(); got != 2 {
					t.Fatalf("%d instances ran, want 2: a failed start is not retried", got)
				}

				assertExitCodes(t, exit, 1)
			})
		})
	}
}

// A function that doesn't stop in its time is abandoned, and the old instance may
// still be running it. Starting the new instance beside it would be two copies of
// the program, so the run fails instead.
func TestAnAbandonedFunctionDuringARestartFailsTheRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		hang := make(chan struct{})
		defer close(hang)

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			f.Run("stuck", func(f *foundation.F) {
				f.SetTimeout(time.Second)
				f.Parallel()

				<-hang
			})

			f.Restart("now")
		}))

		if got := p.instances(); got != 1 {
			t.Fatalf("%d instances ran, want 1: the stuck function may still be running", got)
		}

		assertExitCodes(t, exit, 1)

		if len(entriesNamed(recorder, "function did not exit within shutdown timeout")) != 1 {
			t.Error("the abandoned function should have been named")
		}
	})
}

// An abandoned function is still not a failure when the program is only shutting
// down: the existing rule stands, and only a restart, which would run a second
// copy, makes it one.
func TestAnAbandonedFunctionDuringAShutdownIsNotAFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		hang := make(chan struct{})
		defer close(hang)

		foundation.Run("svc", func(f *foundation.F) {
			f.Run("stuck", func(f *foundation.F) {
				f.SetTimeout(time.Second)
				f.Parallel()

				<-hang
			})

			f.Shutdown("bye")
		})

		assertExitCodes(t, exit)
	})
}

// A dry run starts nothing, so there is nothing to restart: the request is
// ignored, whether the dry run was chosen in code or on the command line.
func TestRestartInADryRunOrHelpDoesNothing(t *testing.T) {
	tests := map[string][]foundation.Option{
		"WithDryRun": {foundation.WithDryRun(true)},
		"--dry-run":  {foundation.WithArgs([]string{"--dry-run"})},
		"--help":     {foundation.WithArgs([]string{"--help"}), foundation.WithHelpOutput(io.Discard)},
	}

	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				exit := installExit(t)

				p := program{countDryRuns: true}

				foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
					f.Restart("now")
				}), opts...)

				if got := p.instances(); got != 1 {
					t.Fatalf("%d instances ran, want 1", got)
				}

				assertExitCodes(t, exit)
			})
		})
	}
}

// Configuration is read again by every instance: that is what makes restarting on
// a change to a file useful. The providers are the process's, so the options keep
// working, strict mode included.
func TestARestartReadsConfigurationAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		path := filepath.Join(t.TempDir(), "svc.json")

		write := func(addr string) {
			t.Helper()

			if err := os.WriteFile(path, []byte(`{"svc": {"addr": "`+addr+`"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		write(":1111")

		var (
			p    program
			seen []string
		)

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			var cfg struct {
				Addr string `config:"addr"`
			}

			f.Load(&cfg)

			seen = append(seen, cfg.Addr)

			if n == 1 {
				write(":2222")

				f.Restart("config changed")

				return
			}

			f.Shutdown("done")
		}),
			foundation.WithDocumentConfig(json.File(path)),
			foundation.WithStrictConfig(),
		)

		if len(seen) != 2 || seen[0] != ":1111" || seen[1] != ":2222" {
			t.Fatalf("the instances read %v, want [:1111 :2222]", seen)
		}

		assertExitCodes(t, exit)
	})
}

// What makes a program restart is the program: nothing in the environment or in
// configuration can ask for it.
func TestTheEnvironmentCannotRestartTheProgram(t *testing.T) {
	for _, name := range []string{"RESTART", "SVC_RESTART", "FOUNDATION_RESTART", "SVC_WATCH", "SVC_RELOAD"} {
		t.Setenv(name, "1")
	}

	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			var cfg struct {
				Addr string `config:"addr"`
			}

			f.Load(&cfg)
		}))

		if got := p.instances(); got != 1 {
			t.Fatalf("%d instances ran, want 1", got)
		}

		assertExitCodes(t, exit)
	})
}

// The reason is for a log and comes from whatever noticed the change, which can be
// a file name that anyone who can write the directory chose. It is escaped, and
// bounded, so it can't forge a line, repaint a terminal or fill a log.
func TestTheReasonForARestartIsLoggedSafely(t *testing.T) {
	reasons := map[string]string{
		"newline forging a line": "changed\n{\"level\":\"ERROR\",\"msg\":\"forged\"}",
		"escape sequence":        "changed \x1b[2J\x1b]0;owned\x07",
		"NUL":                    "a\x00b",
		"carriage return":        "ok\rERROR forged",
		"invalid UTF-8":          "a\xff\xfeb",
		"a megabyte":             strings.Repeat("x", 1<<20),
		"empty":                  "",
	}

	for name, reason := range reasons {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				installExit(t)

				recorder := foundationtest.NewLogRecorder()
				recorder.Install(t)

				var p program

				foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
					if n == 1 {
						f.Restart(reason)

						return
					}

					f.Shutdown("done")
				}))

				if got := p.instances(); got != 2 {
					t.Fatalf("%d instances ran, want 2: the reason must not matter", got)
				}

				restarting := entriesNamed(recorder, "restarting")
				if len(restarting) != 1 {
					t.Fatalf("want one %q entry, got %d", "restarting", len(restarting))
				}

				got := restarting[0].attrs["reason"]

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
			})
		})
	}
}

// Nothing is left behind by an instance: no goroutine, no timer. The bubble ends
// only when every goroutine has, so many restarts are the leak check.
func TestManyRestartsLeaveNothingBehind(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		var p program

		const last = 200

		foundation.Run("svc", func(f *foundation.F) {
			if f.DryRun() {
				return
			}

			f.Run("worker", func(f *foundation.F) {
				f.Parallel()

				until(f)
			})

			p.mu.Lock()
			p.n++
			n := p.n
			p.mu.Unlock()

			if n < last {
				f.Restart("again")

				return
			}

			f.Shutdown("done")
		}, foundation.WithRestartPolicy(foundation.RestartPolicy{
			Debounce:    time.Millisecond,
			MaxDebounce: time.Millisecond,
			MinInterval: time.Millisecond,
			Limit:       1000,
		}))

		if got := p.instances(); got != last {
			t.Fatalf("%d instances ran, want %d", got, last)
		}

		assertExitCodes(t, exit)
	})
}

// A restart and a shutdown asked for at the same moment, from anywhere, in any
// order: Run always returns, no more than one instance is ever running, and the
// shutdown is never lost. Run it with -race.
func TestARestartRacingAShutdownEndsTheProgram(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		for range 100 {
			var (
				p  program
				wg sync.WaitGroup
			)

			foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
				if n == 1 {
					wg.Go(func() { f.Restart("race") })
					wg.Go(func() { f.Shutdown("race") })
				}

				until(f)
			}))

			wg.Wait()

			if got := p.instances(); got < 1 || got > 2 {
				t.Fatalf("%d instances ran, want 1 or 2", got)
			}

			if got := p.overlap(); got != 1 {
				t.Fatalf("%d instances were running at once", got)
			}
		}

		assertExitCodes(t, exit)
	})
}

// A restart that is rejected says why, on the line that says it was: the first
// thing that went wrong in the candidate (its failure, its panic, that it shut the
// program down, that it left a function running), as the `cause`. It is text the
// candidate has already logged on its own line, so it says nothing new, and it is
// escaped and bounded like the reason. What is left to the candidate's lines is
// the detail (which field, which key), which the `hint` points to.

// rejection runs a program whose candidate does bad and returns the one line that
// says the restart was rejected.
func rejection(t *testing.T, bad func(f *foundation.F)) logged {
	t.Helper()

	installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var p program

	p.onCandidate = func(f *foundation.F, k int) { bad(f) }

	foundation.Run("svc", live(t, &p), quick(foundation.RestartPolicy{}))

	rejected := entriesNamed(recorder, "restart rejected")
	if len(rejected) != 1 {
		t.Fatalf("want one rejection, got %+v", rejected)
	}

	return rejected[0]
}

func TestARejectedRestartSaysWhatWentWrong(t *testing.T) {
	hang := make(chan struct{})

	tests := map[string]struct {
		bad  func(f *foundation.F)
		want string
	}{
		"a failure":          {func(f *foundation.F) { f.Fail(errors.New("boom")) }, "boom"},
		"a panic":            {func(f *foundation.F) { panic("kaboom") }, "kaboom"},
		"a failure in child": {func(f *foundation.F) { f.Run("child", func(f *foundation.F) { f.Fail(errors.New("boom")) }) }, "boom"},
		"a panic in a child": {func(f *foundation.F) { f.Run("child", func(f *foundation.F) { panic("kaboom") }) }, "kaboom"},
		"a shutdown":         {func(f *foundation.F) { f.Shutdown("no") }, "shut down"},
		"a function that can't be stopped": {func(f *foundation.F) {
			f.Run("stuck", func(f *foundation.F) {
				f.SetTimeout(time.Second)
				f.Parallel()

				<-hang
			})
		}, "did not stop"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hang = make(chan struct{})
				defer close(hang)

				got := rejection(t, tt.bad)

				cause := got.attrs["cause"]
				if !strings.Contains(cause, tt.want) {
					t.Errorf("the cause is %q, want it to say %q", cause, tt.want)
				}

				// The old wording sent the reader to find out for themselves.
				if why := got.attrs["why"]; why == "" || !strings.Contains(why, "left as it was") {
					t.Errorf("why = %q, want it to say the running program was left as it was", why)
				}

				if hint := got.attrs["hint"]; !strings.Contains(hint, "candidate=true") {
					t.Errorf("hint = %q, want it to say where the detail is", hint)
				}

				if got.attrs["reason"] != "edit" {
					t.Errorf("the reason is %q", got.attrs["reason"])
				}
			})
		})
	}
}

// The cause is the first thing that went wrong, not what a later failure in the
// same candidate, such as one while it is being stopped, says.
func TestTheCauseIsTheFirstFailureOfTheCandidate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		got := rejection(t, func(f *foundation.F) {
			f.Run("late", func(f *foundation.F) {
				f.Parallel()

				<-f.Context().Done()

				f.Fail(errors.New("second"))
			})

			f.Fail(errors.New("first"))
		})

		cause := got.attrs["cause"]
		if !strings.Contains(cause, "first") || strings.Contains(cause, "second") {
			t.Errorf("the cause is %q, want the first failure", cause)
		}
	})
}

// A cause is on every rejection: the reader is never left to find it.
func TestARejectionAlwaysHasACause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		got := rejection(t, func(f *foundation.F) { f.Fail(errors.New("")) })

		if got.attrs["cause"] == "" {
			t.Errorf("a failure with no message has no cause: %+v", got.attrs)
		}
	})
}

// What a candidate failed with may come from anywhere (a file, a name that anyone
// who can write the directory chose), so on this line it is made safe like the
// reason: no control character, nothing that forges another line, and cut short.
func TestTheCauseOfARejectedRestartIsLoggedSafely(t *testing.T) {
	causes := map[string]string{
		"newline forging a line": "bad\n{\"level\":\"ERROR\",\"msg\":\"forged\"}",
		"escape sequence":        "bad \x1b[2J\x1b]0;owned\x07",
		"invalid UTF-8":          "bad \xff\xfe",
		"a megabyte":             strings.Repeat("x", 1<<20),
	}

	for name, text := range causes {
		for how, bad := range map[string]func(f *foundation.F){
			"a failure": func(f *foundation.F) { f.Fail(errors.New(text)) },
			"a panic":   func(f *foundation.F) { panic(text) },
		} {
			t.Run(name+" in "+how, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					installExit(t)

					recorder := foundationtest.NewLogRecorder()
					recorder.Install(t)

					var p program

					p.onCandidate = func(f *foundation.F, k int) { bad(f) }

					foundation.Run("svc", live(t, &p), quick(foundation.RestartPolicy{}))

					rejected := entriesNamed(recorder, "restart rejected")
					if len(rejected) != 1 {
						t.Fatalf("%d rejections, want 1", len(rejected))
					}

					cause := rejected[0].attrs["cause"]

					if len(cause) > 1024 {
						t.Errorf("the cause is %d bytes in the log", len(cause))
					}

					for _, r := range cause {
						if unicode.IsControl(r) {
							t.Errorf("the logged cause contains %q", r)
						}
					}

					if got := entriesNamed(recorder, "forged"); len(got) != 0 {
						t.Errorf("a line was forged: %+v", got)
					}
				})
			})
		}
	}
}

// A candidate that fails because of a configuration file says so in a few words,
// and neither the line nor the cause has the path in it: the details (provider,
// field, key) are on the candidate's own lines.
func TestTheCauseOfABadEditIsThatTheConfigurationIsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), canaryPath+".json")

	t.Setenv("SVC_CONFIG", path)

	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		edit(t, path, addrDoc(":1"), 1)
		editAfter(t, 5*time.Second, path, `{"svc": `, 2)

		var c cfgProgram

		foundation.Run("svc", c.fn(func(f *foundation.F, n int, _ cfgDoc) {
			time.Sleep(20 * time.Second)

			f.Shutdown("done")
		}), quick(foundation.RestartPolicy{}), foundation.WithDocumentConfig(json.FileFromEnv("SVC_CONFIG", asked)))

		rejected := entriesNamed(recorder, "restart rejected")
		if len(rejected) != 1 {
			t.Fatalf("want one rejection, got %+v", rejected)
		}

		if cause := rejected[0].attrs["cause"]; !strings.Contains(cause, "invalid configuration") {
			t.Errorf("the cause is %q", cause)
		}

		for k, v := range rejected[0].attrs {
			if strings.Contains(v, canaryPath) {
				t.Errorf("the rejection has the path in %s=%q", k, v)
			}
		}

		// The details are where the hint says.
		found := false

		for _, l := range loggedEntries(recorder) {
			if l.attrs["candidate"] == "true" && l.msg == "invalid configuration" {
				found = true
			}
		}

		if !found {
			t.Error("the candidate did not log what was wrong")
		}
	})
}

// One bad edit is one candidate and one rejection, however the path is given: the
// edit is noticed once (a write and the setting of its time are one change), and
// the rejection leaves nothing behind that asks again.
func TestOneBadEditIsOneCandidateAndOneRejection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		path := filepath.Join(t.TempDir(), "svc.json")

		edit(t, path, addrDoc(":1"), 1)
		editAfter(t, 5*time.Second, path, `{"svc": `, 2)

		var c cfgProgram

		foundation.Run("svc", c.fn(func(f *foundation.F, n int, _ cfgDoc) {
			// Long enough for any second attempt to have happened.
			time.Sleep(time.Minute)

			f.Shutdown("done")
		}), quick(foundation.RestartPolicy{}), foundation.WithDocumentConfig(json.File(path, asked)))

		if got := len(entriesNamed(recorder, "restart rejected")); got != 1 {
			t.Errorf("%d rejections, want 1", got)
		}

		candidates := 0

		for _, l := range loggedEntries(recorder) {
			if l.msg == "dry run: nothing will be started" && l.attrs["candidate"] == "true" {
				candidates++
			}
		}

		if candidates != 1 {
			t.Errorf("%d candidates, want 1", candidates)
		}
	})
}
