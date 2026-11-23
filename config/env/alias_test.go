package env_test

import (
	"errors"
	"log/slog"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/foundationtest"
)

// aliased reads names the platform chooses, besides its own.
type aliased struct {
	Port int           `env:"PORT,alias=PORT"`
	DSN  string        `env:"DSN,alias=DATABASE_URL"`
	Wait time.Duration `env:"WAIT,alias=TIMEOUT"`
	Name string        `env:"NAME"`
}

func loadAlias(t *testing.T, vars map[string]string, dst any) error {
	t.Helper()

	return env.New(env.WithMap(vars)).Unmarshal([]string{"svc"}, dst)
}

func TestAnAliasIsReadWhenThePrefixedVariableIsNotSet(t *testing.T) {
	t.Parallel()

	got := aliased{Port: 80}

	if err := loadAlias(t, map[string]string{"PORT": "8080", "DATABASE_URL": "postgres://db/x", "TIMEOUT": "5s"}, &got); err != nil {
		t.Fatal(err)
	}

	if got.Port != 8080 || got.DSN != "postgres://db/x" || got.Wait != 5*time.Second {
		t.Fatalf("got %+v, want the aliases read", got)
	}
}

func TestThePrefixedVariableIsReadWhenThereIsNoAlias(t *testing.T) {
	t.Parallel()

	var got aliased

	if err := loadAlias(t, map[string]string{"SVC_PORT": "9090", "SVC_DSN": "mine"}, &got); err != nil {
		t.Fatal(err)
	}

	if got.Port != 9090 || got.DSN != "mine" {
		t.Fatalf("got %+v", got)
	}
}

// The platform always sets PORT, so being able to override it matters: the
// prefixed variable wins, and the alias is not even parsed.
func TestThePrefixedVariableWinsOverTheAlias(t *testing.T) {
	t.Parallel()

	var got aliased

	vars := map[string]string{"SVC_PORT": "9090", "PORT": "not a number", "SVC_DSN": "mine", "DATABASE_URL": "theirs"}

	if err := loadAlias(t, vars, &got); err != nil {
		t.Fatalf("an alias that is not used must not be an error: %v", err)
	}

	if got.Port != 9090 || got.DSN != "mine" {
		t.Fatalf("got %+v, want the prefixed values", got)
	}
}

func TestWithNeitherSetTheDefaultIsKept(t *testing.T) {
	t.Parallel()

	got := aliased{Port: 80, DSN: "default", Wait: time.Second}

	if err := loadAlias(t, nil, &got); err != nil {
		t.Fatal(err)
	}

	if got.Port != 80 || got.DSN != "default" || got.Wait != time.Second {
		t.Fatalf("got %+v, want the defaults", got)
	}
}

