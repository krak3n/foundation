package yaml_test

import (
	"errors"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/config/yaml"
	"go.krak3n.dev/foundation/foundationtest"
	"go.krak3n.dev/foundation/internal/config/confdir"
)

var (
	_ config.Provider = (*yaml.FileProvider)(nil)
	_ config.Provider = (*yaml.RawProvider)(nil)

	_ config.Describer = (*yaml.FileProvider)(nil)
	_ config.Describer = (*yaml.RawProvider)(nil)

	_ config.Checker = (*yaml.FileProvider)(nil)
	_ config.Checker = (*yaml.RawProvider)(nil)
)

func TestRawReadsBasicTypes(t *testing.T) {
	t.Parallel()

	var cfg struct {
		Addr    string        `config:"addr"`
		Port    int           `config:"port"`
		Debug   bool          `config:"debug"`
		Rate    float64       `config:"rate"`
		Timeout time.Duration `config:"timeout"`
	}

	err := yaml.Raw([]byte(`
# a comment
app:
  server:
    addr: localhost   # another
    port: 8080
    debug: true
    rate: 1.5
    timeout: 30s
`)).Unmarshal([]string{"app", "server"}, &cfg)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Addr != "localhost" || cfg.Port != 8080 || !cfg.Debug || cfg.Rate != 1.5 || cfg.Timeout != 30*time.Second {
		t.Fatalf("got %+v", cfg)
	}
}

func TestFlowStylesAndQuotingAreRead(t *testing.T) {
	t.Parallel()

	var cfg struct {
		A string `config:"a"`
		B string `config:"b"`
		C string `config:"c"`
		D string `config:"d"`
		E string `config:"e"`
		F string `config:"f"`
	}

	err := yaml.Raw([]byte("svc:\n  a: \"x y\"\n  b: 'it''s'\n  c: \"tab\\there\"\n  d: |\n    literal\n    text\n  e: >-\n    folded\n    text\n  f: \"unicode \\u00e9 é\"\n")).Unmarshal([]string{"svc"}, &cfg)
	if err != nil {
		t.Fatal(err)
	}

	want := [6]string{"x y", "it's", "tab\there", "literal\ntext\n", "folded text", "unicode é é"}

	if got := [6]string{cfg.A, cfg.B, cfg.C, cfg.D, cfg.E, cfg.F}; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	var flow struct {
		Addr string `config:"addr"`
		Port int    `config:"port"`
	}

	if err := yaml.Raw([]byte(`{svc: {addr: ":1", port: 2}}`)).Unmarshal([]string{"svc"}, &flow); err != nil || flow.Addr != ":1" || flow.Port != 2 {
		t.Fatalf("flow style: %+v, %v", flow, err)
	}
}

func TestOnlyFieldsInTheDocumentAreSet(t *testing.T) {
	t.Parallel()

	cfg := struct {
		Addr string `config:"addr"`
		Port int    `config:"port"`
	}{Addr: "default", Port: 3000}

	if err := yaml.Raw([]byte("app:\n  addr: override\n")).Unmarshal([]string{"app"}, &cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.Addr != "override" || cfg.Port != 3000 {
		t.Fatalf("got %+v", cfg)
	}
}

func TestThePathIsCaseInsensitiveAndSplitLikeTheOthers(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Addr string `config:"addr"`
	}

	tests := []struct {
		name string
		path []string
		doc  string
	}{
		{"case", []string{"app", "http", "server"}, "App:\n  HTTP:\n    Server:\n      ADDR: x\n"},
		{"dot", []string{"app", "http.server"}, "app:\n  http:\n    server:\n      addr: x\n"},
		{"slash", []string{"app", "http/server"}, "app:\n  http:\n    server:\n      addr: x\n"},
		{"hyphen", []string{"app", "http-server"}, "app:\n  http:\n    server:\n      addr: x\n"},
		{"underscore", []string{"app", "http_server"}, "app:\n  http_server:\n    addr: x\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got cfg

			if err := yaml.Raw([]byte(tt.doc)).Unmarshal(tt.path, &got); err != nil || got.Addr != "x" {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}

	// A literal dotted key is not the same as nesting.
	var got cfg

	if err := yaml.Raw([]byte("\"app.addr\": x\n")).Unmarshal([]string{"app"}, &got); err != nil || got.Addr != "" {
		t.Fatalf("a dotted key was matched: %+v, %v", got, err)
	}
}

func TestAMissingLevelIsNotAnError(t *testing.T) {
	t.Parallel()

	cfg := struct {
		Addr string `config:"addr"`
	}{Addr: "default"}

	for _, doc := range []string{"", "# only a comment\n", "other: {addr: x}\n", "app:\n", "app: null\n", "app: ~\n", "app: {}\n", "app:\n  http: {}\n"} {
		if err := yaml.Raw([]byte(doc)).Unmarshal([]string{"app", "http", "server"}, &cfg); err != nil || cfg.Addr != "default" {
			t.Errorf("%q: got %+v, %v", doc, cfg, err)
		}
	}
}

// Something that isn't a mapping where a mapping should be is a mistake in the
// file, not a missing level to be skipped: a service that quietly ignored it
// would run on defaults.
func TestAScalarWhereAMappingBelongsIsAnError(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Addr string `config:"addr"`
		TLS  struct {
			CertFile string `config:"cert_file"`
		} `config:"tls"`
	}

	for _, tt := range []struct {
		name, doc string
		path      []string
		field     string
	}{
		{"top level scalar", "just text\n", []string{"svc"}, ""},
		{"top level sequence", "- a\n- b\n", []string{"svc"}, ""},
		{"a level on the path", "svc: 5\n", []string{"svc"}, ""},
		{"a level further down", "svc:\n  http: [1]\n", []string{"svc", "http"}, ""},
		{"a group", "svc:\n  tls: 5\n", []string{"svc"}, "TLS"},
		{"a group as a sequence", "svc:\n  tls: [a]\n", []string{"svc"}, "TLS"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got cfg

			err := yaml.Raw([]byte(tt.doc)).Unmarshal(tt.path, &got)
			if err == nil {
				t.Fatal("expected an error")
			}

			if tt.field != "" && !slices.Equal(fieldsOf(fieldErrors(err)), []string{tt.field}) {
				t.Fatalf("expected a problem with %s, got %v", tt.field, err)
			}
		})
	}
}

