package env_test

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.krak3n.dev/foundation/config/env"
)

// load sets the given variables, loads them into cfg at the path ["app"], and
// returns the result.
func load[T any](t *testing.T, vars map[string]string, cfg T) (T, error) {
	t.Helper()

	for k, v := range vars {
		t.Setenv(k, v)
	}

	err := env.New().Unmarshal([]string{"app"}, &cfg)

	return cfg, err
}

func mustLoad[T any](t *testing.T, vars map[string]string, cfg T) T {
	t.Helper()

	got, err := load(t, vars, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	return got
}

func expectEqual(t *testing.T, got, want any) {
	t.Helper()

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected value\n got: %#v\nwant: %#v", got, want)
	}
}

func expectErrorContaining(t *testing.T, err error, parts ...string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected an error containing %q, got none", parts)
	}

	for _, part := range parts {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("expected the error to mention %q, got: %v", part, err)
		}
	}
}

// level is a custom type that parses itself, through encoding.TextUnmarshaler.
type level int

func (l *level) UnmarshalText(b []byte) error {
	switch string(b) {
	case "low":
		*l = 1
	case "high":
		*l = 2
	default:
		return fmt.Errorf("unknown level %q", b)
	}

	return nil
}

func TestUnsignedIntegers(t *testing.T) {
	type cfg struct {
		U   uint   `env:"U"`
		U8  uint8  `env:"U8"`
		U16 uint16 `env:"U16"`
		U32 uint32 `env:"U32"`
		U64 uint64 `env:"U64"`
	}

	got := mustLoad(t, map[string]string{
		"APP_U": "7", "APP_U8": "255", "APP_U16": "65535", "APP_U32": "4294967295", "APP_U64": "18446744073709551615",
	}, cfg{})

	expectEqual(t, got, cfg{U: 7, U8: 255, U16: 65535, U32: 4294967295, U64: 18446744073709551615})
}

func TestUnsignedIntegersRejectBadValues(t *testing.T) {
	type cfg struct {
		N uint8 `env:"N"`
	}

	for name, value := range map[string]string{"overflow": "256", "negative": "-1", "not a number": "x"} {
		t.Run(name, func(t *testing.T) {
			_, err := load(t, map[string]string{"APP_N": value}, cfg{})

			expectErrorContaining(t, err, "APP_N")
		})
	}
}

func TestIntegersAreBase10(t *testing.T) {
	type cfg struct {
		N int `env:"N"`
	}

	// A leading zero is not octal, so a zero padded port stays what it looks like.
	expectEqual(t, mustLoad(t, map[string]string{"APP_N": "010"}, cfg{}), cfg{N: 10})

	for _, value := range []string{"0x10", "0b1", "0o7", "1_000"} {
		_, err := load(t, map[string]string{"APP_N": value}, cfg{})

		expectErrorContaining(t, err, "APP_N")
	}
}

func TestSignedIntegersDetectOverflow(t *testing.T) {
	type cfg struct {
		N int8 `env:"N"`
	}

	expectEqual(t, mustLoad(t, map[string]string{"APP_N": "-128"}, cfg{}), cfg{N: -128})

	for _, value := range []string{"128", "-129"} {
		_, err := load(t, map[string]string{"APP_N": value}, cfg{})

		expectErrorContaining(t, err, "APP_N")
	}
}

func TestPointers(t *testing.T) {
	type cfg struct {
		Int      *int           `env:"INT"`
		Str      *string        `env:"STR"`
		Bool     *bool          `env:"BOOL"`
		Duration *time.Duration `env:"DURATION"`
		When     *time.Time     `env:"WHEN"`
		Unset    *int           `env:"UNSET"`
	}

	got := mustLoad(t, map[string]string{
		"APP_INT": "5", "APP_STR": "x", "APP_BOOL": "true", "APP_DURATION": "2s", "APP_WHEN": "2026-01-02T03:04:05Z",
	}, cfg{})

	if got.Int == nil || *got.Int != 5 || got.Str == nil || *got.Str != "x" || got.Bool == nil || !*got.Bool {
		t.Fatalf("expected the pointers to be allocated and set, got %+v", got)
	}

	if got.Duration == nil || *got.Duration != 2*time.Second {
		t.Fatalf("expected a pointer to a duration, got %v", got.Duration)
	}

	if got.When == nil || !got.When.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("expected a pointer to a time, got %v", got.When)
	}

	if got.Unset != nil {
		t.Fatalf("expected a pointer with no variable to stay nil, got %v", *got.Unset)
	}
}

