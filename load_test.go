package foundation_test

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/flags"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/foundationtest"
)

type loadConfig struct {
	Addr string `env:"ADDR" json:"addr"`
	Name string `env:"NAME" json:"name"`
}

// inTempDir runs the test in an empty directory, so the default config.json
// lookup can't find a file from the repository.
func inTempDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	t.Chdir(dir)

	return dir
}

func writeConfigJSON(t *testing.T, dir, data string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadUsesDefaultProviders(t *testing.T) {
	dir := inTempDir(t)

	// The defaults are the environment, and flags with WithArgs. A file in the
	// working directory is not one of them.
	writeConfigJSON(t, dir, `{"test": {"addr": ":json", "name": "from-json"}}`)

	t.Setenv("TEST_ADDR", ":env")

	var cfg loadConfig

	foundation.Run("test", func(f *foundation.F) {
		f.Load(&cfg)
	})

	if cfg.Addr != ":env" || cfg.Name != "" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadKeepsDefaultsWhenNothingIsFound(t *testing.T) {
	inTempDir(t)

	cfg := loadConfig{Addr: ":default"}

	foundation.Run("test", func(f *foundation.F) {
		f.Load(&cfg)
	})

	if cfg.Addr != ":default" {
		t.Fatalf("expected the default to survive, got %+v", cfg)
	}
}

func TestLoadUsesThePathOfTheCallingFunction(t *testing.T) {
	dir := inTempDir(t)

	writeConfigJSON(t, dir, `{"svc": {"http": {"addr": ":json"}}}`)

	t.Setenv("SVC_HTTP_NAME", "from-env")

	var cfg loadConfig

	foundation.Run("svc", func(f *foundation.F) {
		f.Run("http", func(f *foundation.F) {
			f.Load(&cfg)
		})
	}, foundation.WithDocumentConfig(json.File("config.json")))

	if cfg.Addr != ":json" || cfg.Name != "from-env" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadSharesProvidersAcrossTheTree(t *testing.T) {
	inTempDir(t)

	var cfg loadConfig

	foundation.Run("test", func(f *foundation.F) {
		f.Run("a", func(f *foundation.F) {
			f.Run("b", func(f *foundation.F) {
				f.Load(&cfg)
			})
		})
	}, foundation.WithConfigProviders(json.Raw([]byte(`{"test": {"a": {"b": {"name": "deep"}}}}`))))

	if cfg.Name != "deep" {
		t.Fatalf("expected providers set on Run to reach a nested function, got %+v", cfg)
	}
}

func TestWithConfigProvidersReplacesTheDefaults(t *testing.T) {
	dir := inTempDir(t)

	writeConfigJSON(t, dir, `{"test": {"name": "from-file"}}`)

	t.Setenv("TEST_ADDR", ":env")

	var cfg loadConfig

	foundation.Run("test", func(f *foundation.F) {
		f.Load(&cfg)
	}, foundation.WithConfigProviders(env.New()))

	// Env only: nothing else is read.
	if cfg.Addr != ":env" || cfg.Name != "" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestWithConfigProvidersWithNoProvidersLoadsNothing(t *testing.T) {
	dir := inTempDir(t)

	writeConfigJSON(t, dir, `{"test": {"name": "from-file"}}`)

	t.Setenv("TEST_ADDR", ":env")

	cfg := loadConfig{Name: "default"}

	foundation.Run("test", func(f *foundation.F) {
		f.Load(&cfg)
	}, foundation.WithConfigProviders())

	if cfg.Addr != "" || cfg.Name != "default" {
		t.Fatalf("expected config loading to be switched off, got %+v", cfg)
	}
}

func TestLoadExplicitProvidersBypassTheTreesProviders(t *testing.T) {
	inTempDir(t)

	t.Setenv("TEST_NAME", "from-env")

	var cfg loadConfig

	foundation.Run("test", func(f *foundation.F) {
		f.Load(&cfg, env.New())
	}, foundation.WithConfigProviders(json.Raw([]byte(`{"test": {"addr": ":json", "name": "from-json"}}`))))

	if cfg.Addr != "" || cfg.Name != "from-env" {
		t.Fatalf("expected only the explicit provider to be used, got %+v", cfg)
	}
}

func TestLoadAppliesProvidersInOrder(t *testing.T) {
	setName := func(name string) config.Provider {
		return config.ProviderFunc(func(_ []string, dst any) error {
			dst.(*loadConfig).Name = name

			return nil
		})
	}

	var cfg loadConfig

	foundation.Run("test", func(f *foundation.F) {
		f.Load(&cfg, setName("first"), setName("second"), setName("third"))
	})

	if cfg.Name != "third" {
		t.Fatalf("expected the last provider to win, got %q", cfg.Name)
	}
}

func TestLoadPassesThePathToProviders(t *testing.T) {
	var got []string

	record := config.ProviderFunc(func(path []string, _ any) error {
		got = slices.Clone(path)

		return nil
	})

	foundation.Run("svc", func(f *foundation.F) {
		f.Run("http", func(f *foundation.F) {
			f.Load(&loadConfig{}, record)
		})
	})

	if want := []string{"svc", "http"}; !slices.Equal(got, want) {
		t.Fatalf("expected path %v, got %v", want, got)
	}
}

func TestLoadSkipsNilProviders(t *testing.T) {
	cfg := loadConfig{}

	foundation.Run("test", func(f *foundation.F) {
		f.Load(&cfg, nil, config.ProviderFunc(func(_ []string, dst any) error {
			dst.(*loadConfig).Name = "set"

			return nil
		}), nil)
	})

	if cfg.Name != "set" {
		t.Fatalf("expected the non-nil provider to run, got %+v", cfg)
	}
}

func TestLoadErrorFailsTheFunction(t *testing.T) {
	exit := installExit(t)

	failing := config.ProviderFunc(func([]string, any) error {
		return errors.New("bad config")
	})

	var afterLoad, laterRan bool

	foundation.Run("test", func(f *foundation.F) {
		f.Run("loads", func(f *foundation.F) {
			f.Load(&loadConfig{}, failing)

			afterLoad = true // Load fails the function, so this must not run.
		})

		f.Run("later", func(f *foundation.F) {
			laterRan = true
		})
	})

	if afterLoad || laterRan {
		t.Fatalf("expected a provider error to stop the function and the run (afterLoad=%v, later=%v)", afterLoad, laterRan)
	}

	assertExitCodes(t, exit, 1)
}

// Providers are chosen per run, and the env provider can read from anywhere, so
// tests of code that uses f.Load need neither t.Setenv nor a shared process
// environment, and can run in parallel.
func TestLoadWithAnInjectedEnvironment(t *testing.T) {
	for _, name := range []string{"a", "b", "c", "d"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var cfg loadConfig

			lookup := func(key string) (string, bool) {
				if key == "SVC_NAME" {
					return name, true
				}

				return "", false
			}

			foundation.Run("svc", func(f *foundation.F) {
				f.Load(&cfg)
			}, foundation.WithConfigProviders(env.New(env.WithLookup(lookup))))

			if cfg.Name != name {
				t.Fatalf("expected %q, got %q", name, cfg.Name)
			}
		})
	}
}

// A missing required field says to set either name.
func TestRequiredFieldWithAnAliasNamesBothVariables(t *testing.T) {
	type cfg struct {
		Port int `env:"PORT,alias=PORT" json:"port" config:"required"`
	}

	res := loadWith(t, &cfg{}, []config.Provider{envOf(nil)})

	if res.finished || len(res.problems) != 1 {
		t.Fatalf("expected one problem, got %+v", res.problems)
	}

	if got := res.problems[0].attrs["key"]; got != "SVC_PORT, PORT" {
		t.Fatalf("key = %q, want both names", got)
	}
}

func TestRequiredFieldIsSatisfiedByTheAlias(t *testing.T) {
	type cfg struct {
		Port int `env:"PORT,alias=PORT" config:"required"`
	}

	var c cfg

	res := loadWith(t, &c, []config.Provider{envOf(map[string]string{"PORT": "8080"})})

	if !res.finished || c.Port != 8080 {
		t.Fatalf("got %+v (finished %v), want the alias to satisfy the field", c, res.finished)
	}
}

// The alias is an environment feature: the JSON provider reads its own tag, and
// layering still applies, so a later provider overrides an earlier one.
func TestAnAliasLayersLikeAnyVariable(t *testing.T) {
	type cfg struct {
		Port int `env:"PORT,alias=PORT" json:"port"`
	}

	var c cfg

	res := loadWith(t, &c, []config.Provider{
		json.Raw([]byte(`{"svc": {"port": 1111}}`)),
		envOf(map[string]string{"PORT": "2222"}),
	})

	if !res.finished || c.Port != 2222 {
		t.Fatalf("got %+v, want the environment alias to override the file", c)
	}

	c = cfg{}

	res = loadWith(t, &c, []config.Provider{
		json.Raw([]byte(`{"svc": {"port": 1111}}`)),
		envOf(map[string]string{"SVC_PORT": "3333", "PORT": "2222"}),
	})

	if !res.finished || c.Port != 3333 {
		t.Fatalf("got %+v, want the prefixed variable over the alias", c)
	}
}

// A bad value from the alias fails the load like any other, and says which
// variable, never the value.
func TestABadAliasValueFailsTheLoadWithoutTheValue(t *testing.T) {
	type cfg struct {
		Port int `env:"PORT,alias=PORT"`
	}

	res := loadWith(t, &cfg{}, []config.Provider{envOf(map[string]string{"PORT": "hunter2-SECRET"})})

	if res.finished || len(res.problems) != 1 {
		t.Fatalf("expected the load to fail with one problem, got %+v", res.problems)
	}

	if res.problems[0].attrs["key"] != "PORT" || res.problems[0].attrs["field"] != "Port" {
		t.Fatalf("unexpected problem: %+v", res.problems[0])
	}

	for k, v := range res.problems[0].attrs {
		if strings.Contains(v, "hunter2") || strings.Contains(v, "SECRET") {
			t.Fatalf("%s contains the value: %q", k, v)
		}
	}
}

// A field named once, with `config:"name"`, is read by the environment and by
// files, and the rest of Load (required, expand, strict mode, help) works with the
// name it is actually read by.

func TestLoadReadsACommonName(t *testing.T) {
	type cfg struct {
		Addr string `config:"addr"`
		Port int    `config:"port" env:"PORT_NUMBER" json:"portNo"`
	}

	tests := []struct {
		name      string
		providers []config.Provider
	}{
		{"the environment", []config.Provider{envOf(map[string]string{"SVC_ADDR": ":1", "SVC_PORT_NUMBER": "9"})}},
		{"a document", []config.Provider{json.Raw([]byte(`{"svc": {"addr": ":1", "portNo": 9}}`))}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got cfg

			res := loadWith(t, &got, tt.providers)

			if !res.finished || len(res.exit) != 0 || got.Addr != ":1" || got.Port != 9 {
				t.Fatalf("got %+v, %+v", got, res)
			}
		})
	}
}

func TestLoadRequiredWorksWithACommonName(t *testing.T) {
	inTempDir(t)

	type cfg struct {
		Addr string `config:"addr,required"`
		Port int    `config:"port,required" env:"PORT_NUMBER" json:"portNo"`
	}

	res := loadWith(t, &cfg{}, nil, foundation.WithDocumentConfig(json.File("config.json")))

	if res.finished || !slices.Equal(fieldsOf(res.problems), []string{"Addr", "Port"}) {
		t.Fatalf("unexpected result: %+v", res)
	}

	// The keys are the ones each provider would read: the file first, then the
	// environment, as in the layers.
	for i, want := range []string{"svc.addr, SVC_ADDR", "svc.portNo, SVC_PORT_NUMBER"} {
		if got := res.problems[i].attrs["key"]; got != want {
			t.Errorf("problem %d: key = %q, want %q", i, got, want)
		}
	}

	// And it is satisfied by either.
	for _, p := range []config.Provider{
		envOf(map[string]string{"SVC_ADDR": ":1", "SVC_PORT_NUMBER": "9"}),
		json.Raw([]byte(`{"svc": {"addr": ":1", "portNo": 9}}`)),
	} {
		res := loadWith(t, &cfg{}, []config.Provider{p})

		if !res.finished || len(res.problems) != 0 {
			t.Fatalf("expected a clean load, got %+v", res)
		}
	}
}

// The options on their own, as they were written before names, still mean what
// they did: a field with an env tag and `config:"required"` is read by that name.
func TestLoadOptionsWithoutANameKeepTheirMeaning(t *testing.T) {
	type onlyRequired struct {
		Addr string `env:"ADDR" config:"required"`
	}

	type leadingComma struct {
		Addr string `env:"ADDR" config:",required"`
	}

	type both struct {
		Addr string `env:"ADDR" config:"required,expand"`
	}

	type bothReversed struct {
		Addr string `env:"ADDR" config:"expand,required"`
	}

	for _, dst := range []any{&onlyRequired{}, &leadingComma{}, &both{}, &bothReversed{}} {
		res := loadWith(t, dst, []config.Provider{envOf(nil)})

		if res.finished || !slices.Equal(fieldsOf(res.problems), []string{"Addr"}) {
			t.Errorf("%T: a missing required field was not reported: %+v", dst, res)
		}

		res = loadWith(t, dst, []config.Provider{envOf(map[string]string{"SVC_ADDR": ":1"})})

		if !res.finished || len(res.problems) != 0 {
			t.Errorf("%T: a field that is set was reported: %+v", dst, res)
		}
	}
}

func TestLoadExpandsAFieldWithACommonName(t *testing.T) {
	type cfg struct {
		URL string `config:"url,expand"`
	}

	exit := installExit(t)

	var got cfg

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&got)
	},
		foundation.WithConfigProviders(envOf(map[string]string{"SVC_URL": "https://${HOST}:8443"})),
		foundation.WithLookupEnv(func(name string) (string, bool) {
			if name == "HOST" {
				return "example.com", true
			}

			return "", false
		}),
	)

	assertExitCodes(t, exit)

	if got.URL != "https://example.com:8443" {
		t.Fatalf("URL = %q", got.URL)
	}
}

