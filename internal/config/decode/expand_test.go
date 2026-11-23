package decode_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.krak3n.dev/foundation/internal/config/decode"
)

func lookupFrom(vars map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := vars[name]

		return v, ok
	}
}

var expandVars = map[string]string{
	"A":       "1",
	"B":       "2",
	"EMPTY":   "",
	"PATH":    "/usr/bin",
	"TRICKY":  "${A}",
	"DOLLARS": "$$",
}

func TestExpand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text", "plain", "plain"},
		{"empty", "", ""},
		{"one reference", "${A}", "1"},
		{"text around references", "x${A}y${B}z", "x1y2z"},
		{"adjacent references", "${A}${B}", "12"},
		{"a variable set to empty", "x${EMPTY}y", "xy"},
		{"a value with a slash", "${PATH}/bin", "/usr/bin/bin"},

		{"default when unset", "${UNSET:-def}", "def"},
		{"default when empty", "${EMPTY:-def}", "def"},
		{"default ignored when set", "${A:-def}", "1"},
		{"empty default", "${UNSET:-}", ""},
		{"default with punctuation", "${UNSET:-a:b/c d}", "a:b/c d"},
		{"default that is a url", "${UNSET:-http://h:80}", "http://h:80"},
		{"default among text", "a-${UNSET:-b}-c", "a-b-c"},

		{"escaped dollar", "$$", "$"},
		{"escaped dollar in text", "a$$b", "a$b"},
		{"two escaped dollars", "$$$$", "$$"},
		{"escaped reference", "$${A}", "${A}"},
		{"escaped dollar then reference", "$$${A}", "$1"},

		{"bare name is literal", "$A", "$A"},
		{"lone dollar", "$", "$"},
		{"dollar before a digit", "cost $5", "cost $5"},
		{"trailing dollar", "a$", "a$"},
		{"braces without a dollar", "{A}", "{A}"},

		// A value is never scanned again, so what a variable holds can't trigger
		// another lookup.
		{"a value that looks like a reference", "${TRICKY}", "${A}"},
		{"a value that looks like an escape", "${DOLLARS}", "$$"},

		// A default is literal text up to the first brace, so there is no nesting.
		{"no nesting in a default", "${UNSET:-${A}}", "${A}"},

		{"unicode is kept", "日本${A}é", "日本1é"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, undefined, err := decode.Expand(tt.in, lookupFrom(expandVars))
			if err != nil || len(undefined) != 0 {
				t.Fatalf("unexpected problems: undefined=%v err=%v", undefined, err)
			}

			if got != tt.want {
				t.Fatalf("Expand(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExpandReportsEveryUndefinedVariableOnce(t *testing.T) {
	t.Parallel()

	_, undefined, err := decode.Expand("${X}-${A}-${Y}-${X}-${Z:-ok}", lookupFrom(expandVars))
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"X", "Y"}; !slices.Equal(undefined, want) {
		t.Fatalf("undefined = %v, want %v", undefined, want)
	}
}

func TestExpandAnEmptyVariableIsDefinedWithoutADefault(t *testing.T) {
	t.Parallel()

	_, undefined, _ := decode.Expand("${EMPTY}", lookupFrom(expandVars))

	if len(undefined) != 0 {
		t.Fatalf("a variable that is set to empty is defined, got %v", undefined)
	}
}

func TestExpandSyntaxErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{"${A", "unterminated"},
		{"x ${", "unterminated"},
		{"${A:-x", "unterminated"},
		{"${}", "empty"},
		{"${:-x}", "empty"},
		{"${1A}", "invalid"},
		{"${A-B}", "invalid"},
		{"${A b}", "invalid"},
		{"${A:B}", "invalid"},
		{"${A.B}", "invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()

			_, _, err := decode.Expand(tt.in, lookupFrom(expandVars))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Expand(%q) error = %v, want one saying %q", tt.in, err, tt.want)
			}
		})
	}
}

func TestExpandNamesAreLettersDigitsAndUnderscores(t *testing.T) {
	t.Parallel()

	vars := map[string]string{"_A1": "x", "a_b": "y", "Z9": "z"}

	got, undefined, err := decode.Expand("${_A1}${a_b}${Z9}", lookupFrom(vars))
	if err != nil || len(undefined) != 0 || got != "xyz" {
		t.Fatalf("got %q, undefined=%v, err=%v", got, undefined, err)
	}
}

func TestExpandDoesNotLookAnythingUpForPlainText(t *testing.T) {
	t.Parallel()

	lookup := func(string) (string, bool) {
		t.Error("unexpected lookup")

		return "", false
	}

	for _, s := range []string{"plain", "$$", "$A", "$", "{A}", "cost $5"} {
		if _, _, err := decode.Expand(s, lookup); err != nil {
			t.Fatal(err)
		}
	}
}

