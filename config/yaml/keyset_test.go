package yaml_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/yaml"
)

type strictConfig struct {
	Addr string `config:"addr"`
	Name string `config:"name"`
	TLS  struct {
		CertFile string `config:"cert_file"`
		Port     int    `config:"port"`
	} `config:"tls"`
}

func unknownKeys(t *testing.T, err error) []string {
	t.Helper()

	var keys []string

	for _, fe := range fieldErrors(err) {
		if fe.Provider != "yaml" || fe.Field != "" || !errors.Is(fe, config.ErrUnknown) {
			t.Fatalf("expected an unknown key problem from yaml with no field, got %+v", fe)
		}

		keys = append(keys, fe.Key)
	}

	return keys
}

func askFor(t *testing.T, p config.Provider, path []string, cfg any) {
	t.Helper()

	if err := p.Unmarshal(path, cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckFlagsKeysNothingAskedFor(t *testing.T) {
	t.Parallel()

	p := yaml.Raw([]byte("svc:\n  addr: ':1'\n  name: x\n  adrr: ':9'\n  tls:\n    cert_file: c\n    cert_flie: d\n"))

	askFor(t, p, []string{"svc"}, &strictConfig{})

	err := p.Check([]string{"svc"})

	// By their path in the document, sorted.
	if got := unknownKeys(t, err); !slices.Equal(got, []string{"svc.adrr", "svc.tls.cert_flie"}) {
		t.Fatalf("unexpected unknown keys %v", got)
	}

	if !strings.Contains(err.Error(), "yaml: svc.adrr: not recognised") {
		t.Fatalf("unexpected message %q", err.Error())
	}
}

func TestCheckFindsNothingWhenEverythingWasAskedFor(t *testing.T) {
	t.Parallel()

	p := yaml.Raw([]byte("svc:\n  addr: ':1'\n  tls:\n    port: 1\n"))

	askFor(t, p, []string{"svc"}, &strictConfig{})

	if err := p.Check([]string{"svc"}); err != nil {
		t.Fatalf("expected no problems, got %v", err)
	}
}

// A field the document has no value for is still asked for.
func TestAFieldTheDocumentLacksIsStillKnown(t *testing.T) {
	t.Parallel()

	p := yaml.Raw([]byte("svc:\n  name: x\n"))

	askFor(t, p, []string{"svc"}, &strictConfig{})

	if err := p.Check([]string{"svc"}); err != nil {
		t.Fatalf("expected no problems, got %v", err)
	}
}

func TestKeysAreMatchedCaseInsensitivelyAndReportedAsSpelt(t *testing.T) {
	t.Parallel()

	p := yaml.Raw([]byte("SVC:\n  ADDR: ':1'\n  Tls: {CERT_FILE: c, PORT: 1}\n  ADRR: ':9'\n"))

	askFor(t, p, []string{"svc"}, &strictConfig{})

	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"SVC.ADRR"}) {
		t.Fatalf("unexpected unknown keys %v", got)
	}
}

func TestChildFunctionsKeysAreKnownOnceTheyLoad(t *testing.T) {
	t.Parallel()

	type child struct {
		Port int `config:"port"`
	}

	p := yaml.Raw([]byte("svc:\n  addr: ':1'\n  http:\n    port: 1\n    prot: 2\n"))

	askFor(t, p, []string{"svc"}, &strictConfig{})
	askFor(t, p, []string{"svc", "http"}, &child{})

	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"svc.http.prot"}) {
		t.Fatalf("unexpected unknown keys %v", got)
	}
}

func TestAChildThatNeverLoadedIsReportedOnce(t *testing.T) {
	t.Parallel()

	p := yaml.Raw([]byte("svc:\n  addr: ':1'\n  http:\n    port: 1\n    nested: {a: 1}\n"))

	askFor(t, p, []string{"svc"}, &strictConfig{})

	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"svc.http"}) {
		t.Fatalf("unexpected unknown keys %v", got)
	}
}

func TestAnUnknownKeyIsReportedWhateverItsValue(t *testing.T) {
	t.Parallel()

	p := yaml.Raw([]byte("svc:\n  addr: ':1'\n  an_array: [1, 2]\n  a_null:\n  a_string: x\n  a_number: 1\n"))

	askFor(t, p, []string{"svc"}, &strictConfig{})

	want := []string{"svc.a_null", "svc.a_number", "svc.a_string", "svc.an_array"}

	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The names strict mode knows are the ones fields are read by.
func TestCheckKnowsTheEffectiveNames(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Addr string `config:"addr"`
		Port int    `config:"port" yaml:"portNo"`
	}

	p := yaml.Raw([]byte("svc:\n  addr: ':1'\n  portNo: 1\n  port: 2\n"))

	askFor(t, p, []string{"svc"}, &cfg{})

	// "port" was the common name that the yaml tag replaced, so it is a typo.
	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"svc.port"}) {
		t.Fatalf("got %v", got)
	}
}

func TestDescribeDoesNotCountAsAsking(t *testing.T) {
	t.Parallel()

	p := yaml.Raw([]byte("svc:\n  addr: ':1'\n"))

	if _, err := p.Describe([]string{"svc"}, &strictConfig{}); err != nil {
		t.Fatal(err)
	}

	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"svc.addr"}) {
		t.Fatalf("got %v", got)
	}
}

func TestCheckFindsNothingUnderARootThatIsNotThere(t *testing.T) {
	t.Parallel()

	for _, doc := range []string{"", "other: {a: 1}\n", "svc:\n", "svc: ~\n"} {
		if err := yaml.Raw([]byte(doc)).Check([]string{"svc"}); err != nil {
			t.Errorf("%q: %v", doc, err)
		}
	}
}

func TestCheckIgnoresWhatIsOutsideTheRoot(t *testing.T) {
	t.Parallel()

	p := yaml.Raw([]byte("svc:\n  addr: ':1'\nneighbour:\n  anything: 1\n"))

	askFor(t, p, []string{"svc"}, &strictConfig{})

	if err := p.Check([]string{"svc"}); err != nil {
		t.Fatal(err)
	}
}

// Keys nobody reads are not a reason to refuse a file for being ambiguous, and
// strict mode reports a repeated unknown key as the one problem it is.
func TestCheckWithDuplicatesNobodyReads(t *testing.T) {
	t.Parallel()

	p := yaml.Raw([]byte("svc:\n  addr: ':1'\n  _comment: a\n  _comment: b\n"))

	askFor(t, p, []string{"svc"}, &strictConfig{})

	if got := unknownKeys(t, p.Check([]string{"svc"})); len(got) == 0 || got[0] != "svc._comment" {
		t.Fatalf("got %v", got)
	}
}

func TestCheckOnAFileNamesTheFile(t *testing.T) {
	t.Parallel()

	path := writeDoc(t, []byte("svc:\n  addr: ':1'\n  adrr: ':9'\n"))

	p := yaml.File(path)

	askFor(t, p, []string{"svc"}, &strictConfig{})

	err := p.Check([]string{"svc"})

	if got := unknownKeys(t, err); !slices.Equal(got, []string{"svc.adrr"}) || !strings.Contains(err.Error(), path) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestCheckIsConcurrencySafe(t *testing.T) {
	t.Parallel()

	p := yaml.Raw([]byte("svc:\n  addr: ':1'\n  http:\n    port: 1\n"))

	done := make(chan struct{})

	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()

			var cfg strictConfig

			_ = p.Unmarshal([]string{"svc"}, &cfg)
			_ = p.Check([]string{"svc"})
		}()
	}

	for range 8 {
		<-done
	}
}