func TestPointersKeepTheirValueWhenUnset(t *testing.T) {
	preset := 9

	got := mustLoad(t, nil, struct {
		N *int `env:"N"`
	}{N: &preset})

	if got.N != &preset {
		t.Fatal("expected an unset variable to leave the pointer alone")
	}
}

func TestPointersAreReplacedNotModifiedThrough(t *testing.T) {
	shared := 9

	got := mustLoad(t, map[string]string{"APP_N": "5"}, struct {
		N *int `env:"N"`
	}{N: &shared})

	// The default may point at a value shared elsewhere, so it must not change.
	if shared != 9 || *got.N != 5 {
		t.Fatalf("expected a fresh value (shared=%d, got=%d)", shared, *got.N)
	}
}

func TestSlices(t *testing.T) {
	type cfg struct {
		Strings   []string        `env:"STRINGS"`
		Ints      []int           `env:"INTS"`
		Uints     []uint16        `env:"UINTS"`
		Durations []time.Duration `env:"DURATIONS"`
		Bools     []bool          `env:"BOOLS"`
		Single    []string        `env:"SINGLE"`
	}

	got := mustLoad(t, map[string]string{
		"APP_STRINGS": "a,b,c", "APP_INTS": "1,2,3", "APP_UINTS": "10,20", "APP_DURATIONS": "1s,2m", "APP_BOOLS": "true,false",
		"APP_SINGLE": "only",
	}, cfg{})

	expectEqual(t, got, cfg{
		Strings:   []string{"a", "b", "c"},
		Ints:      []int{1, 2, 3},
		Uints:     []uint16{10, 20},
		Durations: []time.Duration{time.Second, 2 * time.Minute},
		Bools:     []bool{true, false},
		Single:    []string{"only"},
	})
}

func TestSlicesTrimSpaceAroundItemsAndKeepEmptyOnes(t *testing.T) {
	got := mustLoad(t, map[string]string{"APP_L": " a , b,,c "}, struct {
		L []string `env:"L"`
	}{})

	expectEqual(t, got.L, []string{"a", "b", "", "c"})
}

func TestSlicesReplaceTheDefault(t *testing.T) {
	got := mustLoad(t, map[string]string{"APP_L": "x"}, struct {
		L []string `env:"L"`
	}{L: []string{"a", "b", "c"}})

	expectEqual(t, got.L, []string{"x"})
}

func TestSlicesRejectABadItem(t *testing.T) {
	_, err := load(t, map[string]string{"APP_NUMS": "1,two,3"}, struct {
		Nums []int `env:"NUMS"`
	}{})

	expectErrorContaining(t, err, "APP_NUMS")
}

func TestMaps(t *testing.T) {
	type cfg struct {
		Strings map[string]string        `env:"STRINGS"`
		Ints    map[string]int           `env:"INTS"`
		IntKeys map[int]string           `env:"INTKEYS"`
		Times   map[string]time.Duration `env:"TIMES"`
	}

	got := mustLoad(t, map[string]string{
		"APP_STRINGS": "a:1,b:2", "APP_INTS": "x:10,y:20", "APP_INTKEYS": "1:one,2:two", "APP_TIMES": "short:1s,long:1h",
	}, cfg{})

	expectEqual(t, got, cfg{
		Strings: map[string]string{"a": "1", "b": "2"},
		Ints:    map[string]int{"x": 10, "y": 20},
		IntKeys: map[int]string{1: "one", 2: "two"},
		Times:   map[string]time.Duration{"short": time.Second, "long": time.Hour},
	})
}

func TestMapValuesMayContainColons(t *testing.T) {
	got := mustLoad(t, map[string]string{"APP_M": "home:https://example.com:8080,x:y"}, struct {
		M map[string]string `env:"M"`
	}{})

	expectEqual(t, got.M, map[string]string{"home": "https://example.com:8080", "x": "y"})
}

