package foundation_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/flags"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/foundationtest"
)

type argsConfig struct {
	Addr    string `env:"ADDR" json:"addr" flag:"addr"`
	Port    int    `env:"PORT" json:"port" flag:"port,short=p"`
	Verbose bool   `env:"VERBOSE" flag:"verbose"`
	Plain   string `env:"PLAIN"`
}

type argsWorker struct {
	Workers int `flag:"workers"`
}

type argsRun struct {
	exit []int
	rec  *foundationtest.LogRecorder
	ran  bool
}

// runArgs runs a service named svc, in an empty directory so no config.json is
// found, with the arguments given to WithArgs.
func runArgs(t *testing.T, args []string, fn foundation.Func, opts ...foundation.Option) argsRun {
	t.Helper()

	inTempDir(t)

	exit := installExit(t)

	rec := foundationtest.NewLogRecorder()
	rec.Install(t)

	var res argsRun

	foundation.Run("svc", func(f *foundation.F) {
		res.ran = true

		if fn != nil {
			fn(f)
		}
	}, append([]foundation.Option{foundation.WithArgs(args)}, opts...)...)

	res.exit = exit.Codes()
	res.rec = rec

	return res
}

// argsProblems are the problems Load logged, one entry each.
func argsProblems(r *foundationtest.LogRecorder) []logged {
	var out []logged

	for _, l := range loggedEntries(r) {
		if l.msg == "invalid configuration" && l.attrs["count"] == "" {
			out = append(out, l)
		}
	}

	return out
}

// logText is everything that was logged, messages and attributes, as one string.
func logText(r *foundationtest.LogRecorder) string {
	var b strings.Builder

	for _, l := range loggedEntries(r) {
		b.WriteString(l.msg)
		b.WriteByte(' ')

		for k, v := range l.attrs {
			b.WriteString(k + "=" + v + " ")
		}

		b.WriteByte('\n')
	}

	return b.String()
}

func TestWithArgsReadsFlagsInTheDefaultProviders(t *testing.T) {
	var cfg argsConfig

	res := runArgs(t, []string{"--addr=:9", "-p", "81", "--verbose"}, func(f *foundation.F) {
		f.Load(&cfg)
	})

	if len(res.exit) != 0 {
		t.Fatalf("exit codes %v, problems %v", res.exit, argsProblems(res.rec))
	}

	if cfg.Addr != ":9" || cfg.Port != 81 || !cfg.Verbose {
		t.Fatalf("got %+v", cfg)
	}
}

func TestAChildFunctionReadsItsOwnFlags(t *testing.T) {
	var root argsConfig

	var api, worker argsWorker

	res := runArgs(t, []string{"--addr=:1", "--api", "--workers=3", "--worker", "--workers=5"}, func(f *foundation.F) {
		f.Load(&root)

		f.Run("api", func(f *foundation.F) { f.Load(&api) })
		f.Run("worker", func(f *foundation.F) { f.Load(&worker) })
	})

	if len(res.exit) != 0 {
		t.Fatalf("exit codes %v, problems %v", res.exit, argsProblems(res.rec))
	}

	if root.Addr != ":1" || api.Workers != 3 || worker.Workers != 5 {
		t.Fatalf("root %+v, api %+v, worker %+v", root, api, worker)
	}
}

// A function with a dot in its name is two markers, as it is two words in an
// environment variable.
func TestADottedFunctionNameIsSeveralMarkers(t *testing.T) {
	var got argsWorker

	res := runArgs(t, []string{"--http", "--server", "--workers=7"}, func(f *foundation.F) {
		f.Run("http.server", func(f *foundation.F) { f.Load(&got) })
	})

	if len(res.exit) != 0 || got.Workers != 7 {
		t.Fatalf("exit %v, got %+v, problems %v", res.exit, got, argsProblems(res.rec))
	}
}

