package foundation_test

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unicode"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/foundationtest"
	"go.krak3n.dev/foundation/health/monitor"
)

// Before a restart stops anything, the new instance is tried as a candidate: the
// root function is run again, in a dry run, beside the old instance, which keeps
// running. A candidate that finishes cleanly lets the restart go ahead. One that
// does not (a configuration that no longer loads, a failure, a panic, a function
// that never finishes or can't be stopped, a root function that shuts the program
// down) is rejected: nothing is stopped, the run does not fail, and one error says
// so. A bad edit can therefore no longer take a running service down.
//
// A candidate has a tree, context, probes and process state of its own, so what it
// does (Fail, Shutdown, a panic) can't end or fail the real run, and it can't
// restart anything. Its log lines carry candidate=true. It follows the dry run
// contract: a function that starts something skips when f.DryRun() is true.
//
// The candidate is a check made at one moment: a file that changes after it can
// still fail the real instance, which fails the run, as a start that fails after
// the old instance has stopped always does.

// live is the root function of a program that restarts once: its first instance
// asks for a restart, waits for the candidate to have been tried, checks that it
// was never stopped by it, and then shuts the program down.
func live(t *testing.T, p *program) foundation.Func {
	t.Helper()

	return p.fn(func(f *foundation.F, n int) {
		if n > 1 {
			f.Shutdown("done")

			return
		}

		f.Restart("edit")

		time.Sleep(time.Minute)

		if err := f.Context().Err(); err != nil {
			t.Errorf("a candidate that was rejected stopped the instance: %v", err)
		}

		f.Shutdown("done")
	})
}

func TestTheCandidateRunsInADryRunWhileTheOldInstanceKeepsRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		var (
			p     program
			first *foundation.F
		)

		db := monitor.NewProbe("db", monitor.LivenessMode, alwaysHealthy{})

		p.onCandidate = func(f *foundation.F, k int) {
			if !f.DryRun() {
				t.Error("the candidate is not a dry run")
			}

			if err := first.Context().Err(); err != nil {
				t.Errorf("the old instance was stopped before the candidate ran: %v", err)
			}

			if got := f.String(); got != "svc" {
				t.Errorf("the candidate is %q, want svc", got)
			}

			p.ev.add("candidate")

			// A probe of the same name as one the live instance has registered is
			// not a duplicate: the candidate has a registry of its own, and the live
			// one is left as it was.
			f.Probes().Add(db)

			count := 0
			for range first.Probes().Seq() {
				count++
			}

			if count != 1 {
				t.Errorf("the live registry has %d probes, want its own one", count)
			}

			// A candidate starts nothing, and that includes another restart.
			f.Restart("from the candidate")
		}

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			if f.DryRun() {
				t.Error("the real instance is a dry run")
			}

			if n > 1 {
				f.Shutdown("done")

				return
			}

			first = f

			f.Probes().Add(db)
			f.Restart("edit")

			until(f)
		}), quick(foundation.RestartPolicy{}))

		// The candidate runs while the old instance is up, and before it stops.
		assertEvents(t, p.ev.get(), []string{"1:start", "candidate", "1:end", "2:start", "2:end"})

		if got := p.instances(); got != 2 {
			t.Errorf("%d instances ran, want 2: the candidate is not one", got)
		}

		if got := p.candidates.Load(); got != 1 {
			t.Errorf("%d candidates ran, want 1 (and none from itself)", got)
		}

		assertExitCodes(t, exit)

		if got := len(entriesNamed(recorder, "restart rejected")); got != 0 {
			t.Errorf("%d rejections for a candidate that passed", got)
		}
	})
}

// A candidate's log lines say what they are, so a failure in one is not mistaken
// for a failure of the program; the live instance's are untouched.
func TestACandidatesLogLinesAreTagged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		var p program

		p.onCandidate = func(f *foundation.F, k int) { f.Skip("not starting") }

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			f.Log().Info("live line")
			f.Restart("edit")

			until(f)
		}), quick(foundation.RestartPolicy{}))

		for _, msg := range []string{"skipped", "dry run: nothing will be started", "dry run complete"} {
			got := entriesNamed(recorder, msg)
			if len(got) != 1 || got[0].attrs["candidate"] != "true" {
				t.Errorf("%q: want one entry tagged candidate=true, got %+v", msg, got)
			}
		}

		for _, msg := range []string{"live line", "restarting"} {
			got := entriesNamed(recorder, msg)
			if len(got) != 1 {
				t.Fatalf("%q: %d entries", msg, len(got))
			}

			if _, tagged := got[0].attrs["candidate"]; tagged {
				t.Errorf("%q is tagged as a candidate's: %+v", msg, got[0].attrs)
			}
		}
	})
}