func TestMapsReplaceTheDefault(t *testing.T) {
	got := mustLoad(t, map[string]string{"APP_M": "b:2"}, struct {
		M map[string]string `env:"M"`
	}{M: map[string]string{"a": "1"}})

	expectEqual(t, got.M, map[string]string{"b": "2"})
}

func TestMapsRejectBadItems(t *testing.T) {
	type cfg struct {
		M map[string]int `env:"M"`
	}

	for name, value := range map[string]string{"no separator": "a1", "bad value": "a:x"} {
		t.Run(name, func(t *testing.T) {
			_, err := load(t, map[string]string{"APP_M": value}, cfg{})

			expectErrorContaining(t, err, "APP_M")
		})
	}
}

func TestTextUnmarshalers(t *testing.T) {
	type cfg struct {
		When  time.Time         `env:"WHEN"`
		IP    net.IP            `env:"IP"`
		Log   slog.Level        `env:"LOG"`
		Level level             `env:"LEVEL"`
		IPs   []net.IP          `env:"IPS"`
		ByKey map[string]level  `env:"BYKEY"`
		Named map[string]string `env:"NAMED"`
	}

	got := mustLoad(t, map[string]string{
		"APP_WHEN": "2026-01-02T03:04:05Z", "APP_IP": "10.0.0.1", "APP_LOG": "debug", "APP_LEVEL": "high",
		"APP_IPS": "10.0.0.1,10.0.0.2", "APP_BYKEY": "a:low,b:high",
	}, cfg{})

	if !got.When.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("unexpected time %v", got.When)
	}

	if got.IP.String() != "10.0.0.1" || got.Log != slog.LevelDebug || got.Level != 2 {
		t.Fatalf("unexpected values: %+v", got)
	}

	if len(got.IPs) != 2 || got.IPs[1].String() != "10.0.0.2" {
		t.Fatalf("expected a slice of unmarshalers, got %v", got.IPs)
	}

	expectEqual(t, got.ByKey, map[string]level{"a": 1, "b": 2})
}

func TestTextUnmarshalerErrorsAreReported(t *testing.T) {
	_, err := load(t, map[string]string{"APP_LEVEL": "extreme"}, struct {
		Level level `env:"LEVEL"`
	}{})

	// A custom type's error often quotes its input, so it is not shown: the
	// variable and the type are.
	expectErrorContaining(t, err, "APP_LEVEL", "level")

	if strings.Contains(err.Error(), "extreme") {
		t.Fatalf("the error repeats the value: %v", err)
	}
}

func TestURLs(t *testing.T) {
	got := mustLoad(t, map[string]string{"APP_A": "https://example.com/x?y=1", "APP_B": "http://localhost:8080"}, struct {
		A url.URL  `env:"A"`
		B *url.URL `env:"B"`
	}{})

	if got.A.Host != "example.com" || got.A.Path != "/x" || got.B == nil || got.B.Host != "localhost:8080" {
		t.Fatalf("unexpected urls: %+v %v", got.A, got.B)
	}

	_, err := load(t, map[string]string{"APP_A": "http://[::1"}, struct {
		A url.URL `env:"A"`
	}{})

	expectErrorContaining(t, err, "APP_A")
}

// A tagged struct that knows how to parse itself is a value, not a nested group.
func TestTaggedTextUnmarshalerStructsAreValues(t *testing.T) {
	got := mustLoad(t, map[string]string{"APP_WHEN": "2026-01-02T03:04:05Z", "APP_WHEN_YEAR": "1999"}, struct {
		When time.Time `env:"WHEN"`
	}{})

	if got.When.Year() != 2026 {
		t.Fatalf("expected the time to be parsed as a value, got %v", got.When)
	}
}

type Shared struct {
	Region string `env:"REGION"`
}

type hidden struct {
	Zone string `env:"ZONE"`
}

func TestUntaggedEmbeddedStructsAreFlattened(t *testing.T) {
	type cfg struct {
		Shared
		hidden
		Name string `env:"NAME"`
	}

	got := mustLoad(t, map[string]string{"APP_REGION": "eu", "APP_ZONE": "a", "APP_NAME": "svc"}, cfg{})

	if got.Region != "eu" || got.Zone != "a" || got.Name != "svc" {
		t.Fatalf("expected the embedded fields to load as if declared in the parent, got %+v", got)
	}
}