// Defaults, then a file the program named, then the environment, then flags.
func TestFlagsOverrideTheEnvironmentWhichOverridesTheFile(t *testing.T) {
	dir := inTempDir(t)

	writeConfigJSON(t, dir, `{"svc": {"addr": "file", "port": 1}}`)

	t.Setenv("SVC_ADDR", "env")

	// Each layer sets something the one above it leaves alone.
	exit := installExit(t)

	rec := foundationtest.NewLogRecorder()
	rec.Install(t)

	cfg := argsConfig{Addr: "default", Port: 0}

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&cfg)
	}, foundation.WithArgs([]string{"--verbose"}), foundation.WithDocumentConfig(json.File("config.json")))

	if len(exit.Codes()) != 0 {
		t.Fatalf("exit %v: %v", exit.Codes(), argsProblems(rec))
	}

	if cfg.Addr != "env" || cfg.Port != 1 || !cfg.Verbose {
		t.Fatalf("got %+v, want addr from the environment, port from the file, verbose from the flag", cfg)
	}

	cfg = argsConfig{}

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&cfg)
	}, foundation.WithArgs([]string{"--addr=flag", "--port=2"}), foundation.WithDocumentConfig(json.File("config.json")))

	if cfg.Addr != "flag" || cfg.Port != 2 {
		t.Fatalf("got %+v, want the flags to win over the environment and the file", cfg)
	}
}

// Arguments are only ever a choice the program made: without WithArgs nothing is
// read from os.Args, even when it holds something that looks like a flag.
func TestOsArgsAreNeverReadWithoutWithArgs(t *testing.T) {
	inTempDir(t)

	saved := os.Args

	t.Cleanup(func() { os.Args = saved })

	os.Args = []string{"svc", "--addr=from-os-args", "--nope"}

	exit := installExit(t)

	var cfg argsConfig

	foundation.Run("svc", func(f *foundation.F) { f.Load(&cfg) })

	if len(exit.Codes()) != 0 || cfg.Addr != "" {
		t.Fatalf("exit %v, got %+v", exit.Codes(), cfg)
	}
}

// WithArgs(nil) is a choice too: the program says it has no arguments.
func TestWithArgsNilDoesNotFallBackToOsArgs(t *testing.T) {
	inTempDir(t)

	saved := os.Args

	t.Cleanup(func() { os.Args = saved })

	os.Args = []string{"svc", "--nope"}

	exit := installExit(t)

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&argsConfig{})
	}, foundation.WithArgs(nil))

	if len(exit.Codes()) != 0 {
		t.Fatalf("exit %v, want os.Args to be left alone", exit.Codes())
	}
}

func TestTheArgumentsAreCopiedByWithArgs(t *testing.T) {
	inTempDir(t)

	installExit(t)

	args := []string{"--addr=first"}

	opt := foundation.WithArgs(args)

	args[0] = "--addr=second"

	var cfg argsConfig

	foundation.Run("svc", func(f *foundation.F) { f.Load(&cfg) }, opt)

	if cfg.Addr != "first" {
		t.Fatalf("Addr = %q", cfg.Addr)
	}
}

// An argument nobody asked for is a mistake, whether or not strict mode is on:
// a flag is typed by a person, and a typo must not be silently ignored.
func TestAnUnknownFlagFailsTheRun(t *testing.T) {
	res := runArgs(t, []string{"--adrr=:9"}, func(f *foundation.F) {
		f.Load(&argsConfig{})
	})

	if !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("exit codes %v, want [1]", res.exit)
	}

	problems := argsProblems(res.rec)
	if len(problems) != 1 {
		t.Fatalf("problems %v", problems)
	}

	p := problems[0]

	if p.attrs["provider"] != "flags" || p.attrs["key"] != "--adrr" || !strings.Contains(p.attrs["err"], "not recognised") {
		t.Fatalf("got %v", p.attrs)
	}
}

func TestAStrayArgumentFailsTheRunWithoutRepeatingIt(t *testing.T) {
	res := runArgs(t, []string{"serve", "--addr=:9"}, func(f *foundation.F) {
		f.Load(&argsConfig{})
	})

	if !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("exit codes %v, want [1]", res.exit)
	}

	problems := argsProblems(res.rec)
	if len(problems) != 1 || problems[0].attrs["key"] != "argument 1" {
		t.Fatalf("problems %v", problems)
	}

	if strings.Contains(logText(res.rec), "serve") {
		t.Fatal("the argument itself was logged")
	}
}