// "requried" read as a name would quietly make a field optional, which is the
// kind of mistake that has to stop startup.
func TestLoadRefusesAMisspeltOption(t *testing.T) {
	type typo struct {
		Addr string `config:"requried"`
	}

	type capital struct {
		Addr string `config:"Required"`
	}

	type expand struct {
		Addr string `config:"expnad"`
	}

	type afterAName struct {
		Addr string `config:"addr,requried"`
	}

	for _, dst := range []any{&typo{}, &capital{}, &expand{}, &afterAName{}} {
		res := loadWith(t, dst, []config.Provider{envOf(map[string]string{"SVC_ADDR": ":1"})})

		if res.finished || len(res.exit) != 1 || len(res.problems) == 0 {
			t.Errorf("%T: expected the load to fail, got %+v", dst, res)

			continue
		}

		for _, p := range res.problems {
			if p.attrs["field"] != "Addr" {
				t.Errorf("%T: unexpected problem %+v", dst, p)
			}
		}
	}
}

// Options are for values, as before: a group is a name and nothing else.
func TestLoadRefusesOptionsOnAGroup(t *testing.T) {
	type tls struct {
		CertFile string `config:"cert_file"`
	}

	type cfg struct {
		TLS tls `config:"tls,required"`
	}

	res := loadWith(t, &cfg{}, []config.Provider{envOf(nil)})

	if res.finished || len(res.problems) == 0 || res.problems[0].attrs["field"] != "TLS" {
		t.Fatalf("expected a problem with TLS, got %+v", res)
	}
}

