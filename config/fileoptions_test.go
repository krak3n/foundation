package config_test

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"go.krak3n.dev/foundation/config"
)

// The options that every file or directory provider takes are defined here, once,
// so a provider of any format (and one written elsewhere) means the same by them.
// Providers read them through ResolveFile. An option is a value of the framework's
// own: nobody else can make one, and nothing in an environment or a document can.

func TestResolveFile(t *testing.T) {
	tests := map[string]struct {
		opts []config.FileOption
		want config.FileSettings
	}{
		// The zero value is the strict one: the check is on and nothing is required,
		// so leaving the options out never makes a provider weaker.
		"none":                     {nil, config.FileSettings{}},
		"empty":                    {[]config.FileOption{}, config.FileSettings{}},
		"a nil option":             {[]config.FileOption{nil}, config.FileSettings{}},
		"only nils":                {[]config.FileOption{nil, nil}, config.FileSettings{}},
		"required":                 {[]config.FileOption{config.WithRequired()}, config.FileSettings{Required: true}},
		"without the check":        {[]config.FileOption{config.WithoutPermissionCheck()}, config.FileSettings{NoPermissionCheck: true}},
		"both":                     {[]config.FileOption{config.WithRequired(), config.WithoutPermissionCheck()}, config.FileSettings{Required: true, NoPermissionCheck: true}},
		"both, the other way":      {[]config.FileOption{config.WithoutPermissionCheck(), config.WithRequired()}, config.FileSettings{Required: true, NoPermissionCheck: true}},
		"twice":                    {[]config.FileOption{config.WithRequired(), config.WithRequired()}, config.FileSettings{Required: true}},
		"a nil between":            {[]config.FileOption{config.WithRequired(), nil, config.WithoutPermissionCheck()}, config.FileSettings{Required: true, NoPermissionCheck: true}},
		"a slice":                  {[]config.FileOption{config.FileOptions{config.WithRequired()}}, config.FileSettings{Required: true}},
		"an empty slice":           {[]config.FileOption{config.FileOptions{}}, config.FileSettings{}},
		"a nil slice":              {[]config.FileOption{config.FileOptions(nil)}, config.FileSettings{}},
		"a slice with a nil":       {[]config.FileOption{config.FileOptions{nil, config.WithoutPermissionCheck()}}, config.FileSettings{NoPermissionCheck: true}},
		"slices inside slices":     {[]config.FileOption{config.FileOptions{config.FileOptions{config.WithRequired()}, config.WithoutPermissionCheck()}}, config.FileSettings{Required: true, NoPermissionCheck: true}},
		"a slice beside an option": {[]config.FileOption{config.FileOptions{config.WithRequired()}, config.WithoutPermissionCheck()}, config.FileSettings{Required: true, NoPermissionCheck: true}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := config.ResolveFile(tt.opts...); got != tt.want {
				t.Fatalf("ResolveFile = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// An option is a value: resolving does not change it, and what one call resolves
// does not leak into the next. There is no state that is shared.
func TestResolveFileKeepsNoState(t *testing.T) {
	relaxed := config.WithoutPermissionCheck()

	if got := config.ResolveFile(relaxed); !got.NoPermissionCheck {
		t.Fatalf("the opt-out was not applied: %+v", got)
	}

	if got := config.ResolveFile(); got != (config.FileSettings{}) {
		t.Errorf("a call with no options was changed by an earlier one: %+v", got)
	}

	if got := config.ResolveFile(config.WithRequired()); got.NoPermissionCheck {
		t.Errorf("a call was changed by an earlier one: %+v", got)
	}

	// The same option used twice says the same thing both times.
	if a, b := config.ResolveFile(relaxed), config.ResolveFile(relaxed); a != b {
		t.Errorf("one option resolved to %+v and then %+v", a, b)
	}
}

// Options given as a slice are applied in order and a slice does not hold on to
// the caller's: appending to it afterwards changes nothing already resolved.
func TestResolveFileDoesNotHoldTheCallersSlice(t *testing.T) {
	opts := config.FileOptions{config.WithRequired()}

	got := config.ResolveFile(opts)

	opts[0] = config.WithoutPermissionCheck()

	if !got.Required || got.NoPermissionCheck {
		t.Errorf("a result changed after the slice did: %+v", got)
	}
}

// config is the package that third party providers import, and it imports no
// package of the framework that depends on it: nothing here may reach out, so
// that imports always point at the framework and never back.
func TestConfigImportsNothingOfTheFramework(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	checked := 0

	for _, e := range entries {
		name := e.Name()

		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}

		checked++

		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)

			if first, _, _ := strings.Cut(path, "/"); !strings.Contains(first, ".") {
				continue // the standard library
			}

			if !strings.HasPrefix(path, "go.krak3n.dev/foundation/internal/") {
				t.Errorf("%s imports %s", name, path)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no source files found")
	}
}
