package json_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/json"
)

type ambiguousConfig struct {
	Addr string `json:"addr"`
	Name string `json:"name"`
	TLS  struct {
		CertFile string `json:"cert_file"`
	} `json:"tls"`
	Labels map[string]string `json:"labels"`
}

func loadAmbiguous(t *testing.T, doc string, path ...string) (ambiguousConfig, error) {
	t.Helper()

	if len(path) == 0 {
		path = []string{"svc"}
	}

	var got ambiguousConfig

	err := json.Raw([]byte(doc)).Unmarshal(path, &got)

	return got, err
}

// A key that is in the document twice is not a value to pick one of. Which one
// won used to depend on Go's map order: 177 loads to 23 over 200 of one document.
func TestAKeyThatDiffersOnlyInCaseIsAnError(t *testing.T) {
	t.Parallel()

	for range 200 {
		got, err := loadAmbiguous(t, `{"svc": {"addr": "reviewed-value", "ADDR": "smuggled-value"}}`)

		errs := fieldErrors(err)

		if len(errs) != 1 || errs[0].Field != "Addr" || errs[0].Key != "svc.addr" || !errors.Is(err, config.ErrAmbiguous) {
			t.Fatalf("expected one ambiguity error for Addr, got %v", err)
		}

		if got.Addr != "" {
			t.Fatalf("neither value should be used, got %q", got.Addr)
		}

		msg := errs[0].Err.Error()

		// It says what is repeated, as spelt in the document, and not the values.
		if !strings.Contains(msg, `"addr"`) || !strings.Contains(msg, `"ADDR"`) {
			t.Fatalf("expected both spellings in %q", msg)
		}

		if strings.Contains(msg, "reviewed") || strings.Contains(msg, "smuggled") {
			t.Fatalf("the error shows a value: %q", msg)
		}
	}
}

func TestAnExactDuplicateKeyIsAnError(t *testing.T) {
	t.Parallel()

	got, err := loadAmbiguous(t, `{"svc": {"addr": "first", "addr": "second"}}`)

	if errs := fieldErrors(err); len(errs) != 1 || errs[0].Field != "Addr" || !errors.Is(err, config.ErrAmbiguous) {
		t.Fatalf("got %v", err)
	}

	if got.Addr != "" {
		t.Fatalf("got %q", got.Addr)
	}
}

func TestMoreThanTwoSpellingsAreAllReported(t *testing.T) {
	t.Parallel()

	_, err := loadAmbiguous(t, `{"svc": {"addr": "a", "Addr": "b", "ADDR": "c"}}`)

	errs := fieldErrors(err)

	if len(errs) != 1 {
		t.Fatalf("got %v", err)
	}

	for _, spelling := range []string{`"addr"`, `"Addr"`, `"ADDR"`} {
		if !strings.Contains(errs[0].Err.Error(), spelling) {
			t.Fatalf("expected %s in %q", spelling, errs[0].Err)
		}
	}
}

func TestADuplicatedNullIsStillAmbiguous(t *testing.T) {
	t.Parallel()

	if _, err := loadAmbiguous(t, `{"svc": {"addr": null, "addr": "x"}}`); !errors.Is(err, config.ErrAmbiguous) {
		t.Fatalf("got %v", err)
	}
}

// The other fields are fine, and still load.
func TestOtherFieldsStillLoadWhenOneIsAmbiguous(t *testing.T) {
	t.Parallel()

	got, err := loadAmbiguous(t, `{"svc": {"addr": "a", "ADDR": "b", "name": "shop"}}`)

	if len(fieldErrors(err)) != 1 {
		t.Fatalf("got %v", err)
	}

	if got.Name != "shop" || got.Addr != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestAnAmbiguousGroupIsAnErrorAgainstTheGroup(t *testing.T) {
	t.Parallel()

	got, err := loadAmbiguous(t, `{"svc": {"tls": {"cert_file": "a"}, "TLS": {"cert_file": "b"}}}`)

	errs := fieldErrors(err)

	if len(errs) != 1 || errs[0].Field != "TLS" || errs[0].Key != "svc.tls" || !errors.Is(err, config.ErrAmbiguous) {
		t.Fatalf("got %v", err)
	}

	if got.TLS.CertFile != "" {
		t.Fatalf("got %q", got.TLS.CertFile)
	}
}

// Where the function's own section is ambiguous there is no field to blame, so
// the whole load is an error, naming where.
func TestAnAmbiguousSectionForTheFunctionIsAnError(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		doc  string
		path []string
		want []string
	}{
		"top level":        {`{"svc": {"addr": "x"}, "SVC": {"addr": "y"}}`, []string{"svc"}, []string{`"svc"`, `"SVC"`}},
		"nested":           {`{"svc": {"http": {"addr": "x"}, "HTTP": {"addr": "y"}}}`, []string{"svc", "http"}, []string{`"http"`, `"HTTP"`}},
		"exact duplicates": {`{"svc": {"addr": "x"}, "svc": {"addr": "y"}}`, []string{"svc"}, []string{`"svc"`}},
		"dotted name":      {`{"svc": {"http": {"addr": "x"}, "http": {"addr": "y"}}}`, []string{"svc", "http.server"}, []string{`"http"`}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := loadAmbiguous(t, tt.doc, tt.path...)

			if !errors.Is(err, config.ErrAmbiguous) {
				t.Fatalf("got %v", err)
			}

			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Fatalf("expected %s in %q", w, err)
				}
			}

			if strings.Contains(err.Error(), `"x"`) || strings.Contains(err.Error(), `"y"`) {
				t.Fatalf("the error shows a value: %q", err)
			}

			if got.Addr != "" {
				t.Fatalf("nothing should be loaded from an ambiguous section, got %q", got.Addr)
			}
		})
	}
}

