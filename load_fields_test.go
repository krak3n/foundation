package foundation_test

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/foundationtest"
)

func lookupEnvOf(vars map[string]string) foundation.Option {
	return foundation.WithLookupEnv(func(name string) (string, bool) {
		v, ok := vars[name]

		return v, ok
	})
}

type dsnConfig struct {
	DSN  string  `env:"DSN" json:"dsn" config:"expand"`
	Addr *string `env:"ADDR" json:"addr" config:"expand"`
	Raw  string  `env:"RAW" json:"raw"`
}

func TestLoadExpandsValuesFromProviders(t *testing.T) {
	var dst dsnConfig

	res := loadWith(t, &dst, []config.Provider{json.Raw([]byte(`{"svc": {
		"dsn": "postgres://${DB_USER}:${DB_PASSWORD}@${DB_HOST:-localhost}:5432/shop",
		"addr": ":${PORT:-8080}"
	}}`))}, lookupEnvOf(map[string]string{"DB_USER": "app", "DB_PASSWORD": "pw", "PORT": "9000"}))

	if !res.finished || len(res.problems) != 0 {
		t.Fatalf("expected a clean load, got %+v", res)
	}

	if dst.DSN != "postgres://app:pw@localhost:5432/shop" {
		t.Fatalf("DSN = %q", dst.DSN)
	}

	if dst.Addr == nil || *dst.Addr != ":9000" {
		t.Fatalf("Addr = %v", dst.Addr)
	}
}

func TestLoadExpandsValuesFromTheEnvironmentProvider(t *testing.T) {
	var dst dsnConfig

	res := loadWith(t, &dst, []config.Provider{envOf(map[string]string{"SVC_DSN": "host=${DB_HOST}"})},
		lookupEnvOf(map[string]string{"DB_HOST": "db.internal"}))

	if !res.finished || dst.DSN != "host=db.internal" {
		t.Fatalf("got %q, %+v", dst.DSN, res)
	}
}

func TestLoadLeavesFieldsWithoutTheTagAlone(t *testing.T) {
	var dst dsnConfig

	loadWith(t, &dst, []config.Provider{json.Raw([]byte(`{"svc": {"raw": "${NOT_EXPANDED}"}}`))}, lookupEnvOf(nil))

	if dst.Raw != "${NOT_EXPANDED}" {
		t.Fatalf("Raw = %q", dst.Raw)
	}
}

func TestLoadTurnsDoubledDollarsIntoOne(t *testing.T) {
	var dst dsnConfig

	loadWith(t, &dst, []config.Provider{json.Raw([]byte(`{"svc": {"dsn": "pa$$word-$${LITERAL}"}}`))}, lookupEnvOf(nil))

	if dst.DSN != "pa$word-${LITERAL}" {
		t.Fatalf("DSN = %q", dst.DSN)
	}
}

// Only what a provider supplied is a template. A default is written by the
// developer, who can build it in code, and leaving it alone is what makes a
// second Load safe.
func TestLoadDoesNotExpandDefaults(t *testing.T) {
	dst := dsnConfig{DSN: "${DEFAULT_IS_LITERAL}"}

	res := loadWith(t, &dst, []config.Provider{envOf(nil)}, lookupEnvOf(map[string]string{"DEFAULT_IS_LITERAL": "expanded"}))

	if !res.finished || dst.DSN != "${DEFAULT_IS_LITERAL}" {
		t.Fatalf("DSN = %q, %+v", dst.DSN, res)
	}

	// Including when there are no providers at all.
	dst = dsnConfig{DSN: "${DEFAULT_IS_LITERAL}"}

	loadWith(t, &dst, nil, foundation.WithConfigProviders(), lookupEnvOf(map[string]string{"DEFAULT_IS_LITERAL": "expanded"}))

	if dst.DSN != "${DEFAULT_IS_LITERAL}" {
		t.Fatalf("DSN = %q", dst.DSN)
	}
}

