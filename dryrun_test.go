package foundation_test

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/foundationtest"
)

// dryRun runs fn under the root function "svc" in dry run mode and returns the
// exit codes and what was logged.
func dryRun(t *testing.T, fn foundation.Func, opts ...foundation.Option) ([]int, *foundationtest.LogRecorder) {
	t.Helper()

	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("svc", fn, append([]foundation.Option{foundation.WithDryRun(true)}, opts...)...)

	return exit.Codes(), recorder
}

func entriesNamed(r *foundationtest.LogRecorder, msg string) []logged {
	var out []logged

	for _, l := range loggedEntries(r) {
		if l.msg == msg {
			out = append(out, l)
		}
	}

	return out
}

func TestDryRunIsOffByDefault(t *testing.T) {
	installExit(t)

	var seen []bool

	for _, opts := range [][]foundation.Option{nil, {foundation.WithDryRun(false)}} {
		foundation.Run("svc", func(f *foundation.F) {
			seen = append(seen, f.DryRun())
		}, opts...)
	}

	if !slices.Equal(seen, []bool{false, false}) {
		t.Fatalf("DryRun() = %v, want false both times", seen)
	}
}

func TestDryRunIsVisibleAtEveryDepth(t *testing.T) {
	var depths []string

	dryRun(t, func(f *foundation.F) {
		if f.DryRun() {
			depths = append(depths, "root")
		}

		f.Run("a", func(f *foundation.F) {
			if f.DryRun() {
				depths = append(depths, "a")
			}

			f.Run("b", func(f *foundation.F) {
				if f.DryRun() {
					depths = append(depths, "b")
				}
			})
		})
	})

	if !slices.Equal(depths, []string{"root", "a", "b"}) {
		t.Fatalf("DryRun() was true at %v", depths)
	}
}

// A dry run starts nothing, so nothing that anyone can set from outside the
// program may switch it on: a service that quietly did nothing and exited 0
// would look healthy to an orchestrator.
func TestDryRunCannotBeSwitchedOnFromTheEnvironmentOrConfiguration(t *testing.T) {
	for _, name := range []string{"DRY_RUN", "DRYRUN", "FOUNDATION_DRY_RUN", "SVC_DRY_RUN", "SVC_DRYRUN", "FOUNDATION_DRYRUN"} {
		t.Setenv(name, "1")
	}

	installExit(t)

	var on bool

	foundation.Run("svc", func(f *foundation.F) {
		var cfg struct {
			DryRun bool `env:"DRY_RUN" json:"dry_run"`
		}

		f.Load(&cfg)

		on = f.DryRun()
	})

	if on {
		t.Fatal("DryRun() was true without foundation.WithDryRun")
	}
}

func TestSkipStopsTheFunctionAndTheRunCarriesOn(t *testing.T) {
	var (
		after    bool
		cleanups []string
		ran      []string
	)

	codes, recorder := dryRun(t, func(f *foundation.F) {
		f.Run("server", func(f *foundation.F) {
			f.Cleanup(func() { cleanups = append(cleanups, "first") })
			f.Cleanup(func() { cleanups = append(cleanups, "second") })

			ran = append(ran, "server")

			f.Skip("not in a dry run")

			after = true
		})

		f.Run("next", func(f *foundation.F) {
			ran = append(ran, "next")
		})
	})

	if after {
		t.Fatal("code after Skip ran")
	}

	if !slices.Equal(ran, []string{"server", "next"}) {
		t.Fatalf("ran %v, want the next sibling to run after the skip", ran)
	}

	if !slices.Equal(cleanups, []string{"second", "first"}) {
		t.Fatalf("cleanups ran %v, want them last in first out", cleanups)
	}

	if len(codes) != 0 {
		t.Fatalf("exit codes = %v, want none: a skip is not a failure", codes)
	}

	skipped := entriesNamed(recorder, "skipped")

	if len(skipped) != 1 || skipped[0].level != slog.LevelInfo || skipped[0].attrs["fn"] != "svc/server" || skipped[0].attrs["reason"] != "not in a dry run" {
		t.Fatalf("unexpected skip log: %+v", skipped)
	}
}

