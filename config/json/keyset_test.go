package json_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.krak3n.dev/foundation/config"
	configjson "go.krak3n.dev/foundation/config/json"
)

type strictConfig struct {
	Addr string `json:"addr"`
	Name string `json:"name"`
	TLS  struct {
		CertFile string `json:"cert_file"`
		Port     int    `json:"port"`
	} `json:"tls"`
}

// unknownKeys returns the document paths Check reported, in the order reported.
func unknownKeys(t *testing.T, err error) []string {
	t.Helper()

	var keys []string

	for _, fe := range fieldErrors(err) {
		if fe.Provider != "json" || fe.Field != "" || !errors.Is(fe, config.ErrUnknown) {
			t.Fatalf("expected an unknown key error from json with no field, got %+v", fe)
		}

		keys = append(keys, fe.Key)
	}

	return keys
}

// askFor asks for the fields of cfg at path, as f.Load would.
func askFor(t *testing.T, p config.Provider, path []string, cfg any) {
	t.Helper()

	if err := p.Unmarshal(path, cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckFlagsKeysNothingAskedFor(t *testing.T) {
	p := configjson.Raw([]byte(`{"svc": {
		"addr": ":1", "name": "x", "adrr": ":9",
		"tls": {"cert_file": "c", "cert_flie": "d"}
	}}`))

	askFor(t, p, []string{"svc"}, &strictConfig{})

	err := p.Check([]string{"svc"})

	// By their path in the document, sorted.
	if got := unknownKeys(t, err); !slices.Equal(got, []string{"svc.adrr", "svc.tls.cert_flie"}) {
		t.Fatalf("unexpected unknown keys %v", got)
	}

	if !strings.Contains(err.Error(), "json: svc.adrr: not recognised") {
		t.Fatalf("unexpected message %q", err.Error())
	}
}

func TestCheckFindsNothingWhenEverythingWasAskedFor(t *testing.T) {
	p := configjson.Raw([]byte(`{"svc": {"addr": ":1", "tls": {"port": 1}}}`))

	askFor(t, p, []string{"svc"}, &strictConfig{})

	if err := p.Check([]string{"svc"}); err != nil {
		t.Fatalf("expected no problems, got %v", err)
	}
}

// A field the document has no value for is still asked for: only what is in the
// document and unasked can be unknown.
func TestAFieldTheDocumentLacksIsStillKnown(t *testing.T) {
	p := configjson.Raw([]byte(`{"svc": {"name": "x"}}`))

	askFor(t, p, []string{"svc"}, &strictConfig{})

	if err := p.Check([]string{"svc"}); err != nil {
		t.Fatalf("expected no problems, got %v", err)
	}
}

// Keys are matched without regard to case, as they are when loading, and are
// reported as the document spells them.
func TestKeysAreMatchedCaseInsensitively(t *testing.T) {
	p := configjson.Raw([]byte(`{"SVC": {"ADDR": ":1", "Tls": {"CERT_FILE": "c", "PORT": 1}, "ADRR": ":9"}}`))

	askFor(t, p, []string{"svc"}, &strictConfig{})

	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"SVC.ADRR"}) {
		t.Fatalf("unexpected unknown keys %v", got)
	}
}

func TestChildFunctionsKeysAreKnownOnceTheyLoad(t *testing.T) {
	type child struct {
		Port int `json:"port"`
	}

	p := configjson.Raw([]byte(`{"svc": {"addr": ":1", "http": {"port": 1, "prot": 2}}}`))

	askFor(t, p, []string{"svc"}, &strictConfig{})
	askFor(t, p, []string{"svc", "http"}, &child{})

	// "http" is on the way to the child's keys, so it is known, and only the typo
	// inside it is not.
	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"svc.http.prot"}) {
		t.Fatalf("unexpected unknown keys %v", got)
	}
}

func TestAChildThatNeverLoadedIsReportedOnce(t *testing.T) {
	p := configjson.Raw([]byte(`{"svc": {"addr": ":1", "http": {"port": 1, "nested": {"a": 1}}}}`))

	askFor(t, p, []string{"svc"}, &strictConfig{})

	// Nothing under "http" was asked for, so it is one problem, not one for every
	// key in it.
	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"svc.http"}) {
		t.Fatalf("unexpected unknown keys %v", got)
	}
}

func TestAnUnknownKeyIsReportedWhateverItsValue(t *testing.T) {
	p := configjson.Raw([]byte(`{"svc": {"addr": ":1", "an_array": [1, 2], "a_null": null, "a_string": "x", "a_number": 1}}`))

	askFor(t, p, []string{"svc"}, &strictConfig{})

	want := []string{"svc.a_null", "svc.a_number", "svc.a_string", "svc.an_array"}

	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, want) {
		t.Fatalf("unexpected unknown keys %v, want %v", got, want)
	}
}

func TestCheckResultsAreSorted(t *testing.T) {
	p := configjson.Raw([]byte(`{"svc": {"zeta": 1, "alpha": 1, "mid": 1, "beta": 1, "omega": 1}}`))

	want := []string{"svc.alpha", "svc.beta", "svc.mid", "svc.omega", "svc.zeta"}

	// A document is an unordered map, so this only holds if the order is imposed.
	for range 20 {
		if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, want) {
			t.Fatalf("unexpected order %v", got)
		}
	}
}