// Expansion is a single pass over the text, so a lot of references is not slow.
func TestExpandIsLinear(t *testing.T) {
	t.Parallel()

	in := strings.Repeat("${A}", 300_000)

	got, _, err := decode.Expand(in, lookupFrom(expandVars))
	if err != nil || len(got) != 300_000 {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
}

type name string

func expandable(t *testing.T, dst any) ([]decode.ExpandField, []decode.Problem) {
	t.Helper()

	return decode.Expandable(reflect.ValueOf(dst).Elem())
}

func pathsOf(fields []decode.ExpandField) []string {
	var out []string

	for _, f := range fields {
		out = append(out, f.Path)
	}

	return out
}

func TestExpandableListsTaggedStringsInOrder(t *testing.T) {
	type cfg struct {
		DSN    string  `config:"expand"`
		Skip   string  `config:""`
		Addr   *string `config:"expand"`
		Name   name    `config:"expand"`
		Req    string  `config:"required"`
		Both   string  `config:"required,expand"`
		Spaced string  `config:"required, expand"`
	}

	fields, problems := expandable(t, &cfg{})

	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}

	if want := []string{"DSN", "Addr", "Name", "Both", "Spaced"}; !slices.Equal(pathsOf(fields), want) {
		t.Fatalf("fields = %v, want %v", pathsOf(fields), want)
	}

	for _, f := range fields {
		if !f.Value.CanSet() {
			t.Errorf("%s is not settable", f.Path)
		}
	}
}

func TestExpandableWalksGroupsAndEmbeddedStructs(t *testing.T) {
	type tls struct {
		CertFile string `config:"expand"`
	}

	type shared struct {
		Region string `config:"expand"`
	}

	type cfg struct {
		shared

		TLS tls
		Sub struct {
			Deep struct {
				Name string `config:"expand"`
			}
		}
	}

	fields, problems := expandable(t, &cfg{})

	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}

	if want := []string{"Region", "TLS.CertFile", "Sub.Deep.Name"}; !slices.Equal(pathsOf(fields), want) {
		t.Fatalf("fields = %v, want %v", pathsOf(fields), want)
	}
}

func TestExpandableOnlyAppliesToStrings(t *testing.T) {
	type group struct {
		A string
	}

	type cfg struct {
		Port  int      `config:"expand"`
		Tags  []string `config:"expand"`
		Group group    `config:"expand"`
		On    bool     `config:"expand"`
		Ok    string   `config:"expand"`
	}

	fields, problems := expandable(t, &cfg{})

	var got []string

	for _, p := range problems {
		if p.Err == nil {
			t.Errorf("problem for %s has no error", p.GoName)
		}

		got = append(got, p.GoName)
	}

	if want := []string{"Port", "Tags", "Group", "On"}; !slices.Equal(got, want) {
		t.Fatalf("problems for %v, want %v", got, want)
	}

	if !strings.Contains(problems[0].Err.Error(), "string") {
		t.Errorf("expected the problem to say expand is for strings, got %q", problems[0].Err)
	}

	if want := []string{"Ok"}; !slices.Equal(pathsOf(fields), want) {
		t.Fatalf("fields = %v, want %v", pathsOf(fields), want)
	}
}

// Whether a field is read from a file is for the providers to say (see
// config.FileBacked), not for the tag walker to guess from one provider's tag, so
// Expandable lists such a field like any other string.
func TestExpandableDoesNotJudgeProviderTags(t *testing.T) {
	type cfg struct {
		Password string `env:"PASSWORD,file" config:"expand"`
		Plain    string `env:"PLAIN" config:"expand"`
		Other    string `json:"other,omitempty" config:"expand"`
	}

	fields, problems := expandable(t, &cfg{})

	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}

	if want := []string{"Password", "Plain", "Other"}; !slices.Equal(pathsOf(fields), want) {
		t.Fatalf("fields = %v, want %v", pathsOf(fields), want)
	}
}

// Required reports mistakes in the tag itself; Expandable must not repeat them,
// or each would be reported twice.
func TestExpandableLeavesTagMistakesToRequired(t *testing.T) {
	type cfg struct {
		A string `config:"expand,wat"`
		B string `config:"Expand"`
		C string `config:"expand"`
	}

	fields, problems := expandable(t, &cfg{})

	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}

	if want := []string{"C"}; !slices.Equal(pathsOf(fields), want) {
		t.Fatalf("fields = %v, want %v", pathsOf(fields), want)
	}

	_, mistakes := required(t, &cfg{})

	if len(mistakes) != 2 {
		t.Fatalf("expected Required to report the two tag mistakes, got %v", mistakes)
	}
}

func TestExpandableSkipsUnexportedFields(t *testing.T) {
	type cfg struct {
		hidden string `config:"expand"` //lint:ignore U1000 never read: it is here to show that an unexported field is left alone
	}

	fields, problems := expandable(t, &cfg{})

	if len(fields) != 0 || len(problems) != 0 {
		t.Fatalf("fields=%v problems=%v", fields, problems)
	}
}

// expand is a valid option for the required walker, which ignores it.
func TestRequiredAcceptsTheExpandOption(t *testing.T) {
	type cfg struct {
		A string `config:"required,expand"`
		B string `config:"expand"`
		C string `config:"expand,required"`
	}

	missing, problems := required(t, &cfg{})

	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}

	if want := []string{"A", "C"}; !slices.Equal(missing, want) {
		t.Fatalf("missing = %v, want %v", missing, want)
	}
}
