package yaml_test

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/yaml"
)

// A configuration file is untrusted: it may come from a ConfigMap, a template, a
// pull request or another team. Everything here is a way a YAML file can say
// something other than what a reviewer reads, or cost more than it should.

type hostileConfig struct {
	Addr string `config:"addr"`
	Name string `config:"name"`
}

// Anything the provider refuses is refused as a whole, wherever in the file it
// is, even in a part that nothing reads, and nothing is set from it. The error
// says where (the line) and what kind of thing it was, and never repeats the file.
func TestFeaturesThatHideTheValueAreRefusedAnywhere(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		doc  string
		line int
		what string
	}{
		{"an anchor", "svc:\n  addr: &hunter2 x\n", 2, "anchor"},
		{"an anchor on a mapping", "svc:\n  base: &hunter2\n    a: 1\n  addr: x\n", 2, "anchor"},
		{"an anchor on a sequence", "other: &hunter2 [a, b]\nsvc:\n  addr: x\n", 1, "anchor"},
		{"an anchor on a key", "svc:\n  &hunter2 addr: x\n", 2, "anchor"},
		{"an alias", "svc:\n  addr: *hunter2\n", 2, "alias"},
		{"an alias with its anchor", "base: &hunter2 x\nsvc:\n  addr: *hunter2\n", 1, "anchor"},
		{"a merge key", "svc:\n  <<: {addr: x}\n", 2, "merge"},
		{"a quoted merge key", "svc:\n  \"<<\": {addr: x}\n", 0, ""},
		{"a standard tag", "svc:\n  addr: !!str hunter2\n", 2, "tag"},
		{"a tag that changes the type", "svc:\n  addr: !!binary aHVudGVyMg==\n", 2, "tag"},
		{"a custom tag", "svc:\n  addr: !hunter2 x\n", 2, "tag"},
		{"a tag on a mapping", "svc: !hunter2\n  addr: x\n", 1, "tag"},
		{"a tag in a part nothing reads", "other:\n  - !!int 5\nsvc:\n  addr: x\n", 2, "tag"},
		{"a complex key", "svc:\n  ? [hunter2]\n  : x\n", 2, "complex"},
		{"a mapping as a key", "svc:\n  {a: hunter2}: x\n", 2, "complex"},
		{"a second document", "svc:\n  addr: x\n---\nsvc:\n  addr: hunter2\n", 3, "document"},
		{"an empty second document", "svc:\n  addr: x\n---\n", 3, "document"},
		{"a document after an end marker", "svc:\n  addr: x\n...\n---\nsvc:\n  addr: hunter2\n", 0, "document"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.what == "" {
				// A quoted "<<" is a string like any other key, so it is not a
				// merge key and is only an unread key.
				var got hostileConfig

				if err := yaml.Raw([]byte(tt.doc)).Unmarshal([]string{"svc"}, &got); err != nil {
					t.Fatalf("a quoted << is an ordinary key: %v", err)
				}

				return
			}

			got := hostileConfig{Addr: "default"}

			p := yaml.Raw([]byte(tt.doc))

			err := p.Unmarshal([]string{"svc"}, &got)

			if err == nil {
				t.Fatalf("expected an error, got %+v", got)
			}

			if got.Addr != "default" || got.Name != "" {
				t.Fatalf("something was set from a refused file: %+v", got)
			}

			assertSafeError(t, err, "hunter2")

			if !strings.Contains(strings.ToLower(err.Error()), tt.what) {
				t.Errorf("the error should say what was refused (%q): %v", tt.what, err)
			}

			if tt.line > 0 && !strings.Contains(err.Error(), fmt.Sprintf("line %d", tt.line)) {
				t.Errorf("the error should name line %d: %v", tt.line, err)
			}

			// Strict mode reads the file too, so it has to refuse the same things.
			if err := p.Check([]string{"svc"}); err == nil {
				t.Errorf("Check accepted the file")
			} else {
				assertSafeError(t, err, "hunter2")
			}
		})
	}
}