// What the file says is the text of the scalar, not what YAML 1.1 would have made
// of it. A string field gets exactly what was written.
func TestStringsAreTheTextAsWritten(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		"no", "yes", "on", "off", "y", "n", "No", "NO", "true", "false",
		"0755", "0o17", "0x10", "1_000", "1.0", "1e3", ".5", "+1", "-0",
		".inf", "-.inf", ".nan", "1:30", "2001-12-14", "2001-12-14T21:59:43Z",
		"0.10", "00", "1,000", "<<", "=",
	} {
		var got struct {
			S string `config:"s"`
		}

		if err := yaml.Raw([]byte("svc:\n  s: "+text+"\n")).Unmarshal([]string{"svc"}, &got); err != nil || got.S != text {
			t.Errorf("%s: got %q, %v", text, got.S, err)
		}
	}
}

func TestQuotedNullIsAString(t *testing.T) {
	t.Parallel()

	for doc, want := range map[string]string{
		`s: "null"`: "null",
		`s: 'null'`: "null",
		`s: "~"`:    "~",
		`s: ""`:     "",
		`s: ''`:     "",
	} {
		got := struct {
			S string `config:"s"`
		}{S: "default"}

		if err := yaml.Raw([]byte("svc:\n  "+doc+"\n")).Unmarshal([]string{"svc"}, &got); err != nil || got.S != want {
			t.Errorf("%s: got %q, %v", doc, got.S, err)
		}
	}
}

// An unquoted null, ~ or nothing at all leaves the field as it was, for every
// type, as in the JSON provider.
func TestAnUnquotedNullLeavesTheFieldAlone(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"null", "~", ""} {
		got := struct {
			S string        `config:"s"`
			I int           `config:"i"`
			B bool          `config:"b"`
			D time.Duration `config:"d"`
			L []string      `config:"l"`
		}{S: "s", I: 7, B: true, D: time.Second, L: []string{"x"}}

		doc := "svc:\n  s: " + value + "\n  i: " + value + "\n  b: " + value + "\n  d: " + value + "\n  l: " + value + "\n"

		if err := yaml.Raw([]byte(doc)).Unmarshal([]string{"svc"}, &got); err != nil {
			t.Fatalf("%q: %v", value, err)
		}

		if got.S != "s" || got.I != 7 || !got.B || got.D != time.Second || !slices.Equal(got.L, []string{"x"}) {
			t.Errorf("%q: the defaults were changed: %+v", value, got)
		}
	}
}

func TestBoolsAreTrueOrFalseAndNothingElse(t *testing.T) {
	t.Parallel()

	type cfg struct {
		B bool `config:"b"`
	}

	for doc, want := range map[string]bool{"true": true, "false": false, `"true"`: true, `'false'`: false} {
		got := cfg{B: !want}

		if err := yaml.Raw([]byte("svc:\n  b: "+doc+"\n")).Unmarshal([]string{"svc"}, &got); err != nil || got.B != want {
			t.Errorf("%s: got %+v, %v", doc, got, err)
		}
	}

	// YAML 1.1 says several of these are booleans, so another tool may read the
	// same file as true. Here they are mistakes.
	for _, doc := range []string{"yes", "no", "on", "off", "y", "n", "Yes", "NO", "1", "0", "t", "f", "True", "TRUE", "False", "FALSE", "2", "maybe"} {
		got := cfg{B: true}

		err := yaml.Raw([]byte("svc:\n  b: "+doc+"\n")).Unmarshal([]string{"svc"}, &got)

		if errs := fieldErrors(err); len(errs) != 1 || errs[0].Field != "B" || errs[0].Key != "svc.b" {
			t.Errorf("%s: expected one problem with B, got %v", doc, err)
		}

		if !got.B {
			t.Errorf("%s: the field was changed", doc)
		}
	}
}

func TestIntegersAreBase10(t *testing.T) {
	t.Parallel()

	type cfg struct {
		I int `config:"i"`
	}

	for doc, want := range map[string]int{"8080": 8080, `"8080"`: 8080, "-5": -5, "+7": 7, "0755": 755, "00": 0} {
		var got cfg

		if err := yaml.Raw([]byte("svc:\n  i: "+doc+"\n")).Unmarshal([]string{"svc"}, &got); err != nil || got.I != want {
			t.Errorf("%s: got %+v, %v", doc, got, err)
		}
	}

	for _, doc := range []string{"0x10", "0o17", "0b1", "1_000", "1e3", "1.5", "8080.0", ".inf", ".nan", "abc", "9223372036854775808", "1,000", "'  5'", "true", "[1]", "{a: 1}"} {
		got := cfg{I: 9}

		err := yaml.Raw([]byte("svc:\n  i: "+doc+"\n")).Unmarshal([]string{"svc"}, &got)

		if errs := fieldErrors(err); len(errs) != 1 || errs[0].Field != "I" {
			t.Errorf("%s: expected one problem with I, got %v", doc, err)
		}

		if got.I != 9 {
			t.Errorf("%s: the field was changed to %d", doc, got.I)
		}
	}
}

func TestSizedIntegersRefuseWhatDoesNotFit(t *testing.T) {
	t.Parallel()

	var got struct {
		Small int8   `config:"small"`
		Big   uint8  `config:"big"`
		Wide  uint64 `config:"wide"`
	}

	err := yaml.Raw([]byte("svc:\n  small: 300\n  big: -1\n  wide: 18446744073709551615\n")).Unmarshal([]string{"svc"}, &got)

	if !slices.Equal(fieldsOf(fieldErrors(err)), []string{"Small", "Big"}) {
		t.Fatalf("got %v", err)
	}

	if got.Wide != 18446744073709551615 {
		t.Fatalf("Wide = %d", got.Wide)
	}
}

