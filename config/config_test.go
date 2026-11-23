package config_test

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
)

func TestProviderFuncCallsTheFunction(t *testing.T) {
	var (
		gotPath []string
		gotDst  any
	)

	want := errors.New("boom")

	var p config.Provider = config.ProviderFunc(func(path []string, dst any) error {
		gotPath, gotDst = path, dst

		return want
	})

	dst := &struct{}{}

	if err := p.Unmarshal([]string{"a", "b"}, dst); !errors.Is(err, want) {
		t.Fatalf("expected the function's error, got %v", err)
	}

	if !slices.Equal(gotPath, []string{"a", "b"}) || gotDst != dst {
		t.Fatalf("expected the arguments to be passed through, got %v, %v", gotPath, gotDst)
	}
}

func TestFieldErrorMessage(t *testing.T) {
	cause := errors.New("boom")

	tests := []struct {
		name string
		err  *config.FieldError
		want string
	}{
		{"everything", &config.FieldError{Provider: "env", Field: "TLS.CertFile", Key: "APP_TLS_CERT_FILE", Err: cause}, "env: field TLS.CertFile (APP_TLS_CERT_FILE): boom"},
		{"no key", &config.FieldError{Provider: "env", Field: "Port", Err: cause}, "env: field Port: boom"},
		{"no provider", &config.FieldError{Field: "Port", Key: "APP_PORT", Err: cause}, "field Port (APP_PORT): boom"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Fatalf("unexpected message\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}

func TestFieldErrorUnwrapsToItsCause(t *testing.T) {
	cause := errors.New("boom")

	var err error = &config.FieldError{Provider: "env", Field: "Port", Err: cause}

	if !errors.Is(err, cause) {
		t.Fatal("expected errors.Is to reach the cause")
	}
}

func TestFieldErrorsCanBeFoundInAJoinedError(t *testing.T) {
	first := &config.FieldError{Provider: "env", Field: "A", Err: errors.New("a")}
	second := &config.FieldError{Provider: "json", Field: "B", Err: errors.New("b")}

	joined := errors.Join(first, errors.New("plain"), second)

	var found *config.FieldError

	if !errors.As(joined, &found) || found != first {
		t.Fatalf("expected errors.As to find the first field error, got %v", found)
	}

	// Both are reachable through the join, and the message lists every problem.
	if msg := joined.Error(); !strings.Contains(msg, "field A") || !strings.Contains(msg, "field B") {
		t.Fatalf("expected the joined message to list every problem, got %q", msg)
	}
}

func TestAFieldErrorWithNoFieldNamesTheKey(t *testing.T) {
	err := &config.FieldError{Provider: "env", Key: "SVC_ADRR", Err: config.ErrUnknown}

	if got, want := err.Error(), "env: SVC_ADRR: not recognised"; got != want {
		t.Fatalf("unexpected message\n got: %s\nwant: %s", got, want)
	}

	if !errors.Is(err, config.ErrUnknown) {
		t.Fatal("expected errors.Is to find ErrUnknown")
	}
}

// A provider that can check, once everything is loaded, for values it holds that
// nothing asked for, is a Checker.
type checker struct{}

func (checker) Unmarshal([]string, any) error { return nil }
func (checker) Check([]string) error          { return nil }

var _ config.Checker = checker{}
var _ config.Provider = checker{} // a Checker is a Provider

func TestErrAmbiguous(t *testing.T) {
	t.Parallel()

	err := &config.FieldError{Provider: "json", Field: "Addr", Key: "svc.addr", Err: config.ErrAmbiguous}

	if !errors.Is(err, config.ErrAmbiguous) {
		t.Fatal("expected errors.Is to see through the FieldError")
	}

	for _, other := range []error{config.ErrUnknown, config.ErrRequired, config.ErrUndefined} {
		if errors.Is(err, other) {
			t.Fatalf("ErrAmbiguous must be distinct from %v", other)
		}
	}

	if got, want := err.Error(), "json: field Addr (svc.addr): appears more than once"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestErrUndefined(t *testing.T) {
	t.Parallel()

	err := &config.FieldError{Field: "DSN", Key: "DB_PASSWORD", Err: config.ErrUndefined}

	if !errors.Is(err, config.ErrUndefined) {
		t.Fatal("expected errors.Is to see through the FieldError")
	}

	if errors.Is(err, config.ErrRequired) || errors.Is(err, config.ErrUnknown) {
		t.Fatal("ErrUndefined must be distinct from the other causes")
	}

	if got, want := err.Error(), "field DSN (DB_PASSWORD): undefined variable"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestErrRequired(t *testing.T) {
	t.Parallel()

	err := &config.FieldError{Field: "TLS.CertFile", Err: config.ErrRequired}

	if !errors.Is(err, config.ErrRequired) {
		t.Fatal("expected errors.Is to see through the FieldError")
	}

	if errors.Is(err, config.ErrUnknown) {
		t.Fatal("ErrRequired must be distinct from ErrUnknown")
	}

	// No provider found it, so the message doesn't name one.
	if got, want := err.Error(), "field TLS.CertFile: required but not set"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

type validated struct{ err error }

func (v validated) Validate() error { return v.err }

type validatedPtr struct{ err error }

func (v *validatedPtr) Validate() error { return v.err }

// Both receivers satisfy the interface when given a pointer, which is what Load
// is always given.
func TestValidator(t *testing.T) {
	t.Parallel()

	var _ config.Validator = validated{}

	var _ config.Validator = &validatedPtr{}

	var _ config.Validator = &validated{}
}

// toyProvider is a provider written the way a third party would write one: it
// uses only the public config package, never the framework's internals. It reads
// names such as SVC_PORT from a map, with the same rules as the env provider, so
// anything that differs from env is the facade's doing.
type toyProvider struct {
	vars map[string]string
}

func (p toyProvider) Unmarshal(path []string, dst any) error {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("toy: Unmarshal(%T)", dst)
	}

	prefix := strings.ToUpper(strings.Join(config.Keys(path), "_"))

	return errors.Join(p.load(prefix, "", rv.Elem())...)
}

func (p toyProvider) load(prefix, goPrefix string, rv reflect.Value) []error {
	fields, problems := config.Fields(rv, "env", parseToyTag)

	var errs []error

	for _, pr := range problems {
		errs = append(errs, &config.FieldError{Provider: "toy", Field: goPath(goPrefix, pr.GoName), Err: pr.Err})
	}

	for _, f := range fields {
		key := prefix + "_" + strings.ToUpper(f.Name)
		field := goPath(goPrefix, f.GoName)

		if f.Group {
			errs = append(errs, p.load(key, field, f.Value)...)

			continue
		}

		val, ok := p.vars[key]
		if !ok || (val == "" && !config.IsPlainString(f.Value.Type())) {
			continue
		}

		if err := config.SetString(f.Value, val); err != nil {
			errs = append(errs, &config.FieldError{Provider: "toy", Field: field, Key: key, Err: err})
		}
	}

	return errs
}

func goPath(prefix, name string) string {
	if prefix == "" {
		return name
	}

	return prefix + "." + name
}

func parseToyTag(tag string) (config.Tag, error) {
	if tag == "-" {
		return config.Tag{Skip: true}, nil
	}

	if name, _, hasOptions := strings.Cut(tag, ","); hasOptions {
		return config.Tag{Name: name}, fmt.Errorf("unknown toy tag option in %q", tag)
	}

	return config.Tag{Name: tag}, nil
}

// A provider built from the facade loads every supported type exactly as the
// built in env provider does.
func TestFacadeProviderAgreesWithEnvOnTypes(t *testing.T) {
	t.Parallel()

	vars := map[string]string{
		"SVC_REGION":  "eu",
		"SVC_PORT":    "8080",
		"SVC_COUNT":   "9007199254740993",
		"SVC_RATIO":   "0.25",
		"SVC_DEBUG":   "true",
		"SVC_TIMEOUT": "1m30s",
		"SVC_WHEN":    "2026-01-02T03:04:05Z",
		"SVC_IP":      "10.0.0.1",
		"SVC_HOME":    "https://example.com:8443/home",
		"SVC_RETRIES": "3",
		"SVC_TAGS":    "a,b,c",
		"SVC_WEIGHTS": "1,2,3",
		"SVC_LIMITS":  "cpu:2,mem:512",
		"SVC_LABELS":  "team:core,tier:backend",
	}

	path := []string{"svc"}

	fromEnv, fromToy := typesConfig{Defaults: "kept"}, typesConfig{Defaults: "kept"}

	if err := env.New(env.WithMap(vars)).Unmarshal(path, &fromEnv); err != nil {
		t.Fatalf("env: %v", err)
	}

	if err := (toyProvider{vars}).Unmarshal(path, &fromToy); err != nil {
		t.Fatalf("toy: %v", err)
	}

	if fromToy.Port != 8080 || fromToy.Retries == nil || len(fromToy.Limits) != 2 {
		t.Fatalf("toy provider didn't load the values: %+v", fromToy)
	}

	if !reflect.DeepEqual(fromEnv, fromToy) {
		t.Fatalf("providers disagree\n env: %+v\n toy: %+v", fromEnv, fromToy)
	}
}

// Values that don't fit are rejected by a facade provider just as env does.
func TestFacadeProviderRejectsBadValues(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Small int8      `env:"SMALL"`
		Byte  uint8     `env:"BYTE"`
		When  time.Time `env:"WHEN"`
		Nums  []int     `env:"NUMS"`
	}

	for name, vars := range map[string]map[string]string{
		"signed overflow":   {"SVC_SMALL": "300"},
		"negative unsigned": {"SVC_BYTE": "-1"},
		"bad time":          {"SVC_WHEN": "yesterday"},
		"bad slice item":    {"SVC_NUMS": "1,x"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := (toyProvider{vars}).Unmarshal([]string{"svc"}, &cfg{}); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

// An empty value is unset, except for a plain string where it is a value.
func TestFacadeProviderTreatsEmptyAsUnset(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Name  string `env:"NAME"`
		Port  int    `env:"PORT"`
		Limit *int   `env:"LIMIT"`
	}

	got := cfg{Name: "default", Port: 80}

	err := toyProvider{map[string]string{"SVC_NAME": "", "SVC_PORT": "", "SVC_LIMIT": ""}}.Unmarshal([]string{"svc"}, &got)
	if err != nil {
		t.Fatal(err)
	}

	if got.Name != "" || got.Port != 80 || got.Limit != nil {
		t.Fatalf("got %+v, want an empty name and the other fields untouched", got)
	}
}

// Fields decides which fields a provider loads: groups, flattened embedded
// structs, skipped fields and fields without the tag, the same for every
// provider that uses it.
func TestFacadeFieldsStructure(t *testing.T) {
	t.Parallel()

	type tls struct {
		CertFile string `env:"CERT_FILE"`
	}

	type embedded struct {
		Region string `env:"REGION"`
	}

	type cfg struct {
		embedded

		Addr    string `env:"ADDR"`
		TLS     tls    `env:"TLS"`
		Skipped string `env:"-"`
		Plain   string
		hidden  string `env:"HIDDEN"` //lint:ignore U1000 never read: it is here to show that an unexported field is left alone
	}

	got := cfg{}

	err := toyProvider{map[string]string{
		"SVC_REGION": "eu", "SVC_ADDR": ":1", "SVC_TLS_CERT_FILE": "c.pem", "SVC_SKIPPED": "x", "SVC_PLAIN": "x", "SVC_HIDDEN": "x",
	}}.Unmarshal([]string{"svc"}, &got)
	if err != nil {
		t.Fatal(err)
	}

	want := cfg{embedded: embedded{Region: "eu"}, Addr: ":1", TLS: tls{CertFile: "c.pem"}}

	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// Every mistake in the struct is reported, not just the first, alongside the
// fields that are fine, and the problems arrive in declaration order.
func TestFacadeFieldsProblems(t *testing.T) {
	t.Parallel()

	type tls struct {
		Ch chan int `env:"CH"`
	}

	type cfg struct {
		Bad    chan int `env:"BAD"`
		Opt    string   `env:"OPT,wat"`
		Nested tls      `env:"NESTED"`
		Ok     string   `env:"OK"`
	}

	got := cfg{}

	err := toyProvider{map[string]string{"SVC_OK": "fine"}}.Unmarshal([]string{"svc"}, &got)
	if err == nil {
		t.Fatal("expected an error")
	}

	if want := []string{"Bad", "Opt", "Nested.Ch"}; !slices.Equal(fieldsThatFailed(err), want) {
		t.Fatalf("reported %v, want %v", fieldsThatFailed(err), want)
	}

	if got.Ok != "fine" {
		t.Fatalf("valid fields should still be set, got %+v", got)
	}

	var fe *config.FieldError

	if !errors.As(err, &fe) || !strings.Contains(fe.Err.Error(), "unsupported type") {
		t.Fatalf("first problem should say the type is unsupported, got %v", err)
	}
}

// The three exported types are what a provider hands back and forth, so their
// fields are part of the contract.
func TestFacadeTypesShape(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Addr string `env:"ADDR"`
	}

	fields, problems := config.Fields(reflect.ValueOf(&cfg{}).Elem(), "env", func(tag string) (config.Tag, error) {
		return config.Tag{Name: tag}, nil
	})

	if len(problems) != 0 || len(fields) != 1 {
		t.Fatalf("got %d fields and %d problems, want 1 and 0", len(fields), len(problems))
	}

	var f config.Field = fields[0]

	if f.GoName != "Addr" || f.Name != "ADDR" || f.Group || !f.Value.CanSet() {
		t.Fatalf("unexpected field: %+v", f)
	}

	var pr config.Problem = config.Problem{GoName: "X", Name: "Y", Err: errors.New("z")}

	if pr.GoName != "X" || pr.Name != "Y" || pr.Err == nil {
		t.Fatalf("unexpected problem: %+v", pr)
	}
}

// A provider's own tag options come back with the field, so third party
// providers can have options of their own.
func TestFacadeFieldsPassTagOptionsThrough(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Password string `env:"PASSWORD,file"`
		Plain    string `env:"PLAIN"`
	}

	fields, problems := config.Fields(reflect.ValueOf(&cfg{}).Elem(), "env", func(tag string) (config.Tag, error) {
		name, option, hasOption := strings.Cut(tag, ",")

		if !hasOption {
			return config.Tag{Name: name}, nil
		}

		return config.Tag{Name: name, Options: []string{option}}, nil
	})

	if len(problems) != 0 || len(fields) != 2 {
		t.Fatalf("got %d fields and %d problems", len(fields), len(problems))
	}

	if !slices.Equal(fields[0].Options, []string{"file"}) || len(fields[1].Options) != 0 {
		t.Fatalf("options not passed through: %+v", fields)
	}
}
