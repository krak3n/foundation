package env_test

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
)

type basicConfig struct {
	Addr    string        `env:"ADDR"`
	Port    int           `env:"PORT"`
	Debug   bool          `env:"DEBUG"`
	Rate    float64       `env:"RATE"`
	Timeout time.Duration `env:"TIMEOUT"`
}

func TestProviderUnmarshalBasicTypes(t *testing.T) {
	t.Setenv("APP_SERVER_ADDR", "localhost")
	t.Setenv("APP_SERVER_PORT", "8080")
	t.Setenv("APP_SERVER_DEBUG", "true")
	t.Setenv("APP_SERVER_RATE", "1.5")
	t.Setenv("APP_SERVER_TIMEOUT", "30s")

	p := env.New()

	var cfg basicConfig

	if err := p.Unmarshal([]string{"app", "server"}, &cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Addr != "localhost" {
		t.Errorf("Addr: expected %q, got %q", "localhost", cfg.Addr)
	}

	if cfg.Port != 8080 {
		t.Errorf("Port: expected %d, got %d", 8080, cfg.Port)
	}

	if !cfg.Debug {
		t.Error("Debug: expected true, got false")
	}

	if cfg.Rate != 1.5 {
		t.Errorf("Rate: expected %f, got %f", 1.5, cfg.Rate)
	}

	if cfg.Timeout != 30*time.Second {
		t.Errorf("Timeout: expected %v, got %v", 30*time.Second, cfg.Timeout)
	}
}

func TestProviderUnmarshalPreservesDefaults(t *testing.T) {
	t.Setenv("APP_ADDR", "override")

	p := env.New()

	cfg := basicConfig{
		Addr: "default",
		Port: 3000,
	}

	if err := p.Unmarshal([]string{"app"}, &cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Addr != "override" {
		t.Errorf("Addr: expected %q, got %q", "override", cfg.Addr)
	}

	if cfg.Port != 3000 {
		t.Errorf("Port: should be preserved, expected %d, got %d", 3000, cfg.Port)
	}
}

func TestProviderUnmarshalSkipsUntaggedFields(t *testing.T) {
	t.Setenv("APP_UNTAGGED", "value")

	p := env.New()

	cfg := struct {
		Untagged string
		Tagged   string `env:"TAGGED"`
	}{Untagged: "original"}

	if err := p.Unmarshal([]string{"app"}, &cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Untagged != "original" {
		t.Errorf("Untagged: expected %q, got %q", "original", cfg.Untagged)
	}
}

func TestProviderUnmarshalNestedPath(t *testing.T) {
	t.Setenv("APP_HTTP_SERVER_ADDR", ":9090")

	p := env.New()

	var cfg struct {
		Addr string `env:"ADDR"`
	}

	if err := p.Unmarshal([]string{"app", "http", "server"}, &cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Addr != ":9090" {
		t.Errorf("Addr: expected %q, got %q", ":9090", cfg.Addr)
	}
}

func TestProviderUnmarshalNoMatchingVars(t *testing.T) {
	p := env.New()

	cfg := basicConfig{Addr: "default"}

	if err := p.Unmarshal([]string{"nonexistent"}, &cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Addr != "default" {
		t.Errorf("Addr: expected %q preserved, got %q", "default", cfg.Addr)
	}
}

func TestProviderUnmarshalNilPointerError(t *testing.T) {
	p := env.New()

	err := p.Unmarshal([]string{"app"}, nil)
	if err == nil {
		t.Fatal("expected error for nil dst")
	}
}

func TestProviderUnmarshalNonPointerError(t *testing.T) {
	p := env.New()

	var cfg basicConfig

	err := p.Unmarshal([]string{"app"}, cfg)
	if err == nil {
		t.Fatal("expected error for non-pointer dst")
	}
}

func TestProviderUnmarshalNonStructError(t *testing.T) {
	p := env.New()

	var s string

	err := p.Unmarshal([]string{"app"}, &s)
	if err == nil {
		t.Fatal("expected error for non-struct pointer dst")
	}
}

// fieldErrors lists every *config.FieldError in err, however deeply joined.
func fieldErrors(err error) []*config.FieldError {
	var out []*config.FieldError

	var walk func(error)

	walk = func(e error) {
		if j, ok := e.(interface{ Unwrap() []error }); ok {
			for _, c := range j.Unwrap() {
				walk(c)
			}

			return
		}

		if fe, ok := e.(*config.FieldError); ok {
			out = append(out, fe)
		}
	}

	walk(err)

	return out
}

func fieldsOf(errs []*config.FieldError) []string {
	var out []string

	for _, e := range errs {
		out = append(out, e.Field)
	}

	return out
}

func lookupOf(m map[string]string) env.Option {
	return env.WithLookup(func(key string) (string, bool) {
		v, ok := m[key]

		return v, ok
	})
}

func TestEveryBadValueIsReported(t *testing.T) {
	type cfg struct {
		Port    int           `env:"PORT"`
		Name    string        `env:"NAME"`
		Timeout time.Duration `env:"TIMEOUT"`
		Ratio   float64       `env:"RATIO"`
	}

	var got cfg

	err := env.New(lookupOf(map[string]string{
		"APP_PORT": "x", "APP_NAME": "svc", "APP_TIMEOUT": "soon", "APP_RATIO": "half",
	})).Unmarshal([]string{"app"}, &got)

	errs := fieldErrors(err)

	// In the order the fields are declared.
	if want := []string{"Port", "Timeout", "Ratio"}; !slices.Equal(fieldsOf(errs), want) {
		t.Fatalf("expected problems with %v, got %v (%v)", want, fieldsOf(errs), err)
	}

	for _, e := range errs {
		if e.Provider != "env" {
			t.Errorf("expected provider env, got %q", e.Provider)
		}
	}

	if errs[0].Key != "APP_PORT" || errs[1].Key != "APP_TIMEOUT" || errs[2].Key != "APP_RATIO" {
		t.Fatalf("unexpected keys: %q %q %q", errs[0].Key, errs[1].Key, errs[2].Key)
	}

	// The valid field is still set, and the cause can be inspected.
	if got.Name != "svc" {
		t.Fatalf("expected the valid field to be set, got %q", got.Name)
	}

	if !errors.Is(err, strconv.ErrSyntax) {
		t.Fatalf("expected the underlying cause to be reachable: %v", err)
	}
}

func TestProblemsInNestedAndEmbeddedStructsNameTheFieldPath(t *testing.T) {
	type nested struct {
		Port int `env:"PORT"`
	}

	type cfg struct {
		Shared
		TLS nested `env:"TLS"`
	}

	err := env.New(lookupOf(map[string]string{
		"APP_REGION": "", "APP_TLS_PORT": "x",
	})).Unmarshal([]string{"app"}, &cfg{})

	errs := fieldErrors(err)

	if len(errs) != 1 || errs[0].Field != "TLS.Port" || errs[0].Key != "APP_TLS_PORT" {
		t.Fatalf("expected a problem at TLS.Port (APP_TLS_PORT), got %v", errs)
	}

	type flat struct {
		Shared
		Count int `env:"COUNT"`
	}

	err = env.New(lookupOf(map[string]string{"APP_COUNT": "x"})).Unmarshal([]string{"app"}, &flat{})

	if errs := fieldErrors(err); len(errs) != 1 || errs[0].Field != "Count" {
		t.Fatalf("expected the embedded struct's fields to be named as if in the parent, got %v", errs)
	}
}

func TestStructuralProblemsAreReportedWithValueProblems(t *testing.T) {
	type cfg struct {
		Port int      `env:"PORT"`
		Bad  chan int `env:"BAD"`
		Opt  string   `env:"OPT,required"`
		Name string   `env:""`
		Rate int      `env:"RATE"`
	}

	var got cfg

	err := env.New(lookupOf(map[string]string{"APP_PORT": "x", "APP_RATE": "y"})).Unmarshal([]string{"app"}, &got)

	errs := fieldErrors(err)

	// Mistakes in the struct come first, then problems with the values, each in
	// the order the fields are declared.
	if want := []string{"Bad", "Opt", "Name", "Port", "Rate"}; !slices.Equal(fieldsOf(errs), want) {
		t.Fatalf("expected problems with %v, got %v", want, fieldsOf(errs))
	}

	// The key is known whenever the tag has a name, and empty when it doesn't.
	keys := map[string]string{}

	for _, e := range errs {
		keys[e.Field] = e.Key
	}

	if keys["Bad"] != "APP_BAD" || keys["Opt"] != "APP_OPT" || keys["Name"] != "" || keys["Port"] != "APP_PORT" {
		t.Fatalf("unexpected keys: %v", keys)
	}
}

func TestOneProblemIsStillAFieldError(t *testing.T) {
	err := env.New(lookupOf(map[string]string{"APP_PORT": "x"})).Unmarshal([]string{"app"}, &struct {
		Port int `env:"PORT"`
	}{})

	var fe *config.FieldError

	if !errors.As(err, &fe) || fe.Field != "Port" {
		t.Fatalf("expected errors.As to find the field error, got %v", err)
	}
}

func TestNoProblemsIsNoError(t *testing.T) {
	if err := env.New(lookupOf(nil)).Unmarshal([]string{"app"}, &struct {
		Port int `env:"PORT"`
	}{}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestAnInvalidDestinationIsNotAFieldError(t *testing.T) {
	err := env.New().Unmarshal([]string{"app"}, nil)

	var invalid *env.InvalidUnmarshalError

	if !errors.As(err, &invalid) || len(fieldErrors(err)) != 0 {
		t.Fatalf("expected an InvalidUnmarshalError, got %v", err)
	}
}

type addrConfig struct {
	Addr string `env:"ADDR"`
}

// However a node name separates its words, the variable name joins them with an
// underscore. These names are part of the public contract and must not change.
func TestUnmarshalSeparatorsBecomeUnderscores(t *testing.T) {
	tests := []struct {
		name string
		path []string
		env  string
	}{
		{"dot", []string{"svc", "http.server"}, "SVC_HTTP_SERVER_ADDR"},
		{"slash", []string{"svc", "http/server"}, "SVC_HTTP_SERVER_ADDR"},
		{"hyphen", []string{"svc", "http-server"}, "SVC_HTTP_SERVER_ADDR"},
		{"separate segments", []string{"svc", "http", "server"}, "SVC_HTTP_SERVER_ADDR"},
		{"underscore", []string{"svc", "http_server"}, "SVC_HTTP_SERVER_ADDR"},
		{"mixed case", []string{"Svc", "Http.Server"}, "SVC_HTTP_SERVER_ADDR"},
		{"digits", []string{"svc", "v2.api"}, "SVC_V2_API_ADDR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.env, ":8080")

			var cfg addrConfig

			if err := env.New().Unmarshal(tt.path, &cfg); err != nil {
				t.Fatal(err)
			}

			if cfg.Addr != ":8080" {
				t.Fatalf("expected %s to be read for path %q, got %q", tt.env, tt.path, cfg.Addr)
			}
		})
	}
}

// Configuration problems are logged, so they name the variable and say what is
// wrong with it, and never repeat what it holds.
func TestErrorsNeverContainTheVariablesValue(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Port  int           `env:"PORT"`
		Small int8          `env:"SMALL"`
		Wait  time.Duration `env:"WAIT"`
		Home  url.URL       `env:"HOME"`
		When  time.Time     `env:"WHEN"`
		Level level         `env:"LEVEL"`
		Nums  []int         `env:"NUMS"`
		Ratio float64       `env:"RATIO"`
		On    bool          `env:"ON"`
	}

	const secret = "hunter2-SECRET"

	err := env.New(env.WithMap(map[string]string{
		"SVC_PORT":  secret,
		"SVC_SMALL": "300" + secret,
		"SVC_WAIT":  secret,
		"SVC_HOME":  "https://admin:" + secret + "@[::1",
		"SVC_WHEN":  secret,
		"SVC_LEVEL": secret,
		"SVC_NUMS":  "1," + secret,
		"SVC_RATIO": secret,
		"SVC_ON":    secret,
	})).Unmarshal([]string{"svc"}, &cfg{})

	errs := fieldErrors(err)

	if len(errs) != 9 {
		t.Fatalf("expected 9 problems, got %d: %v", len(errs), err)
	}

	for _, e := range errs {
		if strings.Contains(e.Error(), "hunter2") || strings.Contains(e.Error(), "SECRET") {
			t.Errorf("the error for %s repeats the value: %q", e.Field, e)
		}

		// It still says which variable.
		if e.Key == "" || !strings.Contains(e.Error(), e.Key) {
			t.Errorf("the error for %s doesn't name its variable: %q", e.Field, e)
		}
	}
}

// lookupMap is a lookup that reads from a map, so a test needs no process
// environment.
func lookupMap(m map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := m[key]

		return v, ok
	}
}

type lookupConfig struct {
	Addr string `env:"ADDR"`
	Name string `env:"NAME"`
	TLS  struct {
		CertFile string `env:"CERT_FILE"`
	} `env:"TLS"`
}

func TestWithLookupIsUsedInsteadOfTheProcessEnvironment(t *testing.T) {
	t.Setenv("APP_ADDR", ":from-process")
	t.Setenv("APP_NAME", "from-process")

	var cfg lookupConfig

	p := env.New(env.WithLookup(lookupMap(map[string]string{"APP_ADDR": ":injected"})))

	if err := p.Unmarshal([]string{"app"}, &cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.Addr != ":injected" {
		t.Fatalf("expected the injected value, got %q", cfg.Addr)
	}

	// The process environment is not consulted at all, not even as a fallback.
	if cfg.Name != "" {
		t.Fatalf("expected the process environment to be ignored, got %q", cfg.Name)
	}
}

func TestLookupIsAskedForTheExactKeys(t *testing.T) {
	var (
		mu   sync.Mutex
		keys []string
	)

	lookup := func(key string) (string, bool) {
		mu.Lock()
		defer mu.Unlock()

		keys = append(keys, key)

		return "", false
	}

	if err := env.New(env.WithLookup(lookup)).Unmarshal([]string{"svc", "http.server"}, &lookupConfig{}); err != nil {
		t.Fatal(err)
	}

	want := []string{"SVC_HTTP_SERVER_ADDR", "SVC_HTTP_SERVER_NAME", "SVC_HTTP_SERVER_TLS_CERT_FILE"}

	if slices.Sort(keys); !slices.Equal(keys, want) {
		t.Fatalf("unexpected keys\n got: %v\nwant: %v", keys, want)
	}
}

// Variable names are matched exactly, as the operating system does on Linux and
// macOS: APP_addr is a different variable from APP_ADDR.
func TestNamesAreMatchedExactly(t *testing.T) {
	t.Run("injected", func(t *testing.T) {
		var cfg lookupConfig

		p := env.New(env.WithLookup(lookupMap(map[string]string{"APP_addr": ":x", "app_ADDR": ":y"})))

		if err := p.Unmarshal([]string{"app"}, &cfg); err != nil {
			t.Fatal(err)
		}

		if cfg.Addr != "" {
			t.Fatalf("expected differently cased names not to match, got %q", cfg.Addr)
		}
	})

	t.Run("process environment", func(t *testing.T) {
		t.Setenv("APP_Addr", ":x")

		var cfg lookupConfig

		if err := env.New().Unmarshal([]string{"app"}, &cfg); err != nil {
			t.Fatal(err)
		}

		if cfg.Addr != "" {
			t.Fatalf("expected a differently cased name not to match, got %q", cfg.Addr)
		}
	})
}

func TestWithLookupNilUsesTheProcessEnvironment(t *testing.T) {
	t.Setenv("APP_ADDR", ":from-process")

	var cfg lookupConfig

	if err := env.New(env.WithLookup(nil)).Unmarshal([]string{"app"}, &cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.Addr != ":from-process" {
		t.Fatalf("expected a nil lookup to mean the default, got %q", cfg.Addr)
	}
}

func TestTheLastLookupOptionWins(t *testing.T) {
	var cfg lookupConfig

	p := env.New(
		env.WithLookup(lookupMap(map[string]string{"APP_ADDR": ":first"})),
		env.WithLookup(lookupMap(map[string]string{"APP_ADDR": ":second"})),
	)

	if err := p.Unmarshal([]string{"app"}, &cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.Addr != ":second" {
		t.Fatalf("expected the last option to win, got %q", cfg.Addr)
	}
}

func TestNilOptionsAreSkipped(t *testing.T) {
	var cfg lookupConfig

	p := env.New(nil, env.WithLookup(lookupMap(map[string]string{"APP_ADDR": ":x"})), nil)

	if err := p.Unmarshal([]string{"app"}, &cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.Addr != ":x" {
		t.Fatalf("unexpected value %q", cfg.Addr)
	}
}

// A lookup says whether a variable is set separately from its value, so the
// difference between unset and empty survives.
func TestLookupDistinguishesUnsetFromEmpty(t *testing.T) {
	type cfg struct {
		Name string `env:"NAME"`
		Addr string `env:"ADDR"`
		Port int    `env:"PORT"`
	}

	got := cfg{Name: "default-name", Addr: "default-addr", Port: 8080}

	lookup := func(key string) (string, bool) {
		switch key {
		case "APP_NAME":
			return "", true // set but empty: a plain string is blanked
		case "APP_PORT":
			return "", true // set but empty: a number is treated as unset
		}

		return "ignored", false // not set
	}

	if err := env.New(env.WithLookup(lookup)).Unmarshal([]string{"app"}, &got); err != nil {
		t.Fatal(err)
	}

	if got != (cfg{Name: "", Addr: "default-addr", Port: 8080}) {
		t.Fatalf("unexpected config %+v", got)
	}
}

// Providers with different sources work at the same time without interfering,
// which is what t.Setenv can't offer: it forbids t.Parallel.
func TestProvidersWithDifferentSourcesAreIndependent(t *testing.T) {
	for i := range 20 {
		t.Run(fmt.Sprintf("source %d", i), func(t *testing.T) {
			t.Parallel()

			want := fmt.Sprintf(":%d", 8000+i)

			p := env.New(env.WithLookup(lookupMap(map[string]string{"APP_ADDR": want})))

			for range 50 {
				var cfg lookupConfig

				if err := p.Unmarshal([]string{"app"}, &cfg); err != nil {
					t.Fatal(err)
				}

				if cfg.Addr != want {
					t.Fatalf("expected %q, got %q", want, cfg.Addr)
				}
			}
		})
	}
}

func TestOneProviderIsSafeForConcurrentUse(t *testing.T) {
	p := env.New(env.WithLookup(lookupMap(map[string]string{"APP_ADDR": ":x", "APP_TLS_CERT_FILE": "c.pem"})))

	var wg sync.WaitGroup

	for range 20 {
		wg.Go(func() {
			for range 50 {
				var cfg lookupConfig

				if err := p.Unmarshal([]string{"app"}, &cfg); err != nil || cfg.Addr != ":x" || cfg.TLS.CertFile != "c.pem" {
					t.Errorf("unexpected result %+v, %v", cfg, err)

					return
				}
			}
		})
	}

	wg.Wait()
}

type describedTLS struct {
	CertFile string `env:"CERT_FILE"`
}

type describedShared struct {
	Region string `env:"REGION"`
}

type described struct {
	describedShared

	Addr    string `env:"ADDR"`
	Skipped string `env:"-"`
	Plain   string
	TLS     describedTLS `env:"TLS"`
	Port    int          `env:"PORT"`
	hidden  string       `env:"HIDDEN"` //lint:ignore U1000 never read: it is here to show that an unexported field is left alone
}

func describe(t *testing.T, p *env.Provider, path []string, dst any) ([]string, []string) {
	t.Helper()

	keys, err := p.Describe(path, dst)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	var fields, names []string

	for _, k := range keys {
		fields = append(fields, k.Field)
		names = append(names, k.Key)
	}

	return fields, names
}

func TestDescribeListsTheVariablesInDeclarationOrder(t *testing.T) {
	t.Parallel()

	fields, names := describe(t, env.New(), []string{"svc"}, &described{})

	// An embedded struct's fields are named as if declared in the parent; fields
	// that are skipped, untagged or unexported are not listed.
	wantFields := []string{"Region", "Addr", "TLS.CertFile", "Port"}
	wantNames := []string{"SVC_REGION", "SVC_ADDR", "SVC_TLS_CERT_FILE", "SVC_PORT"}

	if !slices.Equal(fields, wantFields) || !slices.Equal(names, wantNames) {
		t.Fatalf("got fields %v keys %v\nwant fields %v keys %v", fields, names, wantFields, wantNames)
	}
}

func TestDescribeUsesTheKeysOfThePath(t *testing.T) {
	t.Parallel()

	for _, path := range [][]string{
		{"svc", "http", "server"},
		{"svc", "http.server"},
		{"Svc", "HTTP-Server"},
	} {
		_, names := describe(t, env.New(), path, &describedTLS{})

		if want := []string{"SVC_HTTP_SERVER_CERT_FILE"}; !slices.Equal(names, want) {
			t.Errorf("path %q: got %v, want %v", path, names, want)
		}
	}
}

func TestDescribeDoesNotReadTheEnvironmentOrChangeTheStruct(t *testing.T) {
	t.Setenv("SVC_ADDR", ":from-process")

	p := env.New(env.WithMap(map[string]string{"SVC_ADDR": ":1", "SVC_PORT": "9"}))

	dst := described{Addr: "default"}

	describe(t, p, []string{"svc"}, &dst)

	if dst.Addr != "default" || dst.Port != 0 {
		t.Fatalf("Describe changed the struct: %+v", dst)
	}
}

func TestDescribeDoesNotCountAsAskingForTheVariables(t *testing.T) {
	t.Parallel()

	p := env.New(env.WithMap(map[string]string{"SVC_ADDR": ":1"}))

	describe(t, p, []string{"svc"}, &described{})

	// Nothing has loaded, so the variable is still one nothing asked for.
	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"SVC_ADDR"}) {
		t.Fatalf("unexpected unknown variables %v", got)
	}
}

func TestDescribeNeedsNoSource(t *testing.T) {
	t.Parallel()

	// A lookup that can't list is fine, as nothing is looked up.
	p := env.New(env.WithLookup(func(string) (string, bool) { panic("looked something up") }))

	_, names := describe(t, p, []string{"svc"}, &described{})

	if len(names) != 4 {
		t.Fatalf("got %v", names)
	}
}

func TestDescribeReportsMistakesInTheStructAlongsideTheFieldsThatAreFine(t *testing.T) {
	t.Parallel()

	type nested struct {
		Ch chan int `env:"CH"`
	}

	type cfg struct {
		Bad    chan int `env:"BAD"`
		Opt    string   `env:"OPT,wat"`
		Nested nested   `env:"NESTED"`
		Ok     string   `env:"OK"`
	}

	keys, err := env.New().Describe([]string{"svc"}, &cfg{})
	if err == nil {
		t.Fatal("expected the mistakes to be reported")
	}

	if want := []string{"Bad", "Opt", "Nested.Ch"}; !slices.Equal(fieldsOf(fieldErrors(err)), want) {
		t.Fatalf("reported %v, want %v", fieldsOf(fieldErrors(err)), want)
	}

	if len(keys) != 1 || keys[0].Field != "Ok" || keys[0].Key != "SVC_OK" {
		t.Fatalf("expected only the valid field, got %+v", keys)
	}
}

func TestDescribeRejectsAnInvalidDestination(t *testing.T) {
	t.Parallel()

	var nilPtr *described

	for name, dst := range map[string]any{
		"nil":         nil,
		"non pointer": described{},
		"nil pointer": nilPtr,
		"not struct":  new(int),
	} {
		_, err := env.New().Describe([]string{"svc"}, dst)

		var invalid *env.InvalidUnmarshalError

		if !errors.As(err, &invalid) {
			t.Errorf("%s: expected an InvalidUnmarshalError, got %v", name, err)
		}
	}
}

// What Describe lists is what Unmarshal reads: give every listed variable a
// value and loading finds nothing wrong and leaves nothing unknown.
func TestDescribeListsExactlyWhatUnmarshalReads(t *testing.T) {
	t.Parallel()

	type inner struct {
		Ratio float64 `env:"RATIO"`
		Tags  []int   `env:"TAGS"`
	}

	type cfg struct {
		describedShared

		Name  string `env:"NAME"`
		Port  int    `env:"PORT"`
		Debug bool   `env:"DEBUG"`
		Inner inner  `env:"INNER"`
	}

	path := []string{"svc", "http.server"}

	_, names := describe(t, env.New(), path, &cfg{})

	vars := map[string]string{}

	for _, name := range names {
		vars[name] = "1"
	}

	p := env.New(env.WithMap(vars))

	var loaded cfg

	if err := p.Unmarshal(path, &loaded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if loaded.Name != "1" || loaded.Port != 1 || !loaded.Debug || loaded.Inner.Ratio != 1 || loaded.Region != "1" {
		t.Fatalf("not every listed variable was read: %+v", loaded)
	}

	if err := p.Check(path); err != nil {
		t.Fatalf("expected every variable to be known, got %v", err)
	}

	// And nothing else is listed: a variable outside the list is unknown.
	vars["SVC_HTTP_SERVER_EXTRA"] = "1"

	withExtra := env.New(env.WithMap(vars))

	if err := withExtra.Unmarshal(path, &cfg{}); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got := unknownKeys(t, withExtra.Check(path)); !slices.Equal(got, []string{"SVC_HTTP_SERVER_EXTRA"}) {
		t.Fatalf("unexpected unknown variables %v", got)
	}
}

type fileBacked struct {
	Password string `env:"PASSWORD,file"`
	Addr     string `env:"ADDR"`
	DB       struct {
		Token string `env:"TOKEN,file"`
		Name  string `env:"NAME"`
	} `env:"DB"`
	Skipped string `env:"-"`
}

// The env provider says which fields it can read from a file, by Go name, so that
// Load can refuse to expand them whatever the provider is.
func TestEnvProviderReportsTheFieldsItReadsFromFiles(t *testing.T) {
	var p config.FileBacked = env.New()

	got, err := p.FileFields([]string{"svc"}, &fileBacked{})
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"Password", "DB.Token"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Asking doesn't read anything or count as asking for the variables.
func TestEnvProviderFileFieldsReadsNothing(t *testing.T) {
	looked := false

	p := env.New(env.WithEnviron(func() []string { return nil }), env.WithLookup(func(string) (string, bool) {
		looked = true

		return "", false
	}))

	if _, err := p.FileFields([]string{"svc"}, &fileBacked{}); err != nil {
		t.Fatal(err)
	}

	if looked {
		t.Fatal("FileFields must not read the environment")
	}

	if err := p.Check([]string{"svc"}); err != nil {
		t.Fatalf("FileFields should not have recorded any keys: %v", err)
	}
}

func TestEnvProviderFileFieldsRejectsANonStruct(t *testing.T) {
	if _, err := env.New().FileFields([]string{"svc"}, 3); err == nil {
		t.Fatal("expected an error")
	}
}