// Skip is the same outside a dry run, which is how a disabled feature stops.
func TestSkipWorksOutsideADryRun(t *testing.T) {
	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var after, next bool

	foundation.Run("svc", func(f *foundation.F) {
		f.Run("feature", func(f *foundation.F) {
			f.Skip("disabled")

			after = true
		})

		f.Run("next", func(f *foundation.F) { next = true })
	})

	if after || !next || len(exit.Codes()) != 0 {
		t.Fatalf("after = %v, next = %v, exit codes = %v", after, next, exit.Codes())
	}

	if len(entriesNamed(recorder, "skipped")) != 1 {
		t.Fatal("expected the skip to be logged")
	}

	if len(entriesNamed(recorder, "dry run complete")) != 0 {
		t.Fatal("nothing about a dry run should be logged")
	}
}

func TestSkipAfterParallel(t *testing.T) {
	var after bool

	codes, _ := dryRun(t, func(f *foundation.F) {
		f.Run("worker", func(f *foundation.F) {
			f.Parallel()

			f.Skip("dry run")

			after = true
		})
	})

	if after || len(codes) != 0 {
		t.Fatalf("after = %v, exit codes = %v", after, codes)
	}
}

func TestSkipInTheRootFunction(t *testing.T) {
	var cleaned, after bool

	codes, _ := dryRun(t, func(f *foundation.F) {
		f.Cleanup(func() { cleaned = true })

		f.Skip("nothing to do")

		after = true
	})

	if after || !cleaned || len(codes) != 0 {
		t.Fatalf("after = %v, cleaned = %v, exit codes = %v", after, cleaned, codes)
	}
}

func TestSkipLogsItsAttributes(t *testing.T) {
	_, recorder := dryRun(t, func(f *foundation.F) {
		f.Run("a", func(f *foundation.F) {
			f.Skip("dry run", slog.String("addr", ":8080"))
		})
	})

	skipped := entriesNamed(recorder, "skipped")

	if len(skipped) != 1 || skipped[0].attrs["addr"] != ":8080" {
		t.Fatalf("unexpected skip log: %+v", skipped)
	}
}

// A reason is the developer's text, but it may be built from something else, so
// a newline or an escape sequence in it must not reach the log as it is.
func TestSkipEscapesControlCharactersInTheReason(t *testing.T) {
	_, recorder := dryRun(t, func(f *foundation.F) {
		f.Run("a", func(f *foundation.F) {
			f.Skip("off\nINFO forged line\x1b[2J")
		})
	})

	skipped := entriesNamed(recorder, "skipped")

	if len(skipped) != 1 {
		t.Fatalf("unexpected skip log: %+v", skipped)
	}

	reason := skipped[0].attrs["reason"]

	if strings.ContainsFunc(reason, func(r rune) bool { return r < ' ' || r == 0x7f }) || !strings.Contains(reason, `\n`) {
		t.Fatalf("reason = %q, want the control characters escaped", reason)
	}
}

func TestADryRunSaysSoAtTheStartAndTheEnd(t *testing.T) {
	_, recorder := dryRun(t, func(f *foundation.F) {
		f.Run("a", func(f *foundation.F) { f.Skip("dry run") })
		f.Run("b", func(f *foundation.F) { f.Skip("dry run") })
		f.Run("c", func(f *foundation.F) {})
	})

	logs := loggedEntries(recorder)

	if len(logs) == 0 || logs[0].msg != "dry run: nothing will be started" || logs[0].level != slog.LevelInfo {
		t.Fatalf("the first line should say this is a dry run, got %+v", logs)
	}

	done := entriesNamed(recorder, "dry run complete")

	if len(done) != 1 || done[0].attrs["skipped"] != "2" || done[0].level != slog.LevelInfo {
		t.Fatalf("unexpected completion log: %+v", done)
	}

	if logs[len(logs)-1].msg != "dry run complete" {
		t.Fatalf("the last line should be the completion, got %q", logs[len(logs)-1].msg)
	}
}

func TestNothingIsSaidAboutADryRunOtherwise(t *testing.T) {
	installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("svc", func(f *foundation.F) {})

	for _, l := range loggedEntries(recorder) {
		if strings.Contains(l.msg, "dry run") {
			t.Fatalf("unexpected line %q", l.msg)
		}
	}
}