// Strict mode knows which names were read, so a variable nothing asked for is
// still found, and the name a field is read by is not mistaken for a typo.
func TestStrictConfigKnowsTheEffectiveNames(t *testing.T) {
	type cfg struct {
		Addr string `config:"addr"`
		Port int    `config:"port" env:"PORT_NUMBER"`
	}

	run := func(t *testing.T, vars map[string]string) []int {
		t.Helper()

		exit := installExit(t)

		foundation.Run("svc", func(f *foundation.F) {
			f.Load(&cfg{})
		}, foundation.WithConfigProviders(envOf(vars)), foundation.WithStrictConfig())

		return exit.Codes()
	}

	t.Run("names that are read", func(t *testing.T) {
		if codes := run(t, map[string]string{"SVC_ADDR": ":1", "SVC_PORT_NUMBER": "9"}); len(codes) != 0 {
			t.Fatalf("the names the fields are read by were reported: exit codes %v", codes)
		}
	})

	t.Run("the name an env tag replaced", func(t *testing.T) {
		// SVC_PORT is the common name, which the env tag replaced, so it is a typo.
		if codes := run(t, map[string]string{"SVC_ADDR": ":1", "SVC_PORT": "9"}); !slices.Equal(codes, []int{1}) {
			t.Fatalf("an unread variable was accepted: exit codes %v", codes)
		}
	})
}