// Every way a candidate can go wrong rejects the restart, and only that: the old
// instance is left running, the run has not failed, and there is one error.
func TestARejectedCandidateLeavesTheOldInstanceRunning(t *testing.T) {
	hang := make(chan struct{})

	tests := map[string]func(f *foundation.F){
		"a failure": func(f *foundation.F) { f.Fail(errors.New("boom")) },
		"a panic":   func(f *foundation.F) { panic("boom") },
		"a failure in a child": func(f *foundation.F) {
			f.Run("child", func(f *foundation.F) { f.Fail(errors.New("boom")) })
		},
		"a panic in a child": func(f *foundation.F) {
			f.Run("child", func(f *foundation.F) { panic("boom") })
		},
		"a shutdown by the root function": func(f *foundation.F) { f.Shutdown("no") },
		"a function that can't be stopped": func(f *foundation.F) {
			// It starts, doesn't skip, and ignores its context: it would be left
			// running beside the live instance.
			f.Run("stuck", func(f *foundation.F) {
				f.SetTimeout(time.Second)
				f.Parallel()

				<-hang
			})
		},
	}

	for name, bad := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				exit := installExit(t)

				recorder := foundationtest.NewLogRecorder()
				recorder.Install(t)

				hang = make(chan struct{})
				defer close(hang)

				var p program

				p.onCandidate = func(f *foundation.F, k int) { bad(f) }

				foundation.Run("svc", live(t, &p), quick(foundation.RestartPolicy{}))

				if got := p.instances(); got != 1 {
					t.Fatalf("%d instances ran, want 1: nothing may restart after a rejection", got)
				}

				if got := p.candidates.Load(); got != 1 {
					t.Errorf("%d candidates ran, want 1", got)
				}

				// The candidate's failure is the candidate's, not the program's.
				assertExitCodes(t, exit)

				rejected := entriesNamed(recorder, "restart rejected")
				if len(rejected) != 1 || rejected[0].level != slog.LevelError {
					t.Fatalf("want one error log for the rejection, got %+v", rejected)
				}

				if rejected[0].attrs["reason"] != "edit" {
					t.Errorf("the rejection says %v, want the reason of the restart", rejected[0].attrs)
				}

				if got := len(entriesNamed(recorder, "restarting")); got != 0 {
					t.Errorf("%d restarting entries after a rejection", got)
				}

				tagged := 0

				for _, l := range loggedEntries(recorder) {
					if l.attrs["candidate"] == "true" {
						tagged++
					}
				}

				if tagged == 0 {
					t.Error("nothing the candidate logged was tagged")
				}
			})
		})
	}
}

// What a candidate does to its own process (fail, shut down) doesn't touch the
// real one: after a rejection a later restart still works, and the run ends as a
// run that never failed.
func TestARejectionDoesNotFailOrEndTheRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		var p program

		p.onCandidate = func(f *foundation.F, k int) {
			if k == 1 {
				f.Fail(errors.New("boom"))
			}
		}

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			f.Restart("one")

			time.Sleep(10 * time.Second)

			if err := f.Context().Err(); err != nil {
				t.Errorf("the instance was stopped: %v", err)
			}

			// A change that is fine this time.
			f.Restart("two")

			until(f)
		}), quick(foundation.RestartPolicy{}))

		if got := p.instances(); got != 2 {
			t.Fatalf("%d instances ran, want 2", got)
		}

		if got := p.candidates.Load(); got != 2 {
			t.Errorf("%d candidates ran, want 2", got)
		}

		assertExitCodes(t, exit)
	})
}

