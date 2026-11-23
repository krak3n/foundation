package foundation_test

import (
	"testing"
	"testing/synctest"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/flags"
	"go.krak3n.dev/foundation/foundationtest"
)

// A provider that the program makes and hands to the framework is one object for
// the life of the program: every instance of a program that restarts uses it, and
// the trial run of a restart (the candidate) uses it beside the live instance. It
// must keep working, or a restart would be refused for a reason that is not about
// the new instance at all.

type reuseConfig struct {
	Addr string `config:"addr" flag:"addr"`
}

// providerOptions are the ways a program gives a flags provider of its own.
func providerOptions() map[string]func() []foundation.Option {
	args := []string{"--addr", ":1"}

	return map[string]func() []foundation.Option{
		"WithConfigProviders": func() []foundation.Option {
			return []foundation.Option{foundation.WithConfigProviders(
				flags.New(flags.WithArgs(args)),
				env.New(env.WithMap(map[string]string{})),
			)}
		},
		"SetCommandConfig": func() []foundation.Option {
			return []foundation.Option{
				foundation.WithArgs(args),
				foundation.SetCommandConfig(flags.New(flags.WithArgs(args))),
			}
		},
	}
}

func TestAFlagsProviderOfTheProgramsOwnSurvivesRestarts(t *testing.T) {
	for name, options := range providerOptions() {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				exit := installExit(t)

				recorder := foundationtest.NewLogRecorder()
				recorder.Install(t)

				var (
					p     program
					addrs []string
				)

				foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
					var c reuseConfig

					f.Load(&c)

					addrs = append(addrs, c.Addr)

					if n < 4 {
						f.Restart("again")

						return
					}

					f.Shutdown("done")
				}), options()...)

				settle()

				if got := p.instances(); got != 4 {
					t.Errorf("%d instances ran, want 4", got)
				}

				if got := p.candidates.Load(); got != 3 {
					t.Errorf("%d candidates ran, want 3", got)
				}

				for _, a := range addrs {
					if a != ":1" {
						t.Errorf("an instance read Addr = %q, want :1", a)
					}
				}

				for _, l := range loggedEntries(recorder) {
					switch l.msg {
					case "restart rejected", "invalid configuration":
						t.Errorf("%q was logged: %v", l.msg, l.attrs)
					}
				}

				assertExitCodes(t, exit)
			})
		})
	}
}

// The candidate is run with the same options, so it loads the root function's
// whole configuration again beside the live instance, which loads more later.
func TestACandidateAndTheLiveInstanceShareAFlagsProvider(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		var p program

		foundation.Run("svc", p.fn(func(f *foundation.F, n int) {
			f.Run("worker", func(w *foundation.F) {
				var c reuseConfig

				w.Load(&c)
			})

			var c reuseConfig

			f.Load(&c)

			if n == 1 {
				f.Restart("again")

				return
			}

			f.Shutdown("done")
		}), foundation.WithConfigProviders(flags.New(flags.WithArgs([]string{"--addr", ":1"}))))

		settle()

		if got := p.instances(); got != 2 {
			t.Errorf("%d instances ran, want 2", got)
		}

		for _, l := range loggedEntries(recorder) {
			if l.msg == "restart rejected" || l.msg == "invalid configuration" {
				t.Errorf("%q was logged: %v", l.msg, l.attrs)
			}
		}

		assertExitCodes(t, exit)
	})
}

// The same provider in two runs of Run, which is what a test does and a program
// that embeds another one may.
func TestAFlagsProviderCanBeUsedByTwoRuns(t *testing.T) {
	provider := flags.New(flags.WithArgs([]string{"--addr", ":1"}))

	for run := 1; run <= 2; run++ {
		exit := installExit(t)

		var got string

		foundation.Run("svc", func(f *foundation.F) {
			var c reuseConfig

			f.Load(&c)

			got = c.Addr
		}, foundation.WithConfigProviders(provider))

		if got != ":1" {
			t.Errorf("run %d: Addr = %q, want :1", run, got)
		}

		assertExitCodes(t, exit)
	}
}

// What is wrong stays wrong: a program that has two different structs reading one
// flag is refused at its first start, as before, and a restart does not change it.
func TestTwoDifferentStructsSharingAFlagAreStillRefused(t *testing.T) {
	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	type other struct {
		Addr string `flag:"addr"`
	}

	foundation.Run("svc", func(f *foundation.F) {
		var a reuseConfig

		f.Load(&a)

		var b other

		f.Load(&b)
	}, foundation.WithConfigProviders(flags.New(flags.WithArgs([]string{"--addr", ":1"}))))

	assertExitCodesEqual(t, exit.Codes(), 1)

	var found bool

	for _, l := range loggedEntries(recorder) {
		if l.msg == "invalid configuration" && l.attrs["key"] == "--addr" {
			found = true
		}
	}

	if !found {
		t.Error("the flag shared by two structs was not reported")
	}
}
