package env_test

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
)

type strictConfig struct {
	Addr string `env:"ADDR"`
	Name string `env:"NAME"`
}

// unknownKeys returns the keys Check reported as unknown, sorted.
func unknownKeys(t *testing.T, err error) []string {
	t.Helper()

	var keys []string

	for _, fe := range fieldErrors(err) {
		if fe.Provider != "env" || fe.Field != "" || !errors.Is(fe, config.ErrUnknown) {
			t.Fatalf("expected an unknown variable error from env with no field, got %+v", fe)
		}

		keys = append(keys, fe.Key)
	}

	slices.Sort(keys)

	return keys
}

func TestWithEnvironIsWhereCheckLooksForVariables(t *testing.T) {
	t.Setenv("SVC_FROM_PROCESS", "x")

	p := env.New(
		env.WithLookup(func(string) (string, bool) { return "", false }),
		env.WithEnviron(func() []string { return []string{"SVC_INJECTED=1", "OTHER=2"} }),
	)

	err := p.Check([]string{"svc"})

	// The process environment is not consulted at all.
	if got := unknownKeys(t, err); !slices.Equal(got, []string{"SVC_INJECTED"}) {
		t.Fatalf("unexpected unknown variables %v", got)
	}
}

func TestWithEnvironNilMeansTheProcessEnvironment(t *testing.T) {
	t.Setenv("SVC_FROM_PROCESS", "x")

	err := env.New(env.WithEnviron(nil)).Check([]string{"svc"})

	if got := unknownKeys(t, err); !slices.Contains(got, "SVC_FROM_PROCESS") {
		t.Fatalf("expected the process environment to be checked, got %v", got)
	}
}

func TestTheProcessEnvironmentIsTheDefault(t *testing.T) {
	t.Setenv("SVC_ADRR", ":9")

	err := env.New().Check([]string{"svc"})

	if got := unknownKeys(t, err); !slices.Contains(got, "SVC_ADRR") {
		t.Fatalf("expected the process environment to be checked by default, got %v", got)
	}
}

// WithMap is the one option a test needs: the same map answers lookups and is
// what Check lists.
func TestWithMapSetsBothTheLookupAndTheEnvironment(t *testing.T) {
	p := env.New(env.WithMap(map[string]string{"SVC_ADDR": ":8080", "SVC_ADRR": ":9"}))

	var cfg strictConfig

	if err := p.Unmarshal([]string{"svc"}, &cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.Addr != ":8080" {
		t.Fatalf("expected the map to answer lookups, got %q", cfg.Addr)
	}

	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"SVC_ADRR"}) {
		t.Fatalf("expected the map to be what Check lists, got %v", got)
	}
}

func TestWithMapNilIsAnEmptyEnvironment(t *testing.T) {
	t.Setenv("SVC_FROM_PROCESS", "x")

	if err := env.New(env.WithMap(nil)).Check([]string{"svc"}); err != nil {
		t.Fatalf("expected an empty environment, and so no problems, got %v", err)
	}
}

func TestCheckFlagsVariablesNothingAskedFor(t *testing.T) {
	p := env.New(env.WithMap(map[string]string{
		"SVC_ADDR": ":8080", // asked for
		"SVC_NAME": "x",     // asked for
		"SVC_ADRR": ":9",    // a typo: nothing asked for it
		"OTHER_X":  "1",     // not ours
	}))

	if err := p.Unmarshal([]string{"svc"}, &strictConfig{}); err != nil {
		t.Fatal(err)
	}

	err := p.Check([]string{"svc"})

	if got := unknownKeys(t, err); !slices.Equal(got, []string{"SVC_ADRR"}) {
		t.Fatalf("expected only the typo to be flagged, got %v", got)
	}

	if msg := err.Error(); msg != "env: SVC_ADRR: not recognised" {
		t.Fatalf("unexpected message %q", msg)
	}
}

func TestCheckFindsNothingWhenEverythingWasAskedFor(t *testing.T) {
	p := env.New(env.WithMap(map[string]string{"SVC_ADDR": ":8080"}))

	if err := p.Unmarshal([]string{"svc"}, &strictConfig{}); err != nil {
		t.Fatal(err)
	}

	if err := p.Check([]string{"svc"}); err != nil {
		t.Fatalf("expected no problems, got %v", err)
	}
}

// What counts is what was asked for, not what was set: a field with no variable
// still claims its key, so a variable that appears later under that name, or in
// another source, is not a typo.
func TestKeysAreRecordedWhetherOrNotAValueWasSet(t *testing.T) {
	p := env.New(
		env.WithLookup(func(string) (string, bool) { return "", false }),
		env.WithEnviron(func() []string { return []string{"SVC_ADDR=:x"} }),
	)

	if err := p.Unmarshal([]string{"svc"}, &strictConfig{}); err != nil {
		t.Fatal(err)
	}

	if err := p.Check([]string{"svc"}); err != nil {
		t.Fatalf("expected a key that was asked for to be known, got %v", err)
	}
}

