package flags_test

import (
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/flags"
)

type rootConfig struct {
	Addr    string            `flag:"addr"`
	Port    int               `flag:"port,short=p"`
	Debug   bool              `flag:"debug,short=d"`
	Timeout time.Duration     `flag:"timeout"`
	Ratio   float64           `flag:"ratio"`
	IP      net.IP            `flag:"ip"`
	Tags    []string          `flag:"tags"`
	Labels  map[string]string `flag:"labels"`
	Level   *int              `flag:"level"`

	// Nothing asks for these.
	Plain   string
	Skipped string `flag:"-"`
}

func TestWithNoArgumentsEveryDefaultSurvives(t *testing.T) {
	t.Parallel()

	got := rootConfig{Addr: ":80", Port: 80, Debug: true, Tags: []string{"x"}}

	mustUnmarshal(t, newProvider(), root, &got)

	if got.Addr != ":80" || got.Port != 80 || !got.Debug || !slices.Equal(got.Tags, []string{"x"}) {
		t.Fatalf("defaults were changed: %+v", got)
	}

	if err := newProvider().Check(root); err != nil {
		t.Fatalf("Check with no arguments: %v", err)
	}
}

func TestLongFlagForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"equals", []string{"--addr=:1"}, ":1"},
		{"space", []string{"--addr", ":1"}, ":1"},
		{"empty string is a value", []string{"--addr="}, ""},
		{"the value may hold an equals sign", []string{"--addr=a=b"}, "a=b"},
		{"the value may hold spaces", []string{"--addr", "a b"}, "a b"},
		{"the value may look like a short flag with equals", []string{"--addr=-x"}, "-x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := rootConfig{Addr: "default"}

			p := newProvider(tt.args...)

			mustUnmarshal(t, p, root, &got)

			if got.Addr != tt.want {
				t.Fatalf("Addr = %q, want %q", got.Addr, tt.want)
			}

			if keys := check(t, p); keys != nil {
				t.Fatalf("Check reported %q, want everything claimed", keys)
			}
		})
	}
}

func TestTypesAreParsedLikeEveryOtherProvider(t *testing.T) {
	t.Parallel()

	var got rootConfig

	p := newProvider(
		"--port=8080", "--timeout", "5s", "--ratio=0.25", "--ip=10.0.0.1",
		"--tags=a,b,c", "--labels=k:v,x:y", "--level=3",
	)

	mustUnmarshal(t, p, root, &got)

	if got.Port != 8080 || got.Timeout != 5*time.Second || got.Ratio != 0.25 {
		t.Fatalf("scalars wrong: %+v", got)
	}

	if !got.IP.Equal(net.ParseIP("10.0.0.1")) {
		t.Fatalf("IP = %v", got.IP)
	}

	if !slices.Equal(got.Tags, []string{"a", "b", "c"}) {
		t.Fatalf("Tags = %v", got.Tags)
	}

	if got.Labels["k"] != "v" || got.Labels["x"] != "y" || len(got.Labels) != 2 {
		t.Fatalf("Labels = %v", got.Labels)
	}

	if got.Level == nil || *got.Level != 3 {
		t.Fatalf("Level = %v", got.Level)
	}
}

func TestABoolIsBareOrTakesAnEquals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		args    []string
		def     bool
		want    bool
		invalid bool
	}{
		{args: []string{"--debug"}, want: true},
		{args: []string{"--debug=true"}, want: true},
		{args: []string{"--debug=false"}, def: true, want: false},
		{args: []string{"-d"}, want: true},
		{args: []string{"-d=false"}, def: true, want: false},
		{args: []string{"--debug=maybe"}, def: true, want: true, invalid: true},
		{args: []string{"--debug="}, def: true, want: true, invalid: true},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()

			got := rootConfig{Debug: tt.def}

			err := newProvider(tt.args...).Unmarshal(root, &got)

			if (err != nil) != tt.invalid {
				t.Fatalf("Unmarshal(%q) error = %v, want error: %v", tt.args, err, tt.invalid)
			}

			if got.Debug != tt.want {
				t.Fatalf("Debug = %v, want %v", got.Debug, tt.want)
			}
		})
	}
}