// What a variable holds is never expanded, and a second Load that doesn't change
// a field doesn't expand its value again, so that guarantee holds across calls.
func TestLoadDoesNotExpandAValueAgainOnASecondLoad(t *testing.T) {
	installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var dst dsnConfig

	lookup := lookupEnvOf(map[string]string{
		"HOLDER": "${SECRET}", // a variable whose value looks like a reference
		"SECRET": "top-secret",
	})

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&dst, json.Raw([]byte(`{"svc": {"dsn": "${HOLDER}", "raw": "$${SECRET}"}}`)))

		// A second Load, with nothing for these fields.
		f.Load(&dst, envOf(nil))
	}, lookup)

	if dst.DSN != "${SECRET}" {
		t.Fatalf("DSN = %q: a value was expanded a second time", dst.DSN)
	}

	// Raw isn't expanded at all, so its escape is untouched.
	if dst.Raw != "$${SECRET}" {
		t.Fatalf("Raw = %q", dst.Raw)
	}
}

// Which values a provider supplied is found by comparing with what the field
// held before, so text a provider supplies that is identical to the default is
// taken as the default and left as it is. It fails visibly, as a literal ${...},
// rather than expanding something it shouldn't, and this pins it as the known edge.
func TestLoadDoesNotExpandAValueIdenticalToTheDefault(t *testing.T) {
	dst := dsnConfig{DSN: "${A}"}

	loadWith(t, &dst, []config.Provider{json.Raw([]byte(`{"svc": {"dsn": "${A}"}}`))}, lookupEnvOf(map[string]string{"A": "1"}))

	if dst.DSN != "${A}" {
		t.Fatalf("DSN = %q", dst.DSN)
	}
}

func TestLoadExpandsAgainWhenAProviderSuppliesTheValueAgain(t *testing.T) {
	installExit(t)

	var dst dsnConfig

	lookup := lookupEnvOf(map[string]string{"A": "1"})

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&dst, json.Raw([]byte(`{"svc": {"dsn": "x${A}"}}`)))
		f.Load(&dst, json.Raw([]byte(`{"svc": {"dsn": "y${A}"}}`)))
	}, lookup)

	if dst.DSN != "y1" {
		t.Fatalf("DSN = %q", dst.DSN)
	}
}

func TestLoadReportsAnUndefinedVariableAgainstTheField(t *testing.T) {
	var dst dsnConfig

	res := loadWith(t, &dst, []config.Provider{json.Raw([]byte(`{"svc": {"dsn": "u:${DB_PASSWORD}@h"}}`))}, lookupEnvOf(nil))

	if res.finished || !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("expected Load to fail the run, got %+v", res)
	}

	if len(res.problems) != 1 {
		t.Fatalf("expected 1 problem, got %+v", res.problems)
	}

	p := res.problems[0]

	if p.attrs["field"] != "DSN" || p.attrs["key"] != "DB_PASSWORD" || p.attrs["err"] != config.ErrUndefined.Error() {
		t.Fatalf("unexpected problem: %+v", p)
	}

	if _, ok := p.attrs["provider"]; ok {
		t.Errorf("no provider is to blame, got %v", p.attrs)
	}
}

func TestLoadReportsEveryUndefinedVariable(t *testing.T) {
	var dst dsnConfig

	res := loadWith(t, &dst, []config.Provider{json.Raw([]byte(`{"svc": {"dsn": "${X}${Y}${X}", "addr": "${Z}"}}`))}, lookupEnvOf(nil))

	var got []string

	for _, p := range res.problems {
		got = append(got, p.attrs["field"]+"/"+p.attrs["key"])
	}

	if want := []string{"DSN/X", "DSN/Y", "Addr/Z"}; !slices.Equal(got, want) {
		t.Fatalf("problems = %v, want %v", got, want)
	}

	if res.summary == nil || res.summary.attrs["count"] != "3" {
		t.Fatalf("unexpected summary: %+v", res.summary)
	}
}

func TestLoadReportsASyntaxErrorAgainstTheField(t *testing.T) {
	var dst dsnConfig

	res := loadWith(t, &dst, []config.Provider{json.Raw([]byte(`{"svc": {"dsn": "host=${DB"}}`))}, lookupEnvOf(nil))

	if len(res.problems) != 1 || res.problems[0].attrs["field"] != "DSN" || !strings.Contains(res.problems[0].attrs["err"], "unterminated") {
		t.Fatalf("unexpected problems: %+v", res.problems)
	}

	if key, ok := res.problems[0].attrs["key"]; ok {
		t.Errorf("a syntax error has no variable, got key %q", key)
	}
}