func TestFloatsAndDurations(t *testing.T) {
	t.Parallel()

	var got struct {
		F float64       `config:"f"`
		G float32       `config:"g"`
		D time.Duration `config:"d"`
		E time.Duration `config:"e"`
	}

	if err := yaml.Raw([]byte("svc:\n  f: 1e3\n  g: 2\n  d: 1h30m\n  e: \"250ms\"\n")).Unmarshal([]string{"svc"}, &got); err != nil {
		t.Fatal(err)
	}

	if got.F != 1000 || got.G != 2 || got.D != 90*time.Minute || got.E != 250*time.Millisecond {
		t.Fatalf("got %+v", got)
	}

	// A duration is written with its unit. A bare number is not a number of
	// nanoseconds here: it would be easy to mean seconds.
	for _, doc := range []string{"30", "30x", "abc", "[1]"} {
		var d struct {
			D time.Duration `config:"d"`
		}

		err := yaml.Raw([]byte("svc:\n  d: "+doc+"\n")).Unmarshal([]string{"svc"}, &d)

		if errs := fieldErrors(err); len(errs) != 1 || errs[0].Field != "D" {
			t.Errorf("%s: expected one problem with D, got %v", doc, err)
		}
	}
}

func TestTextUnmarshalersAndURLs(t *testing.T) {
	t.Parallel()

	var got struct {
		When time.Time `config:"when"`
		IP   net.IP    `config:"ip"`
		Home url.URL   `config:"home"`
		Next *url.URL  `config:"next"`
	}

	if err := yaml.Raw([]byte("svc:\n  when: 2024-05-06T07:08:09Z\n  ip: 10.0.0.1\n  home: https://example.com/a?b=c\n  next: http://localhost:8080\n")).Unmarshal([]string{"svc"}, &got); err != nil {
		t.Fatal(err)
	}

	if !got.When.Equal(time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)) || got.IP.String() != "10.0.0.1" || got.Home.Host != "example.com" || got.Next == nil || got.Next.Port() != "8080" {
		t.Fatalf("got %+v", got)
	}
}

func TestPointersAreGivenNewValues(t *testing.T) {
	t.Parallel()

	one := 1

	got := struct {
		Set   *int    `config:"set"`
		Unset *int    `config:"unset"`
		Str   *string `config:"str"`
	}{Set: &one}

	if err := yaml.Raw([]byte("svc:\n  set: 2\n  str: x\n")).Unmarshal([]string{"svc"}, &got); err != nil {
		t.Fatal(err)
	}

	if one != 1 {
		t.Fatal("the value the pointer was given was written through")
	}

	if got.Set == nil || *got.Set != 2 || got.Unset != nil || got.Str == nil || *got.Str != "x" {
		t.Fatalf("got %+v", got)
	}
}

func TestSequencesAndMaps(t *testing.T) {
	t.Parallel()

	got := struct {
		Names []string          `config:"names"`
		Ports []int             `config:"ports"`
		Flow  []string          `config:"flow"`
		Empty []string          `config:"empty"`
		Env   map[string]string `config:"env"`
		Limit map[string]int    `config:"limit"`
	}{Names: []string{"default"}, Empty: []string{"default"}}

	doc := `svc:
  names:
    - a
    - "b, c"
  ports: [1, 2, 3]
  flow: [x, "y"]
  empty: []
  env:
    A: "1"
    B: two
  limit: {x: 1, y: 2}
`

	if err := yaml.Raw([]byte(doc)).Unmarshal([]string{"svc"}, &got); err != nil {
		t.Fatal(err)
	}

	// An element is a value, so a comma inside one is not a separator, and a
	// sequence replaces the default rather than adding to it.
	if !slices.Equal(got.Names, []string{"a", "b, c"}) || !slices.Equal(got.Ports, []int{1, 2, 3}) || !slices.Equal(got.Flow, []string{"x", "y"}) || len(got.Empty) != 0 {
		t.Fatalf("sequences: %+v", got)
	}

	if len(got.Env) != 2 || got.Env["A"] != "1" || got.Env["B"] != "two" || got.Limit["x"] != 1 || got.Limit["y"] != 2 {
		t.Fatalf("maps: %+v", got)
	}
}

func TestBadItemsAndShapesAreReportedAgainstTheField(t *testing.T) {
	t.Parallel()

	var got struct {
		Ports []int          `config:"ports"`
		Names []string       `config:"names"`
		Env   map[string]int `config:"env"`
		Port  int            `config:"port"`
		Name  string         `config:"name"`
	}

	err := yaml.Raw([]byte(`svc:
  ports: [1, two]
  names: just-a-string
  env: {a: x}
  port: [1]
  name: {a: b}
`)).Unmarshal([]string{"svc"}, &got)

	want := []string{"Ports", "Names", "Env", "Port", "Name"}

	if !slices.Equal(fieldsOf(fieldErrors(err)), want) {
		t.Fatalf("expected problems with %v, got %v", want, err)
	}
}

func TestEveryProblemIsReportedAndTheValidFieldsAreStillSet(t *testing.T) {
	t.Parallel()

	var got struct {
		A    int    `config:"a"`
		Good string `config:"good"`
		B    int    `config:"b"`
	}

	err := yaml.Raw([]byte("svc:\n  a: x\n  good: fine\n  b: y\n")).Unmarshal([]string{"svc"}, &got)

	if !slices.Equal(fieldsOf(fieldErrors(err)), []string{"A", "B"}) || got.Good != "fine" {
		t.Fatalf("got %+v, %v", got, err)
	}

	for _, fe := range fieldErrors(err) {
		if fe.Provider != "yaml" {
			t.Errorf("Provider = %q", fe.Provider)
		}
	}
}

func TestADestinationThatIsNotAStructPointerIsAnError(t *testing.T) {
	t.Parallel()

	var s struct{}

	for _, dst := range []any{nil, 5, s, (*struct{})(nil), new(int)} {
		if err := yaml.Raw([]byte("a: 1\n")).Unmarshal([]string{"svc"}, dst); err == nil {
			t.Errorf("%T: expected an error", dst)
		}
	}
}