// A bool never takes the next argument: "--debug false" is a flag followed by an
// argument nobody asked for, not "debug is false". Guessing would be wrong
// whichever way it went.
func TestABoolDoesNotTakeTheNextArgument(t *testing.T) {
	t.Parallel()

	var got rootConfig

	p := newProvider("--debug", "false")

	mustUnmarshal(t, p, root, &got)

	if !got.Debug {
		t.Fatal("--debug should have set the bool")
	}

	assertKeys(t, check(t, p), "argument 2")
}

func TestShortFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		port int
	}{
		{"space", []string{"-p", "8080"}, 8080},
		{"equals", []string{"-p=8080"}, 8080},
		{"negative needs equals", []string{"-p=-1"}, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got rootConfig

			p := newProvider(tt.args...)

			mustUnmarshal(t, p, root, &got)

			if got.Port != tt.port {
				t.Fatalf("Port = %d, want %d", got.Port, tt.port)
			}

			if keys := check(t, p); keys != nil {
				t.Fatalf("Check reported %q", keys)
			}
		})
	}
}

func TestShortFlagsAreCaseSensitive(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Port  int `flag:"port,short=p"`
		Proxy int `flag:"proxy,short=P"`
	}

	var got cfg

	mustUnmarshal(t, newProvider("-P", "2", "-p", "1"), root, &got)

	if got.Port != 1 || got.Proxy != 2 {
		t.Fatalf("got %+v, want -p and -P to be different flags", got)
	}
}

// What a long flag could do with "--name value", a short one can't safely: a
// negative number or a following flag look the same as a value.
func TestAValueThatStartsWithADashNeedsAnEquals(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"--port", "-1"},
		{"-p", "-1"},
		{"--addr", "--port=1"},
		{"--addr", "-p"},
	} {
		got := rootConfig{Addr: "d", Port: 7}

		err := newProvider(args...).Unmarshal(root, &got)

		errs := fieldErrors(err)
		if len(errs) == 0 {
			t.Errorf("%q: expected a problem with the flag that has no value", args)

			continue
		}

		if got.Port == -1 {
			t.Errorf("%q: took the next flag as a value", args)
		}
	}
}

func TestAFlagWithNoValueIsAProblem(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"--addr"}, {"--port"}, {"-p"}, {"--timeout"}} {
		err := newProvider(args...).Unmarshal(root, &rootConfig{})

		errs := fieldErrors(err)
		if len(errs) != 1 || errs[0].Provider != "flags" || errs[0].Key != args[0] {
			t.Errorf("%q: got %v, want one problem naming the flag as given", args, err)
		}
	}
}

// For anything but a string an explicit empty value is a mistake, not "unset":
// someone typed it.
func TestAnEmptyValueIsAnErrorUnlessTheFieldIsAString(t *testing.T) {
	t.Parallel()

	got := rootConfig{Port: 7}

	err := newProvider("--port=").Unmarshal(root, &got)

	if len(fieldErrors(err)) != 1 || got.Port != 7 {
		t.Fatalf("got %+v, %v; want a problem and the default kept", got, err)
	}
}

// A value that doesn't parse is reported by field and flag, with the reason and
// never the text: it could be a password typed into the wrong flag.
func TestInvalidValuesNameTheFieldAndFlagButNeverTheValue(t *testing.T) {
	t.Parallel()

	got := rootConfig{Port: 7}

	err := newProvider("--port="+canary, "--timeout", canary, "-p", canary).Unmarshal(root, &got)

	assertNoCanary(t, err)

	errs := fieldErrors(err)

	// --port and -p are the same field given twice, so the field is ambiguous
	// rather than two bad values; --timeout is bad on its own.
	if !slices.Contains(fieldsOf(errs), "Timeout") {
		t.Fatalf("expected a problem with Timeout, got %v", err)
	}

	if got.Port != 7 {
		t.Fatalf("Port = %d, want the default kept", got.Port)
	}

	var only rootConfig

	err = newProvider("--port="+canary).Unmarshal(root, &only)

	errs = fieldErrors(err)
	if len(errs) != 1 {
		t.Fatalf("expected one problem, got %v", err)
	}

	if errs[0].Field != "Port" || errs[0].Key != "--port" || errs[0].Provider != "flags" {
		t.Fatalf("got %+v", errs[0])
	}

	if !errors.Is(errs[0], strconv.ErrSyntax) {
		t.Fatalf("expected the reason to keep strconv.ErrSyntax reachable, got %v", errs[0].Err)
	}

	assertNoCanary(t, err)
}