func TestLoadExpansionHappensBeforeRequiredAndValidate(t *testing.T) {
	type cfg struct {
		A string `json:"a" config:"required,expand"`
		B string `json:"b" config:"required,expand"`
	}

	var dst cfg

	// A set variable satisfies required. One that expands to nothing doesn't.
	res := loadWith(t, &dst, []config.Provider{json.Raw([]byte(`{"svc": {"a": "${SET}", "b": "${UNSET:-}"}}`))},
		lookupEnvOf(map[string]string{"SET": "value"}))

	if want := []string{"B"}; !slices.Equal(fieldsOf(res.problems), want) {
		t.Fatalf("fields = %v, want %v", fieldsOf(res.problems), want)
	}

	if dst.A != "value" {
		t.Fatalf("A = %q", dst.A)
	}
}

type expandedValidated struct {
	DSN string `json:"dsn" config:"expand"`

	saw *string
}

func (c *expandedValidated) Validate() error {
	*c.saw = c.DSN

	return nil
}

func TestValidateSeesExpandedValues(t *testing.T) {
	var saw string

	dst := expandedValidated{saw: &saw}

	loadWith(t, &dst, []config.Provider{json.Raw([]byte(`{"svc": {"dsn": "h=${HOST}"}}`))}, lookupEnvOf(map[string]string{"HOST": "db"}))

	if saw != "h=db" {
		t.Fatalf("Validate saw %q", saw)
	}
}

func TestValidateIsNotCalledWhenExpansionFails(t *testing.T) {
	var saw string

	dst := expandedValidated{saw: &saw}

	res := loadWith(t, &dst, []config.Provider{json.Raw([]byte(`{"svc": {"dsn": "h=${MISSING}"}}`))}, lookupEnvOf(nil))

	if saw != "" || len(res.problems) != 1 {
		t.Fatalf("Validate was called after a problem: saw=%q problems=%+v", saw, res.problems)
	}
}

func TestLoadReadsTheProcessEnvironmentByDefault(t *testing.T) {
	t.Setenv("EXPAND_TEST_HOST", "from-process")

	var dst dsnConfig

	res := loadWith(t, &dst, []config.Provider{json.Raw([]byte(`{"svc": {"dsn": "${EXPAND_TEST_HOST}"}}`))})

	if !res.finished || dst.DSN != "from-process" {
		t.Fatalf("DSN = %q, %+v", dst.DSN, res)
	}

	// A nil lookup means the same.
	dst = dsnConfig{}

	loadWith(t, &dst, []config.Provider{json.Raw([]byte(`{"svc": {"dsn": "${EXPAND_TEST_HOST}"}}`))}, foundation.WithLookupEnv(nil))

	if dst.DSN != "from-process" {
		t.Fatalf("DSN = %q with a nil lookup", dst.DSN)
	}
}

func TestWithLookupEnvReplacesTheProcessEnvironment(t *testing.T) {
	t.Setenv("EXPAND_TEST_HOST", "from-process")

	var dst dsnConfig

	res := loadWith(t, &dst, []config.Provider{json.Raw([]byte(`{"svc": {"dsn": "${EXPAND_TEST_HOST}"}}`))}, lookupEnvOf(nil))

	// The process environment is not consulted at all.
	if len(res.problems) != 1 || res.problems[0].attrs["key"] != "EXPAND_TEST_HOST" {
		t.Fatalf("expected the variable to be undefined, got %q and %+v", dst.DSN, res.problems)
	}
}

// Logs and errors name variables, never what is in them.
func TestNoLogLineContainsAnExpandedValue(t *testing.T) {
	installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var dst dsnConfig

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&dst, json.Raw([]byte(`{"svc": {"dsn": "u:${DB_PASSWORD}@${DB_HOST}", "addr": "${NOPE}"}}`)))
	}, lookupEnvOf(map[string]string{"DB_PASSWORD": "hunter2", "DB_HOST": "db.internal"}))

	if len(recorder.Entries()) == 0 {
		t.Fatal("expected the undefined variable to be logged")
	}

	for _, l := range loggedEntries(recorder) {
		line := l.msg

		for _, v := range l.attrs {
			line += " " + v
		}

		if strings.Contains(line, "hunter2") || strings.Contains(line, "db.internal") {
			t.Fatalf("a log line contains an expanded value: %q", line)
		}
	}
}