func TestUnsupportedTaggedTypesFailEvenWhenNothingIsSet(t *testing.T) {
	t.Parallel()

	var got struct {
		C chan int `config:"c"`
	}

	if err := yaml.Raw(nil).Unmarshal([]string{"svc"}, &got); !slices.Equal(fieldsOf(fieldErrors(err)), []string{"C"}) {
		t.Fatalf("got %v", err)
	}
}

func TestComfortableDocumentsAreRead(t *testing.T) {
	t.Parallel()

	var got struct {
		Addr string `config:"addr"`
		Name string `config:"name"`
	}

	docs := map[string]string{
		"a BOM":          "\xef\xbb\xbfsvc:\n  addr: x\n  name: y\n",
		"CRLF":           "svc:\r\n  addr: x\r\n  name: y\r\n",
		"an end marker":  "svc:\n  addr: x\n  name: y\n...\n",
		"a start marker": "---\nsvc:\n  addr: x\n  name: y\n",
		"no final line":  "svc:\n  addr: x\n  name: y",
	}

	for name, doc := range docs {
		got.Addr, got.Name = "", ""

		if err := yaml.Raw([]byte(doc)).Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != "x" || got.Name != "y" {
			t.Errorf("%s: got %+v, %v", name, got, err)
		}
	}
}

const documentLimit = 8 << 20

type fileConfig struct {
	Addr string `config:"addr"`
}

func TestFileReadsTheDocument(t *testing.T) {
	t.Parallel()

	var got fileConfig

	if err := yaml.File(writeDoc(t, []byte("svc:\n  addr: x\n"))).Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != "x" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestAMissingFileIsSkippedUnlessItIsRequired(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing.yaml")

	got := fileConfig{Addr: "default"}

	p := yaml.File(path)

	if err := p.Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != "default" {
		t.Fatalf("got %+v, %v", got, err)
	}

	if err := p.Check([]string{"svc"}); err != nil {
		t.Fatalf("Check: %v", err)
	}

	if keys, err := p.Describe([]string{"svc"}, &got); err != nil || len(keys) != 1 {
		t.Fatalf("Describe: %v, %v", keys, err)
	}

	err := yaml.File(path, config.WithRequired()).Unmarshal([]string{"svc"}, &got)

	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), path) || !strings.HasPrefix(err.Error(), "yaml: ") {
		t.Fatalf("err = %v", err)
	}

	// The error says which file and why, once, and not through the path error's
	// own repeat of the name.
	if strings.Count(err.Error(), path) != 1 {
		t.Fatalf("the path is repeated: %v", err)
	}
}

func TestAFileThatIsNotReadableNamesTheFileButNeverItsContents(t *testing.T) {
	t.Parallel()

	if os.Getuid() == 0 {
		t.Skip("root can read anything")
	}

	path := writeDoc(t, []byte("svc:\n  addr: hunter2\n"))

	if err := os.Chmod(path, 0); err != nil {
		t.Skip(err)
	}

	err := yaml.File(path).Unmarshal([]string{"svc"}, &fileConfig{})

	if err == nil || !strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("err = %v", err)
	}
}

