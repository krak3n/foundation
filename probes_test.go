package foundation_test

import (
	"errors"
	"iter"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/foundationtest"
	"go.krak3n.dev/foundation/health/monitor"
)

// The probes of an instance are the instance's. When it has stopped completely, and
// before the next one starts, they are taken out of its registry and the instance is
// sealed: what an abandoned function adds afterwards is ignored and what it asks
// for is nothing, so it can't reach anything of the instances that follow.
//
// It is at the end, not when the function that added a probe stops: a service that
// has stopped serving but is still shutting down should go on failing its probe
// until it is gone, as a probe that is missing passes. The registry is always the
// instance's own (there is no way to give the framework one), and a program that
// restarts registers its probes from the root function, which runs again.

func dbProbe() monitor.Probe {
	return monitor.NewProbe("db", monitor.LivenessMode, alwaysHealthy{})
}

func namesOfProbes(seq iter.Seq[monitor.Probe]) []string {
	var names []string

	for p := range seq {
		names = append(names, p.Name())
	}

	return names
}

// The probes are gone when the instance ends, and the next instance registers the
// same name again without a clash.
func TestTheProbesOfAnInstanceAreGoneWhenItEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		var (
			p     program
			first *foundation.F
		)

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			f.Probes().Add(dbProbe())

			if n > 1 {
				f.Shutdown("done")

				return
			}

			first = f

			if got := namesOfProbes(f.Probes().Seq()); !slices.Equal(got, []string{"db"}) {
				t.Errorf("the probe is not registered while the instance runs: %v", got)
			}

			f.Restart("now")

			until(f)
		}), quick(foundation.RestartPolicy{}))

		assertExitCodes(t, exit)

		if got := p.instances(); got != 2 {
			t.Errorf("%d instances ran, want 2", got)
		}

		if got := namesOfProbes(first.Probes().Seq()); len(got) != 0 {
			t.Errorf("the instance has ended and still has probes: %v", got)
		}
	})
}

// They are taken out when every function has stopped, not when the one that added
// the probe has: until then the probe is still there to say the service is not well.
func TestTheProbesAreTakenOutAfterTheLastFunctionStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		var (
			mu     sync.Mutex
			events []string
			root   *foundation.F
		)

		note := func(s string) {
			mu.Lock()
			defer mu.Unlock()

			events = append(events, s)
		}

		foundation.Run("svc", func(f *foundation.F) {
			root = f

			f.Run("server", func(f *foundation.F) {
				f.Probes().Add(dbProbe())
				f.Parallel()

				<-f.Context().Done()

				// Slow to stop, and the probe is still registered.
				time.Sleep(5 * time.Second)

				if got := namesOfProbes(f.Probes().Seq()); !slices.Equal(got, []string{"db"}) {
					t.Errorf("the probe was removed when its function was stopped: %v", got)
				}

				note("server stopped")
			})

			f.Cleanup(func() {
				if got := namesOfProbes(f.Probes().Seq()); !slices.Equal(got, []string{"db"}) {
					t.Errorf("the probe was removed before the cleanups ran: %v", got)
				}

				note("cleanup")
			})

			f.Shutdown("done")
		})

		if got := namesOfProbes(root.Probes().Seq()); len(got) != 0 {
			t.Errorf("the probe is there after the instance has ended: %v", got)
		}

		mu.Lock()
		defer mu.Unlock()

		if want := []string{"server stopped", "cleanup"}; !slices.Equal(events, want) {
			t.Errorf("events = %v, want %v", events, want)
		}
	})
}