func TestOutOfRangeNumbersAreAnError(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Small uint8 `flag:"small"`
	}

	var got cfg

	err := newProvider("--small=300").Unmarshal(root, &got)

	if !errors.Is(err, strconv.ErrRange) || got.Small != 0 {
		t.Fatalf("got %+v, %v; want ErrRange and no wrapped value", got, err)
	}
}

// Every problem is reported, and the fields that are fine are still set.
func TestAllProblemsAreReportedAndGoodFieldsAreSet(t *testing.T) {
	t.Parallel()

	var got rootConfig

	err := newProvider("--addr=ok", "--port=x", "--ratio=y", "--debug").Unmarshal(root, &got)

	if got.Addr != "ok" || !got.Debug {
		t.Fatalf("good fields were not set: %+v", got)
	}

	if want := []string{"Port", "Ratio"}; !slices.Equal(fieldsOf(fieldErrors(err)), want) {
		t.Fatalf("problems with %v, want %v (%v)", fieldsOf(fieldErrors(err)), want, err)
	}
}

// Which of two values to use is not defined, so neither is.
func TestAFlagGivenTwiceIsAmbiguous(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		{"--port=1", "--port=2"},
		{"--port", "1", "--port", "2"},
		{"-p", "1", "-p", "2"},
		{"-p", "1", "--port", "2"},
		{"--port=1", "-p=1"},
	}

	for _, args := range tests {
		got := rootConfig{Port: 7}

		err := newProvider(args...).Unmarshal(root, &got)

		errs := fieldErrors(err)
		if len(errs) != 1 || !isAmbiguous(errs[0]) || errs[0].Field != "Port" {
			t.Errorf("%q: got %v, want one ambiguous problem with Port", args, err)
		}

		if got.Port != 7 {
			t.Errorf("%q: Port = %d, want the default kept", args, got.Port)
		}
	}
}

func TestFlagNamesAreExactAndCaseSensitive(t *testing.T) {
	t.Parallel()

	for _, arg := range []string{"--Addr=1", "--ADDR=1", "--ad=1", "--addrs=1", "--addr-=1", "--a=1"} {
		var got rootConfig

		p := newProvider(arg)

		mustUnmarshal(t, p, root, &got)

		if got.Addr != "" {
			t.Errorf("%q set Addr: names are exact, with no prefixes or case folding", arg)
		}

		if keys := check(t, p); len(keys) != 1 {
			t.Errorf("%q: Check reported %q, want the flag as unknown", arg, keys)
		}
	}
}

func TestOnlyTaggedFieldsAreRead(t *testing.T) {
	t.Parallel()

	var got rootConfig

	p := newProvider("--plain=x", "--skipped=y")

	mustUnmarshal(t, p, root, &got)

	if got.Plain != "" || got.Skipped != "" {
		t.Fatalf("untagged or skipped fields were set: %+v", got)
	}

	assertKeys(t, check(t, p), "--plain", "--skipped")
}

// Mistakes in the struct are reported whether or not an argument names the field,
// as they are for the other providers.
func TestAFieldOfAnUnsupportedTypeIsAProblemEvenWithNoArguments(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Bad chan int `flag:"bad"`
	}

	err := newProvider().Unmarshal(root, &cfg{})

	errs := fieldErrors(err)
	if len(errs) != 1 || errs[0].Field != "Bad" {
		t.Fatalf("got %v, want one problem with Bad", err)
	}
}