func TestADirectoryIsAnError(t *testing.T) {
	t.Parallel()

	if err := yaml.File(t.TempDir()).Unmarshal([]string{"svc"}, &fileConfig{}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestASymlinkIsFollowed(t *testing.T) {
	t.Parallel()

	target := writeDoc(t, []byte("svc:\n  addr: linked\n"))
	link := filepath.Join(t.TempDir(), "link.yaml")

	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}

	var got fileConfig

	if err := yaml.File(link).Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != "linked" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestADanglingSymlinkIsAMissingFile(t *testing.T) {
	t.Parallel()

	link := filepath.Join(t.TempDir(), "dangling.yaml")

	if err := os.Symlink(filepath.Join(t.TempDir(), "nowhere"), link); err != nil {
		t.Skip(err)
	}

	if err := yaml.File(link).Unmarshal([]string{"svc"}, &fileConfig{}); err != nil {
		t.Fatalf("skipped files are not errors: %v", err)
	}

	if err := yaml.File(link, config.WithRequired()).Unmarshal([]string{"svc"}, &fileConfig{}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}

// A configuration file is a few kilobytes. One over 8 MiB is refused instead of
// being read, and parsed, in full.
func TestAFileOverTheLimitIsRefused(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "big.yaml")

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := f.Truncate(documentLimit + 1); err != nil {
		t.Skip(err)
	}

	f.Close()

	p := yaml.File(path)

	for name, err := range map[string]error{
		"Unmarshal": p.Unmarshal([]string{"svc"}, &fileConfig{}),
		"Check":     p.Check([]string{"svc"}),
	} {
		if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "8 MiB") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestAFileOfExactlyTheLimitIsRead(t *testing.T) {
	t.Parallel()

	head := "svc:\n  addr: "

	doc := head + strings.Repeat("a", documentLimit-len(head))

	var got fileConfig

	if err := yaml.File(writeDoc(t, []byte(doc))).Unmarshal([]string{"svc"}, &got); err != nil || len(got.Addr) != documentLimit-len(head) {
		t.Fatalf("read %d bytes of the value, %v", len(got.Addr), err)
	}
}

func TestAnErrorInAFileNamesTheFile(t *testing.T) {
	t.Parallel()

	path := writeDoc(t, []byte("svc:\n  addr: &hunter2 x\n"))

	err := yaml.File(path).Unmarshal([]string{"svc"}, &fileConfig{})

	if err == nil || !strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "hunter2") || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v", err)
	}
}

func TestAFileIsReadEachTimeItIsAskedFor(t *testing.T) {
	t.Parallel()

	path := writeDoc(t, []byte("svc:\n  addr: one\n"))

	p := yaml.File(path)

	var got fileConfig

	if err := p.Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != "one" {
		t.Fatalf("got %+v, %v", got, err)
	}

	if err := os.WriteFile(path, []byte("svc:\n  addr: two\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := p.Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != "two" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// FileFromEnv is for a program that lets whoever runs it choose the file: the
// program names the variable, in code, and the framework reads no other. An unset
// variable means no file, which is the one thing File("") can't say, since an
// empty path there is a mistake.

type fromEnvConfig struct {
	Addr string `config:"addr"`
}

// doc is a document that sets svc.addr to value.
func fromEnvDoc(value string) string {
	return "svc:\n  addr: " + value + "\n"
}

func writeFromEnv(t *testing.T, dir, data string) string {
	t.Helper()

	path := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestFileFromEnvReadsTheFileTheVariableNames(t *testing.T) {
	path := writeFromEnv(t, t.TempDir(), fromEnvDoc("from-file"))

	t.Setenv("SHOP_CONFIG", path)

	p := yaml.FileFromEnv("SHOP_CONFIG")

	var got fromEnvConfig

	if err := p.Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != "from-file" {
		t.Fatalf("got %+v, %v", got, err)
	}

	if p.Path() != path {
		t.Errorf("Path() = %q, want %q", p.Path(), path)
	}
}

// The variable is read when the provider is made, so the provider doesn't change
// under a running program, and the same file is not read twice by accident.
func TestFileFromEnvReadsTheVariableOnce(t *testing.T) {
	dir := t.TempDir()

	first := writeFromEnv(t, dir, fromEnvDoc("first"))

	t.Setenv("SHOP_CONFIG", first)

	p := yaml.FileFromEnv("SHOP_CONFIG")

	t.Setenv("SHOP_CONFIG", filepath.Join(dir, "elsewhere.yaml"))

	var got fromEnvConfig

	if err := p.Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != "first" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestAnUnsetOrEmptyVariableMeansNoFile(t *testing.T) {
	for name, set := range map[string]bool{"unset": false, "empty": true} {
		t.Run(name, func(t *testing.T) {
			if set {
				t.Setenv("SHOP_CONFIG", "")
			}

			p := yaml.FileFromEnv("SHOP_CONFIG")

			got := fromEnvConfig{Addr: "default"}

			if err := p.Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != "default" {
				t.Fatalf("got %+v, %v", got, err)
			}

			if p.Path() != "" {
				t.Errorf("Path() = %q, want none", p.Path())
			}

			if err := p.Check([]string{"svc"}); err != nil {
				t.Errorf("Check: %v", err)
			}
		})
	}
}

// Saying the file is required means one must be named: an unset variable is a
// mistake that stops startup, naming the variable.
func TestAnUnsetVariableIsAnErrorWhenTheFileIsRequired(t *testing.T) {
	for name, set := range map[string]bool{"unset": false, "empty": true} {
		t.Run(name, func(t *testing.T) {
			if set {
				t.Setenv("SHOP_CONFIG", "")
			}

			err := yaml.FileFromEnv("SHOP_CONFIG", config.WithRequired()).Unmarshal([]string{"svc"}, &fromEnvConfig{})
			if err == nil {
				t.Fatal("expected an error")
			}

			if !strings.Contains(err.Error(), "SHOP_CONFIG") {
				t.Errorf("the error should name the variable: %v", err)
			}
		})
	}
}

func TestARequiredFileFromEnvMustExist(t *testing.T) {
	t.Setenv("SHOP_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))

	var got fromEnvConfig

	if err := yaml.FileFromEnv("SHOP_CONFIG").Unmarshal([]string{"svc"}, &got); err != nil {
		t.Errorf("an optional file that is missing should be skipped: %v", err)
	}

	if err := yaml.FileFromEnv("SHOP_CONFIG", config.WithRequired()).Unmarshal([]string{"svc"}, &got); err == nil {
		t.Error("a required file that is missing should be an error")
	}
}

// An empty path given in code is a bug, not an optional file: an unset variable
// passed through os.Getenv would otherwise run without its configuration.
func TestAnEmptyPathIsAnError(t *testing.T) {
	for name, p := range map[string]*yaml.FileProvider{
		"optional": yaml.File(""),
		"required": yaml.File("", config.WithRequired()),
	} {
		t.Run(name, func(t *testing.T) {
			if err := p.Unmarshal([]string{"svc"}, &fromEnvConfig{}); err == nil {
				t.Fatal("expected an error")
			}

			if err := p.Check([]string{"svc"}); err == nil {
				t.Fatal("expected Check to refuse it too")
			}
		})
	}
}

// The value of the variable is a path from the environment. Errors name the
// variable, not what it holds, so pointing the helper at the wrong variable can't
// put its value in a log.
func TestFileFromEnvErrorsNameTheVariableNeverItsValue(t *testing.T) {
	const canary = "canary-value-7f3a"

	tests := map[string]struct {
		prepare  func(t *testing.T, dir string) string
		required bool
		run      func(p *yaml.FileProvider) error
		names    bool // the error says where the problem is, so it names the variable
	}{
		"missing": {
			prepare:  func(t *testing.T, dir string) string { return filepath.Join(dir, "missing.yaml") },
			required: true,
			run:      func(p *yaml.FileProvider) error { return p.Unmarshal([]string{"svc"}, &fromEnvConfig{}) },
			names:    true,
		},
		"a directory": {
			prepare: func(t *testing.T, dir string) string { return dir },
			run:     func(p *yaml.FileProvider) error { return p.Unmarshal([]string{"svc"}, &fromEnvConfig{}) },
			names:   true,
		},
		"not a document": {
			prepare: func(t *testing.T, dir string) string { return writeFromEnv(t, dir, "svc: [\n") },
			run:     func(p *yaml.FileProvider) error { return p.Unmarshal([]string{"svc"}, &fromEnvConfig{}) },
		},
		"a key nothing reads": {
			prepare: func(t *testing.T, dir string) string { return writeFromEnv(t, dir, "svc:\n  addr: x\n  typo: 1\n") },
			run: func(p *yaml.FileProvider) error {
				if err := p.Unmarshal([]string{"svc"}, &fromEnvConfig{}); err != nil {
					return err
				}

				return p.Check([]string{"svc"})
			},
			names: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), canary)

			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}

			t.Setenv("SHOP_CONFIG", tt.prepare(t, dir))

			var opts []config.FileOption
			if tt.required {
				opts = append(opts, config.WithRequired())
			}

			err := tt.run(yaml.FileFromEnv("SHOP_CONFIG", opts...))
			if err == nil {
				t.Fatal("expected an error")
			}

			if strings.Contains(err.Error(), canary) {
				t.Errorf("the error contains the value of the variable: %v", err)
			}

			if tt.names && !strings.Contains(err.Error(), "$SHOP_CONFIG") {
				t.Errorf("the error should name the variable as $SHOP_CONFIG: %v", err)
			}
		})
	}
}

// A file named in code still shows its path, as that is the program's own text.
func TestFileErrorsStillNameAFileNamedInCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")

	err := yaml.File(path, config.WithRequired()).Unmarshal([]string{"svc"}, &fromEnvConfig{})
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("got %v", err)
	}
}

// The name is checked as an alias is, and whatever the value holds is never
// looked at when it is refused.
func TestFileFromEnvRefusesBadVariableNames(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "canary-secret-9c1d")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "canary-secret-9c1d")

	tests := map[string]string{
		"empty":        "",
		"lower case":   "shop_config",
		"space":        "SHOP CONFIG",
		"equals":       "A=B",
		"NUL":          "A\x00B",
		"newline":      "A\nB",
		"too long":     strings.Repeat("A", 129),
		"PATH":         "PATH",
		"HOME":         "HOME",
		"credentials":  "AWS_SECRET_ACCESS_KEY",
		"google":       "GOOGLE_APPLICATION_CREDENTIALS",
		"LD_":          "LD_PRELOAD",
		"SSH_":         "SSH_AUTH_SOCK",
		"starts digit": "1CONFIG",
	}

	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			for _, opts := range [][]config.FileOption{nil, {config.WithRequired()}} {
				p := yaml.FileFromEnv(in, opts...)

				for what, err := range map[string]error{
					"Unmarshal": p.Unmarshal([]string{"svc"}, &fromEnvConfig{}),
					"Check":     p.Check([]string{"svc"}),
				} {
					if err == nil {
						t.Errorf("%s: %q was accepted", what, in)

						continue
					}

					if strings.Contains(err.Error(), "canary") || len(err.Error()) > 300 {
						t.Errorf("%s: unsafe error: %q", what, err.Error())
					}

					for _, r := range err.Error() {
						if r < 0x20 || r > 0x7e {
							t.Errorf("%s: the error contains %q", what, r)

							break
						}
					}
				}
			}
		})
	}
}

