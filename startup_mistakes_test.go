package foundation_test

import (
	"io"
	"strings"
	"testing"
	"unicode"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/foundationtest"
)

// A mistake in how the program was set up stops startup before the root function
// runs, says what it is, and says all of it: fixing one mistake must not be what
// shows the next.

// startupResult is what a run that may not have started left behind.
type startupResult struct {
	started bool
	codes   []int
	failure string
	all     []logged
}

func runStartup(t *testing.T, opts ...foundation.Option) startupResult {
	t.Helper()

	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var res startupResult

	foundation.Run("svc", func(f *foundation.F) {
		res.started = true
	}, opts...)

	res.codes = exit.Codes()
	res.all = loggedEntries(recorder)

	for _, l := range res.all {
		if l.msg == "shutting down due to failure" {
			res.failure = l.attrs["err"]
		}
	}

	return res
}

// refusesStartup checks what every startup mistake has in common: the root
// function did not run, the run failed once, and the failure is text a person can
// read, with no stack and no control characters.
func refusesStartup(t *testing.T, res startupResult, mentions ...string) {
	t.Helper()

	if res.started {
		t.Error("the root function ran")
	}

	if len(res.codes) != 1 || res.codes[0] != 1 {
		t.Errorf("exit codes = %v, want [1]", res.codes)
	}

	for _, m := range mentions {
		if !strings.Contains(res.failure, m) {
			t.Errorf("the failure %q does not say %q", res.failure, m)
		}
	}

	for _, r := range res.failure {
		if r != '\n' && unicode.IsControl(r) {
			t.Errorf("the failure contains %q", r)
		}
	}

	for _, l := range res.all {
		if _, ok := l.attrs["stack"]; ok {
			t.Errorf("%q was logged with a stack: a mistake in the options is not a crash", l.msg)
		}

		if strings.Contains(l.attrs["err"], "panic") {
			t.Errorf("%q says %q: a mistake in the options is not a panic", l.msg, l.attrs["err"])
		}
	}
}

// nilMap is a provider that is a map, so it can be a nil one.
type nilMap map[string]string

func (nilMap) Unmarshal([]string, any) error { return nil }

// nilProviders are the ways a provider can be nil without being the nil
// interface, which would panic when it was used.
func nilProviders() map[string]config.Provider {
	var (
		pointer *json.FileProvider
		fn      config.ProviderFunc
		m       nilMap
	)

	return map[string]config.Provider{
		"an untyped nil": nil,
		"a nil pointer":  pointer,
		"a nil function": fn,
		"a nil map":      m,
	}
}

func TestANilProviderInTheConfigProvidersStopsStartup(t *testing.T) {
	modes := map[string][]foundation.Option{
		"a run":     nil,
		"a dry run": {foundation.WithDryRun(true)},
		"help":      {foundation.WithArgs([]string{"--help"}), foundation.WithHelpOutput(io.Discard)},
	}

	for name, nilProvider := range nilProviders() {
		for mode, extra := range modes {
			t.Run(name+" in "+mode, func(t *testing.T) {
				opts := append([]foundation.Option{
					foundation.WithConfigProviders([]config.Provider{nilProvider}...),
				}, extra...)

				res := runStartup(t, opts...)

				refusesStartup(t, res, "invalid configuration options", "WithConfigProviders", "provider 1 is nil")
			})
		}
	}
}

func TestTheNilProviderIsNamedByPosition(t *testing.T) {
	var pointer *json.FileProvider

	vars := env.WithMap(map[string]string{})

	res := runStartup(t, foundation.WithConfigProviders(env.New(vars), pointer, env.New(vars), nil))

	refusesStartup(t, res, "provider 2 is nil", "provider 4 is nil")

	if strings.Contains(res.failure, "provider 1 ") || strings.Contains(res.failure, "provider 3 ") {
		t.Errorf("the failure %q blames a provider that is fine", res.failure)
	}
}

// Both mistakes are found in one go.
func TestANilProviderAndALayerOptionAreBothReported(t *testing.T) {
	var pointer *json.FileProvider

	res := runStartup(t,
		foundation.WithConfigProviders(pointer),
		foundation.WithDocumentConfig(),
	)

	refusesStartup(t, res, "provider 1 is nil", "no providers given", "can't be combined")
}