func TestNestedStructsFlattenIntoHyphenatedNames(t *testing.T) {
	t.Parallel()

	type db struct {
		Host string `flag:"host"`
		Port int    `flag:"port"`
	}

	type shared struct {
		Region string `flag:"region"`
	}

	type cfg struct {
		shared

		DB db `flag:"db"`
	}

	var got cfg

	p := newProvider("--db-host=h", "--db-port", "5432", "--region=eu")

	mustUnmarshal(t, p, root, &got)

	if got.DB.Host != "h" || got.DB.Port != 5432 || got.Region != "eu" {
		t.Fatalf("got %+v", got)
	}

	if keys := check(t, p); keys != nil {
		t.Fatalf("Check reported %q", keys)
	}
}

func TestUnmarshalNeedsAPointerToAStruct(t *testing.T) {
	t.Parallel()

	for _, dst := range []any{nil, 1, rootConfig{}, (*rootConfig)(nil), new(int)} {
		if err := newProvider("--addr=1").Unmarshal(root, dst); err == nil {
			t.Errorf("Unmarshal(%T) = nil, want an error", dst)
		}
	}
}

// Errors from flags are ordinary field errors, so Load can report them like any
// other provider's.
func TestProblemsAreFieldErrors(t *testing.T) {
	t.Parallel()

	err := newProvider("--port=x").Unmarshal(root, &rootConfig{})

	var fe *config.FieldError

	if !errors.As(err, &fe) || fe.Provider != "flags" {
		t.Fatalf("got %v, want a *config.FieldError from flags", err)
	}
}

// A mistake in a tag is a problem at load time, whether or not an argument names
// the field, so it shows on the first run and not when someone finally uses it.
func TestBadTagsAreProblems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dst  any
	}{
		{"empty name", &struct {
			F string `flag:""`
		}{}},
		{"upper case name", &struct {
			F string `flag:"Port"`
		}{}},
		{"leading dash", &struct {
			F string `flag:"-port"`
		}{}},
		{"two dashes", &struct {
			F string `flag:"--port"`
		}{}},
		{"trailing hyphen", &struct {
			F string `flag:"port-"`
		}{}},
		{"underscore", &struct {
			F string `flag:"po_rt"`
		}{}},
		{"space", &struct {
			F string `flag:"a b"`
		}{}},
		{"equals sign", &struct {
			F string `flag:"a=b"`
		}{}},
		{"leading digit", &struct {
			F string `flag:"1port"`
		}{}},
		{"non ASCII", &struct {
			F string `flag:"pört"`
		}{}},
		{"control character", &struct {
			F string `flag:"a\nb"`
		}{}},
		{"too long", &struct {
			F string `flag:"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`
		}{}},
		{"unknown option", &struct {
			F string `flag:"port,alias=p"`
		}{}},
		{"short without a name", &struct {
			F string `flag:"short=p"`
		}{}},
		{"short twice", &struct {
			F string `flag:"port,short=p,short=q"`
		}{}},
		{"empty short", &struct {
			F string `flag:"port,short="`
		}{}},
		{"long short", &struct {
			F string `flag:"port,short=pp"`
		}{}},
		{"digit short", &struct {
			F string `flag:"port,short=1"`
		}{}},
		{"dash short", &struct {
			F string `flag:"port,short=-"`
		}{}},
		{"non ASCII short", &struct {
			F string `flag:"port,short=é"`
		}{}},
		{"equals short", &struct {
			F string `flag:"port,short=="`
		}{}},
		{"short on a group", &struct {
			F struct {
				X string `flag:"x"`
			} `flag:"group,short=g"`
		}{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := newProvider().Unmarshal(root, tt.dst)

			errs := fieldErrors(err)
			if len(errs) != 1 || errs[0].Provider != "flags" || errs[0].Field != "F" {
				t.Fatalf("got %v, want one problem with F from flags", err)
			}

			// The problem is about the tag, which is code, but it is never longer than
			// a log line should be.
			if len(errs[0].Error()) > 300 {
				t.Fatalf("error is %d bytes long", len(errs[0].Error()))
			}
		})
	}
}