func TestFileFromEnvAcceptsOrdinaryNames(t *testing.T) {
	for _, name := range []string{"SHOP_CONFIG", "CONFIG_FILE", "A", "X1_Y2"} {
		t.Run(name, func(t *testing.T) {
			if err := yaml.FileFromEnv(name).Unmarshal([]string{"svc"}, &fromEnvConfig{}); err != nil {
				t.Fatalf("an unset variable should be no file: %v", err)
			}
		})
	}
}

// A provider that was asked to restart the program when what it reads changes
// (config.WithRestartOnChange) says what that is, as data, and the framework does
// the watching. One that was not asked says nothing, so it is never watched.

func sourcesOf(t *testing.T, p config.Provider) []config.Source {
	t.Helper()

	r, ok := p.(config.Restartable)
	if !ok {
		t.Fatalf("%T is not a config.Restartable", p)
	}

	return r.RestartSources()
}

func TestAFileThatAsksToRestartSaysWhatItReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "svc"+dirExts[0])

	got := sourcesOf(t, yaml.File(path, config.WithRestartOnChange()))

	want := []config.Source{{Path: path}}
	if len(got) != 1 || got[0].Path != want[0].Path || got[0].Label != "" || got[0].Dir || len(got[0].Extensions) != 0 {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestADirectoryThatAsksToRestartSaysWhatItReads(t *testing.T) {
	dir := t.TempDir()

	got := sourcesOf(t, yaml.Dir(dir, config.WithRestartOnChange()))

	if len(got) != 1 || got[0].Path != dir || got[0].Label != "" || !got[0].Dir || !slices.Equal(got[0].Extensions, dirExts) {
		t.Fatalf("got %+v, want the directory with %v", got, dirExts)
	}
}

// A path that came from the environment is not for a log: the source carries the
// name of the variable to use instead, and the path itself is only data for the
// watch.
func TestAnEnvironmentPathIsLabelledWithTheVariable(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "svc"+dirExts[0])

	t.Setenv("SHOP_CONFIG", file)
	t.Setenv("SHOP_CONFIGS", dir)

	f := sourcesOf(t, yaml.FileFromEnv("SHOP_CONFIG", config.WithRestartOnChange()))
	if len(f) != 1 || f[0].Path != file || f[0].Label != "$SHOP_CONFIG" || f[0].Dir {
		t.Errorf("FileFromEnv: got %+v", f)
	}

	d := sourcesOf(t, yaml.DirFromEnv("SHOP_CONFIGS", config.WithRestartOnChange()))
	if len(d) != 1 || d[0].Path != dir || d[0].Label != "$SHOP_CONFIGS" || !d[0].Dir || !slices.Equal(d[0].Extensions, dirExts) {
		t.Errorf("DirFromEnv: got %+v", d)
	}
}

// A variable that isn't set means there is no file. There is nothing to watch, and
// no path to make up.
func TestNothingIsWatchedForAVariableThatIsNotSet(t *testing.T) {
	t.Setenv("SHOP_CONFIG", "")

	if got := sourcesOf(t, yaml.FileFromEnv("SHOP_CONFIG", config.WithRestartOnChange())); len(got) != 0 {
		t.Errorf("an empty variable: got %+v", got)
	}

	if got := sourcesOf(t, yaml.DirFromEnv("SHOP_CONFIGS_NOT_SET", config.WithRestartOnChange())); len(got) != 0 {
		t.Errorf("a variable that is not set: got %+v", got)
	}

	if got := sourcesOf(t, yaml.FileFromEnv("SHOP_CONFIG", config.WithRestartOnChange(), config.WithRequired())); len(got) != 0 {
		t.Errorf("required, and not given: got %+v", got)
	}
}