// A candidate that doesn't finish is failed by the dry run watchdog, which is
// [foundation.WithDryRunTimeout] (10s by default), and the stuck function is given
// its shutdown timeout, so the rejection comes after both.
func TestACandidateThatNeverFinishesIsRejectedAfterTheDryRunTimeout(t *testing.T) {
	tests := map[string]struct {
		opts []foundation.Option
		want time.Duration
	}{
		// The restart is due at 250ms, the watchdog fires after its timeout, and the
		// function that won't stop is abandoned a second later.
		"the default timeout": {nil, 250*time.Millisecond + 10*time.Second + time.Second},
		"a timeout of 3s":     {[]foundation.Option{foundation.WithDryRunTimeout(3 * time.Second)}, 250*time.Millisecond + 3*time.Second + time.Second},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				installExit(t)

				recorder := foundationtest.NewLogRecorder()
				recorder.Install(t)

				hang := make(chan struct{})
				defer close(hang)

				t0 := time.Now()

				var p program

				// The root function never returns, so the candidate never reaches
				// the end of startup, and ignores its context when it is stopped.
				p.onCandidate = func(f *foundation.F, k int) {
					f.SetTimeout(time.Second)

					<-hang
				}

				foundation.Run("svc", live(t, &p), append([]foundation.Option{quick(foundation.RestartPolicy{})}, tt.opts...)...)

				if got := len(entriesNamed(recorder, "dry run did not finish")); got != 1 {
					t.Errorf("%d watchdog reports, want 1", got)
				}

				var when time.Duration

				for _, e := range recorder.Entries() {
					if e.Record.Message == "restart rejected" {
						when = e.Record.Time.Sub(t0)
					}
				}

				if when != tt.want {
					t.Errorf("the restart was rejected after %v, want %v", when, tt.want)
				}

				if got := p.instances(); got != 1 {
					t.Errorf("%d instances ran, want 1", got)
				}
			})
		})
	}
}

// SkipValidation is for a program that can't be run twice, even as a dry run: the
// restart goes ahead with no candidate, and a bad start fails the run as it would
// without this. It is chosen in code.
func TestSkipValidationRestartsWithoutACandidate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		var p program

		// It would reject, if it were run.
		p.onCandidate = func(f *foundation.F, k int) { f.Fail(errors.New("boom")) }

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			f.Restart("edit")

			until(f)
		}), quick(foundation.RestartPolicy{SkipValidation: true}))

		if got := p.candidates.Load(); got != 0 {
			t.Errorf("%d candidates ran, want none", got)
		}

		if got := p.instances(); got != 2 {
			t.Errorf("%d instances ran, want 2", got)
		}

		assertExitCodes(t, exit)
	})
}

// The reason comes from whatever noticed the change, and is on the line that says
// the restart was rejected: escaped and bounded, as on the one that says it began.
func TestTheReasonOfARejectedRestartIsLoggedSafely(t *testing.T) {
	reasons := map[string]string{
		"newline forging a line": "changed\n{\"level\":\"ERROR\",\"msg\":\"forged\"}",
		"escape sequence":        "changed \x1b[2J\x1b]0;owned\x07",
		"a megabyte":             strings.Repeat("x", 1<<20),
	}

	for name, reason := range reasons {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				installExit(t)

				recorder := foundationtest.NewLogRecorder()
				recorder.Install(t)

				var p program

				p.onCandidate = func(f *foundation.F, k int) { f.Fail(errors.New("boom")) }

				foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
					f.Restart(reason)

					time.Sleep(time.Minute)

					f.Shutdown("done")
				}), quick(foundation.RestartPolicy{}))

				rejected := entriesNamed(recorder, "restart rejected")
				if len(rejected) != 1 {
					t.Fatalf("%d rejections, want 1", len(rejected))
				}

				got := rejected[0].attrs["reason"]

				if len(got) > 1024 {
					t.Errorf("the reason is %d bytes in the log", len(got))
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

// appConfig is the configuration of the program in the tests below.
type appConfig struct {
	Addr string `config:"addr,required"`
	Port int    `config:"port"`
}

// An edit to the file that the program was started with, which is the case this is
// for: a bad one is rejected and the service carries on with what it had, and the
// fix that follows is applied. The candidate runs the program's own root function,
// with the program's own providers, strict mode included, so what it checks is
// what the real start would.
func TestABadEditToAFileIsRejectedAndTheFixIsApplied(t *testing.T) {
	const canary = "canary-secret-91c4"

	edits := map[string]string{
		"a value of the wrong type": `{"svc": {"addr": ":2", "port": "` + canary + `"}}`,
		"a key nothing reads":       `{"svc": {"addr": ":2", "adr": "` + canary + `"}}`,
		"a required value removed":  `{"svc": {"port": 1}}`,
		"not a document":            `{"svc": {"addr": "` + canary + `"`,
		"a key twice":               `{"svc": {"addr": ":2", "addr": "` + canary + `"}}`,
	}

	for name, bad := range edits {
		t.Run(name, func(t *testing.T) {
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
				)

				// The program's root function: load, and start nothing in a dry run.
				app := func(f *foundation.F) {
					var cfg appConfig

					f.Load(&cfg)

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

					write(bad)
					f.Restart("edited")

					time.Sleep(time.Minute)

					if err := f.Context().Err(); err != nil {
						t.Errorf("a bad edit stopped the service: %v", err)
					}

					write(`{"svc": {"addr": ":2"}}`)
					f.Restart("fixed")

					until(f)
				}),
					foundation.WithDocumentConfig(json.File(path)),
					foundation.WithStrictConfig(),
					quick(foundation.RestartPolicy{}),
				)

				mu.Lock()
				defer mu.Unlock()

				// The first instance, then the one after the fix: the bad edit made no
				// instance, and the candidate of the fix saw the fixed file.
				if len(seen) != 2 || seen[0] != ":1" || seen[1] != ":2" {
					t.Fatalf("the instances read %v, want [:1 :2]", seen)
				}

				if got := p.instances(); got != 2 {
					t.Errorf("%d instances ran, want 2", got)
				}

				if got := len(entriesNamed(recorder, "restart rejected")); got != 1 {
					t.Errorf("%d rejections, want 1", got)
				}

				assertExitCodes(t, exit)

				// What was in the file is never in a log.
				for _, l := range loggedEntries(recorder) {
					if strings.Contains(l.msg, canary) {
						t.Errorf("a log message contains the value: %q", l.msg)
					}

					for k, v := range l.attrs {
						if strings.Contains(v, canary) {
							t.Errorf("%q: attribute %s contains the value: %q", l.msg, k, v)
						}
					}
				}
			})
		})
	}
}

