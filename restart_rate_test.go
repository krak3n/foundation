package foundation_test

import (
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/foundationtest"
)

// A restart is not carried out the moment it is asked for. Something that notices
// changes asks again and again (an editor saves a file in four writes, a deploy
// touches fifty files), and a program that restarted each time would spend its
// life starting. The rate is controlled by a [foundation.RestartPolicy], chosen in
// code and never by whatever is being watched:
//
//   - Debounce: the restart begins once no more requests have come for this long
//     (trailing, so it sees the end of a burst), but never later than MaxDebounce
//     after the first of the burst, so a source that never goes quiet still gets a
//     restart. Defaults 250ms and 2s.
//   - MinInterval: a restart does not begin sooner than this after the instance
//     started (the program, for the first), so a program that restarts as soon as
//     it starts is slowed, not spun. Default 1s.
//   - Limit restarts may begin in any Window (defaults 5 and a minute). A restart
//     that is due when the limit is reached is held, not dropped, until the oldest
//     of them is a Window old, so the last change is never lost, and one error is
//     logged for the hold.
//
// All requests in a burst are one restart, which logs the latest reason and how
// many requests there were. Zero in a field means its default, so a policy can't
// switch a limit off; the limits are bounded, and a mistake in a policy stops
// startup. Times in these tests are exact: they run on the fake clock of a
// synctest bubble.

// withoutCandidate is p with the candidate switched off, for the tests of the rate
// controls on their own.
func withoutCandidate(p foundation.RestartPolicy) foundation.RestartPolicy {
	p.SkipValidation = true

	return p
}

// quick is a policy with a short debounce and no waiting between restarts, for the
// tests of one control that shouldn't be affected by the others.
func quick(p foundation.RestartPolicy) foundation.Option {
	if p.Debounce == 0 {
		p.Debounce = 250 * time.Millisecond
	}

	if p.MinInterval == 0 {
		p.MinInterval = time.Millisecond
	}

	return foundation.WithRestartPolicy(p)
}

// timeline records when, since the run began, each instance started.
type timeline struct {
	t0     time.Time
	mu     sync.Mutex
	starts []time.Duration
}

func newTimeline() *timeline { return &timeline{t0: time.Now()} }

func (tl *timeline) mark() time.Duration {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	d := time.Since(tl.t0)

	tl.starts = append(tl.starts, d)

	return d
}

func (tl *timeline) got() []time.Duration {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	return slices.Clone(tl.starts)
}

func (tl *timeline) since() time.Duration { return time.Since(tl.t0) }

func assertStarts(t *testing.T, tl *timeline, want ...time.Duration) {
	t.Helper()

	if got := tl.got(); !slices.Equal(got, want) {
		t.Fatalf("instances started at\n got: %v\nwant: %v", got, want)
	}
}

// chain runs a program whose instances each ask for a restart as soon as they
// start, until there are count of them, and returns when each started.
func chain(t *testing.T, count int, opts ...foundation.Option) (*timeline, *foundationtest.LogRecorder) {
	t.Helper()

	installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	tl := newTimeline()

	var p program

	foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
		tl.mark()

		if n < count {
			f.Restart("again")

			until(f)

			return
		}

		f.Shutdown("done")
	}), opts...)

	return tl, recorder
}

func TestARestartBeginsOnceTheRequestsHaveStopped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		tl := newTimeline()

		var (
			p       program
			stopped time.Duration
		)

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			tl.mark()

			if n > 1 {
				f.Shutdown("done")

				return
			}

			// Three requests, 100ms apart: the last is at 200ms, so with a 250ms
			// debounce the restart begins at 450ms.
			go func() {
				for _, reason := range []string{"first", "second", "last"} {
					f.Restart(reason)

					time.Sleep(100 * time.Millisecond)
				}
			}()

			time.Sleep(449 * time.Millisecond)

			if f.Context().Err() != nil {
				t.Error("the instance was stopped before the debounce was over")
			}

			until(f)

			stopped = tl.since()
		}), quick(foundation.RestartPolicy{}))

		if stopped != 450*time.Millisecond {
			t.Errorf("the restart began at %v, want 450ms", stopped)
		}

		assertStarts(t, tl, 0, 450*time.Millisecond)

		got := entriesNamed(recorder, "restarting")
		if len(got) != 1 {
			t.Fatalf("%d restarting entries, want one for three requests", len(got))
		}

		if got[0].attrs["reason"] != "last" || got[0].attrs["requests"] != "3" {
			t.Errorf("logged %v, want the latest reason and the number of requests", got[0].attrs)
		}
	})
}