// assertSafeError checks that an error built from a hostile file is one line of
// a fixed shape that never carries the file's text.
func assertSafeError(t *testing.T, err error, canaries ...string) {
	t.Helper()

	msg := err.Error()

	for _, canary := range canaries {
		if strings.Contains(msg, canary) {
			t.Errorf("the error repeats the file (%q): %q", canary, msg)
		}
	}

	if len(msg) > 512 {
		t.Errorf("the error is %d bytes long", len(msg))
	}

	if strings.ContainsAny(msg, "\n\r\x00\x1b") {
		t.Errorf("the error has control characters: %q", msg)
	}

	if !strings.HasPrefix(msg, "yaml: ") {
		t.Errorf("the error doesn't say where it is from: %q", msg)
	}
}

// What the library would say about a syntax error often quotes the input, so none
// of it is used. A password that ends up in the wrong place in a file never
// reaches a log.
func TestSyntaxErrorsGiveALineAndAReasonOnly(t *testing.T) {
	t.Parallel()

	docs := []string{
		"svc:\n  addr: [hunter2\n",
		"svc:\n  addr: {hunter2: \n",
		"svc:\n  addr: \"hunter2\n",
		"svc:\n  addr: 'hunter2\n",
		"svc:\n\taddr: hunter2\n",
		"svc:\n  addr: x\n name: hunter2\n",
		"svc:\n  addr: x: hunter2\n",
		"svc:\n  hunter2\n",
		"svc: hunter2: x\n",
		"- hunter2\n  - x\n",
		"svc:\n  addr: \"\\q hunter2\"\n",
		"svc:\n  addr: @hunter2\n",
		"svc:\n  addr: `hunter2\n",
		"svc:\n  addr: %hunter2\n",
		"%YAML 9.9\n---\nsvc: hunter2\n",
		"%TAG ! tag:hunter2,2000:\n---\nsvc: x\n",
		"svc:\n  addr: \"\\xZZhunter2\"\n",
		"svc:\n  addr: |\n   hunter2\n  x\n",
		"svc:\n  - hunter2: [\n",
		"hunter2: [}\n",
	}

	for _, doc := range docs {
		var got hostileConfig

		p := yaml.Raw([]byte(doc))

		for name, err := range map[string]error{
			"Unmarshal": p.Unmarshal([]string{"svc"}, &got),
			"Check":     p.Check([]string{"svc"}),
		} {
			if err == nil {
				// Some of these are valid YAML, such as a plain scalar with a colon
				// inside; only an error has to be safe.
				continue
			}

			if !regexp.MustCompile(`^yaml: (line \d+: )?[a-z]`).MatchString(err.Error()) {
				t.Errorf("%s of %q: unexpected shape %q", name, doc, err)
			}

			assertSafeError(t, err, "hunter2")
		}

		// Every one of these is a mistake of some kind, but at least the ones with
		// an unclosed bracket or quote must be an error.
	}

	for _, doc := range []string{"svc:\n  addr: [hunter2\n", "svc:\n  addr: \"hunter2\n", "svc:\n\taddr: hunter2\n", "hunter2: [}\n"} {
		if err := yaml.Raw([]byte(doc)).Unmarshal([]string{"svc"}, &hostileConfig{}); err == nil {
			t.Errorf("%q: expected an error", doc)
		}
	}
}

// No problem with a value repeats it: the field and the key say where to look. A
// canary in every kind of wrong value stands in for a password in the wrong place
// or a URL with credentials in it.
func TestProblemsNeverContainTheFilesValue(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Port  int            `config:"port"`
		Small int8           `config:"small"`
		Wait  time.Duration  `config:"wait"`
		Home  url.URL        `config:"home"`
		When  time.Time      `config:"when"`
		Flag  bool           `config:"flag"`
		Nums  []int          `config:"nums"`
		Ratio float64        `config:"ratio"`
		Map   map[string]int `config:"map"`
		Shape string         `config:"shape"`
		Group struct {
			A int `config:"a"`
		} `config:"group"`
	}

	const secret = "hunter2-SECRET"

	err := yaml.Raw([]byte(`svc:
  port: `+secret+`
  small: 300
  wait: `+secret+`
  home: "https://admin:`+secret+`@[::1"
  when: `+secret+`
  flag: `+secret+`
  nums: [1, `+secret+`]
  ratio: `+secret+`
  map: {a: `+secret+`}
  shape: {`+secret+`: x}
  group: `+secret+`
`)).Unmarshal([]string{"svc"}, &cfg{})

	errs := fieldErrors(err)

	if len(errs) != 11 {
		t.Fatalf("expected 11 problems, got %d: %v", len(errs), err)
	}

	for _, e := range errs {
		for _, leaked := range []string{"hunter2", "SECRET", "300"} {
			if strings.Contains(e.Error(), leaked) {
				t.Errorf("the problem for %s repeats the value (%q): %q", e.Field, leaked, e)
			}
		}

		if e.Key == "" || !strings.Contains(e.Error(), e.Key) {
			t.Errorf("the problem for %s doesn't name its key: %q", e.Field, e)
		}
	}
}

