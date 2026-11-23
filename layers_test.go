package foundation_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/flags"
	"go.krak3n.dev/foundation/config/json"
)

// Configuration comes from four layers, in an order the framework fixes: the
// document layer (files), the remote layer, the environment layer and the command
// layer (flags), each later one overriding the one before. A provider says which
// layer it belongs to by its type, so it can't be put in the wrong one, and the
// options either add to a layer (With...Config) or replace it (Set...Config).

// layered is what these tests load. Each field has a flag so that the default
// command layer can set it when arguments are given.
type layered struct {
	A string `config:"a" flag:"a"`
	B string `config:"b" flag:"b"`
	C string `config:"c" flag:"c"`
	D string `config:"d" flag:"d"`
}

// needsA has a required field that no provider in these tests sets.
type needsA struct {
	A string `config:"a,required"`
}

// recorder notes what the fake providers were asked, in order.
type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) add(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, call)
}

// only returns the calls that start with prefix, with it removed.
func (r *recorder) only(prefix string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []string

	for _, c := range r.calls {
		if rest, ok := strings.CutPrefix(c, prefix); ok {
			out = append(out, rest)
		}
	}

	return out
}

func (r *recorder) any() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.calls) > 0
}

// fakeLayer is a provider that sets the fields it is told to, to its own name,
// and records that it was asked. It can describe and check as well.
type fakeLayer struct {
	name string
	sets string // fields it sets, such as "AB"
	rec  *recorder
}

func (p *fakeLayer) Unmarshal(_ []string, dst any) error {
	p.rec.add("load:" + p.name)

	c, ok := dst.(*layered)
	if !ok {
		return nil
	}

	for _, field := range p.sets {
		switch field {
		case 'A':
			c.A = p.name
		case 'B':
			c.B = p.name
		case 'C':
			c.C = p.name
		case 'D':
			c.D = p.name
		}
	}

	return nil
}

func (p *fakeLayer) Describe(_ []string, _ any) ([]config.FieldKey, error) {
	p.rec.add("describe:" + p.name)

	return []config.FieldKey{{Field: "A", Key: p.name + "-key"}}, nil
}

func (p *fakeLayer) Check(_ []string) error {
	p.rec.add("check:" + p.name)

	return nil
}

// One type per layer, so the marker is what puts a provider in a layer.
type (
	docFake    struct{ *fakeLayer }
	remoteFake struct{ *fakeLayer }
	envFake    struct{ *fakeLayer }
	cmdFake    struct{ *fakeLayer }
)

func (docFake) DocumentLayer()    {}
func (remoteFake) RemoteLayer()   {}
func (envFake) EnvironmentLayer() {}
func (cmdFake) CommandLayer()     {}
func newFake(name, sets string, rec *recorder) *fakeLayer {
	return &fakeLayer{name: name, sets: sets, rec: rec}
}

// layerCase is one layer, with a way to add fakes to it and to replace it with
// them. Each fake sets A to its own name.
type layerCase struct {
	name string

	with func(rec *recorder, names ...string) foundation.Option
	set  func(rec *recorder, names ...string) foundation.Option

	// setup makes the default for the layer present, so it can be seen to be
	// read, or not. The default sets B to "default". nil if the layer has none,
	// as the document and remote layers don't: they are empty until a program
	// names something.
	setup func(t *testing.T) []foundation.Option
}