// The candidate is a check made at one moment. A file that goes bad after it has
// passed is not caught by it: the real instance fails, and the run fails, once,
// with no retry.
func TestAFileThatGoesBadAfterTheCandidateFailsTheRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		path := filepath.Join(t.TempDir(), "svc.json")

		write := func(data string) {
			t.Helper()

			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		write(`{"svc": {"addr": ":1"}}`)

		var p program

		app := func(f *foundation.F) {
			var cfg appConfig

			f.Load(&cfg)

			if f.DryRun() {
				f.Skip("dry run")
			}
		}

		p.onCandidate = func(f *foundation.F, k int) {
			// After the candidate has loaded and been checked, and before the real
			// instance starts.
			f.Cleanup(func() { write(`{"svc": {"port": "x"}}`) })

			app(f)
		}

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			app(f)

			if n == 1 {
				f.Restart("edit")

				until(f)

				return
			}

			f.Shutdown("done")
		}),
			foundation.WithDocumentConfig(json.File(path)),
			quick(foundation.RestartPolicy{}),
		)

		if got := p.instances(); got != 2 {
			t.Fatalf("%d instances ran, want 2: a failed start is not retried", got)
		}

		assertExitCodes(t, exit, 1)
	})
}

// What a candidate costs, and what it is not allowed to cost. A candidate is a
// second copy of the program, so it is limited as restarts are, it can't be
// started while another is running, a real shutdown stops it, and a program whose
// candidates leave things running is not trusted with another.
//
//   - Every candidate is an attempt, and attempts count against RestartPolicy's
//     Limit whether they pass or not, so a flood of bad edits is at most Limit
//     candidates a Window, not one for each.
//   - A request made while a candidate runs sets its result aside, as what it
//     checked may be out of date, and the request takes its turn. That is done
//     once, so that a source that never goes quiet still gets a restart.
//   - A candidate that leaves a function running (abandoned: it ignores its
//     context) is a leak, and one per attempt would add up, so after one every
//     later restart is refused, with an error.
//   - A shutdown or failure while a candidate runs stops it, and Run does not
//     return until it has stopped.

// attemptsOf is the times a candidate started, which a program records itself.
func attemptsOf(p *program, tl *timeline, then func(f *foundation.F, k int)) {
	p.onCandidate = func(f *foundation.F, k int) {
		tl.mark()

		if then != nil {
			then(f, k)
		}
	}
}