// A source that never goes quiet still gets a restart, after the cap. (With a
// candidate a request that arrives while it runs sets its result aside, which these
// requests, at the very instant it would run, always do; that is for the tests of
// the candidate, so this one is about the debounce alone.)
func TestADebounceThatNeverEndsIsCapped(t *testing.T) {
	tests := map[string]struct {
		policy foundation.RestartPolicy
		want   time.Duration
	}{
		"the default cap is 2s": {foundation.RestartPolicy{}, 2 * time.Second},
		"a cap of 3s":           {foundation.RestartPolicy{MaxDebounce: 3 * time.Second}, 3 * time.Second},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				installExit(t)

				tl := newTimeline()

				var (
					p  program
					wg sync.WaitGroup
				)

				foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
					tl.mark()

					if n > 1 {
						f.Shutdown("done")

						return
					}

					// A request every 200ms, for longer than any cap: each is
					// inside the 250ms debounce of the one before.
					wg.Go(func() {
						for range 40 {
							f.Restart("busy")

							time.Sleep(200 * time.Millisecond)
						}
					})

					until(f)
				}), quick(withoutCandidate(tt.policy)))

				// The requests go on after the restart, to an instance that has gone.
				wg.Wait()

				assertStarts(t, tl, 0, tt.want)
			})
		})
	}
}

func TestTheDebounceIsAnOptionOfThePolicy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tl, _ := chain(t, 2, quick(foundation.RestartPolicy{Debounce: 3 * time.Second, MaxDebounce: 5 * time.Second}))

		assertStarts(t, tl, 0, 3*time.Second)
	})
}

// However quickly a program asks to be restarted, it is not started again sooner
// than the minimum interval after the last start.
func TestARestartDoesNotBeginSoonerThanTheMinimumInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		tl := newTimeline()

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			tl.mark()

			switch n {
			case 1:
				// Asked for at once, but the program has only just started.
				f.Restart("early")

				until(f)
			case 2:
				// Asked for at 7s, which is after the debounce but only 2s after
				// this instance started, so it waits for the 5s.
				time.Sleep(2 * time.Second)

				f.Restart("again")

				until(f)
			default:
				f.Shutdown("done")
			}
		}), foundation.WithRestartPolicy(foundation.RestartPolicy{
			Debounce:    10 * time.Millisecond,
			MinInterval: 5 * time.Second,
		}))

		assertStarts(t, tl, 0, 5*time.Second, 10*time.Second)
	})
}

// A restart that is long past its minimum interval is not delayed by it.
func TestAnInstanceThatHasRunForLongerThanTheMinimumIntervalRestartsAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		tl := newTimeline()

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			tl.mark()

			if n > 1 {
				f.Shutdown("done")

				return
			}

			time.Sleep(time.Minute)

			f.Restart("late")

			until(f)
		}), foundation.WithRestartPolicy(foundation.RestartPolicy{Debounce: time.Second, MinInterval: 5 * time.Second}))

		assertStarts(t, tl, 0, time.Minute+time.Second)
	})
}

// The defaults are the same whether no policy is given or an empty one is.
func TestTheDefaultPolicyIsTheEmptyPolicy(t *testing.T) {
	for name, opts := range map[string][]foundation.Option{
		"no policy":    nil,
		"empty policy": {foundation.WithRestartPolicy(foundation.RestartPolicy{})},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tl, _ := chain(t, 3, opts...)

				// The first instance asks at once and waits for the minimum
				// interval, 1s, which is longer than the debounce.
				assertStarts(t, tl, 0, time.Second, 2*time.Second)
			})
		})
	}
}