func layerCases() []layerCase {
	return []layerCase{
		{
			name: "document",
			with: func(rec *recorder, names ...string) foundation.Option {
				var layers []config.DocumentLayer
				for _, n := range names {
					layers = append(layers, docFake{newFake(n, "A", rec)})
				}

				return foundation.WithDocumentConfig(layers...)
			},
			set: func(rec *recorder, names ...string) foundation.Option {
				var layers []config.DocumentLayer
				for _, n := range names {
					layers = append(layers, docFake{newFake(n, "A", rec)})
				}

				return foundation.SetDocumentConfig(layers...)
			},
		},
		{
			name: "remote",
			with: func(rec *recorder, names ...string) foundation.Option {
				var layers []config.RemoteLayer
				for _, n := range names {
					layers = append(layers, remoteFake{newFake(n, "A", rec)})
				}

				return foundation.WithRemoteConfig(layers...)
			},
			set: func(rec *recorder, names ...string) foundation.Option {
				var layers []config.RemoteLayer
				for _, n := range names {
					layers = append(layers, remoteFake{newFake(n, "A", rec)})
				}

				return foundation.SetRemoteConfig(layers...)
			},
		},
		{
			name: "environment",
			with: func(rec *recorder, names ...string) foundation.Option {
				var layers []config.EnvironmentLayer
				for _, n := range names {
					layers = append(layers, envFake{newFake(n, "A", rec)})
				}

				return foundation.WithEnvironmentConfig(layers...)
			},
			set: func(rec *recorder, names ...string) foundation.Option {
				var layers []config.EnvironmentLayer
				for _, n := range names {
					layers = append(layers, envFake{newFake(n, "A", rec)})
				}

				return foundation.SetEnvironmentConfig(layers...)
			},
			setup: func(t *testing.T) []foundation.Option {
				inTempDir(t)
				t.Setenv("SVC_B", "default")

				return nil
			},
		},
		{
			name: "command",
			with: func(rec *recorder, names ...string) foundation.Option {
				var layers []config.CommandLayer
				for _, n := range names {
					layers = append(layers, cmdFake{newFake(n, "A", rec)})
				}

				return foundation.WithCommandConfig(layers...)
			},
			set: func(rec *recorder, names ...string) foundation.Option {
				var layers []config.CommandLayer
				for _, n := range names {
					layers = append(layers, cmdFake{newFake(n, "A", rec)})
				}

				return foundation.SetCommandConfig(layers...)
			},
			setup: func(t *testing.T) []foundation.Option {
				inTempDir(t)

				return []foundation.Option{foundation.WithArgs([]string{"--b", "default"})}
			},
		},
	}
}

// load runs a root function that loads a layered, and returns it with what
// happened.
func load(t *testing.T, opts ...foundation.Option) (layered, loadResult) {
	t.Helper()

	var got layered

	return got, loadWith(t, &got, nil, opts...)
}

// refused asserts that Run stopped startup before the root function ran, and
// that no provider was asked for anything.
func refused(t *testing.T, res loadResult, rec *recorder) {
	t.Helper()

	if res.finished {
		t.Error("the root function ran, but the options should have stopped startup")
	}

	if !slices.Equal(res.exit, []int{1}) {
		t.Errorf("exit codes = %v, want [1]", res.exit)
	}

	if res.summary == nil {
		t.Error("expected the run to say why it failed")
	}

	if rec != nil && rec.any() {
		t.Errorf("a provider was used although startup was refused: %v", rec.calls)
	}
}

// The order of the layers belongs to the framework: it doesn't depend on the
// order the options are given in, and each layer overrides the ones below it.
func TestLayersHaveAFixedOrder(t *testing.T) {
	for _, mode := range []string{"with", "set"} {
		t.Run(mode, func(t *testing.T) {
			inTempDir(t)

			rec := &recorder{}

			// Given in the wrong order on purpose.
			cmd, envl, remote, doc := cmdFake{newFake("cmd", "D", rec)}, envFake{newFake("env", "CD", rec)},
				remoteFake{newFake("remote", "BCD", rec)}, docFake{newFake("doc", "ABCD", rec)}

			var opts []foundation.Option

			if mode == "with" {
				opts = []foundation.Option{
					foundation.WithCommandConfig(cmd), foundation.WithEnvironmentConfig(envl),
					foundation.WithRemoteConfig(remote), foundation.WithDocumentConfig(doc),
				}
			} else {
				opts = []foundation.Option{
					foundation.SetCommandConfig(cmd), foundation.SetEnvironmentConfig(envl),
					foundation.SetRemoteConfig(remote), foundation.SetDocumentConfig(doc),
				}
			}

			got, res := load(t, opts...)

			if !res.finished {
				t.Fatalf("Load failed: %+v", res.problems)
			}

			if want := []string{"doc", "remote", "env", "cmd"}; !slices.Equal(rec.only("load:"), want) {
				t.Errorf("providers were asked in the order %v, want %v", rec.only("load:"), want)
			}

			if want := (layered{A: "doc", B: "remote", C: "env", D: "cmd"}); got != want {
				t.Errorf("got %+v, want %+v: each layer should override the ones below it", got, want)
			}
		})
	}
}