// A provider that is in error reads nothing, so it watches nothing: a name that
// is not allowed for a variable never gives a path.
func TestAProviderInErrorWatchesNothing(t *testing.T) {
	t.Setenv("PATH_TO_BE_IGNORED", "/tmp/x"+dirExts[0])

	for _, name := range []string{"PATH", "HOME", "lower", "", "A B", "AWS_SECRET_ACCESS_KEY"} {
		if got := sourcesOf(t, yaml.FileFromEnv(name, config.WithRestartOnChange())); len(got) != 0 {
			t.Errorf("FileFromEnv(%q): got %+v", name, got)
		}

		if got := sourcesOf(t, yaml.DirFromEnv(name, config.WithRestartOnChange())); len(got) != 0 {
			t.Errorf("DirFromEnv(%q): got %+v", name, got)
		}
	}
}

// Not asked for means not watched, for every constructor, however else it is set up.
func TestAProviderThatDidNotAskToRestartSaysNothing(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "svc"+dirExts[0])

	t.Setenv("SHOP_CONFIG", file)
	t.Setenv("SHOP_CONFIGS", dir)

	for name, p := range map[string]config.Provider{
		"File":                     yaml.File(file),
		"File with options":        yaml.File(file, config.WithRequired(), config.WithoutPermissionCheck()),
		"FileFromEnv":              yaml.FileFromEnv("SHOP_CONFIG"),
		"FileFromEnv with options": yaml.FileFromEnv("SHOP_CONFIG", config.WithRequired()),
		"Dir":                      yaml.Dir(dir),
		"Dir with options":         yaml.Dir(dir, config.WithRequired(), config.WithoutPermissionCheck()),
		"DirFromEnv":               yaml.DirFromEnv("SHOP_CONFIGS"),
		"DirFromEnv with options":  yaml.DirFromEnv("SHOP_CONFIGS", config.WithRequired()),
	} {
		if got := sourcesOf(t, p); len(got) != 0 {
			t.Errorf("%s: got %+v", name, got)
		}
	}
}

// Asking to restart does not change what the provider does with its other options.
func TestRestartOnChangeIsOneOptionAmongOthers(t *testing.T) {
	file := filepath.Join(t.TempDir(), "svc"+dirExts[0])

	got := sourcesOf(t, yaml.File(file, config.WithRequired(), config.WithRestartOnChange(), config.WithoutPermissionCheck()))
	if len(got) != 1 || got[0].Path != file {
		t.Errorf("got %+v", got)
	}

	// A file that must exist and isn't there is still an error: asking to watch it
	// does not make that a state the program is happy with.
	if err := yaml.File(file, config.WithRequired(), config.WithRestartOnChange()).Unmarshal([]string{"svc"}, &struct{}{}); err == nil {
		t.Error("a required file that is missing was not an error")
	}

	// And the permission check is still on.
	if got := config.ResolveFile(config.WithRestartOnChange()); got.NoPermissionCheck {
		t.Errorf("the permission check was turned off: %+v", got)
	}
}

// What a provider returns is its own copy: whoever changes it can't change what
// the provider reports next time.
func TestTheSourcesAreACopy(t *testing.T) {
	dir := t.TempDir()
	p := yaml.Dir(dir, config.WithRestartOnChange())

	first := sourcesOf(t, p)

	first[0].Path = "/changed"
	first[0].Label = "changed"
	first[0].Extensions[0] = ".changed"

	_ = append(first, config.Source{Path: "/more"})

	again := sourcesOf(t, p)
	if len(again) != 1 || again[0].Path != dir || again[0].Label != "" || !slices.Equal(again[0].Extensions, dirExts) {
		t.Errorf("the provider's report was changed from outside: %+v", again)
	}
}

// Bytes in memory can't change under the program, and have no option: there is
// nothing to say.
func TestRawIsNeverWatched(t *testing.T) {
	if _, ok := any(yaml.Raw([]byte("x"))).(config.Restartable); ok {
		t.Error("Raw says it can be restarted on: it holds bytes, not a file")
	}
}

// A directory is watched by the rule that reads it: every name the provider reads is
// one the filter built from the source accepts (or an edit to it would be missed),
// and the leftovers of editors, hidden files and names that can't be shown safely
// are neither read nor watched (or saving a file in an editor would restart the
// program). Checked against what the provider really does.
func TestADirectoryIsWatchedByTheRuleThatReadsIt(t *testing.T) {
	ext := dirExts[0]

	names := []string{
		"a" + ext, "00-base" + ext, "A" + ext, "a.b" + ext, "é" + ext,
		".hidden" + ext, "x" + ext + "~", "#x" + ext + "#", "x" + ext + ".swp", "README", "notes.txt",
		"x.JSON", "a\n" + ext, "a\x1b[2J" + ext, "a\u202e" + ext, " x" + ext, "x" + ext + " ",
		strings.Repeat("a", 255-len(ext)) + ext,
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()

			if err := os.WriteFile(filepath.Join(dir, name), []byte(dirDoc("addr", dirQ("a"))), 0o600); err != nil {
				t.Skipf("this system can't create that name: %v", err)
			}

			p := yaml.Dir(dir, config.WithRestartOnChange())
			src := sourcesOf(t, p)[0]

			var got struct {
				Addr string `config:"addr"`
			}

			if err := p.Unmarshal([]string{"svc"}, &got); err != nil {
				// A name the provider refuses outright is not one it reads.
				got.Addr = ""
			}

			read := got.Addr == "a"
			watched := confdir.Match(name, src.Extensions...)

			if read && !watched {
				t.Errorf("%q is read by the provider and would not be watched: an edit to it would be missed", name)
			}

			if watched && !read {
				t.Errorf("%q is watched and not read: it would restart the program for nothing", name)
			}
		})
	}
}

// fieldErrors returns every *config.FieldError inside err, in order.
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

func fieldsOf(errs []*config.FieldError) []string {
	var out []string

	for _, e := range errs {
		out = append(out, e.Field)
	}

	return out
}