// Help says what the flags are, and nothing of the names the environment or a
// file would use, whichever way a field is named.
func TestHelpShowsNoCommonNames(t *testing.T) {
	type cfg struct {
		Addr string `config:"addr" flag:"addr" help:"Address to listen on."`
		Rest string `config:"rest_of_it"`
	}

	exit := installExit(t)

	var out bytes.Buffer

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&cfg{})
	},
		foundation.WithConfigProviders(envOf(nil), flags.New(flags.WithArgs([]string{"--help"}))),
		foundation.WithHelpOutput(&out),
	)

	assertExitCodes(t, exit)

	help := out.String()

	if !strings.Contains(help, "--addr") {
		t.Fatalf("help does not list --addr:\n%s", help)
	}

	for _, banned := range []string{"SVC_ADDR", "svc.addr", "rest_of_it", "REST_OF_IT", "(env", "json "} {
		if strings.Contains(help, banned) {
			t.Errorf("help shows %q:\n%s", banned, help)
		}
	}
}

// logged is a log entry reduced to what these tests look at.
type logged struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

func loggedEntries(r *foundationtest.LogRecorder) []logged {
	var out []logged

	for _, e := range r.Entries() {
		l := logged{level: e.Record.Level, msg: e.Record.Message, attrs: map[string]string{}}

		for _, a := range e.Attrs {
			l.attrs[a.Key] = a.Value.String()
		}

		e.Record.Attrs(func(a slog.Attr) bool {
			l.attrs[a.Key] = a.Value.String()

			return true
		})

		out = append(out, l)
	}

	return out
}

func failing(errs ...error) config.Provider {
	return config.ProviderFunc(func([]string, any) error {
		return errors.Join(errs...)
	})
}

