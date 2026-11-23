package foundation_test

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/foundationtest"
)

type strictConfig struct {
	Addr string `env:"ADDR"`
	Name string `env:"NAME"`
}

// problemsLogged returns the "invalid configuration" entries, in order.
func problemsLogged(r *foundationtest.LogRecorder) []logged {
	var out []logged

	for _, l := range loggedEntries(r) {
		if l.msg == "invalid configuration" {
			out = append(out, l)
		}
	}

	return out
}

// testChecker is a provider that can check, recording what it is asked.
type testChecker struct {
	check func(root []string) error
}

func (testChecker) Unmarshal([]string, any) error { return nil }

func (c testChecker) Check(root []string) error { return c.check(root) }

func TestStrictConfigFailsOnAVariableNothingAskedFor(t *testing.T) {
	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	vars := map[string]string{"SVC_NAME": "x", "SVC_ADRR": ":9"}

	var loaded bool

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&strictConfig{})

		loaded = true
	}, foundation.WithConfigProviders(env.New(env.WithMap(vars))), foundation.WithStrictConfig())

	if !loaded {
		t.Fatal("expected the root function to complete before the check")
	}

	assertExitCodes(t, exit, 1)

	problems := problemsLogged(recorder)

	if len(problems) != 1 {
		t.Fatalf("expected one problem, got %v", problems)
	}

	// Reported like any other configuration problem.
	for k, v := range map[string]string{"provider": "env", "key": "SVC_ADRR", "err": "not recognised", "fn": "svc"} {
		if problems[0].attrs[k] != v {
			t.Errorf("expected %s=%q, got %q", k, v, problems[0].attrs[k])
		}
	}

	if _, ok := problems[0].attrs["field"]; ok {
		t.Errorf("expected no field attribute, got %v", problems[0].attrs)
	}

	var summaries []logged

	for _, l := range loggedEntries(recorder) {
		if l.msg == "shutting down due to failure" {
			summaries = append(summaries, l)
		}
	}

	if len(summaries) != 1 || summaries[0].attrs["err"] != "invalid configuration" || summaries[0].attrs["count"] != "1" {
		t.Fatalf("expected one summary failure with count=1, got %v", summaries)
	}
}

func TestStrictConfigIsOptIn(t *testing.T) {
	exit := installExit(t)

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&strictConfig{})
	}, foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{"SVC_ADRR": ":9"}))))

	assertExitCodes(t, exit)
}

func TestStrictConfigPassesWhenEverythingWasAskedFor(t *testing.T) {
	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&strictConfig{})
	}, foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{"SVC_ADDR": ":8080", "SVC_NAME": "x"}))), foundation.WithStrictConfig())

	assertExitCodes(t, exit)

	if got := problemsLogged(recorder); len(got) != 0 {
		t.Fatalf("expected no problems, got %v", got)
	}
}

func TestStrictConfigCountsWhatTheWholeTreeAskedFor(t *testing.T) {
	vars := map[string]string{"SVC_NAME": "x", "SVC_HTTP_ADDR": ":8080"}

	t.Run("a child that loads claims its variables", func(t *testing.T) {
		exit := installExit(t)

		foundation.Run("svc", func(f *foundation.F) {
			f.Load(&strictConfig{})

			f.Run("http", func(f *foundation.F) {
				f.Load(&strictConfig{})
			})
		}, foundation.WithConfigProviders(env.New(env.WithMap(vars))), foundation.WithStrictConfig())

		assertExitCodes(t, exit)
	})

	t.Run("a child released with Parallel after loading counts too", func(t *testing.T) {
		exit := installExit(t)

		foundation.Run("svc", func(f *foundation.F) {
			f.Load(&strictConfig{})

			f.Run("http", func(f *foundation.F) {
				f.Load(&strictConfig{})

				// Components load before releasing their parent, so by the time the
				// root function returns they have all asked.
				f.Parallel()
			})
		}, foundation.WithConfigProviders(env.New(env.WithMap(vars))), foundation.WithStrictConfig())

		assertExitCodes(t, exit)
	})

	t.Run("variables of a child that never loaded are flagged", func(t *testing.T) {
		exit := installExit(t)

		foundation.Run("svc", func(f *foundation.F) {
			f.Load(&strictConfig{})

			f.Run("http", func(f *foundation.F) {})
		}, foundation.WithConfigProviders(env.New(env.WithMap(vars))), foundation.WithStrictConfig())

		assertExitCodes(t, exit, 1)
	})
}