func TestSkippingAField(t *testing.T) {
	t.Parallel()

	type cfg struct {
		F string `flag:"-"`
	}

	got := cfg{F: "d"}

	p := newProvider("--f=x")

	mustUnmarshal(t, p, root, &got)

	if got.F != "d" {
		t.Fatalf("F = %q, want a skipped field left alone", got.F)
	}

	assertKeys(t, check(t, p), "--f")
}

func TestTwoFieldsCannotShareAFlagName(t *testing.T) {
	t.Parallel()

	type cfg struct {
		A string `flag:"name"`
		B string `flag:"name"`
	}

	var got cfg

	err := newProvider("--name=x").Unmarshal(root, &got)

	errs := fieldErrors(err)
	if len(errs) != 1 || errs[0].Field != "B" {
		t.Fatalf("got %v, want the later field to be the problem", err)
	}

	if got.A != "x" {
		t.Fatalf("A = %q, want the first field to keep the flag", got.A)
	}
}

func TestTwoFieldsCannotShareAShortFlag(t *testing.T) {
	t.Parallel()

	type cfg struct {
		A string `flag:"alpha,short=a"`
		B string `flag:"beta,short=a"`
	}

	var got cfg

	err := newProvider("-a=x").Unmarshal(root, &got)

	errs := fieldErrors(err)
	if len(errs) != 1 || errs[0].Field != "B" || !strings.Contains(errs[0].Error(), "-a") {
		t.Fatalf("got %v, want the later field to be the problem, naming -a", err)
	}

	if got.A != "x" || got.B != "" {
		t.Fatalf("got %+v, want only the first field to read -a", got)
	}
}

// A group's names are hyphenated onto its fields', so they can collide with a
// flag of the same spelling.
func TestAGroupsFlagCanCollideWithAPlainOne(t *testing.T) {
	t.Parallel()

	type db struct {
		Host string `flag:"host"`
	}

	type cfg struct {
		DB   db     `flag:"db"`
		Flat string `flag:"db-host"`
	}

	err := newProvider("--db-host=x").Unmarshal(root, &cfg{})

	errs := fieldErrors(err)
	if len(errs) != 1 || errs[0].Field != "Flat" {
		t.Fatalf("got %v, want the later field to be the problem", err)
	}
}

// Flags are read from argv, which anyone on the machine can see (ps, /proc, shell
// history), so a field that is a secret read from a file can't be one.
func TestAFieldReadFromAFileCannotBeAFlag(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Password string `env:"PASSWORD,file" flag:"password"`
	}

	var got cfg

	err := newProvider("--password="+canary).Unmarshal(root, &got)

	errs := fieldErrors(err)
	if len(errs) != 1 || errs[0].Field != "Password" || errs[0].Provider != "flags" {
		t.Fatalf("got %v, want one problem with Password from flags", err)
	}

	if got.Password != "" {
		t.Fatal("the value was set although it is refused")
	}

	assertNoCanary(t, err)
}

// An env tag without the file option has nothing to do with it.
func TestOtherTagsAreLeftAlone(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Addr string `env:"ADDR,alias=ADDR" json:"addr" flag:"addr"`
	}

	var got cfg

	mustUnmarshal(t, newProvider("--addr=x"), root, &got)

	if got.Addr != "x" {
		t.Fatalf("got %+v", got)
	}
}

func TestNestedProblemsNameThePathToTheField(t *testing.T) {
	t.Parallel()

	type db struct {
		Port int `flag:"port"`
	}

	type cfg struct {
		DB db `flag:"db"`
	}

	err := newProvider("--db-port=x").Unmarshal(root, &cfg{})

	errs := fieldErrors(err)
	if len(errs) != 1 || errs[0].Field != "DB.Port" || errs[0].Key != "--db-port" {
		t.Fatalf("got %v, want DB.Port and --db-port", err)
	}
}

// The flags provider is a provider like the others: it can say where it would
// look, and which of its arguments nothing asked for.
var (
	_ config.Provider  = (*flags.Provider)(nil)
	_ config.Describer = (*flags.Provider)(nil)
	_ config.Checker   = (*flags.Provider)(nil)
)

// canary is a value that must never appear in an error: a password given to the
// wrong flag, or to one that doesn't exist, would otherwise reach a log.
const canary = "hunter2-LEAK-9f3a"