// However the instance ends, its probes are gone.
func TestTheProbesAreGoneHoweverTheInstanceEnds(t *testing.T) {
	tests := map[string]struct {
		end   func(f *foundation.F)
		codes []int
	}{
		"a shutdown": {func(f *foundation.F) { f.Shutdown("done") }, nil},
		"a failure":  {func(f *foundation.F) { f.Fail(errors.New("boom")) }, []int{1}},
		"a panic":    {func(f *foundation.F) { panic("boom") }, []int{1}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				exit := installExit(t)

				var root *foundation.F

				foundation.Run("svc", func(f *foundation.F) {
					root = f

					f.Probes().Add(dbProbe())

					tt.end(f)
				})

				assertExitCodes(t, exit, tt.codes...)

				if got := namesOfProbes(root.Probes().Seq()); len(got) != 0 {
					t.Errorf("the instance has ended and still has probes: %v", got)
				}
			})
		})
	}
}

// A candidate starts nothing and has a registry of its own: the same name as one of
// the live instance's is not a clash, and the live instance's probes are left alone.
func TestACandidateDoesNotClashWithTheLiveInstancesProbes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		var (
			p    program
			live atomic.Pointer[foundation.F]
		)

		p.onCandidate = func(f *foundation.F, k int) {
			f.Probes().Add(dbProbe())

			if got := namesOfProbes(live.Load().Probes().Seq()); !slices.Equal(got, []string{"db"}) {
				t.Errorf("the live instance's probes are %v while the candidate runs", got)
			}
		}

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			f.Probes().Add(dbProbe())

			if n > 1 {
				f.Shutdown("done")

				return
			}

			live.Store(f)

			f.Restart("now")

			until(f)
		}), quick(foundation.RestartPolicy{}))

		assertExitCodes(t, exit)

		if got := p.candidates.Load(); got != 1 {
			t.Fatalf("%d candidates ran, want 1", got)
		}

		if got := entriesNamed(recorder, "restart rejected"); len(got) != 0 {
			t.Errorf("the candidate was rejected: %+v", got)
		}
	})
}

// A function that was abandoned, because it didn't stop in time, may run again after
// the instance is over. What it registers then is nobody's: it is not an error (the
// program has not failed, and the abandoned function is not the one to be told), and
// it sees no probes.
func TestAProbeAddedAfterTheInstanceEndedIsIgnored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		var (
			late atomic.Pointer[foundation.F]
			stop = make(chan struct{})
		)

		foundation.Run("svc", func(f *foundation.F) {
			f.Run("stuck", func(f *foundation.F) {
				f.SetTimeout(time.Second)
				f.Parallel()

				late.Store(f)

				<-stop
			})

			f.Shutdown("done")
		})

		done := make(chan struct{})

		go func() {
			defer close(done)

			late.Load().Probes().Add(dbProbe())

			if got := namesOfProbes(late.Load().Probes().Seq()); len(got) != 0 {
				t.Errorf("an ended instance shows probes: %v", got)
			}
		}()

		close(stop)
		<-done

		synctest.Wait()

		assertExitCodes(t, exit)
	})
}

// A probe that can't be added (the name is taken) fails the function that tried
// to add it, so the log names the function to look at, not the root.
func TestADuplicateProbeFailsTheFunctionThatAddedIt(t *testing.T) {
	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("svc", func(f *foundation.F) {
		f.Probes().Add(dbProbe())

		f.Run("child", func(f *foundation.F) {
			f.Probes().Add(dbProbe())
		})
	})

	if got := exit.Codes(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("exit codes = %v, want [1]", got)
	}

	failures := entriesNamed(recorder, "shutting down due to failure")
	if len(failures) != 1 {
		t.Fatalf("expected one failure logged, got %d", len(failures))
	}

	if got := failures[0].attrs["fn"]; got != "svc/child" {
		t.Fatalf("the failure was attributed to %q, want svc/child", got)
	}
}

// The registry is still the instance's, whoever asks: a probe added by a child
// is seen by the root.
func TestProbesAddedByAChildAreTheInstances(t *testing.T) {
	installExit(t)

	var names []string

	foundation.Run("svc", func(f *foundation.F) {
		f.Run("child", func(f *foundation.F) {
			f.Probes().Add(dbProbe())
		})

		names = namesOfProbes(f.Probes().Seq())
	})

	if !slices.Equal(names, []string{"db"}) {
		t.Fatalf("the root sees probes %v, want [db]", names)
	}
}