// Without options the stack is the environment, then flags (when the program gave
// arguments). No file is read: a program names the files it reads.
func TestTheDefaultLayersAreTheEnvironmentAndFlags(t *testing.T) {
	dir := inTempDir(t)

	// A canary: a config.json in the working directory must not be read.
	writeConfigJSON(t, dir, `{"svc": {"a": "file", "b": "file", "c": "file"}}`)

	t.Setenv("SVC_B", "env")
	t.Setenv("SVC_C", "env")

	got, res := load(t, foundation.WithArgs([]string{"--c", "flag"}))

	if !res.finished {
		t.Fatalf("Load failed: %+v", res.problems)
	}

	if want := (layered{B: "env", C: "flag"}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// A program that names a file reads it at the bottom of the stack: the
// environment overrides it, and flags override both.
func TestAFileTheProgramNamesIsTheBottomOfTheStack(t *testing.T) {
	dir := inTempDir(t)

	writeConfigJSON(t, dir, `{"svc": {"a": "file", "b": "file", "c": "file"}}`)

	t.Setenv("SVC_B", "env")
	t.Setenv("SVC_C", "env")

	got, res := load(t,
		foundation.WithDocumentConfig(json.File("config.json")),
		foundation.WithArgs([]string{"--c", "flag"}),
	)

	if !res.finished {
		t.Fatalf("Load failed: %+v", res.problems)
	}

	if want := (layered{A: "file", B: "env", C: "flag"}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// Documents in the layer are read in the order given, later ones overriding
// earlier ones: this is how a program reads YAML above JSON, or a second file
// above the first.
func TestDocumentsInTheLayerAreReadInTheOrderGiven(t *testing.T) {
	dir := inTempDir(t)

	writeConfigJSON(t, dir, `{"svc": {"a": "first", "b": "first"}}`)

	got, res := load(t,
		foundation.WithDocumentConfig(json.File("config.json")),
		foundation.WithDocumentConfig(json.Raw([]byte(`{"svc": {"a": "second"}}`))),
	)

	if !res.finished {
		t.Fatalf("Load failed: %+v", res.problems)
	}

	if want := (layered{A: "second", B: "first"}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// Nothing in the environment names a file either: the framework reads no
// variable for it, so one that happens to be set (CONFIG_FILE was once such a
// name) changes nothing and is not an error.
func TestNoVariableChoosesAFile(t *testing.T) {
	dir := inTempDir(t)

	canary := writeFile(t, t.TempDir(), "canary.json", `{"svc": {"a": "canary"}}`)

	writeConfigJSON(t, dir, `{"svc": {"a": "canary"}}`)

	for name, value := range map[string]string{
		"CONFIG_FILE": canary,
		"CONFIG":      canary,
		"SVC_CONFIG":  canary,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)

			for _, opts := range [][]foundation.Option{
				nil,
				{lookupEnvOf(map[string]string{name: value})},
				{foundation.WithDocumentConfig(json.Raw([]byte(`{"svc": {"b": "raw"}}`)))},
			} {
				got, res := load(t, opts...)

				if !res.finished || len(res.exit) != 0 {
					t.Fatalf("a variable that names a file broke startup: %+v", res)
				}

				if got.A != "" {
					t.Fatalf("A = %q: a file was read because of a variable", got.A)
				}
			}
		})
	}
}

// Strict mode asks the providers that are there, so a config.json that nothing
// names has no typos to report.
func TestStrictModeDoesNotLookAtAFileNobodyNamed(t *testing.T) {
	dir := inTempDir(t)

	writeConfigJSON(t, dir, `{"svc": {"typo": "x"}}`)

	if _, res := load(t, foundation.WithStrictConfig()); !res.finished || len(res.exit) != 0 {
		t.Fatalf("finished %v, exit %v", res.finished, res.exit)
	}
}

// A file with no path at all is a mistake, not a missing optional file: an unset
// variable passed through os.Getenv would otherwise run the service without the
// configuration its operator meant to give it. FileFromEnv is how a program says
// the file is optional.
func TestAnEmptyFilePathIsAMistake(t *testing.T) {
	inTempDir(t)

	for name, p := range map[string]config.DocumentLayer{
		"optional": json.File(""),
		"required": json.File("", config.WithRequired()),
	} {
		t.Run(name, func(t *testing.T) {
			_, res := load(t, foundation.WithDocumentConfig(p))

			if res.finished || !slices.Equal(res.exit, []int{1}) {
				t.Fatalf("finished %v, exit %v, want the run to fail", res.finished, res.exit)
			}
		})
	}
}

// A group of options shared by several programs is a foundation.Options. A
// function that returns one gives each run its own providers.
func TestAGroupOfLayerOptionsCanBeShared(t *testing.T) {
	dir := inTempDir(t)

	writeConfigJSON(t, dir, `{"svc": {"a": "shared", "b": "shared"}}`)

	shared := func() foundation.Option {
		return foundation.Options{
			foundation.WithDocumentConfig(json.File("config.json")),
			foundation.WithRemoteConfig(remoteFake{newFake("remote", "B", &recorder{})}),
		}
	}

	for range 2 {
		got, res := load(t, shared(), foundation.WithDocumentConfig(json.Raw([]byte(`{"svc": {"c": "own"}}`))))

		if !res.finished {
			t.Fatalf("Load failed: %+v", res.problems)
		}

		if want := (layered{A: "shared", B: "remote", C: "own"}); got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
}

// With adds to what a layer holds, after the default; Set replaces it, so the
// default is not read at all.
func TestWithAddsToALayerAndSetReplacesIt(t *testing.T) {
	for _, lc := range layerCases() {
		t.Run(lc.name, func(t *testing.T) {
			t.Run("with", func(t *testing.T) {
				var opts []foundation.Option

				if lc.setup != nil {
					opts = lc.setup(t)
				} else {
					inTempDir(t)
				}

				rec := &recorder{}

				got, res := load(t, append(opts, lc.with(rec, "added"))...)

				if !res.finished {
					t.Fatalf("Load failed: %+v", res.problems)
				}

				if got.A != "added" {
					t.Errorf("A = %q, want the provider that was added", got.A)
				}

				if lc.setup != nil && got.B != "default" {
					t.Errorf("B = %q, want the default of the layer to be kept", got.B)
				}
			})

			if lc.setup == nil {
				t.Run("set", func(t *testing.T) {
					inTempDir(t)

					rec := &recorder{}

					got, res := load(t, lc.set(rec, "only"))

					if !res.finished || got.A != "only" {
						t.Fatalf("got %+v (finished %v)", got, res.finished)
					}
				})

				return
			}

			t.Run("set", func(t *testing.T) {
				opts := lc.setup(t)

				rec := &recorder{}

				got, res := load(t, append(opts, lc.set(rec, "only"))...)

				if lc.name == "command" {
					// The arguments are still given, and nothing reads them now: that
					// is a mistake, reported, rather than flags that are quietly dropped.
					if res.finished && len(res.exit) == 0 {
						t.Fatalf("arguments nothing reads were accepted: %+v", got)
					}

					return
				}

				if !res.finished {
					t.Fatalf("Load failed: %+v", res.problems)
				}

				if got.A != "only" {
					t.Errorf("A = %q, want the provider that was set", got.A)
				}

				if got.B != "" {
					t.Errorf("B = %q, want the default of the layer to be gone", got.B)
				}
			})
		})
	}
}

// Within a layer, later entries override earlier ones, and options apply in the
// order they are given.
func TestOptionsApplyInTheOrderGiven(t *testing.T) {
	for _, lc := range layerCases() {
		t.Run(lc.name, func(t *testing.T) {
			tests := []struct {
				name string
				opts func(rec *recorder) []foundation.Option
				want []string
			}{
				{"entries of one option", func(r *recorder) []foundation.Option {
					return []foundation.Option{lc.set(r, "one", "two")}
				}, []string{"one", "two"}},
				{"with then with", func(r *recorder) []foundation.Option {
					return []foundation.Option{lc.set(r, "zero"), lc.with(r, "one"), lc.with(r, "two")}
				}, []string{"zero", "one", "two"}},
				{"set then with", func(r *recorder) []foundation.Option {
					return []foundation.Option{lc.set(r, "one"), lc.with(r, "two")}
				}, []string{"one", "two"}},
				{"with then set", func(r *recorder) []foundation.Option {
					return []foundation.Option{lc.with(r, "one"), lc.set(r, "two")}
				}, []string{"two"}},
				{"set then set", func(r *recorder) []foundation.Option {
					return []foundation.Option{lc.set(r, "one"), lc.set(r, "two")}
				}, []string{"two"}},
			}

			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					inTempDir(t)

					rec := &recorder{}

					got, res := load(t, tt.opts(rec)...)

					if !res.finished {
						t.Fatalf("Load failed: %+v", res.problems)
					}

					if !slices.Equal(rec.only("load:"), tt.want) {
						t.Errorf("providers were asked %v, want %v", rec.only("load:"), tt.want)
					}

					if got.A != tt.want[len(tt.want)-1] {
						t.Errorf("A = %q, want the last provider's %q", got.A, tt.want[len(tt.want)-1])
					}
				})
			}
		})
	}
}

// WithConfigProviders is the whole stack, chosen by the program, in the order
// given, with no layers and none of the defaults.
func TestWithConfigProvidersIsExplicit(t *testing.T) {
	dir := inTempDir(t)

	writeConfigJSON(t, dir, `{"svc": {"b": "file"}}`)

	t.Setenv("SVC_C", "env")

	rec := &recorder{}

	// Plain providers: nothing says which layer, because there are none.
	got, res := load(t, foundation.WithConfigProviders(newFake("one", "AD", rec), newFake("two", "A", rec)))

	if !res.finished {
		t.Fatalf("Load failed: %+v", res.problems)
	}

	if want := []string{"one", "two"}; !slices.Equal(rec.only("load:"), want) {
		t.Errorf("providers were asked %v, want %v", rec.only("load:"), want)
	}

	if want := (layered{A: "two", D: "one"}); got != want {
		t.Errorf("got %+v, want %+v: no default provider should have been read", got, want)
	}
}

func TestWithConfigProvidersWithNoneLoadsNothing(t *testing.T) {
	dir := inTempDir(t)

	writeConfigJSON(t, dir, `{"svc": {"a": "file"}}`)

	t.Setenv("SVC_B", "env")

	got, res := load(t, foundation.WithConfigProviders())

	if !res.finished || got != (layered{}) {
		t.Fatalf("got %+v (finished %v), want nothing loaded", got, res.finished)
	}
}

// The two ways of choosing providers don't mix: using both would leave it unclear
// which one the program meant, so it is a startup error whichever comes first.
func TestExplicitProvidersCannotBeCombinedWithLayers(t *testing.T) {
	for _, lc := range layerCases() {
		for _, mode := range []string{"with", "set"} {
			for _, explicitFirst := range []bool{true, false} {
				name := lc.name + "/" + mode
				if explicitFirst {
					name += "/explicit first"
				} else {
					name += "/explicit last"
				}

				t.Run(name, func(t *testing.T) {
					inTempDir(t)

					rec := &recorder{}

					layer := lc.with(rec, "layer")
					if mode == "set" {
						layer = lc.set(rec, "layer")
					}

					explicit := foundation.WithConfigProviders(newFake("explicit", "A", rec))

					opts := []foundation.Option{explicit, layer}
					if !explicitFirst {
						opts = []foundation.Option{layer, explicit}
					}

					_, res := load(t, opts...)

					refused(t, res, rec)
				})
			}
		}
	}
}

// What a program can get wrong with the options stops startup: nothing is read,
// and nothing starts.
func TestBadLayerOptionsStopStartup(t *testing.T) {
	var (
		nilDoc    config.DocumentLayer
		nilRemote config.RemoteLayer
		nilEnv    config.EnvironmentLayer
		nilCmd    config.CommandLayer
	)

	tests := map[string]foundation.Option{
		"nil document (with)":    foundation.WithDocumentConfig(nilDoc),
		"nil document (set)":     foundation.SetDocumentConfig(nilDoc),
		"nil remote (with)":      foundation.WithRemoteConfig(nilRemote),
		"nil remote (set)":       foundation.SetRemoteConfig(nilRemote),
		"nil environment (with)": foundation.WithEnvironmentConfig(nilEnv),
		"nil environment (set)":  foundation.SetEnvironmentConfig(nilEnv),
		"nil command (with)":     foundation.WithCommandConfig(nilCmd),
		"nil command (set)":      foundation.SetCommandConfig(nilCmd),

		// An empty list is a mistake such as spreading a slice that has nothing in
		// it, not a way of turning a layer off: WithConfigProviders() does that.
		"empty document (with)":    foundation.WithDocumentConfig(),
		"empty document (set)":     foundation.SetDocumentConfig(),
		"empty remote (with)":      foundation.WithRemoteConfig(),
		"empty remote (set)":       foundation.SetRemoteConfig(),
		"empty environment (with)": foundation.WithEnvironmentConfig(),
		"empty environment (set)":  foundation.SetEnvironmentConfig(),
		"empty command (with)":     foundation.WithCommandConfig(),
		"empty command (set)":      foundation.SetCommandConfig(),
	}

	for name, opt := range tests {
		t.Run(name, func(t *testing.T) {
			inTempDir(t)

			_, res := load(t, opt)

			refused(t, res, nil)
		})
	}
}

// Two entries for the same file would read it twice, and one of them wins by
// accident, so it is refused, however the path is spelt, and including the file
// the default layer already reads.
func TestTheSameFileTwiceInTheDocumentLayerIsRefused(t *testing.T) {
	tests := map[string]func(dir string) []foundation.Option{
		"twice in a set": func(string) []foundation.Option {
			return []foundation.Option{foundation.SetDocumentConfig(json.File("a.json"), json.File("a.json"))}
		},
		"spelt differently": func(string) []foundation.Option {
			return []foundation.Option{foundation.SetDocumentConfig(json.File("a.json"), json.File("./sub/../a.json"))}
		},
		"absolute and relative": func(dir string) []foundation.Option {
			return []foundation.Option{foundation.SetDocumentConfig(json.File("a.json"), json.File(filepath.Join(dir, "a.json")))}
		},
		"with two options": func(string) []foundation.Option {
			return []foundation.Option{foundation.SetDocumentConfig(json.File("a.json")), foundation.WithDocumentConfig(json.File("a.json"))}
		},
	}

	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			dir := inTempDir(t)

			_, res := load(t, opts(dir)...)

			refused(t, res, nil)
		})
	}

	// Different files are fine.
	t.Run("different files", func(t *testing.T) {
		dir := inTempDir(t)

		writeFile(t, dir, "a.json", `{"svc": {"a": "a"}}`)
		writeFile(t, dir, "b.json", `{"svc": {"a": "b"}}`)

		got, res := load(t, foundation.SetDocumentConfig(json.File("a.json"), json.File("b.json")))

		if !res.finished || got.A != "b" {
			t.Fatalf("got %+v (finished %v)", got, res.finished)
		}
	})
}