func TestTaggedEmbeddedStructsAreNested(t *testing.T) {
	type cfg struct {
		Shared `env:"SHARED"`
	}

	got := mustLoad(t, map[string]string{"APP_SHARED_REGION": "eu", "APP_REGION": "ignored"}, cfg{})

	if got.Region != "eu" {
		t.Fatalf("expected a tagged embedded struct to extend the prefix, got %+v", got)
	}
}

func TestTagOptions(t *testing.T) {
	t.Run("a dash skips the field", func(t *testing.T) {
		got := mustLoad(t, map[string]string{"APP_SKIP": "x", "APP_-": "y"}, struct {
			Skip string `env:"-"`
		}{Skip: "default"})

		expectEqual(t, got.Skip, "default")
	})

	t.Run("an unknown option is an error", func(t *testing.T) {
		// Set or not: it must not be possible to ask for behaviour that silently
		// doesn't happen.
		_, err := load(t, nil, struct {
			Name string `env:"NAME,required"`
		}{})

		expectErrorContaining(t, err, "Name", "required")
	})

	t.Run("an empty name is an error", func(t *testing.T) {
		_, err := load(t, nil, struct {
			Name string `env:""`
		}{})

		expectErrorContaining(t, err, "Name")
	})
}

func TestUnsupportedTypesFailEvenWhenNothingIsSet(t *testing.T) {
	type nested struct{ A int }

	tests := map[string]any{
		"chan": &struct {
			F chan int `env:"F"`
		}{},
		"func": &struct {
			F func() `env:"F"`
		}{},
		"complex": &struct {
			F complex128 `env:"F"`
		}{},
		"interface": &struct {
			F any `env:"F"`
		}{},
		"pointer to struct": &struct {
			F *nested `env:"F"`
		}{},
		"slice of chan": &struct {
			F []chan int `env:"F"`
		}{},
		"slice of slice": &struct {
			F [][]string `env:"F"`
		}{},
		"map of chan": &struct {
			F map[string]chan int `env:"F"`
		}{},
		"array": &struct {
			F [2]string `env:"F"`
		}{},
	}

	for name, dst := range tests {
		t.Run(name, func(t *testing.T) {
			err := env.New().Unmarshal([]string{"app"}, dst)

			expectErrorContaining(t, err, "F")
		})
	}
}

func TestUntaggedUnsupportedTypesAreIgnored(t *testing.T) {
	_, err := load(t, nil, struct {
		C chan int
		F func()
		A any
	}{})
	if err != nil {
		t.Fatalf("expected untagged fields to be ignored, got %v", err)
	}
}

// An empty variable is treated as not set, so a stray "FOO=" can't wipe out a
// default or make loading fail. A plain string is the exception: there, empty
// is a real value.
func TestEmptyVariablesAreUnsetExceptForStrings(t *testing.T) {
	when := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	type cfg struct {
		Debug   bool              `env:"DEBUG"`
		Port    int               `env:"PORT"`
		Ratio   float64           `env:"RATIO"`
		Timeout time.Duration     `env:"TIMEOUT"`
		When    time.Time         `env:"WHEN"`
		Level   level             `env:"LEVEL"`
		List    []string          `env:"LIST"`
		Map     map[string]string `env:"MAP"`
		Ptr     *int              `env:"PTR"`
		Name    string            `env:"NAME"`
	}

	got := mustLoad(t, map[string]string{
		"APP_DEBUG": "", "APP_PORT": "", "APP_RATIO": "", "APP_TIMEOUT": "", "APP_WHEN": "", "APP_LEVEL": "",
		"APP_LIST": "", "APP_MAP": "", "APP_PTR": "", "APP_NAME": "",
	}, cfg{
		Debug: true, Port: 8080, Ratio: 0.5, Timeout: 5 * time.Second, When: when, Level: 2,
		List: []string{"a"}, Map: map[string]string{"k": "v"}, Name: "default",
	})

	expectEqual(t, got, cfg{
		Debug: true, Port: 8080, Ratio: 0.5, Timeout: 5 * time.Second, When: when, Level: 2,
		List: []string{"a"}, Map: map[string]string{"k": "v"}, Ptr: nil,
		Name: "", // a string can be blanked on purpose
	})
}