// A candidate that is rejected still cost something to run, so it still counts.
// The third attempt would have been at 3s, but two have been made in the last 10s
// and the Limit is two, so it waits until the first is exactly a Window old.
func TestARejectedAttemptCountsAgainstTheLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		var (
			p        program
			started  = newTimeline()
			attempts = newTimeline()
		)

		attemptsOf(&p, attempts, func(f *foundation.F, k int) {
			if k < 3 {
				f.Fail(errors.New("boom"))
			}
		})

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			started.mark()

			if n > 1 {
				f.Shutdown("done")

				return
			}

			f.Restart("one")

			time.Sleep(2 * time.Second)
			f.Restart("two")

			time.Sleep(time.Second)
			f.Restart("three")

			until(f)
		}), foundation.WithRestartPolicy(foundation.RestartPolicy{
			Debounce:    10 * time.Millisecond,
			MinInterval: time.Second,
			Limit:       2,
			Window:      10 * time.Second,
		}))

		assertStarts(t, attempts, time.Second, 2*time.Second+10*time.Millisecond, 11*time.Second)

		// The one that passed is the restart, and is not counted twice.
		assertStarts(t, started, 0, 11*time.Second)

		if got := len(entriesNamed(recorder, "restart held")); got != 1 {
			t.Errorf("%d holds logged, want 1", got)
		}
	})
}

// However many edits there are, and however bad, no more than Limit candidates run
// in any Window. A change made while the limit is reached is not lost: the latest
// is tried when the window allows.
func TestAFloodOfBadEditsIsAtMostLimitCandidatesAWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		var (
			p        program
			attempts = newTimeline()
		)

		attemptsOf(&p, attempts, func(f *foundation.F, k int) { f.Fail(errors.New("boom")) })

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			// An edit every 5s for two minutes.
			for range 24 {
				f.Restart("edit")

				time.Sleep(5 * time.Second)
			}

			f.Shutdown("done")
		}), foundation.WithRestartPolicy(foundation.RestartPolicy{
			Debounce:    10 * time.Millisecond,
			MinInterval: time.Second,
			Limit:       5,
			Window:      time.Minute,
		}))

		times := attempts.got()

		if len(times) < 6 {
			t.Fatalf("only %d candidates ran: what was held should have been tried", len(times))
		}

		for i := 0; i+5 < len(times); i++ {
			if span := times[i+5] - times[i]; span < time.Minute {
				t.Errorf("candidates %d to %d began within %v, which is inside one window", i+1, i+6, span)
			}
		}

		if got := len(entriesNamed(recorder, "restart held")); got < 1 || got > 3 {
			t.Errorf("%d holds logged, want one for each, and none for each request", got)
		}
	})
}

// A candidate is a second copy of the program. One that can't be stopped is left
// running, and doing that on each attempt would add up, so the program is not
// restarted again: the first is rejected, every later request is refused, once
// with an error, and the live instance carries on.
func TestACandidateThatLeavesSomethingRunningRefusesLaterRestarts(t *testing.T) {
	leaks := map[string]func(f *foundation.F, hang chan struct{}){
		"a function that can't be stopped": func(f *foundation.F, hang chan struct{}) {
			f.Run("stuck", func(f *foundation.F) {
				f.SetTimeout(time.Second)
				f.Parallel()

				<-hang
			})
		},
		"a root function that ignores its context": func(f *foundation.F, hang chan struct{}) {
			f.SetTimeout(time.Second)

			<-hang
		},
	}

	for name, leak := range leaks {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				exit := installExit(t)

				recorder := foundationtest.NewLogRecorder()
				recorder.Install(t)

				hang := make(chan struct{})
				defer close(hang)

				var p program

				p.onCandidate = func(f *foundation.F, k int) { leak(f, hang) }

				foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
					f.Restart("one")

					// Long enough for the first to have been rejected.
					time.Sleep(30 * time.Second)

					for range 100 {
						f.Restart("again")
					}

					// And nothing that happens later changes it.
					time.Sleep(10 * time.Minute)
					f.Restart("much later")
					time.Sleep(time.Minute)

					if err := f.Context().Err(); err != nil {
						t.Errorf("the instance was stopped: %v", err)
					}

					f.Shutdown("done")
				}), quick(foundation.RestartPolicy{}))

				if got := p.candidates.Load(); got != 1 {
					t.Errorf("%d candidates ran, want 1: none after a leak", got)
				}

				if got := p.instances(); got != 1 {
					t.Errorf("%d instances ran, want 1", got)
				}

				if got := len(entriesNamed(recorder, "restart rejected")); got != 1 {
					t.Errorf("%d rejections, want 1", got)
				}

				refused := entriesNamed(recorder, "restart refused")
				if len(refused) != 1 || refused[0].level != slog.LevelError {
					t.Errorf("want one error log for the refusals, got %+v", refused)
				}

				assertExitCodes(t, exit)
			})
		})
	}
}