func TestCheckOnlyLooksUnderTheRoot(t *testing.T) {
	tests := map[string]string{
		"other applications":    `{"other": {"x": 1}, "svc": {"addr": ":1"}, "svc2": {"y": 1}}`,
		"no object for root":    `{"other": {"x": 1}}`,
		"root is not an object": `{"svc": "x"}`,
		"root is null":          `{"svc": null}`,
		"empty document":        `{}`,
	}

	for name, doc := range tests {
		t.Run(name, func(t *testing.T) {
			p := configjson.Raw([]byte(doc))

			askFor(t, p, []string{"svc"}, &strictConfig{})

			if err := p.Check([]string{"svc"}); err != nil {
				t.Fatalf("expected no problems, got %v", err)
			}
		})
	}
}

// What is inside a field's value is the value, not more configuration.
func TestFieldsAreNotDescendedInto(t *testing.T) {
	type cfg struct {
		Labels  map[string]string `json:"labels"`
		Servers []string          `json:"servers"`
		TLS     struct {
			Port int `json:"port"`
		} `json:"tls"`
	}

	p := configjson.Raw([]byte(`{"svc": {
		"labels": {"team": "core", "tier": "backend"},
		"servers": ["a", "b"],
		"tls": "not an object"
	}}`))

	// Loading finds the problem with the last value, which is not strict mode's
	// business.
	_ = p.Unmarshal([]string{"svc"}, &cfg{})

	if err := p.Check([]string{"svc"}); err != nil {
		t.Fatalf("expected no problems, got %v", err)
	}
}

func TestCheckBeforeAnythingIsLoadedFlagsEverythingUnderTheRoot(t *testing.T) {
	p := configjson.Raw([]byte(`{"svc": {"addr": ":1", "tls": {"port": 1}}}`))

	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"svc.addr", "svc.tls"}) {
		t.Fatalf("unexpected unknown keys %v", got)
	}
}

func TestFieldsWithMistakesAreKnown(t *testing.T) {
	type cfg struct {
		Bad  chan int `json:"bad"`
		Name string   `json:"name"`
	}

	p := configjson.Raw([]byte(`{"svc": {"bad": 1, "name": "x"}}`))

	// A field that can never load still owns its key, so it isn't also reported
	// as unknown.
	if err := p.Unmarshal([]string{"svc"}, &cfg{}); err == nil {
		t.Fatal("expected a problem with the field")
	}

	if err := p.Check([]string{"svc"}); err != nil {
		t.Fatalf("expected no unknown keys, got %v", err)
	}
}

func TestDottedRootNamesAreWalkedAsNestedObjects(t *testing.T) {
	type cfg struct {
		Addr string `json:"addr"`
	}

	p := configjson.Raw([]byte(`{"svc": {"http": {"server": {"addr": ":1", "adrr": ":2"}}}}`))

	askFor(t, p, []string{"svc", "http.server"}, &cfg{})

	if got := unknownKeys(t, p.Check([]string{"svc", "http.server"})); !slices.Equal(got, []string{"svc.http.server.adrr"}) {
		t.Fatalf("unexpected unknown keys %v", got)
	}
}

func TestEmbeddedFieldsAreKnownInTheParent(t *testing.T) {
	type cfg struct {
		Shared
		Name string `json:"name"`
	}

	p := configjson.Raw([]byte(`{"svc": {"region": "eu", "name": "x", "regoin": "e"}}`))

	askFor(t, p, []string{"svc"}, &cfg{})

	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"svc.regoin"}) {
		t.Fatalf("unexpected unknown keys %v", got)
	}
}

func TestCheckOfARawProviderDoesNotNameAFile(t *testing.T) {
	p := configjson.Raw([]byte(`{"svc": {"adrr": 1}}`))

	if msg := p.Check([]string{"svc"}).Error(); msg != "json: svc.adrr: not recognised" {
		t.Fatalf("unexpected message %q", msg)
	}
}

func TestCheckIsSafeToCallWhileLoading(t *testing.T) {
	p := configjson.Raw([]byte(`{"svc": {"addr": ":1", "typo": 1}}`))

	var wg sync.WaitGroup

	for range 10 {
		wg.Go(func() {
			for range 50 {
				_ = p.Unmarshal([]string{"svc"}, &strictConfig{})
			}
		})

		wg.Go(func() {
			for range 50 {
				_ = p.Check([]string{"svc"})
			}
		})
	}

	wg.Wait()

	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"svc.typo"}) {
		t.Fatalf("unexpected unknown keys %v", got)
	}
}

// With several files configured, the path is how you tell which one has the
// typo.
func TestCheckOfAFileNamesTheFile(t *testing.T) {
	path := writeJSON(t, `{"svc": {"addr": ":1", "adrr": ":9"}}`)

	p := configjson.File(path)

	askFor(t, p, []string{"svc"}, &strictConfig{})

	err := p.Check([]string{"svc"})

	if got := unknownKeys(t, err); !slices.Equal(got, []string{"svc.adrr"}) {
		t.Fatalf("unexpected unknown keys %v", got)
	}

	if want := "json: svc.adrr: not recognised (in " + path + ")"; err.Error() != want {
		t.Fatalf("unexpected message\n got: %s\nwant: %s", err.Error(), want)
	}
}

func TestCheckOfAMissingFileFindsNothing(t *testing.T) {
	p := configjson.File(filepath.Join(t.TempDir(), "absent.json"))

	askFor(t, p, []string{"svc"}, &strictConfig{})

	if err := p.Check([]string{"svc"}); err != nil {
		t.Fatalf("expected a missing file to have nothing to report, got %v", err)
	}
}

func TestCheckOfAFileThatIsNotJSONIsAPlainError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := configjson.File(path).Check([]string{"svc"})

	if err == nil || !strings.HasPrefix(err.Error(), "json:") || len(fieldErrors(err)) != 0 {
		t.Fatalf("expected a plain json error, got %v", err)
	}
}