// A failure is a failure: it is reported as one and the dry run does not claim
// to have completed.
func TestADryRunThatFailsIsNotComplete(t *testing.T) {
	codes, recorder := dryRun(t, func(f *foundation.F) {
		f.Run("a", func(f *foundation.F) {
			f.Fail(errors.New("broken"))
		})
	})

	if !slices.Equal(codes, []int{1}) {
		t.Fatalf("exit codes = %v, want [1]", codes)
	}

	if len(entriesNamed(recorder, "dry run complete")) != 0 {
		t.Fatal("a failed dry run must not say it completed")
	}
}

// Everything that checks the configuration runs: this is the point of a dry run.
func TestADryRunLoadsAndValidatesTheConfiguration(t *testing.T) {
	type cfg struct {
		Addr string `env:"ADDR" config:"required"`
		Port int    `env:"PORT"`
	}

	t.Run("a mistake fails it", func(t *testing.T) {
		codes, recorder := dryRun(t, func(f *foundation.F) {
			f.Run("api", func(f *foundation.F) {
				f.Load(&cfg{})

				f.Skip("dry run")
			})
		}, foundation.WithConfigProviders(envOf(map[string]string{"SVC_API_PORT": "not a number"})))

		if !slices.Equal(codes, []int{1}) {
			t.Fatalf("exit codes = %v, want [1]", codes)
		}

		if got := fieldsOf(problemsLogged(recorder)); !slices.Equal(got, []string{"Port", "Addr"}) {
			t.Fatalf("problems for %v, want Port and the missing Addr", got)
		}
	})

	t.Run("a good configuration passes", func(t *testing.T) {
		codes, _ := dryRun(t, func(f *foundation.F) {
			f.Run("api", func(f *foundation.F) {
				f.Load(&cfg{})

				f.Skip("dry run")
			})
		}, foundation.WithConfigProviders(envOf(map[string]string{"SVC_API_ADDR": ":1", "SVC_API_PORT": "80"})))

		if len(codes) != 0 {
			t.Fatalf("exit codes = %v, want none", codes)
		}
	})
}

type validatingConfig struct {
	Port int `env:"PORT"`
}

func (c *validatingConfig) Validate() error {
	if c.Port < 1024 {
		return errors.New("port must be 1024 or above")
	}

	return nil
}

func TestADryRunRunsValidate(t *testing.T) {
	codes, _ := dryRun(t, func(f *foundation.F) {
		f.Load(&validatingConfig{})
	}, foundation.WithConfigProviders(envOf(map[string]string{"SVC_PORT": "80"})))

	if !slices.Equal(codes, []int{1}) {
		t.Fatalf("exit codes = %v, want [1]", codes)
	}
}

// The strict check runs at the end of a dry run, so a typo in a deployment is
// found by running the program with --dry-run before it is rolled out.
func TestADryRunRunsTheStrictCheck(t *testing.T) {
	vars := map[string]string{"SVC_NAME": "x", "SVC_ADRR": ":9", "SVC_LEGACY_MODE": "1"}

	codes, recorder := dryRun(t, func(f *foundation.F) {
		f.Load(&strictConfig{})
	},
		foundation.WithConfigProviders(envOf(vars)),
		foundation.WithStrictConfig(),
		foundation.WithStrictIgnore("SVC_LEGACY_*"),
	)

	if !slices.Equal(codes, []int{1}) {
		t.Fatalf("exit codes = %v, want [1]", codes)
	}

	if got := keysOf(problemsLogged(recorder)); !slices.Equal(got, []string{"SVC_ADRR"}) {
		t.Fatalf("reported %v, want only SVC_ADRR", got)
	}

	if len(entriesNamed(recorder, "dry run complete")) != 0 {
		t.Fatal("a failed dry run must not say it completed")
	}
}