// Nothing a flag was given as a value reaches a log, not for a flag that is
// misspelt, and not for one that is wrong.
func TestNoLogLineContainsAFlagsValue(t *testing.T) {
	const secret = "hunter2-LEAK-9f3a"

	res := runArgs(t, []string{"--passwrod=" + secret, "--port=" + secret, "--oops", secret, "-q=" + secret}, func(f *foundation.F) {
		f.Load(&argsConfig{})
	})

	if !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("exit codes %v, want [1]", res.exit)
	}

	if strings.Contains(logText(res.rec), secret) {
		t.Fatalf("a value was logged:\n%s", logText(res.rec))
	}

	if len(argsProblems(res.rec)) < 4 {
		t.Fatalf("expected every problem to be reported, got %v", argsProblems(res.rec))
	}
}

// Text from the command line can't forge log lines or repaint a terminal.
func TestFlagProblemsAreEscapedInTheLog(t *testing.T) {
	res := runArgs(t, []string{"--bad\x1b[31m\nlevel=ERROR forged"}, func(f *foundation.F) {
		f.Load(&argsConfig{})
	})

	if !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("exit codes %v, want [1]", res.exit)
	}

	problems := argsProblems(res.rec)
	if len(problems) != 1 {
		t.Fatalf("problems %v", problems)
	}

	for k, v := range problems[0].attrs {
		if hasControl(v) {
			t.Errorf("%s holds a control character: %q", k, v)
		}
	}
}

// Flags are never a way to quieten the strict check: its ignore list is for keys
// in the environment and files, so an unknown flag stays an error.
func TestStrictIgnoreDoesNotHideAnUnknownFlag(t *testing.T) {
	res := runArgs(t, []string{"--nope"}, func(f *foundation.F) {
		f.Load(&argsConfig{})
	}, foundation.WithStrictConfig(), foundation.WithStrictIgnore("--nope", "--*x", "nope"))

	if !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("exit codes %v, want [1]", res.exit)
	}
}

func TestStrictModeAndFlagsWorkTogether(t *testing.T) {
	var cfg argsConfig

	res := runArgs(t, []string{"--addr=:9"}, func(f *foundation.F) {
		f.Load(&cfg)
	}, foundation.WithStrictConfig())

	if len(res.exit) != 0 || cfg.Addr != ":9" {
		t.Fatalf("exit %v, got %+v, problems %v", res.exit, cfg, argsProblems(res.rec))
	}
}

// With WithConfigProviders the program chose where configuration comes from, and flags
// are not in that list unless it says so: arguments that nothing reads fail the
// run rather than being ignored.
func TestFlagsAreNotReadWhenProvidersAreChosen(t *testing.T) {
	var cfg argsConfig

	res := runArgs(t, []string{"--addr=:9"}, func(f *foundation.F) {
		f.Load(&cfg)
	}, foundation.WithConfigProviders(envOf(nil)))

	if cfg.Addr != "" || !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("exit %v, got %+v", res.exit, cfg)
	}
}

// ... and with the flags provider named, it is the one that reads them.
func TestFlagsCanBeNamedInTheProviders(t *testing.T) {
	var cfg argsConfig

	args := []string{"--addr=:9"}

	res := runArgs(t, args, func(f *foundation.F) {
		f.Load(&cfg)
	}, foundation.WithConfigProviders(envOf(nil), flags.New(flags.WithArgs(args))))

	if len(res.exit) != 0 || cfg.Addr != ":9" {
		t.Fatalf("exit %v, got %+v, problems %v", res.exit, cfg, argsProblems(res.rec))
	}
}

func TestAMissingRequiredFieldNamesTheFlag(t *testing.T) {
	type cfg struct {
		Port int `env:"PORT" flag:"port,short=p" config:"required"`
	}

	res := runArgs(t, nil, func(f *foundation.F) {
		f.Load(&cfg{})
	})

	problems := argsProblems(res.rec)
	if len(problems) != 1 || !strings.Contains(problems[0].attrs["key"], "--port") {
		t.Fatalf("problems %v, want the key to name --port", problems)
	}
}