func TestLoadReportsEveryProblemFromEveryProvider(t *testing.T) {
	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var secondRan, afterLoad bool

	first := failing(&config.FieldError{Provider: "env", Field: "Port", Key: "SVC_PORT", Err: errors.New("bad port")})

	second := config.ProviderFunc(func([]string, any) error {
		secondRan = true

		return errors.Join(
			&config.FieldError{Provider: "json", Field: "TLS.CertFile", Key: "svc.tls.cert_file", Err: errors.New("bad cert")},
			&config.FieldError{Provider: "json", Field: "Name", Key: "svc.name", Err: errors.New("bad name")},
		)
	})

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&loadConfig{}, first, second)

		afterLoad = true
	})

	// An early provider failing doesn't stop the others being checked.
	if !secondRan {
		t.Fatal("expected every provider to run before failing")
	}

	if afterLoad {
		t.Fatal("expected Load to fail the function")
	}

	assertExitCodes(t, exit, 1)

	var problems, summaries []logged

	for _, l := range loggedEntries(recorder) {
		switch l.msg {
		case "invalid configuration":
			problems = append(problems, l)
		case "shutting down due to failure":
			summaries = append(summaries, l)
		}
	}

	// One line per problem, in provider order then the order each reported.
	if len(problems) != 3 {
		t.Fatalf("expected 3 problems logged, got %d: %v", len(problems), problems)
	}

	want := []map[string]string{
		{"provider": "env", "field": "Port", "key": "SVC_PORT", "err": "bad port", "fn": "svc"},
		{"provider": "json", "field": "TLS.CertFile", "key": "svc.tls.cert_file", "err": "bad cert", "fn": "svc"},
		{"provider": "json", "field": "Name", "key": "svc.name", "err": "bad name", "fn": "svc"},
	}

	for i, p := range problems {
		if p.level != slog.LevelError {
			t.Errorf("problem %d: expected an error level log, got %v", i, p.level)
		}

		for k, v := range want[i] {
			if p.attrs[k] != v {
				t.Errorf("problem %d: expected %s=%q, got %q", i, k, v, p.attrs[k])
			}
		}
	}

	// And exactly one failure, summarising them.
	if len(summaries) != 1 {
		t.Fatalf("expected one failure, got %d", len(summaries))
	}

	if summaries[0].attrs["err"] != "invalid configuration" || summaries[0].attrs["count"] != "3" {
		t.Fatalf("unexpected summary: %v", summaries[0].attrs)
	}
}

func TestLoadLogsProblemsThatAreNotFieldErrors(t *testing.T) {
	installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&loadConfig{}, failing(errors.New("could not open config.json")))
	})

	var problems []logged

	for _, l := range loggedEntries(recorder) {
		if l.msg == "invalid configuration" {
			problems = append(problems, l)
		}
	}

	if len(problems) != 1 {
		t.Fatalf("expected one problem logged, got %v", problems)
	}

	// There is nothing to say about a provider, field or key, so none is made up.
	attrs := problems[0].attrs

	if attrs["err"] != "could not open config.json" || attrs["provider"] != "" || attrs["field"] != "" || attrs["key"] != "" {
		t.Fatalf("unexpected attributes: %v", attrs)
	}
}

func TestLoadOmitsFieldErrorAttributesThatAreEmpty(t *testing.T) {
	installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&loadConfig{}, failing(&config.FieldError{Provider: "env", Field: "Name", Err: errors.New("no name in tag")}))
	})

	for _, l := range loggedEntries(recorder) {
		if l.msg != "invalid configuration" {
			continue
		}

		if _, ok := l.attrs["key"]; ok {
			t.Fatalf("expected no key attribute when the key is unknown, got %v", l.attrs)
		}

		if l.attrs["field"] != "Name" {
			t.Fatalf("unexpected attributes: %v", l.attrs)
		}
	}
}

func TestLoadFlattensNestedJoinedErrors(t *testing.T) {
	installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	nested := errors.Join(
		errors.Join(&config.FieldError{Provider: "env", Field: "A", Err: errors.New("a")}, errors.New("b")),
		&config.FieldError{Provider: "env", Field: "C", Err: errors.New("c")},
	)

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&loadConfig{}, failing(nested))
	})

	var errs []string

	for _, l := range loggedEntries(recorder) {
		if l.msg == "invalid configuration" {
			errs = append(errs, l.attrs["err"])
		}
	}

	if want := []string{"a", "b", "c"}; !slices.Equal(errs, want) {
		t.Fatalf("expected every leaf error logged in order, got %v", errs)
	}
}

func TestLoadLogsNothingWhenThereAreNoProblems(t *testing.T) {
	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	ok := config.ProviderFunc(func([]string, any) error { return nil })

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&loadConfig{}, ok, ok)
	})

	for _, l := range loggedEntries(recorder) {
		if l.level >= slog.LevelError {
			t.Fatalf("expected no error logs, got %v", l)
		}
	}

	assertExitCodes(t, exit)
}

// fileProvider is a provider written the way a third party would write one: it
// can read some fields from files, and says so through config.FileBacked.
type fileProvider struct {
	fromFile []string
	fields   map[string]string
}

func (p fileProvider) Unmarshal(_ []string, dst any) error {
	rv := reflect.ValueOf(dst).Elem()

	for name, value := range p.fields {
		rv.FieldByName(name).SetString(value)
	}

	return nil
}

func (p fileProvider) FileFields(_ []string, _ any) ([]string, error) {
	return p.fromFile, nil
}

var _ config.FileBacked = fileProvider{}