// What is still running when the root function returns is stopped, in the usual
// order, so a function that forgot to skip doesn't keep the program alive.
func TestADryRunStopsWhatIsStillRunningWhenTheRootReturns(t *testing.T) {
	var (
		stopped atomic.Bool
		started = make(chan struct{})
	)

	codes, recorder := dryRun(t, func(f *foundation.F) {
		f.Run("forgot", func(f *foundation.F) {
			f.Parallel()

			close(started)

			<-f.Context().Done()

			stopped.Store(true)
		})
	})

	<-started

	if !stopped.Load() {
		t.Fatal("expected the running function to be stopped")
	}

	if len(codes) != 0 {
		t.Fatalf("exit codes = %v, want none", codes)
	}

	if len(entriesNamed(recorder, "dry run complete")) != 1 {
		t.Fatal("expected the dry run to complete")
	}
}

// The strict check comes before the end, and cleanups of the root run last.
func TestADryRunEndsInOrder(t *testing.T) {
	var order []string

	var mu sync.Mutex

	add := func(s string) {
		mu.Lock()
		defer mu.Unlock()

		order = append(order, s)
	}

	dryRun(t, func(f *foundation.F) {
		f.Cleanup(func() { add("root cleanup") })

		f.Run("a", func(f *foundation.F) {
			f.Cleanup(func() { add("a cleanup") })
			f.Skip("dry run")
		})

		add("root returned")
	})

	if want := []string{"a cleanup", "root returned", "root cleanup"}; !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

// Many functions skipping at once, as they do in a big tree, is safe and every
// skip is counted.
func TestManyFunctionsCanSkipAtOnce(t *testing.T) {
	const n = 200

	codes, recorder := dryRun(t, func(f *foundation.F) {
		var wg sync.WaitGroup

		for i := range n {
			wg.Add(1)

			f.Run(fmt.Sprintf("fn-%d", i), func(f *foundation.F) {
				defer wg.Done()

				f.Parallel()

				time.Sleep(time.Millisecond)

				f.Skip("dry run")
			})
		}

		// A dry run ends when the root function returns, and stops what is still
		// running, so wait for the skips to be counted.
		wg.Wait()
	})

	if len(codes) != 0 {
		t.Fatalf("exit codes = %v, want none", codes)
	}

	if done := entriesNamed(recorder, "dry run complete"); len(done) != 1 || done[0].attrs["skipped"] != fmt.Sprint(n) {
		t.Fatalf("unexpected completion log: %+v", done)
	}
}

// A load that is only reached by a function that skips afterwards still counts
// for strict mode: dry run does not change which variables were asked for.
func TestADryRunCountsEveryLoadForStrictMode(t *testing.T) {
	codes, _ := dryRun(t, func(f *foundation.F) {
		f.Load(&strictConfig{})

		f.Run("http", func(f *foundation.F) {
			f.Load(&strictConfig{})

			f.Skip("dry run")
		})
	},
		foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{"SVC_NAME": "x", "SVC_HTTP_ADDR": ":1"}))),
		foundation.WithStrictConfig(),
	)

	if len(codes) != 0 {
		t.Fatalf("exit codes = %v, want none", codes)
	}
}

// hungRun runs a dry run whose function "worker" never returns and never skips,
// and reports how long the run took and what it logged. The clock is fake, so the
// times are exact.
func hungRun(t *testing.T, opts ...foundation.Option) (time.Duration, []int, *foundationtest.LogRecorder) {
	t.Helper()

	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	// Released when the test ends: every goroutine in a synctest bubble must exit,
	// including the one that was abandoned.
	hang := make(chan struct{})
	defer close(hang)

	start := time.Now()

	foundation.Run("svc", func(f *foundation.F) {
		f.Run("worker", func(f *foundation.F) {
			f.SetTimeout(time.Second)

			<-hang
		})
	}, append([]foundation.Option{foundation.WithDryRun(true)}, opts...)...)

	return time.Since(start), exit.Codes(), recorder
}

// A function that neither returns nor skips would hang a dry run for ever, and a
// --help that hangs is worse than none. After the timeout the run fails and says
// which functions were still running.
func TestADryRunThatHangsFailsAfterTheTimeoutAndNamesTheFunction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		elapsed, codes, recorder := hungRun(t)

		// 10 seconds to the watchdog, then the second the worker is given to stop.
		if want := 11 * time.Second; elapsed != want {
			t.Fatalf("took %v, want %v", elapsed, want)
		}

		if !slices.Equal(codes, []int{1}) {
			t.Fatalf("exit codes = %v, want [1]", codes)
		}

		hung := entriesNamed(recorder, "dry run did not finish")

		if len(hung) != 1 || hung[0].level < 8 {
			t.Fatalf("expected one error, got %+v", hung)
		}

		if !strings.Contains(hung[0].attrs["running"], "svc/worker") || !strings.Contains(hung[0].attrs["hint"], "f.Skip") {
			t.Fatalf("unexpected report: %+v", hung[0].attrs)
		}

		if len(entriesNamed(recorder, "dry run complete")) != 0 {
			t.Fatal("a dry run that hung must not say it completed")
		}
	})
}