func TestExpandOnTheWrongKindOfFieldIsReported(t *testing.T) {
	type cfg struct {
		Port     int    `env:"PORT" config:"expand"`
		Password string `env:"PASSWORD,file" config:"expand"`
		Ok       string `env:"OK" config:"expand"`
	}

	res := loadWith(t, &cfg{}, []config.Provider{envOf(nil)}, lookupEnvOf(nil))

	if want := []string{"Port", "Password"}; !slices.Equal(fieldsOf(res.problems), want) {
		t.Fatalf("fields = %v, want %v", fieldsOf(res.problems), want)
	}

	if !strings.Contains(res.problems[0].attrs["err"], "string") || !strings.Contains(res.problems[1].attrs["err"], "file") {
		t.Fatalf("unexpected problems: %+v", res.problems)
	}
}

// A mistake in the tag is reported once, not once by each check.
func TestATagMistakeIsReportedOnce(t *testing.T) {
	type cfg struct {
		A string `env:"A" config:"expand,wat"`
	}

	res := loadWith(t, &cfg{}, []config.Provider{envOf(nil)}, lookupEnvOf(nil))

	if len(res.problems) != 1 || !strings.Contains(res.problems[0].attrs["err"], `"wat"`) {
		t.Fatalf("unexpected problems: %+v", res.problems)
	}
}

func TestOneFieldsProblemDoesNotStopOthersBeingExpanded(t *testing.T) {
	type cfg struct {
		Port int    `env:"PORT"`
		DSN  string `env:"DSN" config:"expand"`
	}

	// Port's bad value is the provider's to report, and DSN is still expanded.
	var dst cfg

	res := loadWith(t, &dst, []config.Provider{envOf(map[string]string{"SVC_PORT": "x", "SVC_DSN": "${A}"})}, lookupEnvOf(map[string]string{"A": "1"}))

	if want := []string{"Port"}; !slices.Equal(fieldsOf(res.problems), want) {
		t.Fatalf("fields = %v, want %v", fieldsOf(res.problems), want)
	}

	if dst.DSN != "1" {
		t.Fatalf("DSN = %q", dst.DSN)
	}
}

// loadResult is what happened when a function called Load.
type loadResult struct {
	// finished is true if the function carried on after Load.
	finished bool

	// problems are the "invalid configuration" errors logged, in order.
	problems []logged

	// summary is the failure the run ended with, nil if it didn't fail.
	summary *logged

	exit []int
}

// loadWith runs a root function that calls Load with the providers given, and
// reports what came of it. Options go to Run.
func loadWith(t *testing.T, dst any, providers []config.Provider, opts ...foundation.Option) loadResult {
	t.Helper()

	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var res loadResult

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(dst, providers...)

		res.finished = true
	}, opts...)

	for _, l := range loggedEntries(recorder) {
		switch {
		case l.msg == "invalid configuration" && l.attrs["count"] == "":
			res.problems = append(res.problems, l)
		case l.msg == "shutting down due to failure":
			l := l
			res.summary = &l
		}
	}

	res.exit = exit.Codes()

	return res
}

func envOf(vars map[string]string) config.Provider {
	return env.New(env.WithMap(vars))
}

func fieldsOf(problems []logged) []string {
	var out []string

	for _, p := range problems {
		out = append(out, p.attrs["field"])
	}

	return out
}

func TestLoadFailsWhenARequiredFieldIsUnset(t *testing.T) {
	type cfg struct {
		Addr string `env:"ADDR" config:"required"`
	}

	res := loadWith(t, &cfg{}, []config.Provider{envOf(nil)})

	if res.finished {
		t.Fatal("expected Load to fail the function")
	}

	if !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("exit codes = %v, want [1]", res.exit)
	}

	if len(res.problems) != 1 {
		t.Fatalf("expected 1 problem, got %d: %v", len(res.problems), res.problems)
	}

	p := res.problems[0]

	if p.level != slog.LevelError || p.attrs["field"] != "Addr" || p.attrs["err"] != config.ErrRequired.Error() {
		t.Fatalf("unexpected problem: %+v", p)
	}

	// No provider found it, so none is named.
	if _, ok := p.attrs["provider"]; ok {
		t.Errorf("expected no provider attribute, got %v", p.attrs)
	}

	if res.summary == nil || res.summary.attrs["err"] != "invalid configuration" || res.summary.attrs["count"] != "1" {
		t.Fatalf("unexpected summary: %+v", res.summary)
	}
}