// A candidate that fails is not a leak: it was stopped. Only what was left
// running is, so the next edit gets its candidate.
func TestACandidateThatFailedCleanlyIsNotALeak(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		var p program

		p.onCandidate = func(f *foundation.F, k int) {
			if k < 3 {
				f.Fail(errors.New("boom"))
			}
		}

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			for range 2 {
				f.Restart("edit")

				time.Sleep(10 * time.Second)
			}

			f.Restart("fix")

			until(f)
		}), quick(foundation.RestartPolicy{}))

		if got := p.candidates.Load(); got != 3 {
			t.Errorf("%d candidates ran, want 3", got)
		}

		if got := len(entriesNamed(recorder, "restart refused")); got != 0 {
			t.Errorf("%d refusals for candidates that were stopped", got)
		}

		assertExitCodes(t, exit)
	})
}

// A real shutdown or failure that comes while a candidate is running stops it, at
// once, and Run waits for it to have stopped, which takes as long as it takes. It
// was not rejected: nobody is waiting to hear.
func TestAShutdownOrFailureDuringACandidateStopsItAndWaitsForIt(t *testing.T) {
	tests := map[string]struct {
		end  func(f *foundation.F)
		want []int
	}{
		"shutdown": {func(f *foundation.F) { f.Shutdown("sigterm") }, nil},
		"fail":     {func(f *foundation.F) { f.Fail(errors.New("boom")) }, []int{1}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				exit := installExit(t)

				recorder := foundationtest.NewLogRecorder()
				recorder.Install(t)

				t0 := time.Now()

				var p program

				p.onCandidate = func(f *foundation.F, k int) {
					p.ev.add("candidate:start")

					f.Cleanup(func() {
						// Stopping it takes two seconds.
						time.Sleep(2 * time.Second)

						p.ev.add("candidate:cleanup")
					})

					// It is the root function that never returns, until it is told to.
					<-f.Context().Done()

					p.ev.add("candidate:stopped")
				}

				foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
					go func() {
						// The candidate began at 250ms.
						time.Sleep(time.Second)

						tt.end(f)
					}()

					f.Restart("edit")

					until(f)
				}), quick(foundation.RestartPolicy{}))

				// At 1s the candidate is told to stop, and Run is not over until it is:
				// not at the 10s of the watchdog, nor before its cleanup.
				if got := time.Since(t0); got != 3*time.Second {
					t.Errorf("Run returned after %v, want 3s", got)
				}

				var seen []string

				for _, e := range p.ev.get() {
					if strings.HasPrefix(e, "candidate:") {
						seen = append(seen, e)
					}
				}

				want := []string{"candidate:start", "candidate:stopped", "candidate:cleanup"}
				if !slices.Equal(seen, want) {
					t.Errorf("the candidate went through %v, want %v", seen, want)
				}

				if got := p.instances(); got != 1 {
					t.Errorf("%d instances ran, want 1: nothing may start after a shutdown", got)
				}

				for _, msg := range []string{"restarting", "restart rejected"} {
					if got := len(entriesNamed(recorder, msg)); got != 0 {
						t.Errorf("%d %q entries after a shutdown", got, msg)
					}
				}

				assertExitCodes(t, exit, tt.want...)
			})
		})
	}
}

// slow is a candidate that takes a second to run, and passes.
func slow(p *program, attempts *timeline) {
	attemptsOf(p, attempts, func(f *foundation.F, k int) { time.Sleep(time.Second) })
}