// A provider that reads a field from a file says so, and Load then refuses to
// expand that field: a secret's contents are not a template. The rule is the
// same for every provider, not just one that uses the env tag.
func TestExpandIsRejectedOnFieldsAProviderReadsFromAFile(t *testing.T) {
	type cfg struct {
		Password string `config:"expand"`
		Home     string `config:"expand"`
	}

	p := fileProvider{fromFile: []string{"Password"}, fields: map[string]string{"Password": "${HOME}", "Home": "${HOME}"}}

	var c cfg

	res := loadWith(t, &c, []config.Provider{p}, lookupEnvOf(map[string]string{"HOME": "/root"}))

	if res.finished {
		t.Fatal("expected Load to fail")
	}

	if got := fieldsOf(res.problems); len(got) != 1 || got[0] != "Password" {
		t.Fatalf("problems for %v, want only Password", got)
	}

	if err := res.problems[0].attrs["err"]; !strings.Contains(err, "file") || !strings.Contains(err, "secret") {
		t.Fatalf("err = %q, want it to explain the file", err)
	}

	if c.Password != "${HOME}" {
		t.Fatalf("a secret must be left exactly as read, got %q", c.Password)
	}
}

// Fields the provider doesn't read from a file are expanded as before, and a
// provider that doesn't implement the interface changes nothing.
func TestExpandStillWorksForFieldsNotReadFromAFile(t *testing.T) {
	type cfg struct {
		Password string `config:"expand"`
		Home     string `config:"expand"`
	}

	for name, p := range map[string]fileProvider{
		"file-backed for another field": {fromFile: []string{"Other"}, fields: map[string]string{"Home": "${HOME}/x"}},
		"file-backed for none":          {fields: map[string]string{"Home": "${HOME}/x"}},
	} {
		t.Run(name, func(t *testing.T) {
			var c cfg

			res := loadWith(t, &c, []config.Provider{p}, lookupEnvOf(map[string]string{"HOME": "/root"}))

			if !res.finished || c.Home != "/root/x" {
				t.Fatalf("got %+v (finished %v), want Home expanded", c, res.finished)
			}
		})
	}

	var c cfg

	plain := config.ProviderFunc(func(_ []string, dst any) error {
		dst.(*cfg).Home = "${HOME}/x"

		return nil
	})

	if res := loadWith(t, &c, []config.Provider{plain}, lookupEnvOf(map[string]string{"HOME": "/root"})); !res.finished || c.Home != "/root/x" {
		t.Fatalf("got %+v (finished %v), want Home expanded", c, res.finished)
	}
}

// Nested fields are named by their path, as in every other report.
func TestFileBackedNestedFieldsAreNamedByPath(t *testing.T) {
	type db struct {
		Password string `config:"expand"`
	}

	type cfg struct {
		DB db
	}

	// The provider isn't asked to set anything; only what it says it reads from a
	// file matters.
	p := fileProvider{fromFile: []string{"DB.Password"}}

	res := loadWith(t, &cfg{}, []config.Provider{p}, lookupEnvOf(nil))

	if got := fieldsOf(res.problems); len(got) != 1 || got[0] != "DB.Password" {
		t.Fatalf("problems for %v, want DB.Password", got)
	}
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f })
}

// Names and messages in a configuration problem can come from the file or the
// environment being read, so control characters in them must not reach the log
// as they are: a newline would forge a log line, an escape sequence would
// repaint a terminal, whatever the log handler does.
func TestProblemLogsEscapeControlCharacters(t *testing.T) {
	installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	hostile := &config.FieldError{
		Provider: "json",
		Field:    "Addr\nlevel=ERROR forged",
		Key:      "svc.\x1b[31mred\r\nINFO forged line",
		Err:      errors.New("bad\nvalue\x00"),
	}

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&loadConfig{}, failing(hostile))
	})

	var problems []logged

	for _, l := range loggedEntries(recorder) {
		if l.msg == "invalid configuration" && l.attrs["count"] == "" {
			problems = append(problems, l)
		}
	}

	if len(problems) != 1 {
		t.Fatalf("expected one problem logged, got %v", problems)
	}

	for k, v := range problems[0].attrs {
		if hasControl(v) {
			t.Errorf("%s contains a control character: %q", k, v)
		}
	}

	if got := problems[0].attrs["key"]; !strings.Contains(got, `\x1b`) || !strings.Contains(got, `\r\n`) {
		t.Errorf("key = %q, want the control characters shown escaped", got)
	}

	if got := problems[0].attrs["err"]; !strings.Contains(got, `bad\nvalue`) {
		t.Errorf("err = %q, want the newline shown escaped", got)
	}

	// The error handed to callers is left as it was.
	if hostile.Key != "svc.\x1b[31mred\r\nINFO forged line" {
		t.Error("the FieldError itself must not be changed")
	}
}

// The same goes for the keys strict mode reports, which are exactly the ones
// nothing expected.
func TestStrictConfigLogsEscapeControlCharacters(t *testing.T) {
	installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	vars := map[string]string{"SVC_X\nINFO forged": "1", "SVC_\x1b[2J": "1"}

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&strictConfig{})
	}, foundation.WithConfigProviders(env.New(env.WithMap(vars))), foundation.WithStrictConfig())

	problems := problemsLogged(recorder)

	if len(problems) != 2 {
		t.Fatalf("expected two problems, got %v", problems)
	}

	for _, p := range problems {
		for k, v := range p.attrs {
			if hasControl(v) {
				t.Errorf("%s contains a control character: %q", k, v)
			}
		}
	}
}