// Map keys are case sensitive, so case variants in a map are different entries.
// Only the same key twice is ambiguous.
func TestKeysOfAMapFieldAreAmbiguousOnlyWhenExactlyTheSame(t *testing.T) {
	t.Parallel()

	got, err := loadAmbiguous(t, `{"svc": {"labels": {"a": "1", "A": "2"}}}`)
	if err != nil || !slices.Equal(mapKeys(got.Labels), []string{"A", "a"}) {
		t.Fatalf("case variants in a map should both load: %v %v", got.Labels, err)
	}

	got, err = loadAmbiguous(t, `{"svc": {"labels": {"a": "1", "a": "2"}}}`)

	errs := fieldErrors(err)

	if len(errs) != 1 || errs[0].Field != "Labels" || !errors.Is(err, config.ErrAmbiguous) {
		t.Fatalf("an exact duplicate in a map should be an error, got %v", err)
	}

	if got.Labels != nil {
		t.Fatalf("got %v", got.Labels)
	}
}

func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))

	for k := range m {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}

// Only keys that are read matter. People repeat "_comment" in an object, and a
// shared file has sections that belong to other services.
func TestDuplicatesThatAreNotReadAreFine(t *testing.T) {
	t.Parallel()

	docs := map[string]string{
		"a repeated comment key":     `{"svc": {"_comment": "a", "_comment": "b", "addr": ":1"}}`,
		"case variants of a comment": `{"svc": {"//": "a", "//": "b", "addr": ":1"}}`,
		"another service's section":  `{"other": {"x": 1, "X": 2}, "OTHER": {}, "svc": {"addr": ":1"}}`,
		"an unread key with dups":    `{"svc": {"addr": ":1", "extra": {"k": 1, "K": 2, "k": 3}}}`,
		"objects in an array":        `{"svc": {"addr": ":1", "list": [{"a": 1, "a": 2}]}}`,
		"a sibling field, unread":    `{"svc": {"addr": ":1", "unused": 1, "UNUSED": 2}}`,
	}

	for name, doc := range docs {
		got, err := loadAmbiguous(t, doc)
		if err != nil || got.Addr != ":1" {
			t.Errorf("%s: got %q, %v", name, got.Addr, err)
		}
	}
}

func TestAFileWithAnAmbiguousKeyIsAnErrorToo(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.json")

	if err := os.WriteFile(path, []byte(`{"svc": {"addr": "a", "ADDR": "b"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var got ambiguousConfig

	err := json.File(path).Unmarshal([]string{"svc"}, &got)

	if len(fieldErrors(err)) != 1 || !errors.Is(err, config.ErrAmbiguous) {
		t.Fatalf("got %v", err)
	}
}

func TestADocumentThatIsNotJSONStillReportsThat(t *testing.T) {
	t.Parallel()

	for _, doc := range []string{`{"svc": {"addr": "a", "addr": }}`, `{"svc": {"addr": "a"`, `[`, ``, `{"a": 1} {"b": 2}`} {
		_, err := loadAmbiguous(t, doc)

		if err == nil || errors.Is(err, config.ErrAmbiguous) {
			t.Errorf("%q: got %v", doc, err)
		}
	}
}

// Describing, and looking for unknown keys, are unaffected by duplicates.
func TestDescribeAndCheckAreUnaffectedByDuplicates(t *testing.T) {
	t.Parallel()

	p := json.Raw([]byte(`{"svc": {"addr": "a", "ADDR": "b", "adrr": 1, "adrr": 2}}`))

	keys, err := p.Describe([]string{"svc"}, &ambiguousConfig{})
	if err != nil || len(keys) == 0 {
		t.Fatalf("Describe: %v %v", keys, err)
	}

	// Check reports a misspelt key once, however many times it is written.
	if err := p.Unmarshal([]string{"svc"}, &ambiguousConfig{}); !errors.Is(err, config.ErrAmbiguous) {
		t.Fatalf("Unmarshal: %v", err)
	}

	got := unknownKeys(t, p.Check([]string{"svc"}))

	if !slices.Equal(got, []string{"svc.adrr"}) {
		t.Fatalf("Check reported %v", got)
	}
}

// Which entry of a map an error is about must not depend on Go's map order.
func TestErrorsAboutAMapAreTheSameEveryTime(t *testing.T) {
	t.Parallel()

	var first string

	for i := range 100 {
		_, err := loadAmbiguous(t, `{"svc": {"labels": {"9": 7, "0": 2, "5": 1, "z": true}}}`)

		if err == nil {
			t.Fatal("expected an error")
		}

		if i == 0 {
			first = err.Error()

			continue
		}

		if err.Error() != first {
			t.Fatalf("the error changed between loads:\n  %s\n  %s", first, err)
		}
	}
}