func TestAFlagSatisfiesARequiredField(t *testing.T) {
	type cfg struct {
		Port int `flag:"port" config:"required"`
	}

	var got cfg

	res := runArgs(t, []string{"--port=80"}, func(f *foundation.F) {
		f.Load(&got)
	})

	if len(res.exit) != 0 || got.Port != 80 {
		t.Fatalf("exit %v, got %+v", res.exit, got)
	}
}

// A field that asks for expansion gets it for whatever a provider supplied, flags
// included, from the program's lookup and nowhere else.
func TestFlagValuesAreExpandedLikeAnyProvidersWhenTheFieldAsksFor(t *testing.T) {
	type cfg struct {
		DSN string `flag:"dsn" config:"expand"`
	}

	var got cfg

	res := runArgs(t, []string{"--dsn=${HOME}"}, func(f *foundation.F) {
		f.Load(&got)
	}, lookupEnvOf(map[string]string{"HOME": "/home/x"}))

	if len(res.exit) != 0 {
		t.Fatalf("exit %v: %v", res.exit, argsProblems(res.rec))
	}

	if got.DSN != "/home/x" {
		t.Fatalf("DSN = %q: expansion reads the program's lookup, as for every provider", got.DSN)
	}
}

func TestFlagsAreNotAllowedOnAFieldReadFromAFile(t *testing.T) {
	type cfg struct {
		Password string `env:"PASSWORD,file" flag:"password"`
	}

	const secret = "hunter2-LEAK-9f3a"

	res := runArgs(t, []string{"--password=" + secret}, func(f *foundation.F) {
		f.Load(&cfg{})
	})

	if !slices.Equal(res.exit, []int{1}) || strings.Contains(logText(res.rec), secret) {
		t.Fatalf("exit %v, log:\n%s", res.exit, logText(res.rec))
	}
}

// Too many or too long arguments are refused before the program starts.
func TestTooManyArgumentsStopTheRunBeforeTheRootFunction(t *testing.T) {
	res := runArgs(t, slices.Repeat([]string{"--verbose"}, flags.MaxArgs+1), nil)

	if res.ran || !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("ran %v, exit %v", res.ran, res.exit)
	}
}

func TestAnOverlongArgumentStopsTheRunBeforeTheRootFunction(t *testing.T) {
	res := runArgs(t, []string{"--addr=" + strings.Repeat("x", flags.MaxArgLength)}, nil)

	if res.ran || !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("ran %v, exit %v", res.ran, res.exit)
	}
}

// --dry-run is a flag of the root function, defined by the framework with the
// same rules as any other.
func TestDryRunFlag(t *testing.T) {
	var seen bool

	res := runArgs(t, []string{"--dry-run"}, func(f *foundation.F) {
		seen = f.DryRun()
	})

	if !seen || len(res.exit) != 0 {
		t.Fatalf("DryRun() = %v, exit %v", seen, res.exit)
	}

	logs := loggedEntries(res.rec)

	if len(logs) == 0 || logs[0].msg != "dry run: nothing will be started" {
		t.Fatalf("expected the dry run to say so first, got %v", logs)
	}

	if len(entriesNamed(res.rec, "dry run complete")) != 1 {
		t.Fatal("expected the dry run to complete")
	}
}

func TestWithoutTheDryRunFlagThereIsNoDryRun(t *testing.T) {
	for _, args := range [][]string{nil, {"--addr=x"}, {"--dry-run=false"}} {
		var seen bool

		res := runArgs(t, args, func(f *foundation.F) {
			seen = f.DryRun()

			f.Load(&argsConfig{})
		})

		if seen || len(res.exit) != 0 {
			t.Errorf("%q: DryRun() = %v, exit %v", args, seen, res.exit)
		}
	}
}