func TestStrictConfigCheckRunsOnceAfterTheRootFunctionReturns(t *testing.T) {
	installExit(t)

	var ev events

	var roots [][]string

	checker := testChecker{check: func(root []string) error {
		ev.add("check")

		roots = append(roots, slices.Clone(root))

		return nil
	}}

	foundation.Run("svc", func(f *foundation.F) {
		f.Run("child", func(f *foundation.F) {
			ev.add("child")
		})

		ev.add("root returns")
	}, foundation.WithConfigProviders(checker, checker), foundation.WithStrictConfig())

	// Once per provider, never before the tree has been set up, and given the
	// path of the root.
	assertEvents(t, ev.get(), []string{"child", "root returns", "check", "check"})

	for _, root := range roots {
		if !slices.Equal(root, []string{"svc"}) {
			t.Fatalf("expected the root's path, got %v", root)
		}
	}
}

func TestStrictConfigSkipsProvidersThatCannotCheck(t *testing.T) {
	exit := installExit(t)

	plain := config.ProviderFunc(func([]string, any) error { return nil })

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&strictConfig{})
	}, foundation.WithConfigProviders(plain, env.New(env.WithMap(nil))), foundation.WithStrictConfig())

	assertExitCodes(t, exit)
}

func TestStrictConfigReportsEveryProvidersProblems(t *testing.T) {
	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	first := testChecker{check: func([]string) error {
		return errors.Join(
			&config.FieldError{Provider: "env", Key: "SVC_A", Err: config.ErrUnknown},
			&config.FieldError{Provider: "env", Key: "SVC_B", Err: config.ErrUnknown},
		)
	}}

	second := testChecker{check: func([]string) error { return errors.New("could not list the environment") }}

	foundation.Run("svc", func(f *foundation.F) {}, foundation.WithConfigProviders(first, second), foundation.WithStrictConfig())

	assertExitCodes(t, exit, 1)

	var got []string

	for _, p := range problemsLogged(recorder) {
		got = append(got, p.attrs["key"]+"|"+p.attrs["err"])
	}

	want := []string{"SVC_A|not recognised", "SVC_B|not recognised", "|could not list the environment"}

	if !slices.Equal(got, want) {
		t.Fatalf("unexpected problems\n got: %q\nwant: %q", got, want)
	}
}

// A run that has already failed, or is already shutting down, has said what is
// wrong. Checking the configuration then would only add noise.
func TestStrictConfigIsSkippedOnceTheRunIsFailing(t *testing.T) {
	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("svc", func(f *foundation.F) {
		f.Run("breaks", func(f *foundation.F) {
			f.Fail(errors.New("boom"))
		})
	}, foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{"SVC_ADRR": ":9"}))), foundation.WithStrictConfig())

	assertExitCodes(t, exit, 1)

	if got := problemsLogged(recorder); len(got) != 0 {
		t.Fatalf("expected the check to be skipped, got %v", got)
	}
}

func TestStrictConfigIsSkippedWhenShuttingDownIntentionally(t *testing.T) {
	exit := installExit(t)

	foundation.Run("svc", func(f *foundation.F) {
		f.Shutdown("done")
	}, foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{"SVC_ADRR": ":9"}))), foundation.WithStrictConfig())

	assertExitCodes(t, exit)
}

// With no WithConfigProviders the defaults apply, and they can check too.
func TestStrictConfigChecksTheDefaultProviders(t *testing.T) {
	inTempDir(t)

	t.Setenv("SVC_ADRR", ":9")

	exit := installExit(t)

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&strictConfig{})
	}, foundation.WithStrictConfig())

	assertExitCodes(t, exit, 1)
}

// strictBoth can be loaded from the environment and from JSON.
type strictBoth struct {
	Addr string `env:"ADDR" json:"addr"`
	Name string `env:"NAME" json:"name"`
}