func TestLoadRequiredIsSatisfiedByAnySource(t *testing.T) {
	type cfg struct {
		Addr string `env:"ADDR" json:"addr" config:"required"`
	}

	tests := []struct {
		name      string
		defaults  cfg
		providers []config.Provider
	}{
		{"a default", cfg{Addr: ":80"}, []config.Provider{envOf(nil)}},
		{"the environment", cfg{}, []config.Provider{envOf(map[string]string{"SVC_ADDR": ":1"})}},
		{"a json document", cfg{}, []config.Provider{json.Raw([]byte(`{"svc": {"addr": ":1"}}`))}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst := tt.defaults

			res := loadWith(t, &dst, tt.providers)

			if !res.finished || len(res.exit) != 0 || len(res.problems) != 0 {
				t.Fatalf("expected a clean load, got %+v", res)
			}
		})
	}
}

func TestLoadReportsEveryMissingRequiredField(t *testing.T) {
	type tls struct {
		CertFile string `env:"CERT_FILE" config:"required"`
		KeyFile  string `env:"KEY_FILE" config:"required"`
	}

	type shared struct {
		Region string `env:"REGION" config:"required"`
	}

	type cfg struct {
		shared

		Addr string `env:"ADDR" config:"required"`
		Name string `env:"NAME"`
		TLS  tls    `env:"TLS"`
	}

	res := loadWith(t, &cfg{}, []config.Provider{envOf(map[string]string{"SVC_TLS_KEY_FILE": "k.pem"})})

	if want := []string{"Region", "Addr", "TLS.CertFile"}; !slices.Equal(fieldsOf(res.problems), want) {
		t.Fatalf("fields = %v, want %v", fieldsOf(res.problems), want)
	}

	if res.summary == nil || res.summary.attrs["count"] != "3" {
		t.Fatalf("unexpected summary: %+v", res.summary)
	}
}

func TestLoadRequiredFollowsProviderProblems(t *testing.T) {
	type cfg struct {
		Port int    `env:"PORT" config:"required"`
		Addr string `env:"ADDR" config:"required"`
	}

	res := loadWith(t, &cfg{}, []config.Provider{envOf(map[string]string{"SVC_PORT": "x"})})

	// The bad value is the problem with Port. It isn't also reported as missing.
	if want := []string{"Port", "Addr"}; !slices.Equal(fieldsOf(res.problems), want) {
		t.Fatalf("fields = %v, want %v", fieldsOf(res.problems), want)
	}

	if res.problems[0].attrs["provider"] != "env" || res.problems[1].attrs["err"] != config.ErrRequired.Error() {
		t.Fatalf("unexpected problems: %+v", res.problems)
	}

	if res.summary == nil || res.summary.attrs["count"] != "2" {
		t.Fatalf("unexpected summary: %+v", res.summary)
	}
}

func TestLoadRequiredAfterAProviderErrorWithoutAField(t *testing.T) {
	type cfg struct {
		Addr string `env:"ADDR" config:"required"`
	}

	res := loadWith(t, &cfg{}, []config.Provider{failing(errors.New("could not open config.json"))})

	if len(res.problems) != 2 || res.problems[1].attrs["field"] != "Addr" {
		t.Fatalf("expected the file error and the missing field, got %+v", res.problems)
	}
}

func TestLoadRequiredCountsZeroAsUnset(t *testing.T) {
	zero := 0

	type cfg struct {
		Port    int  `env:"PORT" config:"required"`
		Debug   bool `env:"DEBUG" config:"required"`
		Retries *int `env:"RETRIES" config:"required"`
		Limit   *int `env:"LIMIT" config:"required"`
	}

	dst := cfg{Limit: &zero}

	res := loadWith(t, &dst, []config.Provider{envOf(map[string]string{
		"SVC_PORT": "0", "SVC_DEBUG": "false", "SVC_RETRIES": "0",
	})})

	// Zero from a provider is still zero, so a plain int can't be explicitly
	// zero. A pointer can: nil is its only unset state.
	if want := []string{"Port", "Debug"}; !slices.Equal(fieldsOf(res.problems), want) {
		t.Fatalf("fields = %v, want %v", fieldsOf(res.problems), want)
	}
}