// With the defaults, five restarts may begin in a minute. The sixth is held until
// the first is a minute old, and one error says so.
func TestTheSixthRestartInAMinuteIsHeldUntilTheFirstIsAMinuteOld(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tl, recorder := chain(t, 7)

		assertStarts(t, tl,
			0,
			1*time.Second, 2*time.Second, 3*time.Second, 4*time.Second, 5*time.Second,
			// The restart that would have begun at 6s waits until the one that
			// began at 1s is a minute old.
			61*time.Second,
		)

		held := entriesNamed(recorder, "restart held")
		if len(held) != 1 || held[0].level != slog.LevelError {
			t.Fatalf("want one error log for the hold, got %+v", held)
		}
	})
}

// The window is exact: not a restart too soon, and not one too late.
func TestTheWindowIsExact(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tl, _ := chain(t, 4, foundation.WithRestartPolicy(foundation.RestartPolicy{
			Debounce:    10 * time.Millisecond,
			MinInterval: time.Second,
			Limit:       2,
			Window:      10 * time.Second,
		}))

		// Restarts begin at 1s and 2s. The third is due at 3s, with two in the
		// last 10s, so it is held until 11s: exactly 10s after the first.
		assertStarts(t, tl, 0, time.Second, 2*time.Second, 11*time.Second)
	})
}

// Requests while one is held are the same restart: a flood is one error and one
// restart, whatever its size.
func TestAFloodDuringAHoldIsOneErrorAndOneRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		tl := newTimeline()

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			tl.mark()

			switch n {
			case 1:
				f.Restart("first")

				until(f)
			case 2:
				// The limit is one a minute, and one has just begun: everything
				// from here is held.
				var wg sync.WaitGroup

				for range 50 {
					wg.Go(func() {
						for range 200 {
							f.Restart("flood")
						}
					})
				}

				wg.Wait()

				until(f)
			default:
				f.Shutdown("done")
			}
		}), foundation.WithRestartPolicy(foundation.RestartPolicy{
			Debounce:    10 * time.Millisecond,
			MinInterval: time.Millisecond,
			Limit:       1,
			Window:      time.Minute,
		}))

		// The first restart began at 10ms; the held one begins a minute later.
		assertStarts(t, tl, 0, 10*time.Millisecond, time.Minute+10*time.Millisecond)

		if got := len(entriesNamed(recorder, "restart held")); got != 1 {
			t.Errorf("%d error logs for 10,000 requests, want 1", got)
		}

		if got := len(entriesNamed(recorder, "restarting")); got != 2 {
			t.Errorf("%d restarts, want 2", got)
		}
	})
}

// Asking for a restart when one is already waiting is cheap: nothing is started or
// kept for each request, so a flood from a watcher can't use memory or goroutines.
func TestARequestWhileOneIsWaitingDoesNotAllocate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		var (
			p      program
			allocs float64
		)

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			f.Restart("first")

			allocs = testing.AllocsPerRun(1000, func() { f.Restart("again") })

			until(f)
		}), quick(foundation.RestartPolicy{}))

		if allocs > 1 {
			t.Errorf("a request allocates %v times", allocs)
		}
	})
}

// A restart that is held can still be overtaken by a shutdown or a failure.
func TestAShutdownOrFailureEndsWhatIsWaiting(t *testing.T) {
	tests := map[string]struct {
		end  func(f *foundation.F)
		want []int
	}{
		"shutdown": {func(f *foundation.F) { f.Shutdown("bye") }, nil},
		"fail":     {func(f *foundation.F) { f.Fail(errors.New("boom")) }, []int{1}},
	}

	waits := map[string]foundation.RestartPolicy{
		"in the debounce":         {Debounce: time.Hour, MaxDebounce: time.Hour},
		"in the minimum interval": {Debounce: time.Millisecond, MinInterval: time.Hour},
	}

	for wait, policy := range waits {
		for name, tt := range tests {
			t.Run(wait+" then "+name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					exit := installExit(t)

					tl := newTimeline()

					var (
						p     program
						ended time.Duration
					)

					foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
						tl.mark()

						go func() {
							time.Sleep(time.Minute)

							tt.end(f)
						}()

						f.Restart("now")

						until(f)

						ended = tl.since()
					}), foundation.WithRestartPolicy(policy))

					if got := p.instances(); got != 1 {
						t.Fatalf("%d instances ran, want 1: nothing may start after a %s", got, name)
					}

					if ended != time.Minute {
						t.Errorf("the instance stopped at %v, want 1m (the restart was waiting, not begun)", ended)
					}

					assertExitCodes(t, exit, tt.want...)
				})
			})
		}
	}
}

