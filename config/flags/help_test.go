package flags_test

import (
	"slices"
	"testing"
)

type apiConfig struct {
	Workers int `flag:"workers,short=w"`
}

// Help says whether --help or -h was given, and which function it was given
// for: the markers in force where it stands, which is what narrows the help to
// one function. The scope is known once the functions have loaded, as it is
// they who claim the markers, so Help is asked again at the end of startup.
func TestHelpIsNotRequestedByDefault(t *testing.T) {
	t.Parallel()

	p := newProvider("--api", "--workers=2")

	scope, ok, err := p.Help()
	if ok || err != nil || len(scope) != 0 {
		t.Fatalf("got %q, %v, %v", scope, ok, err)
	}
}

func TestHelpIsRequestedByEitherSpelling(t *testing.T) {
	t.Parallel()

	for _, arg := range []string{"--help", "-h"} {
		p := newProvider(arg)

		scope, ok, err := p.Help()
		if !ok || err != nil || len(scope) != 0 {
			t.Errorf("%s: got %q, %v, %v", arg, scope, ok, err)
		}

		// It is read, so it is not an argument nobody understood.
		if keys := check(t, p); keys != nil {
			t.Errorf("%s: Check reported %q", arg, keys)
		}
	}
}

// The scope is the nearest marker before it that a function claimed.
func TestHelpIsForTheFunctionInForce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		path []string
		want []string
	}{
		{"root", []string{"--help"}, []string{"svc", "api"}, nil},
		{"marker", []string{"--api", "--help"}, []string{"svc", "api"}, []string{"api"}},
		{"short", []string{"--api", "-h"}, []string{"svc", "api"}, []string{"api"}},
		{"after its flags", []string{"--api", "--workers", "3", "--help"}, []string{"svc", "api"}, []string{"api"}},
		{"dotted name", []string{"--http", "--server", "--help"}, []string{"svc", "http.server"}, []string{"http", "server"}},
		{"sub command", []string{"--a", "--c", "--help"}, []string{"svc", "a", "c"}, []string{"a", "c"}},
		{"help first", []string{"--help", "--api"}, []string{"svc", "api"}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := newProvider(tt.args...)

			// Asked early, before anything loaded, it only says it was asked for.
			if _, ok, err := p.Help(); !ok || err != nil {
				t.Fatalf("early: %v, %v", ok, err)
			}

			if tt.path != nil {
				_ = p.Unmarshal(tt.path, &apiConfig{})
			}

			scope, ok, err := p.Help()
			if !ok || err != nil || !slices.Equal(scope, tt.want) {
				t.Fatalf("got %q, %v, %v; want %q", scope, ok, err, tt.want)
			}
		})
	}
}

// The scope is the markers that functions claimed. A marker nobody claimed is not
// a scope, and is reported as an argument nobody read.
func TestHelpAfterAnUnknownMarkerIsNotItsScope(t *testing.T) {
	t.Parallel()

	p := newProvider("--nope", "--help")

	mustUnmarshal(t, p, root, &scoped{})

	scope, ok, err := p.Help()
	if !ok || err != nil || len(scope) != 0 {
		t.Fatalf("got %q, %v, %v", scope, ok, err)
	}

	assertKeys(t, check(t, p), "--nope")
}

// --help doesn't end a function's scope: what follows it is still that
// function's.
func TestHelpDoesNotEndAScope(t *testing.T) {
	t.Parallel()

	p := newProvider("--api", "--help", "--workers", "3")

	var got apiConfig

	mustUnmarshal(t, p, []string{"svc", "api"}, &got)

	if got.Workers != 3 {
		t.Fatalf("got %+v", got)
	}

	if _, ok, err := p.Help(); !ok || err != nil {
		t.Fatalf("%v, %v", ok, err)
	}

	if keys := check(t, p); keys != nil {
		t.Fatalf("Check reported %q", keys)
	}
}

// A value is not a flag, whatever it looks like.
func TestAValueThatLooksLikeHelpIsAValue(t *testing.T) {
	t.Parallel()

	p := newProvider("--string=--help", "--num", "1")

	var got scoped

	mustUnmarshal(t, p, root, &got)

	if _, ok, err := p.Help(); ok || err != nil {
		t.Fatalf("%v, %v", ok, err)
	}

	if got.String != "--help" || got.Num != 1 {
		t.Fatalf("got %+v", got)
	}
}

// Asking twice, or for something that takes no value, is a mistake, not a help
// request: nothing is guessed.
func TestHelpMistakesAreProblems(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		{"--help", "--help"},
		{"-h", "-h"},
		{"--help", "-h"},
		{"--help=true"},
		{"--help=" + canary},
		{"-h=" + canary},
		{"--help=", "--api"},
	}

	for _, args := range tests {
		p := newProvider(args...)

		_, ok, err := p.Help()

		if ok || err == nil {
			t.Errorf("%q: got %v, %v, want a problem", args, ok, err)

			continue
		}

		errs := fieldErrors(err)
		if len(errs) == 0 || errs[0].Provider != "flags" {
			t.Errorf("%q: %v is not a problem from flags", args, err)
		}

		assertNoCanary(t, err)
	}
}

// A function can't have a flag called help, in any scope: the word means the same
// everywhere, so that it can be given anywhere.
func TestNoFunctionCanClaimHelp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dst  any
	}{
		{"help", &struct {
			X bool `flag:"help"`
		}{}},
		{"short h", &struct {
			X bool `flag:"hush,short=h"`
		}{}},
	}

	for _, tt := range tests {
		for _, path := range [][]string{root, {"svc", "api"}} {
			err := newProvider().Unmarshal(path, tt.dst)

			if errs := fieldErrors(err); len(errs) != 1 || errs[0].Field != "X" {
				t.Errorf("%s at %v: got %v, want one problem with X", tt.name, path, err)
			}
		}
	}
}

// Help is only ever given as an argument.
func TestHelpComesOnlyFromArguments(t *testing.T) {
	t.Parallel()

	if _, ok, err := newProvider().Help(); ok || err != nil {
		t.Fatalf("%v, %v", ok, err)
	}
}

func FuzzHelp(f *testing.F) {
	for _, seed := range []string{"--help", "-h", "--api", "--help=x", "-h=", "--", "-", "--workers", "3", "\x00", "--help\n"} {
		f.Add(seed, "--api", seed)
	}

	f.Fuzz(func(t *testing.T, a, b, c string) {
		p := newProvider(a, b, c)

		_ = p.Unmarshal([]string{"svc", "api"}, &apiConfig{})

		scope, ok, err := p.Help()
		if err != nil && (ok || len(fieldErrors(err)) == 0) {
			t.Fatalf("%q: ok %v, err %v", []string{a, b, c}, ok, err)
		}

		if !ok && len(scope) != 0 {
			t.Fatalf("%q: a scope without a request: %q", []string{a, b, c}, scope)
		}

		_ = p.Check(root)
	})
}