func TestLoadRequiredTreatsEmptySlicesAndMapsAsUnset(t *testing.T) {
	type cfg struct {
		Tags   []string          `env:"TAGS" config:"required"`
		Labels map[string]string `env:"LABELS" config:"required"`
		Hosts  []string          `env:"HOSTS" config:"required"`
	}

	res := loadWith(t, &cfg{}, []config.Provider{envOf(map[string]string{"SVC_HOSTS": "a,b"})})

	if want := []string{"Tags", "Labels"}; !slices.Equal(fieldsOf(res.problems), want) {
		t.Fatalf("fields = %v, want %v", fieldsOf(res.problems), want)
	}
}

func TestLoadRequiredReportsMistakesInTheTag(t *testing.T) {
	type cfg struct {
		Addr string `env:"ADDR" config:"required,secret"`
		Name string `env:"NAME" config:"required"`
	}

	res := loadWith(t, &cfg{}, []config.Provider{envOf(nil)})

	if want := []string{"Addr", "Name"}; !slices.Equal(fieldsOf(res.problems), want) {
		t.Fatalf("fields = %v, want %v", fieldsOf(res.problems), want)
	}

	if res.problems[0].attrs["err"] == config.ErrRequired.Error() {
		t.Fatalf("expected the tag mistake to be explained, got %+v", res.problems[0])
	}
}

func TestLoadRequiredIsStillCheckedWithNoProviders(t *testing.T) {
	type cfg struct {
		Addr string `env:"ADDR" config:"required"`
		Name string `env:"NAME" config:"required"`
	}

	// With loading switched off a default is the only way to satisfy it.
	res := loadWith(t, &cfg{Name: "svc"}, nil, foundation.WithConfigProviders())

	if want := []string{"Addr"}; !slices.Equal(fieldsOf(res.problems), want) {
		t.Fatalf("fields = %v, want %v", fieldsOf(res.problems), want)
	}
}

func TestLoadRequiredAppliesToTheProvidersGivenToLoad(t *testing.T) {
	type cfg struct {
		Addr string `env:"ADDR" config:"required"`
	}

	// The root's providers don't matter when Load is given its own.
	res := loadWith(t, &cfg{}, []config.Provider{envOf(map[string]string{"SVC_ADDR": ":1"})}, foundation.WithConfigProviders())

	if !res.finished || len(res.problems) != 0 {
		t.Fatalf("expected a clean load, got %+v", res)
	}
}

// The missing field says where each provider that can say would look for it.
func TestLoadRequiredNamesTheKeysProvidersWouldRead(t *testing.T) {
	type tls struct {
		CertFile string `env:"CERT_FILE" json:"cert_file" config:"required"`
	}

	type cfg struct {
		Addr string `env:"ADDR" json:"addr" config:"required"`
		TLS  tls    `env:"TLS" json:"tls"`
	}

	providers := []config.Provider{
		json.Raw([]byte(`{}`)),
		// A provider that can't say where it looks is left out of the list.
		failing(),
		envOf(nil),
	}

	res := loadWith(t, &cfg{}, providers)

	if len(res.problems) != 2 {
		t.Fatalf("expected 2 problems, got %+v", res.problems)
	}

	want := []string{"svc.addr, SVC_ADDR", "svc.tls.cert_file, SVC_TLS_CERT_FILE"}

	for i, p := range res.problems {
		if p.attrs["key"] != want[i] {
			t.Errorf("problem %d: key = %q, want %q", i, p.attrs["key"], want[i])
		}
	}
}

func TestLoadRequiredHasNoKeyWhenNoProviderCanSayWhere(t *testing.T) {
	type cfg struct {
		Addr string `env:"ADDR" config:"required"`
	}

	res := loadWith(t, &cfg{}, []config.Provider{failing()})

	if len(res.problems) != 1 {
		t.Fatalf("expected 1 problem, got %+v", res.problems)
	}

	if key, ok := res.problems[0].attrs["key"]; ok {
		t.Fatalf("expected no key, got %q", key)
	}
}