// An empty variable is not set, except for a plain string, where it is a value:
// the same rule for an alias as for any variable.
func TestEmptyValuesFollowTheUsualRule(t *testing.T) {
	t.Parallel()

	t.Run("an empty prefixed number falls through to the alias", func(t *testing.T) {
		t.Parallel()

		var got aliased

		if err := loadAlias(t, map[string]string{"SVC_PORT": "", "PORT": "8080"}, &got); err != nil || got.Port != 8080 {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("an empty alias number is unset", func(t *testing.T) {
		t.Parallel()

		got := aliased{Port: 80}

		if err := loadAlias(t, map[string]string{"PORT": ""}, &got); err != nil || got.Port != 80 {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("an empty prefixed plain string is a value and wins", func(t *testing.T) {
		t.Parallel()

		got := aliased{DSN: "default"}

		if err := loadAlias(t, map[string]string{"SVC_DSN": "", "DATABASE_URL": "theirs"}, &got); err != nil || got.DSN != "" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("an empty alias plain string is a value", func(t *testing.T) {
		t.Parallel()

		got := aliased{DSN: "default"}

		if err := loadAlias(t, map[string]string{"DATABASE_URL": ""}, &got); err != nil || got.DSN != "" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
}

// An alias is a name of its own, not a suffix: it is the same whatever the path
// is, and it works on nested and embedded fields.
func TestAnAliasIsAbsolute(t *testing.T) {
	t.Parallel()

	type tls struct {
		Cert string `env:"CERT,alias=TLS_CERT"`
	}

	type cfg struct {
		TLS tls `env:"TLS"`
	}

	for _, path := range [][]string{{"svc"}, {"shop", "api"}, {"a", "b.c", "d"}} {
		var got cfg

		err := env.New(env.WithMap(map[string]string{"TLS_CERT": "c.pem"})).Unmarshal(path, &got)
		if err != nil || got.TLS.Cert != "c.pem" {
			t.Fatalf("path %v: got %+v, %v", path, got, err)
		}
	}
}

func TestAnAliasWorksForEveryType(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Hosts  []string       `env:"HOSTS,alias=ALLOWED_HOSTS"`
		Limits map[string]int `env:"LIMITS,alias=PLATFORM_LIMITS"`
		Home   url.URL        `env:"HOME,alias=PUBLIC_URL"`
		Debug  *bool          `env:"DEBUG,alias=DEBUG_MODE"`
	}

	var got cfg

	err := loadAlias(t, map[string]string{
		"ALLOWED_HOSTS":   "a,b",
		"PLATFORM_LIMITS": "cpu:2",
		"PUBLIC_URL":      "https://example.com/x",
		"DEBUG_MODE":      "true",
	}, &got)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(got.Hosts, []string{"a", "b"}) || got.Limits["cpu"] != 2 || got.Home.Host != "example.com" || got.Debug == nil || !*got.Debug {
		t.Fatalf("got %+v", got)
	}
}

// A bad value is reported against the variable that held it, by name, and the
// value itself never appears.
func TestABadAliasValueIsReportedByNameWithoutTheValue(t *testing.T) {
	t.Parallel()

	var got aliased

	err := loadAlias(t, map[string]string{"PORT": "hunter2-SECRET"}, &got)

	var fe *config.FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("expected a FieldError, got %v", err)
	}

	if fe.Provider != "env" || fe.Field != "Port" || fe.Key != "PORT" {
		t.Fatalf("got %+v, want field Port, key PORT", fe)
	}

	if !errors.Is(err, strconv.ErrSyntax) {
		t.Fatalf("the cause should stay reachable: %v", err)
	}

	if s := err.Error(); strings.Contains(s, "hunter2") || strings.Contains(s, "SECRET") {
		t.Fatalf("the error contains the value: %q", s)
	}
}

// Anything wrong in the tag is an error at load time, even if no variable is set.
func TestBadAliasTagsAreReported(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("A", 129)

	for name, tag := range map[string]string{
		"no name":              "PORT,alias=",
		"no equals":            "PORT,alias",
		"lower case":           "PORT,alias=port",
		"starts with digit":    "PORT,alias=1PORT",
		"dash":                 "PORT,alias=A-B",
		"space":                "PORT,alias=A B",
		"newline":              "PORT,alias=A\nB",
		"escape":               "PORT,alias=A\x1b[2J",
		"another equals":       "PORT,alias=A=B",
		"non ascii":            "PORT,alias=PÖRT",
		"too long":             "PORT,alias=" + long,
		"twice":                "PORT,alias=A,alias=B",
		"with the file option": "PORT,file,alias=A",
		"unknown option":       "PORT,alias=A,wat",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dst := reflectStruct(tag)

			err := loadAlias(t, nil, dst)

			var fe *config.FieldError
			if !errors.As(err, &fe) || fe.Provider != "env" || fe.Field != "F" {
				t.Fatalf("expected a FieldError for F, got %v", err)
			}

			if strings.ContainsFunc(err.Error(), func(r rune) bool { return r < ' ' || r == 0x7f }) {
				t.Fatalf("the error contains a control character: %q", err.Error())
			}

			if len(err.Error()) > 500 {
				t.Fatalf("the error echoes too much: %d bytes", len(err.Error()))
			}
		})
	}
}

func TestAliasOnAStructIsReported(t *testing.T) {
	t.Parallel()

	type inner struct {
		A string `env:"A"`
	}

	type cfg struct {
		Inner inner `env:"INNER,alias=INNER"`
	}

	err := loadAlias(t, nil, &cfg{})

	var fe *config.FieldError
	if !errors.As(err, &fe) || fe.Field != "Inner" || !strings.Contains(fe.Err.Error(), "struct") {
		t.Fatalf("expected a problem on Inner mentioning the struct, got %v", err)
	}
}

// Two fields can't read the same variable, whether it is an alias or a name.
func TestAliasCollisionsAreReported(t *testing.T) {
	t.Parallel()

	type sameAlias struct {
		A string `env:"A,alias=SHARED"`
		B string `env:"B,alias=SHARED"`
	}

	type aliasIsAName struct {
		A string `env:"A"`
		B string `env:"B,alias=SVC_A"`
	}

	type aliasIsItsOwnName struct {
		A string `env:"A,alias=SVC_A"`
	}

	type aliasIsAFileName struct {
		A string `env:"A,file"`
		B string `env:"B,alias=SVC_A_FILE"`
	}

	for name, dst := range map[string]any{
		"two fields with one alias":        &sameAlias{},
		"an alias that is another's name":  &aliasIsAName{},
		"an alias that is its own name":    &aliasIsItsOwnName{},
		"an alias that is a file variable": &aliasIsAFileName{},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := loadAlias(t, nil, dst)

			var fe *config.FieldError
			if !errors.As(err, &fe) || !strings.Contains(fe.Err.Error(), "rename") {
				t.Fatalf("expected a collision to be reported, got %v", err)
			}
		})
	}
}

// Names that mean something to the system, or hold credentials of another
// system, can't be an alias: reading them into configuration is a mistake that
// could put them in a log.
func TestAliasesToSensitiveNamesAreRefused(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"PATH", "HOME", "USER", "LOGNAME", "SHELL", "PWD", "OLDPWD", "IFS", "TMPDIR",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS",
		"LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "SSH_AUTH_SOCK",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := loadAlias(t, map[string]string{name: "hunter2-LEAK"}, reflectStruct("X,alias="+name))

			var fe *config.FieldError
			if !errors.As(err, &fe) || fe.Field != "F" {
				t.Fatalf("expected %s to be refused, got %v", name, err)
			}

			if strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("the error contains the value: %q", err.Error())
			}
		})
	}

	for _, name := range []string{"PORT", "DATABASE_URL", "REDIS_URL", "AWS_REGION", "AWS_DEFAULT_REGION", "K_SERVICE", "HTTP_PROXY", "HOMEPAGE", "LDAP_URL"} {
		t.Run("allowed "+name, func(t *testing.T) {
			t.Parallel()

			if err := loadAlias(t, nil, reflectStruct("X,alias="+name)); err != nil {
				t.Fatalf("%s should be allowed: %v", name, err)
			}
		})
	}
}