// A file the program named is one of the providers, so strict mode covers it too.
func TestStrictConfigFlagsATypoInANamedConfigFile(t *testing.T) {
	dir := inTempDir(t)

	writeConfigJSON(t, dir, `{"svc": {"name": "x", "adrr": ":9"}}`)

	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&strictBoth{})
	}, foundation.WithStrictConfig(), foundation.WithDocumentConfig(json.File("config.json")))

	assertExitCodes(t, exit, 1)

	problems := problemsLogged(recorder)

	if len(problems) != 1 {
		t.Fatalf("expected one problem, got %v", problems)
	}

	attrs := problems[0].attrs

	if attrs["provider"] != "json" || attrs["key"] != "svc.adrr" {
		t.Fatalf("unexpected attributes: %v", attrs)
	}

	// The file is named, as the only way to tell several apart.
	if !strings.Contains(attrs["err"], "not recognised") || !strings.Contains(attrs["err"], "config.json") {
		t.Fatalf("expected the error to say what and where, got %q", attrs["err"])
	}
}

func TestStrictConfigPassesACleanOrAbsentConfigFile(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		dir := inTempDir(t)

		writeConfigJSON(t, dir, `{"svc": {"name": "x", "addr": ":1"}, "other": {"anything": 1}}`)

		exit := installExit(t)

		foundation.Run("svc", func(f *foundation.F) {
			f.Load(&strictBoth{})
		}, foundation.WithStrictConfig(), foundation.WithDocumentConfig(json.File("config.json")))

		assertExitCodes(t, exit)
	})

	t.Run("absent", func(t *testing.T) {
		inTempDir(t)

		exit := installExit(t)

		foundation.Run("svc", func(f *foundation.F) {
			f.Load(&strictBoth{})
		}, foundation.WithStrictConfig())

		assertExitCodes(t, exit)
	})
}

func TestStrictConfigReportsTypesInEveryProvider(t *testing.T) {
	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&strictBoth{})
	}, foundation.WithConfigProviders(
		json.Raw([]byte(`{"svc": {"adrr": 1}}`)),
		env.New(env.WithMap(map[string]string{"SVC_NAMEE": "x"})),
	), foundation.WithStrictConfig())

	assertExitCodes(t, exit, 1)

	var got []string

	for _, p := range problemsLogged(recorder) {
		got = append(got, p.attrs["provider"]+"|"+p.attrs["key"])
	}

	// In the order the providers were given.
	if want := []string{"json|svc.adrr", "env|SVC_NAMEE"}; !slices.Equal(got, want) {
		t.Fatalf("unexpected problems\n got: %q\nwant: %q", got, want)
	}
}

func TestStrictConfigCountsTheJSONKeysTheWholeTreeAskedFor(t *testing.T) {
	exit := installExit(t)

	doc := json.Raw([]byte(`{"svc": {"name": "x", "http": {"addr": ":1"}}}`))

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&strictBoth{})

		f.Run("http", func(f *foundation.F) {
			f.Load(&strictBoth{})
		})
	}, foundation.WithConfigProviders(doc), foundation.WithStrictConfig())

	assertExitCodes(t, exit)
}

// ignoreConfig can be read from the environment and from a JSON document.
type ignoreConfig struct {
	Addr string `env:"ADDR" json:"addr"`
	Name string `env:"NAME" json:"name"`
}

// strictRun runs a service named svc, loading strictConfig, with strict mode and
// the given options, on a made up environment and JSON document. It returns the
// exit codes and what was logged.
func strictRun(t *testing.T, vars map[string]string, doc string, opts ...foundation.Option) ([]int, *foundationtest.LogRecorder, bool) {
	t.Helper()

	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	providers := []config.Provider{env.New(env.WithMap(vars))}
	if doc != "" {
		providers = append([]config.Provider{json.Raw([]byte(doc))}, providers...)
	}

	var ran bool

	opts = append([]foundation.Option{foundation.WithConfigProviders(providers...)}, opts...)

	foundation.Run("svc", func(f *foundation.F) {
		ran = true

		f.Load(&ignoreConfig{})

		f.Run("http", func(f *foundation.F) {
			f.Load(&ignoreConfig{})
		})
	}, opts...)

	return exit.Codes(), recorder, ran
}

func keysOf(problems []logged) []string {
	var out []string

	for _, p := range problems {
		out = append(out, p.attrs["key"])
	}

	return out
}