// writeDoc writes contents to a new file and returns its path.
func writeDoc(t *testing.T, contents []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")

	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// This is how a program reads YAML: in the document layer, which the framework
// puts below the environment and flags. The tests live here because the root
// module can't import this one.

type layerConfig struct {
	A string `config:"a"`
	B string `config:"b"`
	C string `config:"c"`
}

// runLoad loads a layerConfig in a root function called "svc" in a directory with
// no files of its own, and returns it with the exit codes.
func runLoad(t *testing.T, opts ...foundation.Option) (layerConfig, []int) {
	t.Helper()

	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	foundationtest.NewLogRecorder().Install(t)

	var got layerConfig

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&got)
	}, opts...)

	return got, exit.Codes()
}

func inEmptyDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	t.Chdir(dir)

	return dir
}

func writeIn(t *testing.T, dir, name, data string) string {
	t.Helper()

	path := filepath.Join(dir, name)

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// The provider types say they are documents, and that a file has a path, so a
// program can only put them in the document layer and the same file given twice
// is noticed. Nothing else about them is a layer.
func TestYAMLProvidersAreDocuments(t *testing.T) {
	var (
		_ config.DocumentLayer = (*yaml.FileProvider)(nil)
		_ config.DocumentLayer = (*yaml.RawProvider)(nil)
		_ config.Pathed        = (*yaml.FileProvider)(nil)
	)

	for _, typ := range []reflect.Type{reflect.TypeFor[*yaml.FileProvider](), reflect.TypeFor[*yaml.RawProvider]()} {
		for _, other := range []reflect.Type{
			reflect.TypeFor[config.RemoteLayer](),
			reflect.TypeFor[config.EnvironmentLayer](),
			reflect.TypeFor[config.CommandLayer](),
		} {
			if typ.Implements(other) {
				t.Errorf("%v should not be a %v", typ, other)
			}
		}
	}

	if reflect.TypeFor[*yaml.RawProvider]().Implements(reflect.TypeFor[config.Pathed]()) {
		t.Error("a YAML document held in memory has no path")
	}
}

func TestWithDocumentConfigCanPutYAMLAboveJSON(t *testing.T) {
	dir := inEmptyDir(t)

	writeIn(t, dir, "config.json", `{"svc": {"a": "json", "b": "json", "c": "json"}}`)
	writeIn(t, dir, "config.yaml", "svc:\n  a: yaml\n  b: yaml\n")

	t.Setenv("SVC_B", "env")

	got, exit := runLoad(t, foundation.WithDocumentConfig(json.File("config.json"), yaml.File("config.yaml")))

	if len(exit) != 0 {
		t.Fatalf("exit codes %v", exit)
	}

	// JSON is the bottom, YAML above it, and the environment above both.
	if want := (layerConfig{A: "yaml", B: "env", C: "json"}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// No file is read by default, so YAML on its own needs no Set: the layer starts
// empty, and a config.json in the directory is never loaded.
func TestYAMLOnItsOwn(t *testing.T) {
	dir := inEmptyDir(t)

	writeIn(t, dir, "config.json", `{"svc": {"a": "canary", "c": "canary"}}`)
	writeIn(t, dir, "config.yaml", "svc:\n  a: yaml\n")

	for name, opt := range map[string]foundation.Option{
		"with": foundation.WithDocumentConfig(yaml.File("config.yaml")),
		"set":  foundation.SetDocumentConfig(yaml.File("config.yaml")),
	} {
		t.Run(name, func(t *testing.T) {
			got, exit := runLoad(t, opt)

			if len(exit) != 0 {
				t.Fatalf("exit codes %v", exit)
			}

			if want := (layerConfig{A: "yaml"}); got != want {
				t.Fatalf("got %+v, want %+v: config.json should not have been read", got, want)
			}
		})
	}
}

func TestYAMLRawIsADocumentToo(t *testing.T) {
	inEmptyDir(t)

	got, exit := runLoad(t, foundation.SetDocumentConfig(yaml.Raw([]byte("svc:\n  a: raw\n"))))

	if len(exit) != 0 || got.A != "raw" {
		t.Fatalf("got %+v, exit codes %v", got, exit)
	}
}

// Nothing in the environment names a file: a variable that happens to be set
// changes nothing, whatever the file is called.
func TestNoVariableChoosesTheYAMLFile(t *testing.T) {
	dir := inEmptyDir(t)

	canary := writeIn(t, t.TempDir(), "canary.yaml", "svc:\n  a: canary\n")

	writeIn(t, dir, "config.yaml", "svc:\n  b: yaml\n")

	t.Setenv("CONFIG_FILE", canary)

	got, exit := runLoad(t, foundation.WithDocumentConfig(yaml.File("config.yaml")))

	if len(exit) != 0 {
		t.Fatalf("exit codes %v", exit)
	}

	if want := (layerConfig{B: "yaml"}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// Strict mode asks every document, so a typo in the YAML file is found when it is
// in the layer, and a key another document reads is not reported.
func TestStrictConfigChecksTheYAMLLayer(t *testing.T) {
	dir := inEmptyDir(t)

	writeIn(t, dir, "config.yaml", "svc:\n  a: yaml\n  typo: x\n")

	_, exit := runLoad(t,
		foundation.SetDocumentConfig(yaml.File("config.yaml")),
		foundation.WithStrictConfig(),
	)

	if !slices.Equal(exit, []int{1}) {
		t.Fatalf("exit codes %v, want [1] for the unknown key", exit)
	}

	writeIn(t, dir, "config.yaml", "svc:\n  a: yaml\n")

	if _, exit := runLoad(t, foundation.SetDocumentConfig(yaml.File("config.yaml")), foundation.WithStrictConfig()); len(exit) != 0 {
		t.Fatalf("exit codes %v, want none", exit)
	}
}

// The same file given as YAML twice is refused, as for any document.
func TestTheSameYAMLFileTwiceIsRefused(t *testing.T) {
	dir := inEmptyDir(t)

	writeIn(t, dir, "config.yaml", "svc:\n  a: yaml\n")

	_, exit := runLoad(t, foundation.WithDocumentConfig(yaml.File("config.yaml"), yaml.File("./config.yaml")))

	if !slices.Equal(exit, []int{1}) {
		t.Fatalf("exit codes %v, want [1]", exit)
	}
}