func TestDescribeListsTheAliasAfterTheName(t *testing.T) {
	t.Parallel()

	fields, names := describe(t, env.New(), []string{"svc"}, &aliased{})

	wantFields := []string{"Port", "Port", "DSN", "DSN", "Wait", "Wait", "Name"}
	wantNames := []string{"SVC_PORT", "PORT", "SVC_DSN", "DATABASE_URL", "SVC_WAIT", "TIMEOUT", "SVC_NAME"}

	if !slices.Equal(fields, wantFields) || !slices.Equal(names, wantNames) {
		t.Fatalf("got fields %v keys %v\nwant fields %v keys %v", fields, names, wantFields, wantNames)
	}
}

// Strict mode only looks under the root's prefix, but whatever was asked for by
// an alias counts as asked for, including an alias that is under the prefix.
func TestStrictModeKnowsAliases(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Port int `env:"PORT,alias=SVC_LEGACY_PORT"`
	}

	p := env.New(env.WithMap(map[string]string{"SVC_LEGACY_PORT": "1", "SVC_TYPO": "x"}))

	if err := p.Unmarshal([]string{"svc"}, &cfg{}); err != nil {
		t.Fatal(err)
	}

	err := p.Check([]string{"svc"})

	if got := unknownKeys(t, err); !slices.Equal(got, []string{"SVC_TYPO"}) {
		t.Fatalf("unknown = %v, want only SVC_TYPO", got)
	}
}