func TestStrictIgnoreSkipsUnknownVariablesThatMatch(t *testing.T) {
	codes, recorder, _ := strictRun(t,
		map[string]string{"SVC_NAME": "x", "SVC_LEGACY_FLAG": "1", "SVC_LEGACY_MODE": "2"},
		"",
		foundation.WithStrictConfig(), foundation.WithStrictIgnore("SVC_LEGACY_*"),
	)

	if len(codes) != 0 {
		t.Fatalf("exit codes = %v, want none", codes)
	}

	if got := problemsLogged(recorder); len(got) != 0 {
		t.Fatalf("unexpected problems: %v", got)
	}
}

// Ignoring one thing doesn't excuse a typo next to it.
func TestStrictIgnoreStillReportsWhatDoesNotMatch(t *testing.T) {
	codes, recorder, _ := strictRun(t,
		map[string]string{"SVC_NAME": "x", "SVC_LEGACY_FLAG": "1", "SVC_ADRR": ":9"},
		"",
		foundation.WithStrictConfig(), foundation.WithStrictIgnore("SVC_LEGACY_*"),
	)

	if len(codes) != 1 || codes[0] != 1 {
		t.Fatalf("exit codes = %v, want [1]", codes)
	}

	if got := keysOf(problemsLogged(recorder)); len(got) != 1 || got[0] != "SVC_ADRR" {
		t.Fatalf("reported %v, want only SVC_ADRR", got)
	}
}

func TestStrictIgnoreSkipsCommentsInJSON(t *testing.T) {
	doc := `{"svc": {"name": "x", "_comment": "raise for batch", "http": {"addr": ":1", "_comment": "behind a proxy"}}}`

	for name, patterns := range map[string][]string{
		"an exact path":          {"svc._comment", "svc.http._comment"},
		"a comment at any depth": {"*._comment"},
	} {
		t.Run(name, func(t *testing.T) {
			codes, recorder, _ := strictRun(t, nil, doc,
				foundation.WithStrictConfig(), foundation.WithStrictIgnore(patterns...),
			)

			if len(codes) != 0 {
				t.Fatalf("exit codes = %v, want none; problems %v", codes, keysOf(problemsLogged(recorder)))
			}
		})
	}
}

func TestStrictIgnoreOfAJSONObjectCoversEverythingInside(t *testing.T) {
	doc := `{"svc": {"name": "x", "legacy": {"a": 1, "b": {"c": 2}}}}`

	codes, _, _ := strictRun(t, nil, doc, foundation.WithStrictConfig(), foundation.WithStrictIgnore("svc.legacy"))

	if len(codes) != 0 {
		t.Fatalf("exit codes = %v, want none", codes)
	}
}

// A JSON key that is misspelt is reported even though comments are ignored.
func TestStrictIgnoreOfCommentsDoesNotHideATypoInJSON(t *testing.T) {
	doc := `{"svc": {"adrr": ":9", "_comment": "x"}}`

	codes, recorder, _ := strictRun(t, nil, doc, foundation.WithStrictConfig(), foundation.WithStrictIgnore("*._comment"))

	if len(codes) != 1 {
		t.Fatalf("exit codes = %v, want [1]", codes)
	}

	if got := keysOf(problemsLogged(recorder)); len(got) != 1 || got[0] != "svc.adrr" {
		t.Fatalf("reported %v, want only svc.adrr", got)
	}
}

// A pattern is written for the key as it is reported: an environment pattern
// hides no JSON key, and the other way round.
func TestStrictIgnorePatternsDoNotCrossOverBetweenProviders(t *testing.T) {
	codes, recorder, _ := strictRun(t,
		map[string]string{"SVC_NAME": "x", "SVC_OLD": "1"},
		`{"svc": {"old": 1}}`,
		foundation.WithStrictConfig(), foundation.WithStrictIgnore("svc.old"),
	)

	if len(codes) != 1 {
		t.Fatalf("exit codes = %v, want [1]", codes)
	}

	if got := keysOf(problemsLogged(recorder)); len(got) != 1 || got[0] != "SVC_OLD" {
		t.Fatalf("reported %v, want only SVC_OLD", got)
	}
}

// Case is significant: a miss reports more, never less.
func TestStrictIgnoreIsCaseSensitive(t *testing.T) {
	codes, _, _ := strictRun(t, nil, `{"svc": {"_comment": "x"}}`,
		foundation.WithStrictConfig(), foundation.WithStrictIgnore("svc._COMMENT"),
	)

	if len(codes) != 1 {
		t.Fatalf("exit codes = %v, want [1]", codes)
	}
}