// A request that arrives while a candidate runs makes what it checked out of date,
// so its result is set aside and the request is tried in its turn: the restart is
// after the second, not the first.
func TestARequestDuringACandidateSetsItsResultAside(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		var (
			p        program
			started  = newTimeline()
			attempts = newTimeline()
		)

		slow(&p, attempts)

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			started.mark()

			if n > 1 {
				f.Shutdown("done")

				return
			}

			f.Restart("a")

			// During the first, which runs from 250ms to 1.25s.
			time.Sleep(500 * time.Millisecond)
			f.Restart("b")

			until(f)
		}), quick(foundation.RestartPolicy{}))

		assertStarts(t, attempts, 250*time.Millisecond, 1250*time.Millisecond)
		assertStarts(t, started, 0, 2250*time.Millisecond)
	})
}

// Once. A request during the second candidate doesn't start a third: the source
// would never be done, and a restart that was never made is worse than one that
// is checked once. If what changed since is bad, the new instance fails the run.
func TestARequestDuringTheSecondCandidateDoesNotSetItAside(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		var (
			p        program
			started  = newTimeline()
			attempts = newTimeline()
		)

		slow(&p, attempts)

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			started.mark()

			if n > 1 {
				f.Shutdown("done")

				return
			}

			f.Restart("a")

			time.Sleep(500 * time.Millisecond)
			f.Restart("b")

			// During the second, which runs from 1.25s to 2.25s.
			time.Sleep(time.Second)
			f.Restart("c")

			until(f)
		}), quick(foundation.RestartPolicy{}))

		assertStarts(t, attempts, 250*time.Millisecond, 1250*time.Millisecond)
		assertStarts(t, started, 0, 2250*time.Millisecond)
	})
}

// A candidate that failed, when there has been another request, is tried again
// rather than reported: the request may have been the fix.
func TestARequestDuringARejectedCandidateIsTriedAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		var (
			p        program
			started  = newTimeline()
			attempts = newTimeline()
		)

		attemptsOf(&p, attempts, func(f *foundation.F, k int) {
			if k == 1 {
				time.Sleep(time.Second)

				f.Fail(errors.New("boom"))
			}
		})

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			started.mark()

			if n > 1 {
				f.Shutdown("done")

				return
			}

			f.Restart("bad")

			time.Sleep(500 * time.Millisecond)
			f.Restart("fixed")

			until(f)
		}), quick(foundation.RestartPolicy{}))

		assertStarts(t, attempts, 250*time.Millisecond, 1250*time.Millisecond)
		assertStarts(t, started, 0, 1250*time.Millisecond)

		assertExitCodes(t, exit)
	})
}

// A source that never goes quiet still gets its restart with a candidate in the
// way: a request in every candidate would otherwise set every result aside for
// ever. At most two candidates are made, so it begins after the cap, and a second.
func TestASourceThatNeverGoesQuietStillRestartsWithACandidate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		var (
			p        program
			started  = newTimeline()
			attempts = newTimeline()
			wg       sync.WaitGroup
		)

		attemptsOf(&p, attempts, func(f *foundation.F, k int) { time.Sleep(100 * time.Millisecond) })

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			started.mark()

			if n > 1 {
				f.Shutdown("done")

				return
			}

			// A request every 190ms, for longer than the cap and a candidate: always
			// inside the 250ms debounce of the one before.
			wg.Go(func() {
				for range 60 {
					f.Restart("busy")

					time.Sleep(190 * time.Millisecond)
				}
			})

			until(f)
		}), quick(foundation.RestartPolicy{}))

		wg.Wait()

		if got := p.candidates.Load(); got != 2 {
			t.Errorf("%d candidates ran, want 2", got)
		}

		starts := started.got()
		if len(starts) != 2 || starts[1] < 2*time.Second || starts[1] > 5*time.Second {
			t.Errorf("instances started at %v, want a restart between the cap (2s) and 5s", starts)
		}
	})
}

// A flood during a candidate is one request, however big: the result is set aside
// once and the restart is made, with no more candidates than before.
func TestAFloodDuringACandidateSetsItsResultAsideOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		var (
			p        program
			started  = newTimeline()
			attempts = newTimeline()
		)

		slow(&p, attempts)

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			started.mark()

			if n > 1 {
				f.Shutdown("done")

				return
			}

			f.Restart("a")

			time.Sleep(500 * time.Millisecond)

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
		}), quick(foundation.RestartPolicy{}))

		assertStarts(t, attempts, 250*time.Millisecond, 1250*time.Millisecond)
		assertStarts(t, started, 0, 2250*time.Millisecond)
	})
}