func TestAnAliasIsRecordedEvenWhenNothingIsSet(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Port int `env:"PORT,alias=SVC_LEGACY_PORT"`
	}

	p := env.New(env.WithEnviron(func() []string { return []string{"SVC_LEGACY_PORT=1"} }), env.WithLookup(func(string) (string, bool) { return "", false }))

	if err := p.Unmarshal([]string{"svc"}, &cfg{}); err != nil {
		t.Fatal(err)
	}

	if err := p.Check([]string{"svc"}); err != nil {
		t.Fatalf("an alias that was asked for is not unknown: %v", err)
	}
}

// Whether an alias was used is something to find out when debugging, so it is
// logged at debug level, by name, only when both were set.
func TestAnIgnoredAliasIsLoggedAtDebugLevel(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var got aliased

	vars := map[string]string{"SVC_PORT": "9090", "PORT": "hunter2-SECRET", "SVC_DSN": "mine-SECRET", "DATABASE_URL": "theirs-SECRET", "TIMEOUT": "5s"}

	if err := loadAlias(t, vars, &got); err != nil {
		t.Fatal(err)
	}

	var logged []map[string]string

	for _, e := range recorder.Entries() {
		if e.Record.Message != "env alias ignored" {
			t.Errorf("unexpected log entry %q", e.Record.Message)

			continue
		}

		if e.Record.Level != slog.LevelDebug {
			t.Errorf("level = %v, want debug", e.Record.Level)
		}

		attrs := map[string]string{}

		e.Record.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.String()

			return true
		})

		logged = append(logged, attrs)
	}

	if len(logged) != 2 {
		t.Fatalf("expected one line for Port and one for DSN, got %v", logged)
	}

	want := []map[string]string{
		{"field": "Port", "key": "SVC_PORT", "alias": "PORT"},
		{"field": "DSN", "key": "SVC_DSN", "alias": "DATABASE_URL"},
	}

	for i, w := range want {
		for k, v := range w {
			if logged[i][k] != v {
				t.Errorf("entry %d: %s = %q, want %q", i, k, logged[i][k], v)
			}
		}

		for k, v := range logged[i] {
			if strings.Contains(v, "SECRET") {
				t.Errorf("entry %d: %s contains a value: %q", i, k, v)
			}
		}
	}
}

func TestNothingIsLoggedWhenOnlyOneNameIsSet(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	for _, vars := range []map[string]string{{"SVC_PORT": "1"}, {"PORT": "1"}, nil} {
		var got aliased

		if err := loadAlias(t, vars, &got); err != nil {
			t.Fatal(err)
		}
	}

	if n := len(recorder.Entries()); n != 0 {
		t.Fatalf("expected a quiet load, got %d entries", n)
	}
}

// reflectStruct returns a pointer to a struct with one string field, F, whose
// env tag is tag, so that tags that can't be written in a source file (control
// characters) can be tried too.
func reflectStruct(tag string) any {
	typ := reflect.StructOf([]reflect.StructField{{
		Name: "F",
		Type: reflect.TypeOf(""),
		Tag:  reflect.StructTag(`env:` + strconv.Quote(tag)),
	}})

	return reflect.New(typ).Interface()
}

// Whatever the tag says, reading it never panics, and what comes back is safe to
// log and bounded in size.
func FuzzAliasTag(f *testing.F) {
	for _, seed := range []string{
		"PORT,alias=PORT", "PORT,alias=", "PORT,alias", "X,alias=A,alias=B", "X,file,alias=A",
		"X,alias=A\nB", "X,alias=" + strings.Repeat("A", 200), "X,alias=AWS_SECRET_ACCESS_KEY", ",alias=A", "-,alias=A",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, tag string) {
		err := env.New(env.WithMap(map[string]string{"PORT": "1", "A": "2"})).Unmarshal([]string{"svc"}, reflectStruct(tag))
		if err == nil {
			return
		}

		// The name in the tag is the developer's own and reaches the log escaped (see
		// the foundation package), so what is checked here is the cause, which is
		// built from the whole tag.
		var fe *config.FieldError
		if !errors.As(err, &fe) {
			t.Fatalf("expected a FieldError, got %v", err)
		}

		cause := fe.Err.Error()

		if strings.ContainsFunc(cause, func(r rune) bool { return r < ' ' || r == 0x7f }) {
			t.Fatalf("the cause contains a control character: %q", cause)
		}

		if len(cause) > 1_000 {
			t.Fatalf("the cause is %d bytes", len(cause))
		}
	})
}