// Only "not recognised" can be ignored. A pattern that matches the key of some
// other kind of problem hides nothing.
func TestStrictIgnoreOnlyHidesUnknownKeys(t *testing.T) {
	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	checker := testChecker{check: func([]string) error {
		return errors.Join(
			&config.FieldError{Provider: "x", Key: "SVC_BOOM", Err: errors.New("boom")},
			errors.New("SVC_UNREADABLE: cannot read the file"),
			&config.FieldError{Provider: "x", Key: "SVC_ODD", Err: config.ErrUnknown},
		)
	}}

	foundation.Run("svc", func(f *foundation.F) {},
		foundation.WithConfigProviders(checker),
		foundation.WithStrictConfig(),
		foundation.WithStrictIgnore("SVC_*"),
	)

	if len(exit.Codes()) != 1 {
		t.Fatalf("exit codes = %v, want [1]", exit.Codes())
	}

	problems := problemsLogged(recorder)

	if len(problems) != 2 {
		t.Fatalf("expected the two problems that aren't unknown keys, got %v", problems)
	}

	for _, p := range problems {
		if p.attrs["key"] == "SVC_ODD" {
			t.Fatalf("an unknown key that matches should be ignored, got %v", p)
		}
	}
}

// The option does nothing without strict mode, which is what it is for.
func TestStrictIgnoreWithoutStrictConfigDoesNothing(t *testing.T) {
	codes, recorder, ran := strictRun(t,
		map[string]string{"SVC_ADRR": ":9"}, "",
		foundation.WithStrictIgnore("SVC_*_X"),
	)

	if !ran || len(codes) != 0 {
		t.Fatalf("ran = %v, exit codes = %v, want a normal run", ran, codes)
	}

	for _, l := range loggedEntries(recorder) {
		if strings.Contains(l.msg, "ignored") {
			t.Fatalf("nothing should be reported about ignoring: %v", l)
		}
	}
}

func TestStrictIgnoreWithNoPatternsChangesNothing(t *testing.T) {
	codes, _, _ := strictRun(t, map[string]string{"SVC_ADRR": ":9"}, "",
		foundation.WithStrictConfig(), foundation.WithStrictIgnore(),
	)

	if len(codes) != 1 {
		t.Fatalf("exit codes = %v, want [1]", codes)
	}
}

// A pattern that is wrong stops startup, before anything runs, whether or not
// strict mode is on: it never quietly ignores nothing, or too much.
func TestStrictIgnoreRejectsBadPatternsAtStartup(t *testing.T) {
	for name, patterns := range map[string][]string{
		"empty":           {""},
		"a bare star":     {"*"},
		"only stars":      {"***"},
		"a newline":       {"SVC_\nX"},
		"an escape":       {"SVC_\x1b[2J"},
		"too long":        {strings.Repeat("a", 300)},
		"too many":        manyIgnorePatterns(100),
		"one bad of many": {"SVC_A_*", "*"},
	} {
		for _, strict := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s strict=%v", name, strict), func(t *testing.T) {
				opts := []foundation.Option{foundation.WithStrictIgnore(patterns...)}
				if strict {
					opts = append(opts, foundation.WithStrictConfig())
				}

				codes, recorder, ran := strictRun(t, map[string]string{"SVC_NAME": "x"}, "", opts...)

				if ran {
					t.Fatal("the service must not start with a bad ignore pattern")
				}

				if len(codes) != 1 || codes[0] != 1 {
					t.Fatalf("exit codes = %v, want [1]", codes)
				}

				var failure *logged

				for _, l := range loggedEntries(recorder) {
					l := l

					if l.level >= slog.LevelError {
						failure = &l
					}
				}

				if failure == nil {
					t.Fatal("expected the failure to be logged")
				}

				for k, v := range failure.attrs {
					if strings.ContainsFunc(v, func(r rune) bool { return r < ' ' || r == 0x7f }) {
						t.Errorf("%s contains a control character: %q", k, v)
					}
				}
			})
		}
	}
}

func manyIgnorePatterns(n int) []string {
	out := make([]string, n)

	for i := range out {
		out[i] = fmt.Sprintf("SVC_GROUP_%d_*", i)
	}

	return out
}