func TestCheckBeforeAnythingIsLoadedFlagsEverythingUnderThePrefix(t *testing.T) {
	p := env.New(env.WithMap(map[string]string{"SVC_ADDR": ":8080", "SVC_NAME": "x"}))

	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"SVC_ADDR", "SVC_NAME"}) {
		t.Fatalf("unexpected unknown variables %v", got)
	}
}

func TestCheckOnlyLooksUnderTheRootsPrefix(t *testing.T) {
	p := env.New(env.WithMap(map[string]string{
		"SVC_A":         "1", // under the prefix
		"SVCX_B":        "2", // a different name that merely starts the same
		"SVC":           "3", // the name itself, with no underscore
		"OTHER_SVC_C":   "4",
		"svc_lowercase": "5", // a different variable, so not under SVC_ either
	}))

	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"SVC_A"}) {
		t.Fatalf("unexpected unknown variables %v", got)
	}
}

func TestCheckUsesTheRootsKeysForThePrefix(t *testing.T) {
	p := env.New(env.WithMap(map[string]string{"SVC_HTTP_SERVER_ADRR": "1", "SVC_ADRR": "2"}))

	if got := unknownKeys(t, p.Check([]string{"svc", "http.server"})); !slices.Equal(got, []string{"SVC_HTTP_SERVER_ADRR"}) {
		t.Fatalf("unexpected unknown variables %v", got)
	}
}

func TestCheckReportsEveryUnknownVariable(t *testing.T) {
	p := env.New(env.WithMap(map[string]string{"SVC_A": "1", "SVC_B": "2", "SVC_C": "3"}))

	err := p.Check([]string{"svc"})

	if got := unknownKeys(t, err); !slices.Equal(got, []string{"SVC_A", "SVC_B", "SVC_C"}) {
		t.Fatalf("expected every problem to be reported, got %v", got)
	}
}

func TestCheckToleratesAwkwardEnvironEntries(t *testing.T) {
	p := env.New(env.WithEnviron(func() []string {
		return []string{"", "SVC_NO_EQUALS", "SVC_URL=a=b=c", "=SVC_ODD"}
	}))

	// A value may hold "="; an entry with none is a name with no value; nothing
	// panics.
	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"SVC_NO_EQUALS", "SVC_URL"}) {
		t.Fatalf("unexpected unknown variables %v", got)
	}
}

// Check can only list variables from a source it knows how to list. A custom
// lookup on its own says nothing about what the environment holds, so rather
// than check the wrong place it says what is missing.
func TestCheckNeedsAnEnvironmentWhenTheLookupIsCustom(t *testing.T) {
	custom := func(string) (string, bool) { return "", false }

	err := env.New(env.WithLookup(custom)).Check([]string{"svc"})

	if err == nil || !strings.Contains(err.Error(), "WithEnviron") || len(fieldErrors(err)) != 0 {
		t.Fatalf("expected a plain error pointing at WithEnviron, got %v", err)
	}

	// Each of these supplies, or defaults, a source to list.
	for name, p := range map[string]*env.Provider{
		"with an environ":    env.New(env.WithLookup(custom), env.WithEnviron(func() []string { return nil })),
		"a nil lookup":       env.New(env.WithLookup(nil)),
		"a map":              env.New(env.WithMap(map[string]string{})),
		"no options":         env.New(),
		"environ and lookup": env.New(env.WithEnviron(func() []string { return nil }), env.WithLookup(custom)),
	} {
		if err := p.Check([]string{"svc"}); err != nil && strings.Contains(err.Error(), "WithEnviron") {
			t.Errorf("%s: unexpected complaint about WithEnviron: %v", name, err)
		}
	}
}

func TestCheckIsSafeToCallWhileLoading(t *testing.T) {
	p := env.New(env.WithMap(map[string]string{"SVC_ADDR": ":x", "SVC_TYPO": "1"}))

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

	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"SVC_TYPO"}) {
		t.Fatalf("unexpected unknown variables %v", got)
	}
}

// A variable named after a nested struct is not read by anything: only the
// variables inside it are.
func TestCheckFlagsAVariableThatNamesAGroup(t *testing.T) {
	type cfg struct {
		TLS struct {
			CertFile string `env:"CERT_FILE"`
		} `env:"TLS"`
	}

	p := env.New(env.WithMap(map[string]string{"SVC_TLS": "true", "SVC_TLS_CERT_FILE": "c.pem"}))

	if err := p.Unmarshal([]string{"svc"}, &cfg{}); err != nil {
		t.Fatal(err)
	}

	if got := unknownKeys(t, p.Check([]string{"svc"})); !slices.Equal(got, []string{"SVC_TLS"}) {
		t.Fatalf("unexpected unknown variables %v", got)
	}
}

func TestCheckUsesTheKeysOfARootNameWithADot(t *testing.T) {
	p := env.New(env.WithMap(map[string]string{"MY_APP_X": "1", "MY_X": "2", "MY_APPX": "3"}))

	if got := unknownKeys(t, p.Check([]string{"my.app"})); !slices.Equal(got, []string{"MY_APP_X"}) {
		t.Fatalf("unexpected unknown variables %v", got)
	}
}