// A key that is there twice has no value to use, and which one a parser keeps
// is an accident of the parser. It would also let a reviewer read one value in a
// diff while another is what runs.
func TestAKeyThatIsThereTwiceIsAmbiguous(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		doc  string
		want []string
	}{
		{"exactly", "svc:\n  addr: reviewed\n  addr: smuggled\n", []string{`"addr"`}},
		{"in another case", "svc:\n  addr: reviewed\n  ADDR: smuggled\n", []string{`"addr"`, `"ADDR"`}},
		{"in three spellings", "svc:\n  addr: a\n  Addr: b\n  ADDR: c\n", []string{`"addr"`, `"Addr"`, `"ADDR"`}},
		{"in a flow mapping", "svc: {addr: reviewed, ADDR: smuggled}\n", []string{`"addr"`, `"ADDR"`}},
		{"quoted and plain", "svc:\n  addr: reviewed\n  \"addr\": smuggled\n", []string{`"addr"`}},
		{"a null and a value", "svc:\n  addr:\n  addr: smuggled\n", []string{`"addr"`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for range 50 {
				var got hostileConfig

				err := yaml.Raw([]byte(tt.doc)).Unmarshal([]string{"svc"}, &got)

				errs := fieldErrors(err)

				if len(errs) != 1 || errs[0].Field != "Addr" || errs[0].Key != "svc.addr" || !errors.Is(err, config.ErrAmbiguous) {
					t.Fatalf("expected one ambiguity problem for Addr, got %v", err)
				}

				if got.Addr != "" {
					t.Fatalf("neither value should be used, got %q", got.Addr)
				}

				for _, spelling := range tt.want {
					if !strings.Contains(errs[0].Err.Error(), spelling) {
						t.Fatalf("expected %s in %q", spelling, errs[0].Err)
					}
				}

				for _, value := range []string{"reviewed", "smuggled"} {
					if strings.Contains(err.Error(), value) {
						t.Fatalf("the error shows a value: %q", err)
					}
				}
			}
		})
	}
}