// Ordinary names, including ones that aren't ASCII, are logged as they are.
func TestProblemLogsLeaveOrdinaryTextAlone(t *testing.T) {
	installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	fe := &config.FieldError{
		Provider: "env",
		Field:    "TLS.CertFile",
		Key:      "SVC_CAFÉ_日本",
		Err:      errors.New(`not a valid duration, such as 30s	(tab kept)`),
	}

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&loadConfig{}, failing(fe))
	})

	for _, l := range loggedEntries(recorder) {
		if l.msg != "invalid configuration" || l.attrs["count"] != "" {
			continue
		}

		if l.attrs["key"] != fe.Key || l.attrs["field"] != fe.Field || l.attrs["provider"] != "env" {
			t.Errorf("attributes changed: %v", l.attrs)
		}

		if !strings.Contains(l.attrs["err"], "such as 30s") {
			t.Errorf("err changed: %q", l.attrs["err"])
		}
	}
}

func writeFile(t *testing.T, dir, name, data string) string {
	t.Helper()

	path := filepath.Join(dir, name)

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// No file is read unless the program names it, so a config.json that happens to
// be in the working directory (a directory someone else may be able to write to)
// is never loaded.
func TestNoConfigFileIsReadByDefault(t *testing.T) {
	dir := inTempDir(t)

	writeConfigJSON(t, dir, `{"svc": {"addr": "canary", "name": "canary"}}`)

	var cfg loadConfig

	res := loadWith(t, &cfg, nil)

	if !res.finished || cfg != (loadConfig{}) {
		t.Fatalf("got %+v (finished %v): config.json was read without being named", cfg, res.finished)
	}
}

// A program that names the file reads it; the environment still wins over it.
func TestAProgramThatNamesAFileReadsIt(t *testing.T) {
	dir := inTempDir(t)

	writeConfigJSON(t, dir, `{"svc": {"addr": "from-file", "name": "from-file"}}`)

	t.Setenv("SVC_NAME", "from-env")

	var cfg loadConfig

	res := loadWith(t, &cfg, nil, foundation.WithDocumentConfig(json.File("config.json")))

	if !res.finished || cfg.Addr != "from-file" || cfg.Name != "from-env" {
		t.Fatalf("got %+v (finished %v)", cfg, res.finished)
	}
}

// A file the program named is optional unless it says otherwise, and then it must
// be there.
func TestANamedFileIsSkippedWhenMissingUnlessRequired(t *testing.T) {
	inTempDir(t)

	var cfg loadConfig

	if res := loadWith(t, &cfg, nil, foundation.WithDocumentConfig(json.File("missing.json"))); !res.finished {
		t.Fatalf("a missing optional file should be skipped: %+v", res.problems)
	}

	res := loadWith(t, &cfg, nil, foundation.WithDocumentConfig(json.File("missing.json", config.WithRequired())))

	if res.finished || !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("finished %v, exit %v, want a required file to fail when missing", res.finished, res.exit)
	}
}

// The file must be an ordinary file, as for every file the framework reads.
func TestANamedFileMustBeARegularFile(t *testing.T) {
	inTempDir(t)

	res := loadWith(t, &loadConfig{}, nil, foundation.WithDocumentConfig(json.File(t.TempDir())))

	if res.finished || !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("finished %v, exit %v, want a directory refused", res.finished, res.exit)
	}
}

// No line that Load logs contains a value from the configuration, whatever is
// wrong with it.
func TestNoLogLineContainsAConfigurationValue(t *testing.T) {
	installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	type cfg struct {
		Port int           `env:"PORT" json:"port"`
		Wait time.Duration `env:"WAIT" json:"wait"`
		Home url.URL       `env:"HOME" json:"home"`
		Nums []int         `env:"NUMS" json:"nums"`
	}

	const secret = "hunter2-SECRET"

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&cfg{},
			envOf(map[string]string{"SVC_PORT": secret, "SVC_HOME": "https://admin:" + secret + "@[::1"}),
			json.Raw([]byte(`{"svc": {"wait": "`+secret+`", "nums": [1, "`+secret+`"]}}`)),
		)
	})

	if len(recorder.Entries()) == 0 {
		t.Fatal("expected the problems to be logged")
	}

	for _, l := range loggedEntries(recorder) {
		line := l.msg

		for _, v := range l.attrs {
			line += " " + v
		}

		if strings.Contains(line, "hunter2") || strings.Contains(line, "SECRET") {
			t.Fatalf("a log line contains a configuration value: %q", line)
		}
	}
}