// What was ignored stays visible: one line says which patterns are active and
// how many keys they hid, and each key is listed at debug level.
func TestStrictIgnoreIsVisibleInTheLog(t *testing.T) {
	_, recorder, _ := strictRun(t,
		map[string]string{"SVC_NAME": "x", "SVC_LEGACY_A": "1", "SVC_LEGACY_B": "2"},
		`{"svc": {"_comment": "x"}}`,
		foundation.WithStrictConfig(), foundation.WithStrictIgnore("SVC_LEGACY_*", "*._comment"),
	)

	var summary []logged

	var listed []string

	for _, l := range loggedEntries(recorder) {
		switch l.msg {
		case "ignored unknown configuration":
			summary = append(summary, l)
		case "ignored unknown key":
			listed = append(listed, l.attrs["key"])
		}
	}

	if len(summary) != 1 {
		t.Fatalf("expected one summary line, got %v", summary)
	}

	if summary[0].level.String() != "INFO" || summary[0].attrs["count"] != "3" || summary[0].attrs["patterns"] != "SVC_LEGACY_*, *._comment" {
		t.Fatalf("unexpected summary: %+v", summary[0])
	}

	if want := "SVC_LEGACY_A SVC_LEGACY_B svc._comment"; !sameSet(listed, want) {
		t.Fatalf("listed %v, want %q", listed, want)
	}
}

// With patterns in force the line is there even when they hid nothing, so a
// reader can see the check is running with exceptions.
func TestStrictIgnoreSummaryAppearsEvenWhenNothingWasIgnored(t *testing.T) {
	_, recorder, _ := strictRun(t, map[string]string{"SVC_NAME": "x"}, "",
		foundation.WithStrictConfig(), foundation.WithStrictIgnore("SVC_LEGACY_*"),
	)

	for _, l := range loggedEntries(recorder) {
		if l.msg == "ignored unknown configuration" {
			if l.attrs["count"] != "0" {
				t.Fatalf("count = %q, want 0", l.attrs["count"])
			}

			return
		}
	}

	t.Fatal("expected the summary line")
}

// The names of what was ignored come from whoever controls the environment or
// the file, so they are escaped like every other name that is logged.
func TestStrictIgnoreEscapesTheKeysItLogs(t *testing.T) {
	_, recorder, _ := strictRun(t,
		map[string]string{"SVC_NAME": "x", "SVC_LEGACY_\nlevel=ERROR forged\x1b[2J": "1"},
		"",
		foundation.WithStrictConfig(), foundation.WithStrictIgnore("SVC_LEGACY_*"),
	)

	var found bool

	for _, l := range loggedEntries(recorder) {
		for k, v := range l.attrs {
			if strings.ContainsFunc(v, func(r rune) bool { return r < ' ' || r == 0x7f }) {
				t.Errorf("%s contains a control character: %q", k, v)
			}
		}

		if l.msg == "ignored unknown key" {
			found = true
		}
	}

	if !found {
		t.Fatal("expected the ignored key to be logged")
	}
}

// A very large environment with the most patterns allowed is checked quickly,
// and the one key that doesn't match is still found.
func TestStrictIgnoreIsFastOnALargeEnvironment(t *testing.T) {
	vars := map[string]string{"SVC_NAME": "x", "SVC_TYPO": "1"}

	for i := range 5_000 {
		vars[fmt.Sprintf("SVC_GROUP_%d_VALUE_%d", i%64, i)] = "1"
	}

	start := time.Now()

	codes, recorder, _ := strictRun(t, vars, "",
		foundation.WithStrictConfig(), foundation.WithStrictIgnore(manyIgnorePatterns(64)...),
	)

	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("the check took %v", elapsed)
	}

	if len(codes) != 1 {
		t.Fatalf("exit codes = %v, want [1]", codes)
	}

	if got := keysOf(problemsLogged(recorder)); len(got) != 1 || got[0] != "SVC_TYPO" {
		t.Fatalf("reported %v, want only SVC_TYPO", got)
	}
}

func sameSet(got []string, want string) bool {
	w := strings.Fields(want)

	if len(got) != len(w) {
		return false
	}

	seen := map[string]bool{}

	for _, g := range got {
		seen[g] = true
	}

	for _, k := range w {
		if !seen[k] {
			return false
		}
	}

	return true
}