func TestDryRunFlagAndOptionAreBothWaysToAskForOne(t *testing.T) {
	var seen bool

	runArgs(t, nil, func(f *foundation.F) { seen = f.DryRun() }, foundation.WithDryRun(true))

	if !seen {
		t.Fatal("WithDryRun(true) with no flag is still a dry run")
	}

	seen = false

	runArgs(t, []string{"--dry-run"}, func(f *foundation.F) { seen = f.DryRun() }, foundation.WithDryRun(false))

	if !seen {
		t.Fatal("--dry-run is a dry run whatever the option says")
	}
}

func TestDryRunFlagWithAMistakeInTheConfiguration(t *testing.T) {
	res := runArgs(t, []string{"--dry-run", "--port=many"}, func(f *foundation.F) {
		f.Load(&argsConfig{})
	})

	if !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("exit codes %v, want [1]", res.exit)
	}
}

// A bad value for the flag itself is a mistake before anything runs: guessing
// whether "maybe" means yes is how a service starts when it should not.
func TestABadDryRunValueStopsTheRunBeforeTheRootFunction(t *testing.T) {
	for _, arg := range []string{"--dry-run=maybe", "--dry-run="} {
		res := runArgs(t, []string{arg}, nil)

		if res.ran || !slices.Equal(res.exit, []int{1}) {
			t.Errorf("%q: ran %v, exit %v", arg, res.ran, res.exit)
		}
	}
}

// Like any root flag it comes before the markers: after one it would be a flag
// of that function.
func TestDryRunAfterAMarkerIsNotTheRootsFlag(t *testing.T) {
	var seen bool

	res := runArgs(t, []string{"--api", "--dry-run"}, func(f *foundation.F) {
		seen = f.DryRun()

		f.Run("api", func(f *foundation.F) { f.Load(&argsWorker{}) })
	})

	if seen || !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("DryRun() = %v, exit %v; want an unknown flag", seen, res.exit)
	}
}

// The framework's own flags are the root's, so a program can't also claim them:
// that would make the same words mean two things.
func TestTheRootCannotClaimTheFrameworksFlags(t *testing.T) {
	tests := []struct {
		name string
		dst  any
	}{
		{"dry-run", &struct {
			X bool `flag:"dry-run"`
		}{}},
		{"help", &struct {
			X bool `flag:"help"`
		}{}},
		{"short h", &struct {
			X bool `flag:"hush,short=h"`
		}{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runArgs(t, nil, func(f *foundation.F) {
				f.Load(tt.dst)
			})

			if !slices.Equal(res.exit, []int{1}) || len(argsProblems(res.rec)) != 1 {
				t.Fatalf("exit %v, problems %v", res.exit, argsProblems(res.rec))
			}
		})
	}
}

// A function that is not the root has its own scope, so the same words are free.
func TestAChildCanUseTheWordsTheRootHasReserved(t *testing.T) {
	type cfg struct {
		Run bool `flag:"dry-run"`
	}

	var got cfg

	res := runArgs(t, []string{"--api", "--dry-run"}, func(f *foundation.F) {
		f.Run("api", func(f *foundation.F) { f.Load(&got) })
	})

	if len(res.exit) != 0 || !got.Run {
		t.Fatalf("exit %v, got %+v, problems %v", res.exit, got, argsProblems(res.rec))
	}
}

// The framework's flags are there even when the providers are chosen, because the
// mode of the run is not configuration.
func TestDryRunFlagWorksWithChosenProviders(t *testing.T) {
	var seen bool

	res := runArgs(t, []string{"--dry-run"}, func(f *foundation.F) {
		seen = f.DryRun()
	}, foundation.WithConfigProviders(envOf(nil)))

	if !seen || len(res.exit) != 0 {
		t.Fatalf("DryRun() = %v, exit %v", seen, res.exit)
	}
}

// A dry run reports an unknown flag, as the end of startup does for a real one.
func TestDryRunReportsUnknownFlags(t *testing.T) {
	res := runArgs(t, []string{"--dry-run", "--nope"}, func(f *foundation.F) {
		f.Load(&argsConfig{})
	})

	if !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("exit codes %v, want [1]", res.exit)
	}
}