// root is the path of the root function; its flags have no marker.
var root = []string{"svc"}

func newProvider(args ...string) *flags.Provider {
	return flags.New(flags.WithArgs(args))
}

// fieldErrors lists every *config.FieldError in err, however deeply joined.
func fieldErrors(err error) []*config.FieldError {
	var out []*config.FieldError

	var walk func(error)

	walk = func(e error) {
		if e == nil {
			return
		}

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

func keysOf(errs []*config.FieldError) []string {
	var out []string

	for _, fe := range errs {
		out = append(out, fe.Key)
	}

	return out
}

func fieldsOf(errs []*config.FieldError) []string {
	var out []string

	for _, fe := range errs {
		out = append(out, fe.Field)
	}

	return out
}

// check asks the provider which arguments nothing asked for and returns the keys
// it names, in the order given, failing the test if an error isn't what a flags
// problem should look like.
func check(t *testing.T, p *flags.Provider) []string {
	t.Helper()

	err := p.Check(root)
	if err == nil {
		return nil
	}

	var keys []string

	for _, fe := range fieldErrors(err) {
		if fe.Provider != "flags" {
			t.Errorf("provider = %q, want flags", fe.Provider)
		}

		if !utf8.ValidString(fe.Key) {
			t.Errorf("key %q is not valid UTF-8", fe.Key)
		}

		keys = append(keys, fe.Key)
	}

	if len(keys) == 0 {
		t.Fatalf("Check returned %v, which holds no field error", err)
	}

	return keys
}

func mustUnmarshal(t *testing.T, p *flags.Provider, path []string, dst any) {
	t.Helper()

	if err := p.Unmarshal(path, dst); err != nil {
		t.Fatalf("Unmarshal(%v): %v", path, err)
	}
}

func assertKeys(t *testing.T, got []string, want ...string) {
	t.Helper()

	if !slices.Equal(got, want) {
		t.Fatalf("keys = %q, want %q", got, want)
	}
}

func assertNoCanary(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		return
	}

	if s := err.Error(); containsCanary(s) {
		t.Fatalf("error contains a value: %q", s)
	}

	for _, fe := range fieldErrors(err) {
		if containsCanary(fe.Key) || containsCanary(fe.Field) || containsCanary(fe.Error()) {
			t.Fatalf("field error contains a value: %+v", fe)
		}
	}
}

func containsCanary(s string) bool {
	return strings.Contains(s, canary)
}

func isAmbiguous(err error) bool {
	return errors.Is(err, config.ErrAmbiguous)
}

// A flag is on a command line, where every user of the machine can see it and
// shell history keeps it, and it carries a short form and help. Whether a field
// is one is its own decision: the common `config` name, which the environment and
// files read, never makes a field a flag.
func TestACommonNameDoesNotMakeAFlag(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Addr     string `config:"addr"`
		Password string `config:"password" env:",file"`
		Port     int    `config:"port" flag:"port"`
	}

	p := newProvider("--addr", "x", "--password", canary, "--port", "9")

	var got cfg

	// The name is not one the provider knows, so it is a mistake, as for any flag
	// that doesn't exist, and nothing is set from it.
	err := p.Unmarshal(root, &got)

	if err == nil {
		err = p.Check(root)
	}

	if err == nil {
		t.Fatal("expected --addr and --password to be refused")
	}

	if got.Addr != "" || got.Password != "" {
		t.Fatalf("a field without a flag tag was set: %+v", got)
	}

	if strings.Contains(err.Error(), canary) {
		t.Fatalf("the error repeats a value: %v", err)
	}
}

// Help lists flags, and so a field with only a common name isn't in it.
func TestACommonNameIsNotInTheFlagsDescribed(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Addr string `config:"addr"`
		Port int    `config:"port" flag:"port"`
	}

	keys, err := newProvider().Describe(root, &cfg{})
	if err != nil {
		t.Fatal(err)
	}

	if len(keys) != 1 || keys[0].Field != "Port" {
		t.Fatalf("keys = %+v, want only Port", keys)
	}
}