// A remote source sits between the documents and the environment, so the
// environment still wins over it; the check that it does is in
// TestLayersHaveAFixedOrder, and here it is seen with the real environment.
func TestTheEnvironmentOverridesTheRemoteLayer(t *testing.T) {
	inTempDir(t)

	t.Setenv("SVC_A", "env")

	rec := &recorder{}

	got, res := load(t, foundation.WithRemoteConfig(remoteFake{newFake("remote", "AB", rec)}))

	if !res.finished {
		t.Fatalf("Load failed: %+v", res.problems)
	}

	if want := (layered{A: "env", B: "remote"}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// Everything that looks at the providers sees one list in one order: loading,
// the strict check, the keys named for a missing required field, and help.
func TestEveryConsumerSeesTheSameOrder(t *testing.T) {
	order := func(rec *recorder) foundation.Option {
		return foundation.Options{
			foundation.WithDocumentConfig(docFake{newFake("doc", "", rec)}),
			foundation.WithRemoteConfig(remoteFake{newFake("remote", "", rec)}),
			foundation.WithEnvironmentConfig(envFake{newFake("env", "", rec)}),
			foundation.WithCommandConfig(cmdFake{newFake("cmd", "", rec)}),
		}
	}

	want := []string{"doc", "remote", "env", "cmd"}

	t.Run("load", func(t *testing.T) {
		inTempDir(t)

		rec := &recorder{}

		if _, res := load(t, order(rec)); !res.finished {
			t.Fatalf("Load failed: %+v", res.problems)
		}

		if got := rec.only("load:"); !slices.Equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("strict check", func(t *testing.T) {
		inTempDir(t)

		rec := &recorder{}

		if _, res := load(t, order(rec), foundation.WithStrictConfig()); !res.finished {
			t.Fatalf("Load failed: %+v", res.problems)
		}

		if got := rec.only("check:"); !slices.Equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("keys named for a missing required field", func(t *testing.T) {
		inTempDir(t)

		rec := &recorder{}

		res := loadWith(t, &needsA{}, nil, order(rec))

		if res.finished || len(res.problems) != 1 {
			t.Fatalf("expected one problem, got %+v", res)
		}

		// Each key is listed where its layer sits.
		wantKeys := "doc-key, remote-key, SVC_A, env-key, cmd-key"

		if got := res.problems[0].attrs["key"]; got != wantKeys {
			t.Errorf("key = %q, want %q", got, wantKeys)
		}
	})

	t.Run("help", func(t *testing.T) {
		inTempDir(t)

		rec := &recorder{}

		var out bytes.Buffer

		if _, res := load(t, order(rec), foundation.WithArgs([]string{"--help"}), foundation.WithHelpOutput(&out)); !res.finished {
			t.Fatalf("Load failed: %+v", res.problems)
		}

		if got := rec.only("describe:"); !slices.Equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}

		// Help describes and never reads.
		if got := rec.only("load:"); len(got) != 0 {
			t.Errorf("help read from %v", got)
		}
	})
}

// A provider is in a layer because of its type, so the built in ones can only be
// used where they belong. Passing one to the wrong option does not compile; this
// checks the types say what is meant.
func TestBuiltInProvidersBelongToOneLayer(t *testing.T) {
	var (
		document    = reflect.TypeFor[config.DocumentLayer]()
		remote      = reflect.TypeFor[config.RemoteLayer]()
		environment = reflect.TypeFor[config.EnvironmentLayer]()
		command     = reflect.TypeFor[config.CommandLayer]()
	)

	tests := []struct {
		name string
		typ  reflect.Type
		want reflect.Type
	}{
		{"json file", reflect.TypeFor[*json.FileProvider](), document},
		{"json raw", reflect.TypeFor[*json.RawProvider](), document},
		{"env", reflect.TypeFor[*env.Provider](), environment},
		{"flags", reflect.TypeFor[*flags.Provider](), command},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, layer := range []reflect.Type{document, remote, environment, command} {
				if got := tt.typ.Implements(layer); got != (layer == tt.want) {
					t.Errorf("implements %v = %v", layer, got)
				}
			}
		})
	}

	// Only a document that reads a file has a path, which is how the same file
	// given twice is noticed.
	pathed := reflect.TypeFor[config.Pathed]()

	if !reflect.TypeFor[*json.FileProvider]().Implements(pathed) {
		t.Error("a json file should have a path")
	}

	if reflect.TypeFor[*json.RawProvider]().Implements(pathed) {
		t.Error("a json document held in memory has no path")
	}

	// And a path is only ever given by the program: nothing can be pointed at
	// another file from outside.
	if _, ok := reflect.TypeFor[*json.FileProvider]().MethodByName("WithPath"); ok {
		t.Error("a file provider should not be retargetable")
	}
}

// A directory of documents is one document provider in the document layer: its
// files are read in name order, and the environment is still above them.
func TestADirectoryOfFilesIsInTheDocumentLayer(t *testing.T) {
	inTempDir(t)

	dir := t.TempDir()

	writeFile(t, dir, "00-base.json", `{"svc": {"addr": "base", "name": "base"}}`)
	writeFile(t, dir, "10-site.json", `{"svc": {"name": "site"}}`)
	writeFile(t, dir, "notes.txt", `not configuration`)

	var cfg loadConfig

	res := loadWith(t, &cfg, nil, foundation.WithDocumentConfig(json.Dir(dir)))

	if !res.finished || cfg.Addr != "base" || cfg.Name != "site" {
		t.Fatalf("got %+v (finished %v, problems %+v)", cfg, res.finished, res.problems)
	}

	t.Setenv("SVC_NAME", "env")

	cfg = loadConfig{}

	res = loadWith(t, &cfg, nil, foundation.WithDocumentConfig(json.Dir(dir)))

	if !res.finished || cfg.Addr != "base" || cfg.Name != "env" {
		t.Fatalf("the environment should win over the directory: %+v (finished %v)", cfg, res.finished)
	}
}

// A file and a directory in the one layer: the later wins, as for any providers.
func TestADirectoryAndAFileInOneLayer(t *testing.T) {
	inTempDir(t)

	dir := t.TempDir()

	writeFile(t, dir, "a.json", `{"svc": {"addr": "dir", "name": "dir"}}`)

	file := writeFile(t, t.TempDir(), "own.json", `{"svc": {"name": "file"}}`)

	var cfg loadConfig

	res := loadWith(t, &cfg, nil, foundation.WithDocumentConfig(json.Dir(dir), json.File(file)))

	if !res.finished || cfg.Addr != "dir" || cfg.Name != "file" {
		t.Fatalf("got %+v (finished %v)", cfg, res.finished)
	}
}

func TestTheSameDirectoryTwiceIsRefused(t *testing.T) {
	inTempDir(t)

	dir := t.TempDir()

	writeFile(t, dir, "a.json", `{"svc": {"addr": "dir"}}`)

	var cfg loadConfig

	res := loadWith(t, &cfg, nil, foundation.WithDocumentConfig(json.Dir(dir), json.Dir(dir+string(filepath.Separator)+".")))

	if res.finished || !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("finished %v, exit %v: the same directory twice should stop startup", res.finished, res.exit)
	}
}

// An unset variable is no directory, and a set one chooses it; the program wrote
// the variable's name, the operator gives the value.
func TestADirectoryChosenByAVariable(t *testing.T) {
	inTempDir(t)

	dir := t.TempDir()

	writeFile(t, dir, "a.json", `{"svc": {"addr": "chosen"}}`)

	opt := func() foundation.Option {
		return foundation.WithDocumentConfig(json.DirFromEnv("SHOP_CONFIGS"))
	}

	var cfg loadConfig

	if res := loadWith(t, &cfg, nil, opt()); !res.finished || cfg != (loadConfig{}) {
		t.Fatalf("unset: got %+v (finished %v)", cfg, res.finished)
	}

	t.Setenv("SHOP_CONFIGS", dir)

	if res := loadWith(t, &cfg, nil, opt()); !res.finished || cfg.Addr != "chosen" {
		t.Fatalf("set: got %+v (finished %v)", cfg, res.finished)
	}
}

// Strict mode asks every file of the directory, and names the one with the typo.
func TestStrictConfigChecksEveryFileOfTheDirectory(t *testing.T) {
	inTempDir(t)

	dir := t.TempDir()

	writeFile(t, dir, "a.json", `{"svc": {"addr": "a"}}`)
	writeFile(t, dir, "b.json", `{"svc": {"adrr": "typo"}}`)

	var cfg loadConfig

	res := loadWith(t, &cfg, nil, foundation.WithDocumentConfig(json.Dir(dir)), foundation.WithStrictConfig())

	if !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("exit codes %v, want [1] for the unknown key", res.exit)
	}
}

// By default a file that others can write stops startup, so a mistake in a
// deployment can't let them change the service. A program can say it knows better.
func TestAConfigurationFileThatOthersCanWriteStopsStartup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("there are no mode bits to look at")
	}

	inTempDir(t)

	dir := t.TempDir()

	file := writeFile(t, dir, "a.json", `{"svc": {"addr": "canary"}}`)

	if err := os.Chmod(file, 0o666); err != nil {
		t.Fatal(err)
	}

	for name, opt := range map[string]foundation.Option{
		"file":      foundation.WithDocumentConfig(json.File(file)),
		"directory": foundation.WithDocumentConfig(json.Dir(dir)),
	} {
		t.Run(name, func(t *testing.T) {
			var cfg loadConfig

			res := loadWith(t, &cfg, nil, opt)

			if res.finished || !slices.Equal(res.exit, []int{1}) || cfg.Addr != "" {
				t.Fatalf("got %+v (finished %v, exit %v): the file should have been refused", cfg, res.finished, res.exit)
			}
		})
	}

	var cfg loadConfig

	res := loadWith(t, &cfg, nil, foundation.WithDocumentConfig(json.File(file, config.WithoutPermissionCheck())))

	if !res.finished || cfg.Addr != "canary" {
		t.Fatalf("with the opt-out: got %+v (finished %v)", cfg, res.finished)
	}
}