func TestTheDryRunTimeoutCanBeChanged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		elapsed, codes, _ := hungRun(t, foundation.WithDryRunTimeout(2*time.Second))

		if want := 3 * time.Second; elapsed != want {
			t.Fatalf("took %v, want %v", elapsed, want)
		}

		if !slices.Equal(codes, []int{1}) {
			t.Fatalf("exit codes = %v, want [1]", codes)
		}
	})
}

// A timeout that is not positive can't switch the watchdog off: it means the
// default.
func TestANonPositiveDryRunTimeoutMeansTheDefault(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		synctest.Test(t, func(t *testing.T) {
			elapsed, codes, _ := hungRun(t, foundation.WithDryRunTimeout(d))

			if want := 11 * time.Second; elapsed != want || !slices.Equal(codes, []int{1}) {
				t.Fatalf("timeout %v: took %v with exit codes %v, want %v and [1]", d, elapsed, codes, want)
			}
		})
	}
}

// The watchdog stops when the root function returns: it can't fire later, or
// leave a goroutine behind.
func TestTheWatchdogDoesNotFireAfterANormalDryRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		foundation.Run("svc", func(f *foundation.F) {
			f.Run("a", func(f *foundation.F) { f.Skip("dry run") })
		}, foundation.WithDryRun(true))

		// Long enough for any timer left behind to fire.
		time.Sleep(time.Minute)

		if len(exit.Codes()) != 0 || len(entriesNamed(recorder, "dry run did not finish")) != 0 {
			t.Fatalf("the watchdog fired after a finished dry run: exit %v", exit.Codes())
		}
	})
}

// Work that is slow but finishes within the timeout is fine.
func TestADryRunThatTakesAWhileButFinishesIsNotFailed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		foundation.Run("svc", func(f *foundation.F) {
			time.Sleep(9 * time.Second)
		}, foundation.WithDryRun(true))

		if len(exit.Codes()) != 0 {
			t.Fatalf("exit codes = %v, want none", exit.Codes())
		}
	})
}

// The watchdog belongs to dry runs. An ordinary run is as long as it likes.
func TestThereIsNoWatchdogOutsideADryRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		foundation.Run("svc", func(f *foundation.F) {
			time.Sleep(time.Hour)
		}, foundation.WithDryRunTimeout(time.Second))

		foundation.Run("svc", func(f *foundation.F) {
			time.Sleep(time.Hour)
		}, foundation.WithDryRun(false), foundation.WithDryRunTimeout(time.Second))

		if len(exit.Codes()) != 0 {
			t.Fatalf("exit codes = %v, want none", exit.Codes())
		}
	})
}

// Every function that was still running is named, in a stable order, with its
// whole path.
func TestTheHangReportNamesEveryFunctionStillRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		hang := make(chan struct{})
		defer close(hang)

		foundation.Run("svc", func(f *foundation.F) {
			f.Run("b", func(f *foundation.F) {
				f.SetTimeout(time.Second)
				f.Parallel()

				<-hang
			})

			f.Run("a", func(f *foundation.F) {
				f.SetTimeout(time.Second)

				f.Run("inner", func(f *foundation.F) {
					f.SetTimeout(time.Second)

					<-hang
				})
			})
		}, foundation.WithDryRun(true))

		hung := entriesNamed(recorder, "dry run did not finish")

		if len(hung) != 1 {
			t.Fatalf("expected one report, got %+v", hung)
		}

		if want := "svc/a, svc/a/inner, svc/b"; hung[0].attrs["running"] != want {
			t.Fatalf("running = %q, want %q", hung[0].attrs["running"], want)
		}
	})
}
