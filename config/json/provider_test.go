package json_test

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/json"
)

func writeJSON(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	return path
}

func TestFileUnmarshalBasicTypes(t *testing.T) {
	path := writeJSON(t, `{
		"app": {
			"server": {
				"addr": "localhost",
				"port": 8080,
				"debug": true,
				"rate": 1.5,
				"timeout": "30s"
			}
		}
	}`)

	p := json.File(path)

	var cfg struct {
		Addr    string        `json:"addr"`
		Port    int           `json:"port"`
		Debug   bool          `json:"debug"`
		Rate    float64       `json:"rate"`
		Timeout time.Duration `json:"timeout"`
	}

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

func TestFileUnmarshalPreservesDefaults(t *testing.T) {
	path := writeJSON(t, `{
		"app": {
			"addr": "override"
		}
	}`)

	p := json.File(path)

	cfg := struct {
		Addr string `json:"addr"`
		Port int    `json:"port"`
	}{
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

func TestFileUnmarshalCaseInsensitivePath(t *testing.T) {
	path := writeJSON(t, `{
		"App": {
			"HTTP": {
				"Server": {
					"addr": ":9090"
				}
			}
		}
	}`)

	p := json.File(path)

	var cfg struct {
		Addr string `json:"addr"`
	}

	if err := p.Unmarshal([]string{"app", "http", "server"}, &cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Addr != ":9090" {
		t.Errorf("Addr: expected %q, got %q", ":9090", cfg.Addr)
	}
}

func TestFileUnmarshalMissingPathReturnsNil(t *testing.T) {
	path := writeJSON(t, `{
		"app": {
			"other": {
				"addr": "value"
			}
		}
	}`)

	p := json.File(path)

	cfg := struct {
		Addr string `json:"addr"`
	}{Addr: "default"}

	if err := p.Unmarshal([]string{"app", "server"}, &cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Addr != "default" {
		t.Errorf("Addr: should be preserved, expected %q, got %q", "default", cfg.Addr)
	}
}

func TestFileUnmarshalMissingFileSkips(t *testing.T) {
	p := json.File("/nonexistent/config.json")

	cfg := struct {
		Addr string `json:"addr"`
	}{Addr: "default"}

	if err := p.Unmarshal([]string{"app"}, &cfg); err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}

	if cfg.Addr != "default" {
		t.Errorf("Addr: should be preserved, expected %q, got %q", "default", cfg.Addr)
	}
}

func TestFileRequiredMissingFileErrors(t *testing.T) {
	p := json.File("/nonexistent/config.json", config.WithRequired())

	var cfg struct {
		Addr string `json:"addr"`
	}

	if err := p.Unmarshal([]string{"app"}, &cfg); err == nil {
		t.Fatal("expected error for missing required file")
	}
}

func TestFileUnmarshalSkipsUntaggedFields(t *testing.T) {
	path := writeJSON(t, `{
		"app": {
			"untagged": "value",
			"tagged": "from-json"
		}
	}`)

	p := json.File(path)

	cfg := struct {
		Untagged string
		Tagged   string `json:"tagged"`
	}{Untagged: "original"}

	if err := p.Unmarshal([]string{"app"}, &cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Untagged != "original" {
		t.Errorf("Untagged: expected %q, got %q", "original", cfg.Untagged)
	}

	if cfg.Tagged != "from-json" {
		t.Errorf("Tagged: expected %q, got %q", "from-json", cfg.Tagged)
	}
}

func TestFileUnmarshalNilPointerError(t *testing.T) {
	path := writeJSON(t, `{"app": {}}`)

	p := json.File(path)

	err := p.Unmarshal([]string{"app"}, nil)
	if err == nil {
		t.Fatal("expected error for nil dst")
	}
}

func TestFileUnmarshalNonPointerError(t *testing.T) {
	path := writeJSON(t, `{"app": {}}`)

	p := json.File(path)

	var cfg struct {
		Addr string `json:"addr"`
	}

	err := p.Unmarshal([]string{"app"}, cfg)
	if err == nil {
		t.Fatal("expected error for non-pointer dst")
	}
}

func TestRawUnmarshal(t *testing.T) {
	p := json.Raw([]byte(`{"app":{"addr":":8080","port":9000}}`))

	var cfg struct {
		Addr string `json:"addr"`
		Port int    `json:"port"`
	}

	if err := p.Unmarshal([]string{"app"}, &cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Addr != ":8080" {
		t.Errorf("Addr: expected %q, got %q", ":8080", cfg.Addr)
	}

	if cfg.Port != 9000 {
		t.Errorf("Port: expected %d, got %d", 9000, cfg.Port)
	}
}

func TestRawUnmarshalInvalidJSON(t *testing.T) {
	p := json.Raw([]byte(`{not valid json}`))

	var cfg struct {
		Addr string `json:"addr"`
	}

	if err := p.Unmarshal([]string{"app"}, &cfg); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// load reads data, a JSON object of the app's settings, into cfg at the path
// ["app"], and returns the result.
func load[T any](t *testing.T, data string, cfg T) (T, error) {
	t.Helper()

	err := json.Raw([]byte(`{"app": `+data+`}`)).Unmarshal([]string{"app"}, &cfg)

	return cfg, err
}

func mustLoad[T any](t *testing.T, data string, cfg T) T {
	t.Helper()

	got, err := load(t, data, cfg)
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
		U   uint   `json:"u"`
		U8  uint8  `json:"u8"`
		U16 uint16 `json:"u16"`
		U32 uint32 `json:"u32"`
		U64 uint64 `json:"u64"`
	}

	got := mustLoad(t, `{"u": 7, "u8": 255, "u16": 65535, "u32": 4294967295, "u64": 18446744073709551615}`, cfg{})

	expectEqual(t, got, cfg{U: 7, U8: 255, U16: 65535, U32: 4294967295, U64: 18446744073709551615})
}

// JSON numbers are decoded exactly, not through a float64, which would round
// anything over 2^53.
func TestLargeIntegersAreExact(t *testing.T) {
	got := mustLoad(t, `{"i": 9007199254740993, "u": 18446744073709551615}`, struct {
		I int64  `json:"i"`
		U uint64 `json:"u"`
	}{})

	if got.I != 9007199254740993 || got.U != 18446744073709551615 {
		t.Fatalf("expected exact integers, got %+v", got)
	}
}

func TestIntegersRejectBadValues(t *testing.T) {
	type cfg struct {
		I8 int8  `json:"i8"`
		U8 uint8 `json:"u8"`
		N  int   `json:"n"`
	}

	tests := map[string]string{
		"int8 overflow":                 `{"i8": 300}`,
		"int8 underflow":                `{"i8": -129}`,
		"uint8 overflow":                `{"u8": 256}`,
		"negative uint":                 `{"u8": -1}`,
		"fraction into an int":          `{"n": 1.5}`,
		"a string that is not a number": `{"n": "x"}`,
	}

	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := load(t, data, cfg{})

			expectErrorContaining(t, err)
		})
	}
}

func TestNumericStringsAreBase10(t *testing.T) {
	type cfg struct {
		N int `json:"n"`
	}

	expectEqual(t, mustLoad(t, `{"n": "010"}`, cfg{}), cfg{N: 10})

	_, err := load(t, `{"n": "0x10"}`, cfg{})

	expectErrorContaining(t, err, "n")
}

func TestPointers(t *testing.T) {
	type cfg struct {
		Int      *int           `json:"int"`
		Str      *string        `json:"str"`
		Bool     *bool          `json:"bool"`
		Duration *time.Duration `json:"duration"`
		When     *time.Time     `json:"when"`
		Unset    *int           `json:"unset"`
	}

	got := mustLoad(t, `{"int": 5, "str": "x", "bool": true, "duration": "2s", "when": "2026-01-02T03:04:05Z"}`, cfg{})

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
		t.Fatalf("expected a pointer with no value to stay nil, got %v", *got.Unset)
	}
}

func TestPointersAreReplacedNotModifiedThrough(t *testing.T) {
	shared := 9

	got := mustLoad(t, `{"n": 5}`, struct {
		N *int `json:"n"`
	}{N: &shared})

	if shared != 9 || *got.N != 5 {
		t.Fatalf("expected a fresh value (shared=%d, got=%d)", shared, *got.N)
	}
}

func TestPointersKeepTheirValueWhenAbsent(t *testing.T) {
	preset := 9

	got := mustLoad(t, `{}`, struct {
		N *int `json:"n"`
	}{N: &preset})

	if got.N != &preset {
		t.Fatal("expected an absent value to leave the pointer alone")
	}
}

func TestSlices(t *testing.T) {
	type cfg struct {
		Strings   []string        `json:"strings"`
		Ints      []int           `json:"ints"`
		Uints     []uint16        `json:"uints"`
		Durations []time.Duration `json:"durations"`
		Bools     []bool          `json:"bools"`
	}

	got := mustLoad(t, `{
		"strings": ["a", "b", "c"], "ints": [1, 2, 3], "uints": [10, 20],
		"durations": ["1s", "2m"], "bools": [true, false]
	}`, cfg{})

	expectEqual(t, got, cfg{
		Strings:   []string{"a", "b", "c"},
		Ints:      []int{1, 2, 3},
		Uints:     []uint16{10, 20},
		Durations: []time.Duration{time.Second, 2 * time.Minute},
		Bools:     []bool{true, false},
	})
}

func TestSlicesReplaceTheDefault(t *testing.T) {
	got := mustLoad(t, `{"l": ["x"]}`, struct {
		L []string `json:"l"`
	}{L: []string{"a", "b", "c"}})

	expectEqual(t, got.L, []string{"x"})
}

func TestSlicesRejectBadItems(t *testing.T) {
	_, err := load(t, `{"nums": [1, "two", 3]}`, struct {
		Nums []int `json:"nums"`
	}{})

	expectErrorContaining(t, err, "nums")

	_, err = load(t, `{"nums": 5}`, struct {
		Nums []int `json:"nums"`
	}{})

	expectErrorContaining(t, err, "nums")
}

func TestMaps(t *testing.T) {
	type cfg struct {
		Strings map[string]string        `json:"strings"`
		Ints    map[string]int           `json:"ints"`
		IntKeys map[int]string           `json:"intkeys"`
		Times   map[string]time.Duration `json:"times"`
	}

	got := mustLoad(t, `{
		"strings": {"a": "1", "b": "2"}, "ints": {"x": 10, "y": 20},
		"intkeys": {"1": "one", "2": "two"}, "times": {"short": "1s", "long": "1h"}
	}`, cfg{})

	expectEqual(t, got, cfg{
		Strings: map[string]string{"a": "1", "b": "2"},
		Ints:    map[string]int{"x": 10, "y": 20},
		IntKeys: map[int]string{1: "one", 2: "two"},
		Times:   map[string]time.Duration{"short": time.Second, "long": time.Hour},
	})
}

func TestMapsReplaceTheDefault(t *testing.T) {
	got := mustLoad(t, `{"m": {"b": "2"}}`, struct {
		M map[string]string `json:"m"`
	}{M: map[string]string{"a": "1"}})

	expectEqual(t, got.M, map[string]string{"b": "2"})
}

func TestMapsRejectBadValues(t *testing.T) {
	_, err := load(t, `{"m": {"a": "x"}}`, struct {
		M map[string]int `json:"m"`
	}{})

	expectErrorContaining(t, err, "m")
}

func TestTextUnmarshalers(t *testing.T) {
	type cfg struct {
		When  time.Time        `json:"when"`
		IP    net.IP           `json:"ip"`
		Log   slog.Level       `json:"log"`
		Level level            `json:"level"`
		IPs   []net.IP         `json:"ips"`
		ByKey map[string]level `json:"bykey"`
	}

	got := mustLoad(t, `{
		"when": "2026-01-02T03:04:05Z", "ip": "10.0.0.1", "log": "debug", "level": "high",
		"ips": ["10.0.0.1", "10.0.0.2"], "bykey": {"a": "low", "b": "high"}
	}`, cfg{})

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
	_, err := load(t, `{"level": "extreme"}`, struct {
		Level level `json:"level"`
	}{})

	// A custom type's error often quotes its input, so it is not shown: the key
	// and the type are.
	expectErrorContaining(t, err, "level")

	if strings.Contains(err.Error(), "extreme") {
		t.Fatalf("the error repeats the value: %v", err)
	}

	_, err = load(t, `{"level": 3}`, struct {
		Level level `json:"level"`
	}{})

	expectErrorContaining(t, err, "level")
}

func TestURLs(t *testing.T) {
	got := mustLoad(t, `{"a": "https://example.com/x?y=1", "b": "http://localhost:8080"}`, struct {
		A url.URL  `json:"a"`
		B *url.URL `json:"b"`
	}{})

	if got.A.Host != "example.com" || got.A.Path != "/x" || got.B == nil || got.B.Host != "localhost:8080" {
		t.Fatalf("unexpected urls: %+v %v", got.A, got.B)
	}

	_, err := load(t, `{"a": "http://[::1"}`, struct {
		A url.URL `json:"a"`
	}{})

	expectErrorContaining(t, err, "a")
}

// A struct that knows how to parse itself is read from a string, not walked as a
// nested object.
func TestTextUnmarshalerStructsAreValues(t *testing.T) {
	got := mustLoad(t, `{"when": "2026-01-02T03:04:05Z"}`, struct {
		When time.Time `json:"when"`
	}{})

	if got.When.Year() != 2026 {
		t.Fatalf("expected the time to be parsed as a value, got %v", got.When)
	}
}

type Shared struct {
	Region string `json:"region"`
}

type hidden struct {
	Zone string `json:"zone"`
}

func TestUntaggedEmbeddedStructsAreFlattened(t *testing.T) {
	type cfg struct {
		Shared
		hidden
		Name string `json:"name"`
	}

	got := mustLoad(t, `{"region": "eu", "zone": "a", "name": "svc"}`, cfg{})

	if got.Region != "eu" || got.Zone != "a" || got.Name != "svc" {
		t.Fatalf("expected the embedded fields to load as if declared in the parent, got %+v", got)
	}
}

func TestTaggedEmbeddedStructsAreNested(t *testing.T) {
	type cfg struct {
		Shared `json:"shared"`
	}

	got := mustLoad(t, `{"shared": {"region": "eu"}, "region": "ignored"}`, cfg{})

	if got.Region != "eu" {
		t.Fatalf("expected a tagged embedded struct to be a nested object, got %+v", got)
	}
}

func TestTagOptions(t *testing.T) {
	t.Run("a dash skips the field", func(t *testing.T) {
		got := mustLoad(t, `{"skip": "x", "-": "y"}`, struct {
			Skip string `json:"-"`
		}{Skip: "default"})

		expectEqual(t, got.Skip, "default")
	})

	t.Run("standard json options are accepted", func(t *testing.T) {
		got := mustLoad(t, `{"addr": ":8080"}`, struct {
			Addr string `json:"addr,omitempty"`
		}{})

		expectEqual(t, got.Addr, ":8080")
	})
}

func TestUnsupportedTypesFailEvenWhenNothingIsSet(t *testing.T) {
	type nested struct{ A int }

	tests := map[string]any{
		"chan": &struct {
			F chan int `json:"f"`
		}{},
		"func": &struct {
			F func() `json:"f"`
		}{},
		"complex": &struct {
			F complex128 `json:"f"`
		}{},
		"interface": &struct {
			F any `json:"f"`
		}{},
		"pointer to struct": &struct {
			F *nested `json:"f"`
		}{},
		"slice of chan": &struct {
			F []chan int `json:"f"`
		}{},
		"map of chan": &struct {
			F map[string]chan int `json:"f"`
		}{},
		"array": &struct {
			F [2]string `json:"f"`
		}{},
	}

	for name, dst := range tests {
		t.Run(name, func(t *testing.T) {
			err := json.Raw([]byte(`{"app": {}}`)).Unmarshal([]string{"app"}, dst)

			expectErrorContaining(t, err, "f")
		})
	}
}

func TestUntaggedUnsupportedTypesAreIgnored(t *testing.T) {
	_, err := load(t, `{}`, struct {
		C chan int
		F func()
		A any
	}{})
	if err != nil {
		t.Fatalf("expected untagged fields to be ignored, got %v", err)
	}
}

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

func TestEveryBadValueIsReported(t *testing.T) {
	type cfg struct {
		Port    int           `json:"port"`
		Name    string        `json:"name"`
		Timeout time.Duration `json:"timeout"`
		Ratio   float64       `json:"ratio"`
	}

	var got cfg

	err := json.Raw([]byte(`{"app": {"port": "x", "name": "svc", "timeout": "soon", "ratio": [1]}}`)).Unmarshal([]string{"app"}, &got)

	errs := fieldErrors(err)

	if want := []string{"Port", "Timeout", "Ratio"}; !slices.Equal(fieldsOf(errs), want) {
		t.Fatalf("expected problems with %v, got %v (%v)", want, fieldsOf(errs), err)
	}

	for _, e := range errs {
		if e.Provider != "json" {
			t.Errorf("expected provider json, got %q", e.Provider)
		}
	}

	if errs[0].Key != "app.port" || errs[1].Key != "app.timeout" || errs[2].Key != "app.ratio" {
		t.Fatalf("unexpected keys: %q %q %q", errs[0].Key, errs[1].Key, errs[2].Key)
	}

	if got.Name != "svc" {
		t.Fatalf("expected the valid field to be set, got %q", got.Name)
	}
}

func TestKeysAreTheFullPathIntoTheDocument(t *testing.T) {
	type nested struct {
		Port int `json:"port"`
	}

	type cfg struct {
		Shared
		TLS nested `json:"tls"`
	}

	err := json.Raw([]byte(`{"svc": {"http": {"server": {"tls": {"port": "x"}}}}}`)).Unmarshal([]string{"svc", "http.server"}, &cfg{})

	errs := fieldErrors(err)

	if len(errs) != 1 || errs[0].Field != "TLS.Port" || errs[0].Key != "svc.http.server.tls.port" {
		t.Fatalf("expected a problem at TLS.Port (svc.http.server.tls.port), got %v", errs)
	}
}

func TestProblemsInsideSlicesAndMapsAreReported(t *testing.T) {
	type cfg struct {
		Nums []int          `json:"nums"`
		Map  map[string]int `json:"map"`
	}

	err := json.Raw([]byte(`{"app": {"nums": [1, "x"], "map": {"a": "y"}}}`)).Unmarshal([]string{"app"}, &cfg{})

	if want := []string{"Nums", "Map"}; !slices.Equal(fieldsOf(fieldErrors(err)), want) {
		t.Fatalf("expected problems with %v, got %v", want, err)
	}
}

func TestStructuralProblemsAreReportedWithValueProblems(t *testing.T) {
	type cfg struct {
		Port int      `json:"port"`
		Bad  chan int `json:"bad"`
		Rate int      `json:"rate"`
	}

	// Even with no data for the path at all, a field that can never be set is
	// reported.
	err := json.Raw([]byte(`{"app": {"port": "x", "rate": "y"}}`)).Unmarshal([]string{"app"}, &cfg{})

	errs := fieldErrors(err)

	if want := []string{"Bad", "Port", "Rate"}; !slices.Equal(fieldsOf(errs), want) {
		t.Fatalf("expected problems with %v, got %v", want, fieldsOf(errs))
	}

	if errs[0].Key != "app.bad" {
		t.Fatalf("expected the key of a structural problem to be known, got %q", errs[0].Key)
	}
}

func TestOneProblemIsStillAFieldError(t *testing.T) {
	err := json.Raw([]byte(`{"app": {"port": "x"}}`)).Unmarshal([]string{"app"}, &struct {
		Port int `json:"port"`
	}{})

	var fe *config.FieldError

	if !errors.As(err, &fe) || fe.Field != "Port" {
		t.Fatalf("expected errors.As to find the field error, got %v", err)
	}
}

// A document that can't be read at all is a different kind of problem: there are
// no fields to blame.
func TestUnreadableJSONIsNotAFieldError(t *testing.T) {
	err := json.Raw([]byte(`{not json`)).Unmarshal([]string{"app"}, &struct {
		Port int `json:"port"`
	}{})

	if err == nil || !strings.HasPrefix(err.Error(), "json:") || len(fieldErrors(err)) != 0 {
		t.Fatalf("expected a plain json error, got %v", err)
	}
}

func TestNoProblemsIsNoError(t *testing.T) {
	if err := json.Raw([]byte(`{}`)).Unmarshal([]string{"app"}, &struct {
		Port int `json:"port"`
	}{}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

type addrConfig struct {
	Addr string `json:"addr"`
}

// A node name with a separator in it is looked up as nested objects, the same
// way the env provider turns it into an underscore separated prefix.
func TestRawSplitsNodeNamesOnSeparators(t *testing.T) {
	tests := []struct {
		name string
		path []string
	}{
		{"dot", []string{"svc", "http.server"}},
		{"slash", []string{"svc", "http/server"}},
		{"hyphen", []string{"svc", "http-server"}},
		{"several in one name", []string{"svc", "http.server"}},
		{"split across segments", []string{"svc", "http", "server"}},
		{"case is ignored", []string{"SVC", "Http.SERVER"}},
	}

	data := []byte(`{"svc": {"http": {"server": {"addr": ":8080"}}}}`)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg addrConfig

			if err := json.Raw(data).Unmarshal(tt.path, &cfg); err != nil {
				t.Fatal(err)
			}

			if cfg.Addr != ":8080" {
				t.Fatalf("expected path %q to find the nested value, got %q", tt.path, cfg.Addr)
			}
		})
	}
}

// The old behaviour, a literal "http.server" key, is gone: one rule applies to
// every provider.
func TestRawDoesNotMatchALiteralDottedKey(t *testing.T) {
	var cfg addrConfig

	err := json.Raw([]byte(`{"svc": {"http.server": {"addr": ":8080"}}}`)).Unmarshal([]string{"svc", "http.server"}, &cfg)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Addr != "" {
		t.Fatalf("expected a literal dotted key not to match, got %q", cfg.Addr)
	}
}

// An underscore is not a separator, so it stays part of the key. (The env
// provider can't tell the two apart, as both give an underscore.)
func TestRawKeepsUnderscoresInNodeNames(t *testing.T) {
	var cfg addrConfig

	err := json.Raw([]byte(`{"svc": {"http_server": {"addr": ":8080"}}}`)).Unmarshal([]string{"svc", "http_server"}, &cfg)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Addr != ":8080" {
		t.Fatalf("expected http_server to be a single key, got %q", cfg.Addr)
	}
}

func TestRawMissingNestedLevelIsNotAnError(t *testing.T) {
	var cfg addrConfig

	err := json.Raw([]byte(`{"svc": {"http": {"addr": ":8080"}}}`)).Unmarshal([]string{"svc", "http.server"}, &cfg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if cfg.Addr != "" {
		t.Fatalf("expected nothing to be set, got %q", cfg.Addr)
	}
}

func TestErrorsNeverContainTheDocumentsValue(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Port  int            `json:"port"`
		Small int8           `json:"small"`
		Wait  time.Duration  `json:"wait"`
		Home  url.URL        `json:"home"`
		When  time.Time      `json:"when"`
		Level level          `json:"level"`
		Nums  []int          `json:"nums"`
		Ratio float64        `json:"ratio"`
		Map   map[string]int `json:"map"`
		Frac  int            `json:"frac"`
		Big   uint8          `json:"big"`
	}

	const secret = "hunter2-SECRET"

	err := json.Raw([]byte(`{"svc": {
		"port": "`+secret+`",
		"small": 300,
		"wait": "`+secret+`",
		"home": "https://admin:`+secret+`@[::1",
		"when": "`+secret+`",
		"level": "`+secret+`",
		"nums": [1, "`+secret+`"],
		"ratio": "`+secret+`",
		"map": {"a": "`+secret+`"},
		"frac": 1.5,
		"big": -1
	}}`)).Unmarshal([]string{"svc"}, &cfg{})

	errs := fieldErrors(err)

	if len(errs) != 11 {
		t.Fatalf("expected 11 problems, got %d: %v", len(errs), err)
	}

	for _, e := range errs {
		for _, leaked := range []string{"hunter2", "SECRET", "300", "1.5", "-1"} {
			if strings.Contains(e.Err.Error(), leaked) {
				t.Errorf("the error for %s repeats the value (%q): %q", e.Field, leaked, e.Err)
			}
		}

		if e.Key == "" || !strings.Contains(e.Error(), e.Key) {
			t.Errorf("the error for %s doesn't name its key: %q", e.Field, e)
		}
	}
}