func TestOtherFieldsStillLoadWhenOneIsAmbiguous(t *testing.T) {
	t.Parallel()

	var got hostileConfig

	err := yaml.Raw([]byte("svc:\n  addr: a\n  ADDR: b\n  name: fine\n")).Unmarshal([]string{"svc"}, &got)

	if !slices.Equal(fieldsOf(fieldErrors(err)), []string{"Addr"}) || got.Name != "fine" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestAnAmbiguousGroupOrSectionIsAProblem(t *testing.T) {
	t.Parallel()

	var got struct {
		TLS struct {
			CertFile string `config:"cert_file"`
		} `config:"tls"`
	}

	err := yaml.Raw([]byte("svc:\n  tls: {cert_file: a}\n  TLS: {cert_file: b}\n")).Unmarshal([]string{"svc"}, &got)

	if errs := fieldErrors(err); len(errs) != 1 || errs[0].Field != "TLS" || !errors.Is(err, config.ErrAmbiguous) {
		t.Fatalf("a repeated group: %v", err)
	}

	// The same on the way down to the function's own section.
	var cfg hostileConfig

	err = yaml.Raw([]byte("svc:\n  addr: a\nSVC:\n  addr: b\n")).Unmarshal([]string{"svc"}, &cfg)

	if !errors.Is(err, config.ErrAmbiguous) || cfg.Addr != "" {
		t.Fatalf("a repeated section: %+v, %v", cfg, err)
	}
}

// Keys nobody reads are nobody's business: a file can repeat _comment, or carry
// duplicated notes for the humans who read it.
func TestDuplicatesThatAreNotReadAreFine(t *testing.T) {
	t.Parallel()

	var got hostileConfig

	err := yaml.Raw([]byte("svc:\n  _comment: one\n  _comment: two\n  addr: x\nother:\n  a: 1\n  a: 2\n")).Unmarshal([]string{"svc"}, &got)

	if err != nil || got.Addr != "x" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// Keys of a map field are case sensitive, so only the same key twice is ambiguous.
func TestKeysOfAMapFieldAreAmbiguousOnlyWhenExactlyTheSame(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Labels map[string]string `config:"labels"`
	}

	var got cfg

	if err := yaml.Raw([]byte("svc:\n  labels:\n    A: 1\n    a: 2\n")).Unmarshal([]string{"svc"}, &got); err != nil || len(got.Labels) != 2 {
		t.Fatalf("keys in other cases are different keys: %+v, %v", got, err)
	}

	got = cfg{}

	err := yaml.Raw([]byte("svc:\n  labels:\n    a: 1\n    a: 2\n")).Unmarshal([]string{"svc"}, &got)

	if errs := fieldErrors(err); len(errs) != 1 || errs[0].Field != "Labels" || !errors.Is(err, config.ErrAmbiguous) || got.Labels != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// The same file gives the same answer every time, whatever Go's map order is.
func TestProblemsAreInAStableOrder(t *testing.T) {
	t.Parallel()

	doc := []byte("svc:\n  a: x\n  b: x\n  c: x\n  d: x\n  e: x\n  m: {z: x, y: x, w: x}\n")

	type cfg struct {
		A int            `config:"a"`
		B int            `config:"b"`
		C int            `config:"c"`
		D int            `config:"d"`
		E int            `config:"e"`
		M map[string]int `config:"m"`
	}

	first := yaml.Raw(doc).Unmarshal([]string{"svc"}, &cfg{}).Error()

	for range 50 {
		if again := yaml.Raw(doc).Unmarshal([]string{"svc"}, &cfg{}).Error(); again != first {
			t.Fatalf("the problems changed:\n%s\n%s", first, again)
		}
	}
}

func deep(open, close string, n int, inner string) []byte {
	return []byte(strings.Repeat(open, n) + inner + strings.Repeat(close, n))
}

// Size, depth and the number of nodes are bounded, so that a file can't use up a
// process before anything has read a value from it.
func TestDeeplyNestedDocumentsAreRefused(t *testing.T) {
	t.Parallel()

	for name, doc := range map[string][]byte{
		"sequences": deep("[", "]", 5000, "x"),
		"mappings":  deep("{a: ", "}", 5000, "x"),
		"blocks":    []byte(strings.Repeat("- ", 5000) + "x\n"),
	} {
		start := time.Now()

		err := yaml.Raw(doc).Unmarshal([]string{"svc"}, &hostileConfig{})

		if err == nil {
			t.Errorf("%s: expected an error", name)

			continue
		}

		assertSafeError(t, err)

		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("%s: took %v", name, elapsed)
		}
	}

	// Nesting that is deep but reasonable is read: 10 levels is more than any
	// configuration needs.
	var got hostileConfig

	if err := yaml.Raw([]byte("svc:\n  addr: x\n  other: "+string(deep("[", "]", 10, "1"))+"\n")).Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != "x" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestADocumentWithTooManyNodesIsRefused(t *testing.T) {
	t.Parallel()

	var b strings.Builder

	b.WriteString("svc:\n  list: [")

	for i := range 200_000 {
		if i > 0 {
			b.WriteString(",")
		}

		b.WriteString("1")
	}

	b.WriteString("]\n")

	start := time.Now()

	err := yaml.Raw([]byte(b.String())).Unmarshal([]string{"svc"}, &struct {
		Addr string `config:"addr"`
	}{})

	if err == nil {
		t.Fatal("expected an error")
	}

	assertSafeError(t, err)

	if !strings.Contains(err.Error(), "too many") && !strings.Contains(err.Error(), "limit") {
		t.Errorf("the error should say why: %v", err)
	}

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v", elapsed)
	}

	// A document of a thousand nodes is nothing.
	var many strings.Builder

	many.WriteString("svc:\n  addr: x\n")

	for i := range 1000 {
		fmt.Fprintf(&many, "  k%d: v\n", i)
	}

	var got hostileConfig

	if err := yaml.Raw([]byte(many.String())).Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != "x" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// The classic: each level of aliases doubles what the one before it stands for.
// Anchors are refused, so it is never expanded, and the error comes at once.
func TestAnAliasBombIsRefusedBeforeItIsExpanded(t *testing.T) {
	t.Parallel()

	var b strings.Builder

	b.WriteString("a: &a [x, x, x, x, x, x, x, x, x]\n")

	prev := "a"

	for _, name := range []string{"b", "c", "d", "e", "f", "g", "h", "i"} {
		fmt.Fprintf(&b, "%s: &%s [*%s, *%s, *%s, *%s, *%s, *%s, *%s, *%s, *%s]\n", name, name, prev, prev, prev, prev, prev, prev, prev, prev, prev)

		prev = name
	}

	b.WriteString("svc:\n  addr: *i\n")

	start := time.Now()

	err := yaml.Raw([]byte(b.String())).Unmarshal([]string{"svc"}, &hostileConfig{})

	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "anchor") {
		t.Fatalf("got %v", err)
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("took %v: the bomb was expanded", elapsed)
	}
}

func TestAHugeValueIsNotAProblemUntilItIsTooBig(t *testing.T) {
	t.Parallel()

	var got hostileConfig

	if err := yaml.Raw([]byte("svc:\n  addr: "+strings.Repeat("a", 1<<20)+"\n")).Unmarshal([]string{"svc"}, &got); err != nil || len(got.Addr) != 1<<20 {
		t.Fatalf("a 1 MiB value: %d bytes, %v", len(got.Addr), err)
	}
}

// Bytes that a terminal or a log reader would act on are not part of a
// configuration file, and the ones that get through are escaped wherever they
// are shown.
func TestControlCharactersInADocument(t *testing.T) {
	t.Parallel()

	for name, doc := range map[string]string{
		"a NUL":                    "svc:\n  addr: a\x00b\n",
		"an escape":                "svc:\n  addr: a\x1b[31mb\n",
		"a bell":                   "svc:\n  addr: a\x07b\n",
		"invalid UTF-8":            "svc:\n  addr: a\xffb\n",
		"a truncated rune":         "svc:\n  addr: a\xe2\x82\n",
		"a lone surrogate":         "svc:\n  addr: a\xed\xa0\x80b\n",
		"an overlong encoding":     "svc:\n  addr: a\xc0\xafb\n",
		"a NUL in a key":           "svc:\n  ad\x00dr: x\n",
		"a byte order mark inside": "svc:\n  addr: a\xef\xbb\xbfb\n",
	} {
		var got hostileConfig

		err := yaml.Raw([]byte(doc)).Unmarshal([]string{"svc"}, &got)

		if name == "a byte order mark inside" {
			// Allowed by the specification in the middle of a value; the point is
			// only that it is not a panic and not an unsafe error.
			if err != nil {
				assertSafeError(t, err)
			}

			continue
		}

		if err == nil {
			t.Errorf("%s: expected an error, got %+v", name, got)

			continue
		}

		assertSafeError(t, err)
	}
}

// Escapes in a quoted scalar are decoded, and a value that comes out holding
// control characters is a value like any other: it is the reader of a log that
// has to escape it, and Load does.
func TestEscapedControlCharactersInAQuotedValueAreTheValue(t *testing.T) {
	t.Parallel()

	var got hostileConfig

	if err := yaml.Raw([]byte("svc:\n  addr: \"a\\nb\\x1bc\"\n")).Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != "a\nb\x1bc" {
		t.Fatalf("got %q, %v", got.Addr, err)
	}
}

// A key is as hostile as a value: it is shown by strict mode, and a field's name
// is never taken from it.
func TestAHostileKeyIsJustAnUnknownKey(t *testing.T) {
	t.Parallel()

	p := yaml.Raw([]byte("svc:\n  addr: x\n  \"evil\\nkey\\x1b[2J\": 1\n"))

	var got hostileConfig

	if err := p.Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != "x" {
		t.Fatalf("got %+v, %v", got, err)
	}

	errs := fieldErrors(p.Check([]string{"svc"}))

	if len(errs) != 1 || !errors.Is(errs[0], config.ErrUnknown) || errs[0].Field != "" {
		t.Fatalf("got %v", errs)
	}
}

// A parser that panics is a bug, but a process that dies on a file it was given
// is worse: nothing a file contains may be a panic.
func TestNothingInAFileIsAPanic(t *testing.T) {
	t.Parallel()

	for _, doc := range []string{
		"", "\x00", "---", "...", "- - - -", "? ? ?", ": :", "[[[[", "{{{{", "&", "*", "!", "!!", "<<: *a",
		"svc: &a\n  <<: *a\n", "svc: [", "svc: {", "svc: \"", "svc: |\n", "svc: >\n", "%", "%YAML", "\ufeff", "\xff\xfe",
		strings.Repeat("a: ", 1000), strings.Repeat("-", 100000), strings.Repeat("? ", 10000), strings.Repeat("&a ", 1000),
	} {
		p := yaml.Raw([]byte(doc))

		_ = p.Unmarshal([]string{"svc"}, &hostileConfig{})
		_ = p.Check([]string{"svc"})
		_, _ = p.Describe([]string{"svc"}, &hostileConfig{})
	}
}

type fuzzConfig struct {
	Addr  string            `config:"addr"`
	Port  int               `config:"port"`
	Debug bool              `config:"debug"`
	Wait  time.Duration     `config:"wait"`
	Home  url.URL           `config:"home"`
	Names []string          `config:"names"`
	Env   map[string]string `config:"env"`
	TLS   struct {
		CertFile string `config:"cert_file"`
	} `config:"tls"`
}

var secretish = regexp.MustCompile(`[A-Za-z0-9_]*[0-9][A-Za-z0-9_]{5,}`)

// FuzzUnmarshal checks what has to hold for any bytes at all: no panic, an error
// that is short, on one line and in the provider's own words, and a result that
// doesn't depend on anything but the bytes.
func FuzzUnmarshal(f *testing.F) {
	for _, seed := range []string{
		"", "svc:\n  addr: x\n", "svc: {addr: x, port: 1}", "svc:\n  addr: &a x\n  name: *a\n",
		"svc:\n  <<: {a: 1}\n", "svc:\n  addr: !!str x\n", "svc:\n  ? [a]\n  : b\n", "a: 1\n---\nb: 2\n",
		"svc:\n  addr: a\n  ADDR: b\n", "svc:\n  names: [a, b]\n  env: {A: b}\n", "svc:\n  port: 0x10\n",
		"svc:\n  wait: 30s\n  home: https://u:hunter2@example.com\n", "\xef\xbb\xbfsvc:\n  addr: x\n", "svc:\n\taddr: x\n",
		"svc:\n  addr: \"a\\x00b\"\n", "[[[[[[[[[[[[[[[[", "svc: |\n  text\n", "%YAML 1.1\n---\nsvc: {}\n",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		p := yaml.Raw(data)

		var first fuzzConfig

		err := p.Unmarshal([]string{"svc"}, &first)

		var second fuzzConfig

		if err2 := yaml.Raw(data).Unmarshal([]string{"svc"}, &second); (err == nil) != (err2 == nil) || (err != nil && err.Error() != err2.Error()) {
			t.Fatalf("two reads of the same bytes differ:\n%v\n%v", err, err2)
		}

		checkErr := p.Check([]string{"svc"})

		for _, e := range []error{err, checkErr} {
			if e == nil {
				continue
			}

			msg := e.Error()

			if len(msg) > 4096 {
				t.Fatalf("an error of %d bytes", len(msg))
			}

			if strings.ContainsAny(msg, "\r\x00\x1b") {
				t.Fatalf("control characters in an error: %q", msg)
			}

			if !strings.HasPrefix(msg, "yaml: ") && len(fieldErrors(e)) == 0 {
				t.Fatalf("an error that isn't from the provider: %q", msg)
			}

			// A token of the input that looks like a secret, as one with a digit
			// and some length, is never repeated by an error. Keys are the
			// exception: strict mode and ambiguity name them, on purpose, and what
			// is logged escapes them.
			if errors.Is(e, config.ErrUnknown) || errors.Is(e, config.ErrAmbiguous) {
				continue
			}

			for _, token := range secretish.FindAllString(string(data), -1) {
				if len(token) >= 8 && strings.Contains(msg, token) && !strings.Contains("yaml: line "+token, msg) {
					t.Fatalf("the error repeats %q from the input: %q", token, msg)
				}
			}
		}

		// Describe never reads, so it can't depend on the bytes at all.
		if _, err := p.Describe([]string{"svc"}, &fuzzConfig{}); err != nil {
			t.Fatalf("Describe: %v", err)
		}
	})
}