// What was already fine stays fine.
func TestValidConfigProvidersAreUnchanged(t *testing.T) {
	t.Run("a stack of providers", func(t *testing.T) {
		var got string

		exit := installExit(t)

		foundation.Run("svc", func(f *foundation.F) {
			var cfg struct {
				Name string `config:"name"`
			}

			f.Load(&cfg)

			got = cfg.Name
		}, foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{"SVC_NAME": "x"}))))

		if got != "x" {
			t.Errorf("Name = %q, want x", got)
		}

		assertExitCodes(t, exit)
	})

	t.Run("no providers switches loading off", func(t *testing.T) {
		res := runStartup(t, foundation.WithConfigProviders())

		if !res.started || len(res.codes) != 0 {
			t.Errorf("started = %v, exit codes = %v, want a normal run", res.started, res.codes)
		}
	})
}

// Load keeps skipping a nil provider it is given directly: that is a convenience
// for a provider that is sometimes there, not a stack the program chose.
func TestLoadStillSkipsANilProviderGivenToIt(t *testing.T) {
	for name, nilProvider := range nilProviders() {
		t.Run(name, func(t *testing.T) {
			exit := installExit(t)

			var got string

			foundation.Run("svc", func(f *foundation.F) {
				var cfg struct {
					Name string `config:"name"`
				}

				f.Load(&cfg, nilProvider, env.New(env.WithMap(map[string]string{"SVC_NAME": "x"})))

				got = cfg.Name
			})

			if got != "x" {
				t.Errorf("Name = %q, want x", got)
			}

			assertExitCodes(t, exit)
		})
	}
}

// A bad WithStrictIgnore used to replace the failure for a mistake in the other
// options, so only one was ever shown.
func TestAMistakeInTheOptionsAndABadStrictIgnoreAreBothReported(t *testing.T) {
	res := runStartup(t,
		foundation.WithDocumentConfig(),
		foundation.WithStrictIgnore(""),
	)

	refusesStartup(t, res, "invalid configuration options", "no providers given", "invalid WithStrictIgnore")
}

func TestEachStartupMistakeOnItsOwnIsReportedAsBefore(t *testing.T) {
	t.Run("an option", func(t *testing.T) {
		res := runStartup(t, foundation.WithDocumentConfig())

		refusesStartup(t, res, "invalid configuration options", "no providers given")

		if strings.Contains(res.failure, "WithStrictIgnore") {
			t.Errorf("the failure %q blames WithStrictIgnore", res.failure)
		}
	})

	t.Run("a strict ignore pattern", func(t *testing.T) {
		res := runStartup(t, foundation.WithStrictIgnore(""))

		refusesStartup(t, res, "invalid WithStrictIgnore")

		if strings.Contains(res.failure, "invalid configuration options") {
			t.Errorf("the failure %q blames the options", res.failure)
		}
	})
}

func TestEveryBadStrictIgnoreIsReportedBesideAnOptionMistake(t *testing.T) {
	many := make([]string, 65)
	for i := range many {
		many[i] = "SVC_LEGACY_" + strings.Repeat("A", i+1)
	}

	tests := map[string][]string{
		"an empty pattern":           {""},
		"only stars":                 {"***"},
		"a control character":        {"SVC_\x1b[2J_" + canaryPath},
		"invalid UTF-8":              {"SVC_\xff_" + canaryPath},
		"a pattern that is too long": {strings.Repeat("a", 257)},
		"too many patterns":          many,
	}

	for name, patterns := range tests {
		t.Run(name, func(t *testing.T) {
			res := runStartup(t,
				foundation.WithDocumentConfig(),
				foundation.WithStrictIgnore(patterns...),
			)

			refusesStartup(t, res, "invalid configuration options", "no providers given", "invalid WithStrictIgnore")

			// A pattern is part of the program, not of its configuration, so the
			// error may show it, but only escaped and cut short.
			if len(res.failure) > 600 {
				t.Errorf("a failure of %d bytes", len(res.failure))
			}

			if strings.ContainsRune(res.failure, '\x1b') || strings.ContainsRune(res.failure, 0xff) {
				t.Errorf("the failure %q has an unescaped character from the pattern", res.failure)
			}
		})
	}
}

// All three at once: a nil provider, a layer option beside it, and a bad pattern.
func TestEveryStartupMistakeIsReportedTogether(t *testing.T) {
	var pointer *json.FileProvider

	res := runStartup(t,
		foundation.WithConfigProviders(pointer),
		foundation.WithDocumentConfig(),
		foundation.WithStrictIgnore("***"),
	)

	refusesStartup(t, res,
		"provider 1 is nil", "no providers given", "can't be combined", "invalid WithStrictIgnore")

	if len(res.codes) != 1 {
		t.Errorf("the run failed %d times, want once", len(res.codes))
	}
}