// A request that has been made is carried out even if the root function has
// returned in the meantime: it is the request that matters, not what asked.
func TestAPendingRestartIsCarriedOutAfterTheRootFunctionReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		tl := newTimeline()

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			tl.mark()

			if n == 1 {
				f.Restart("then return")

				return
			}

			f.Shutdown("done")
		}), quick(foundation.RestartPolicy{}))

		assertStarts(t, tl, 0, 250*time.Millisecond)
	})
}

// And while it waits, a shutdown ends the program at once, rather than when the
// wait is over.
func TestAShutdownEndsAPendingRestartAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		tl := newTimeline()

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			tl.mark()

			go func() {
				time.Sleep(100 * time.Millisecond)

				f.Shutdown("bye")
			}()

			f.Restart("then return")
		}), foundation.WithRestartPolicy(foundation.RestartPolicy{Debounce: time.Hour, MaxDebounce: time.Hour}))

		if got := tl.since(); got != 100*time.Millisecond {
			t.Errorf("Run returned after %v, want 100ms", got)
		}

		if got := p.instances(); got != 1 {
			t.Errorf("%d instances ran, want 1", got)
		}

		assertExitCodes(t, exit)
	})
}

// A policy is the program's and can't be turned into a way to switch the limits
// off or make them absurd: a mistake stops startup, before anything is read or
// started, and says nothing the program didn't write.
func TestAnInvalidPolicyStopsStartup(t *testing.T) {
	day := 24 * time.Hour

	bad := map[string]foundation.RestartPolicy{
		"negative debounce":                    {Debounce: -1},
		"negative cap":                         {MaxDebounce: -1},
		"negative interval":                    {MinInterval: -1},
		"negative window":                      {Window: -1},
		"negative limit":                       {Limit: -1},
		"debounce longer than its cap":         {Debounce: 3 * time.Second, MaxDebounce: time.Second},
		"debounce longer than the default cap": {Debounce: time.Hour},
		"limit over the most":                  {Limit: 1_000_000},
		"window over a day":                    {Window: day + time.Second},
		"interval over a day":                  {MinInterval: day + time.Second},
		"cap over a day":                       {MaxDebounce: day + time.Second},
	}

	for name, policy := range bad {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				exit := installExit(t)

				recorder := foundationtest.NewLogRecorder()
				recorder.Install(t)

				var p program

				foundation.Run("svc", p.fn(func(f *foundation.F, n int) {}), foundation.WithRestartPolicy(policy))

				if got := p.instances(); got != 0 {
					t.Fatal("the root function ran with a policy that is invalid")
				}

				assertExitCodes(t, exit, 1)

				if len(entriesNamed(recorder, "shutting down due to failure")) != 1 {
					t.Error("the failure was not reported")
				}
			})
		})
	}

	good := map[string]foundation.RestartPolicy{
		"empty":                       {},
		"the least":                   {Debounce: 1, MaxDebounce: 1, MinInterval: 1, Window: 1, Limit: 1},
		"the most":                    {Debounce: day, MaxDebounce: day, MinInterval: day, Window: day, Limit: 1000},
		"debounce equal to its cap":   {Debounce: time.Second, MaxDebounce: time.Second},
		"debounce within the default": {Debounce: time.Second},
	}

	for name, policy := range good {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				exit := installExit(t)

				var p program

				foundation.Run("svc", p.fn(func(f *foundation.F, n int) {}), foundation.WithRestartPolicy(policy))

				if got := p.instances(); got != 1 {
					t.Fatal("the root function did not run with a policy that is valid")
				}

				assertExitCodes(t, exit)
			})
		})
	}
}

// Many requests from many goroutines, with a shutdown among them: whatever the
// order, Run returns, no more than one instance runs at once, and nothing is left
// behind (the bubble ends only when every goroutine has). Run it with -race.
func TestRequestsFromManyGoroutinesWithAShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		for round := range 20 {
			var (
				p  program
				wg sync.WaitGroup
			)

			foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
				if n == 1 {
					for range 20 {
						wg.Go(func() {
							for range 100 {
								f.Restart("busy")
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