func TestLoadRequiredNamesKeysFromTheDefaultProviders(t *testing.T) {
	inTempDir(t)

	type cfg struct {
		Addr string `env:"ADDR" json:"addr" config:"required"`
	}

	// By default only the environment is read, so it is the only key named.
	res := loadWith(t, &cfg{}, nil)

	if len(res.problems) != 1 || res.problems[0].attrs["key"] != "SVC_ADDR" {
		t.Fatalf("unexpected problems: %+v", res.problems)
	}

	// A file the program names is listed too, before the environment.
	res = loadWith(t, &cfg{}, nil, foundation.WithDocumentConfig(json.File("config.json")))

	if len(res.problems) != 1 || res.problems[0].attrs["key"] != "svc.addr, SVC_ADDR" {
		t.Fatalf("unexpected problems: %+v", res.problems)
	}
}

// writeSecret writes contents to a file in the test's own directory and returns
// its path.
func writeSecret(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "secret")

	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// A secret can come from a file named by a variable, and counts as a value.
func TestLoadRequiredIsSatisfiedByAFileSecret(t *testing.T) {
	type cfg struct {
		Password string `env:"PASSWORD,file" config:"required"`
	}

	provider := env.New(env.WithMap(map[string]string{"SVC_PASSWORD_FILE": writeSecret(t, "s3cret\n")}))

	var dst cfg

	res := loadWith(t, &dst, []config.Provider{provider})

	if !res.finished || len(res.exit) != 0 || len(res.problems) != 0 {
		t.Fatalf("expected a clean load, got %+v", res)
	}

	if dst.Password != "s3cret" {
		t.Fatalf("got %q", dst.Password)
	}
}

func TestLoadRequiredNamesBothVariablesOfAFileField(t *testing.T) {
	type cfg struct {
		Password string `env:"PASSWORD,file" config:"required"`
	}

	res := loadWith(t, &cfg{}, []config.Provider{envOf(nil)})

	if len(res.problems) != 1 || res.problems[0].attrs["key"] != "SVC_PASSWORD, SVC_PASSWORD_FILE" {
		t.Fatalf("unexpected problems: %+v", res.problems)
	}
}

// Providers apply in order, so a secret in a file overrides a value from an
// earlier provider.
func TestLoadFileSecretOverridesAnEarlierProvider(t *testing.T) {
	type cfg struct {
		Password string `env:"PASSWORD,file" json:"password"`
	}

	later := env.New(env.WithMap(map[string]string{"SVC_PASSWORD_FILE": writeSecret(t, "from-file\n")}))

	var dst cfg

	res := loadWith(t, &dst, []config.Provider{json.Raw([]byte(`{"svc": {"password": "from-json"}}`)), later})

	if !res.finished || dst.Password != "from-file" {
		t.Fatalf("got %q, %+v", dst.Password, res)
	}
}

// checked records how Validate was called and answers with err.
type checked struct {
	Addr string `env:"ADDR" config:"required"`
	Port int    `env:"PORT"`

	calls   *int
	sawAddr *string
	err     error
}

func (c *checked) Validate() error {
	*c.calls++
	*c.sawAddr = c.Addr

	return c.err
}

func newChecked(err error) (*checked, *int, *string) {
	var (
		calls int
		saw   string
	)

	return &checked{calls: &calls, sawAddr: &saw, err: err}, &calls, &saw
}

func TestLoadCallsValidateAfterALoadThatWorked(t *testing.T) {
	c, calls, saw := newChecked(nil)

	res := loadWith(t, c, []config.Provider{envOf(map[string]string{"SVC_ADDR": ":1"})})

	if !res.finished || len(res.exit) != 0 || len(res.problems) != 0 {
		t.Fatalf("expected a clean load, got %+v", res)
	}

	if *calls != 1 {
		t.Fatalf("Validate called %d times, want 1", *calls)
	}

	// It sees the loaded values.
	if *saw != ":1" {
		t.Fatalf("Validate saw Addr %q, want the loaded value", *saw)
	}
}

func TestLoadFailsWhenValidateDoes(t *testing.T) {
	c, _, _ := newChecked(errors.New("port must be above 1024"))

	res := loadWith(t, c, []config.Provider{envOf(map[string]string{"SVC_ADDR": ":1"})})

	if res.finished || !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("expected Load to fail the run, got %+v", res)
	}

	if len(res.problems) != 1 || res.problems[0].attrs["err"] != "port must be above 1024" {
		t.Fatalf("unexpected problems: %+v", res.problems)
	}

	if res.summary == nil || res.summary.attrs["err"] != "invalid configuration" || res.summary.attrs["count"] != "1" {
		t.Fatalf("unexpected summary: %+v", res.summary)
	}
}