// An ambiguous key is a problem against the field, like any other.
func TestLoadReportsAnAmbiguousJSONKey(t *testing.T) {
	type cfg struct {
		Addr string `json:"addr"`
	}

	var dst cfg

	res := loadWith(t, &dst, []config.Provider{json.Raw([]byte(`{"svc": {"addr": "reviewed-value", "ADDR": "smuggled-value"}}`))})

	if res.finished || len(res.problems) != 1 {
		t.Fatalf("got %+v", res)
	}

	p := res.problems[0]

	if p.attrs["field"] != "Addr" || p.attrs["key"] != "svc.addr" || p.attrs["provider"] != "json" {
		t.Fatalf("unexpected problem: %+v", p)
	}

	if strings.Contains(p.attrs["err"], "value") || !strings.Contains(p.attrs["err"], "appears more than once") {
		t.Fatalf("unexpected err: %q", p.attrs["err"])
	}

	if dst.Addr != "" {
		t.Fatalf("Addr = %q: neither value should be used", dst.Addr)
	}
}

// namedNil is a provider whose methods would dereference their receiver.
type namedNil struct{ path string }

func (p *namedNil) Unmarshal([]string, any) error {
	_ = p.path

	return nil
}

func (p *namedNil) Describe([]string, any) ([]config.FieldKey, error) {
	_ = p.path

	return nil, nil
}

func (p *namedNil) FileFields([]string, any) ([]string, error) {
	_ = p.path

	return nil, nil
}

// A nil provider is skipped, and a nil pointer inside the interface is a nil
// provider too, rather than a call on nothing that panics.
func TestLoadSkipsATypedNilProvider(t *testing.T) {
	exit := installExit(t)

	cfg := loadConfig{}

	var typedNil *namedNil

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&cfg, typedNil, nil)
	})

	if got := exit.Codes(); len(got) != 0 {
		t.Fatalf("exit codes = %v, want none", got)
	}
}

// The same goes for the way help and a dry run look at the providers.
func TestHelpAndDryRunSkipATypedNilProvider(t *testing.T) {
	var typedNil *namedNil

	for name, opt := range map[string]foundation.Option{
		"dry run": foundation.WithDryRun(true),
		"help":    foundation.WithArgs([]string{"--help"}),
	} {
		t.Run(name, func(t *testing.T) {
			exit := installExit(t)

			foundation.Run("svc", func(f *foundation.F) {
				f.Load(&loadConfig{}, typedNil)
			}, opt, foundation.WithHelpOutput(io.Discard))

			if got := exit.Codes(); len(got) != 0 {
				t.Fatalf("exit codes = %v, want none", got)
			}
		})
	}
}

// anyDoc is a document provider that can be compared as a type but not always as
// a value: comparing two of them with == panics when the field holds a slice.
type anyDoc struct{ v any }

func (anyDoc) DocumentLayer()                {}
func (anyDoc) Unmarshal([]string, any) error { return nil }

func (anyDoc) RestartSources() []config.Source {
	return []config.Source{{Path: "/nowhere/config.json"}}
}

// A provider that can't be compared is not found in the stack, and so is
// refused as one that nothing watches, not a crash.
func TestLoadWithAnUncomparableProviderIsAFailureNotAPanic(t *testing.T) {
	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	doc := anyDoc{v: []int{1}}

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&loadConfig{}, doc)
	}, foundation.WithDocumentConfig(doc))

	if got := exit.Codes(); !slices.Equal(got, []int{1}) {
		t.Fatalf("exit codes = %v, want [1]", got)
	}

	// The reason is the refusal, not a comparison that panicked and was recovered.
	failures := entriesNamed(recorder, "shutting down due to failure")
	if len(failures) != 1 || !strings.Contains(failures[0].attrs["err"], "nothing would watch") || failures[0].attrs["stack"] != "" {
		t.Fatalf("the failure was %v", failures)
	}
}

// What names a field can come from a file, so a name that is only dangerous to
// someone reading it (a right-to-left override that reorders the line, a line
// separator that some viewers break on) is shown escaped, as a control
// character is.
func TestProblemLogsEscapeWhatMisleadsAReader(t *testing.T) {
	installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	hostile := &config.FieldError{
		Provider: "json\u202e",
		Field:    "Addr\u2028forged",
		Key:      "svc.\u2066hidden\u200b\u2029",
		Err:      errors.New("bad\u202evalue\xff"),
	}

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&loadConfig{}, failing(hostile))
	})

	var seen int

	for _, l := range loggedEntries(recorder) {
		if l.msg != "invalid configuration" || l.attrs["count"] != "" {
			continue
		}

		seen++

		for k, v := range l.attrs {
			if strings.ContainsAny(v, "\u202e\u2028\u2029\u2066\u200b") || !utf8.ValidString(v) {
				t.Errorf("%s still has text that misleads a reader: %q", k, v)
			}
		}

		if got := l.attrs["key"]; !strings.Contains(got, `\u2066`) || !strings.Contains(got, `\u2029`) {
			t.Errorf("key = %q, want the characters shown escaped", got)
		}
	}

	if seen != 1 {
		t.Fatalf("expected one problem logged, got %d", seen)
	}
}