func TestLoadDoesNotValidateAfterProviderProblems(t *testing.T) {
	c, calls, _ := newChecked(errors.New("should not be seen"))

	res := loadWith(t, c, []config.Provider{envOf(map[string]string{"SVC_ADDR": ":1", "SVC_PORT": "x"})})

	if *calls != 0 {
		t.Fatalf("Validate called %d times after a provider problem", *calls)
	}

	if len(res.problems) != 1 || res.problems[0].attrs["field"] != "Port" {
		t.Fatalf("unexpected problems: %+v", res.problems)
	}
}

func TestLoadDoesNotValidateWhenARequiredFieldIsMissing(t *testing.T) {
	c, calls, _ := newChecked(errors.New("should not be seen"))

	res := loadWith(t, c, []config.Provider{envOf(nil)})

	if *calls != 0 {
		t.Fatalf("Validate called %d times with a required field missing", *calls)
	}

	if want := []string{"Addr"}; !slices.Equal(fieldsOf(res.problems), want) {
		t.Fatalf("fields = %v, want %v", fieldsOf(res.problems), want)
	}
}

func TestLoadReportsEachErrorValidateJoins(t *testing.T) {
	c, _, _ := newChecked(errors.Join(
		&config.FieldError{Field: "Port", Err: errors.New("too low")},
		errors.New("addr and port don't agree"),
		errors.Join(errors.New("nested one"), errors.New("nested two")),
	))

	res := loadWith(t, c, []config.Provider{envOf(map[string]string{"SVC_ADDR": ":1"})})

	var errs []string

	for _, p := range res.problems {
		errs = append(errs, p.attrs["err"])
	}

	want := []string{"too low", "addr and port don't agree", "nested one", "nested two"}

	if !slices.Equal(errs, want) {
		t.Fatalf("errors = %v, want %v", errs, want)
	}

	// A FieldError from Validate keeps its field.
	if res.problems[0].attrs["field"] != "Port" {
		t.Fatalf("expected the field to be kept, got %v", res.problems[0].attrs)
	}

	if res.summary == nil || res.summary.attrs["count"] != "4" {
		t.Fatalf("unexpected summary: %+v", res.summary)
	}
}

// valueValidated validates with a value receiver, so Load must find the method
// through the pointer it is given.
type valueValidated struct {
	Addr string `env:"ADDR"`
}

func (c valueValidated) Validate() error {
	if c.Addr == "" {
		return errors.New("addr is empty")
	}

	return nil
}

func TestLoadCallsValidateWithAValueReceiver(t *testing.T) {
	res := loadWith(t, &valueValidated{}, []config.Provider{envOf(nil)})

	if res.finished || len(res.problems) != 1 || res.problems[0].attrs["err"] != "addr is empty" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// outer holds a nested struct that can validate itself. Only the struct given
// to Load is validated, so the outer Validate decides whether to call it.
type inner struct {
	called *bool
}

func (i inner) Validate() error {
	*i.called = true

	return errors.New("inner is invalid")
}

type outer struct {
	Inner inner `env:"INNER"`
}

func TestLoadDoesNotValidateNestedStructs(t *testing.T) {
	called := false

	res := loadWith(t, &outer{Inner: inner{called: &called}}, []config.Provider{envOf(nil)})

	if called {
		t.Fatal("expected the nested Validate not to be called")
	}

	if !res.finished || len(res.problems) != 0 {
		t.Fatalf("expected a clean load, got %+v", res)
	}
}

func TestLoadValidatesOnEveryCall(t *testing.T) {
	installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	c, calls, _ := newChecked(nil)

	provider := envOf(map[string]string{"SVC_ADDR": ":1"})

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(c, provider)
		f.Load(c, provider)
	})

	if *calls != 2 {
		t.Fatalf("Validate called %d times, want 2", *calls)
	}
}

func TestLoadValidatesWithNoProviders(t *testing.T) {
	res := loadWith(t, &valueValidated{}, nil, foundation.WithConfigProviders())

	if res.finished || len(res.problems) != 1 || res.problems[0].attrs["err"] != "addr is empty" {
		t.Fatalf("unexpected result: %+v", res)
	}
}
