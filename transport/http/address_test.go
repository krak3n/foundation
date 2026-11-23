package http_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/foundationtest"
	transporthttp "go.krak3n.dev/foundation/transport/http"
)

// The address is a required setting, so a dry run and a real run find it missing
// through the same rule as every other required field, and help (which reads no
// configuration) does not.
func TestAMissingAddressIsFoundByADryRunAndARealRunButNotByHelp(t *testing.T) {
	tests := map[string]struct {
		opt   foundation.Option
		fails bool
	}{
		"a dry run":  {foundation.WithDryRun(true), true},
		"a real run": {foundation.OptionFunc(func(*foundation.F) {}), true},
		"help":       {foundation.WithArgs([]string{"--help"}), false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			recorder := foundationtest.NewLogRecorder()
			recorder.Install(t)

			foundation.Run("svc", func(f *foundation.F) {
				transporthttp.Server(f, hello)

				f.Shutdown("done")
			}, tt.opt, foundation.WithHelpOutput(io.Discard), foundation.WithConfigProviders(env.New(env.WithMap(nil))))

			if !tt.fails {
				if codes := exit.Codes(); len(codes) != 0 {
					t.Fatalf("exit codes = %v, want none", codes)
				}

				return
			}

			if codes := exit.Codes(); !slices.Equal(codes, []int{1}) {
				t.Fatalf("exit codes = %v, want [1]", codes)
			}

			if got := entriesNamed(recorder, "serving http server"); len(got) != 0 {
				t.Fatal("it listened without an address")
			}

			// The same report as any required field, naming where it is read from.
			var named bool

			for _, e := range entriesNamed(recorder, "invalid configuration") {
				e.Record.Attrs(func(a slog.Attr) bool {
					named = named || (a.Key == "key" && a.Value.String() == "SVC_HTTP_SERVER_ADDR")

					return true
				})
			}

			if !named {
				t.Fatalf("no invalid configuration error named SVC_HTTP_SERVER_ADDR: %v", errsLogged(recorder))
			}
		})
	}
}

// The address can come from any layer, and a dry run with one passes.
func TestAnAddressFromConfigurationSatisfiesTheServer(t *testing.T) {
	for name, opts := range map[string][]foundation.Option{
		"the environment": {foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{"SVC_HTTP_SERVER_ADDR": "127.0.0.1:0"})))},
		"a flag":          {foundation.SetEnvironmentConfig(env.New(env.WithMap(nil))), foundation.WithArgs([]string{"--http", "--server", "--addr", "127.0.0.1:0"})},
		"a dry run":       {foundation.WithDryRun(true), foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{"SVC_HTTP_SERVER_ADDR": "127.0.0.1:0"})))},
	} {
		t.Run(name, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			foundation.Run("svc", func(f *foundation.F) {
				transporthttp.Server(f, hello)

				f.Shutdown("done")
			}, opts...)

			if codes := exit.Codes(); len(codes) != 0 {
				t.Fatalf("exit codes = %v, want none", codes)
			}
		})
	}
}

// Port 0 in the address is how a test gets a random port now, on the host given.
func TestAnAddressWithPortZeroListensOnARandomPortOfThatHost(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var addr string

	foundation.Run("svc", func(f *foundation.F) {
		transporthttp.Server(f, hello)

		for _, e := range entriesNamed(recorder, "serving http server") {
			e.Record.Attrs(func(a slog.Attr) bool {
				if a.Key == "addr" {
					addr = a.Value.String()
				}

				return true
			})
		}

		f.Shutdown("done")
	}, foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{
		"SVC_HTTP_SERVER_ADDR":  "127.0.0.1:0",
		"SVC_HTTP_SERVER_BLOCK": "true",
	}))))

	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" || port == "0" || port == "" {
		t.Fatalf("listening on %q (%v)", addr, err)
	}
}

// A listener given in code is the other way to say where to listen. It needs no
// address in configuration, in a dry run or a real run, and the server serves on it.
func TestAListenerNeedsNoAddress(t *testing.T) {
	for name, opt := range map[string]foundation.Option{
		"a real run": foundation.OptionFunc(func(*foundation.F) {}),
		"a dry run":  foundation.WithDryRun(true),
	} {
		t.Run(name, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}

			defer ln.Close()

			var body string

			foundation.Run("svc", func(f *foundation.F) {
				transporthttp.Server(f, hello, transporthttp.WithListener(ln))

				if !f.DryRun() {
					body = get(t, ln.Addr().String())
				}

				f.Shutdown("done")
			}, opt, foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{"SVC_HTTP_SERVER_BLOCK": "true"}))))

			if codes := exit.Codes(); len(codes) != 0 {
				t.Fatalf("exit codes = %v, want none", codes)
			}

			if name == "a real run" && body != "hello" {
				t.Fatalf("body = %q", body)
			}
		})
	}
}

// A configuration built by hand has no Load to require anything, so
// ServerWithConfig still refuses one that has nowhere to listen.
func TestAHandBuiltConfigWithNoAddressStillFails(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("svc", func(f *foundation.F) {
		f.Run("http.server", func(f *foundation.F) {
			transporthttp.ServerWithConfig(f, transporthttp.DefaultServerConfig(), hello)
		})

		f.Shutdown("done")
	})

	if codes := exit.Codes(); !slices.Equal(codes, []int{1}) {
		t.Fatalf("exit codes = %v, want [1]", codes)
	}

	if !mentions(errsLogged(recorder), "address") {
		t.Fatalf("no error said that the address is missing: %v", errsLogged(recorder))
	}
}

// The settings that configuration can already give have no second way in code:
// the options the server has are for what configuration cannot say, a listener
// and the program's own defaults, and a setting that is only for tests (a random
// port) is not one.
func TestTheServerOptionsAreOnlyWhatConfigurationCannotSay(t *testing.T) {
	got := optionFuncs(t, "ServerOption")

	if want := []string{"WithDefaults", "WithListener"}; !slices.Equal(got, want) {
		t.Errorf("functions returning a ServerOption = %v, want %v", got, want)
	}

	if _, ok := reflect.TypeOf(transporthttp.ServerConfig{}).FieldByName("RandomPort"); ok {
		t.Error("RandomPort is still a setting: an address with port 0 does it")
	}
}

// A listener is the address the server uses, so a configuration cannot point it
// somewhere else: an ADDR beside a listener does not start a second listener.
func TestAConfiguredAddressDoesNotReplaceAListener(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer ln.Close()

	other := freeAddr(t)

	var body string

	foundation.Run("svc", func(f *foundation.F) {
		transporthttp.Server(f, hello, transporthttp.WithListener(ln))

		body = get(t, ln.Addr().String())

		f.Shutdown("done")
	}, foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{"SVC_HTTP_SERVER_ADDR": other, "SVC_HTTP_SERVER_BLOCK": "true"}))))

	if body != "hello" {
		t.Fatalf("body = %q", body)
	}

	if c, err := net.Dial("tcp", other); err == nil {
		c.Close()

		t.Fatal("something listened on the configured address as well as on the listener")
	}
}

// optionFuncs is the sorted names of the exported functions of the package in
// the working directory (not its tests) that return typ, which is how its
// options are found whatever they are called or where they are declared.
func optionFuncs(t *testing.T, typ string) []string {
	t.Helper()

	files := parseSources(t)

	var names []string

	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !fn.Name.IsExported() || fn.Type.Results == nil {
				continue
			}

			for _, res := range fn.Type.Results.List {
				if id, ok := res.Type.(*ast.Ident); ok && id.Name == typ {
					names = append(names, fn.Name.Name)
				}
			}
		}
	}

	slices.Sort(names)

	return names
}

// A listener says where the server is whatever its address looks like: one whose
// address has no text (an in memory listener may be like that) still counts.
func TestAListenerWithAnAddressThatHasNoTextNeedsNoAddress(t *testing.T) {
	for name, opt := range map[string]foundation.Option{
		"a real run": foundation.OptionFunc(func(*foundation.F) {}),
		"a dry run":  foundation.WithDryRun(true),
	} {
		t.Run(name, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}

			defer ln.Close()

			var body string

			foundation.Run("svc", func(f *foundation.F) {
				// So that a server that fails can't leave the run waiting.
				time.AfterFunc(2*time.Second, func() { f.Shutdown("watchdog") })

				transporthttp.Server(f, hello, transporthttp.WithListener(textless{ln}))

				if !f.DryRun() {
					// Not blocking: readiness is a probe of the listener's address, which
					// has no text to dial, so poll for the server instead.
					for deadline := time.Now().Add(time.Second); body == "" && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
						body = tryGet(ln.Addr().String())
					}
				}

				f.Shutdown("done")
			}, opt, foundation.WithConfigProviders(env.New(env.WithMap(nil))))

			if codes := exit.Codes(); len(codes) != 0 {
				t.Fatalf("exit codes = %v, want none", codes)
			}

			if name == "a real run" && body != "hello" {
				t.Fatalf("body = %q", body)
			}
		})
	}
}

// textless is a listener whose address has no text.
type textless struct{ net.Listener }

func (textless) Addr() net.Addr { return textlessAddr{} }

type textlessAddr struct{}

func (textlessAddr) Network() string { return "memory" }
func (textlessAddr) String() string  { return "" }

// A nil listener of a pointer type, in an interface, is no listener: it must not
// panic when its address is asked for, and the server is as it was without one,
// which is that the address is required.
func TestATypedNilListenerIsNoListener(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var ln *net.TCPListener

	foundation.Run("svc", func(f *foundation.F) {
		transporthttp.Server(f, hello, transporthttp.WithListener(ln))

		f.Shutdown("done")
	}, foundation.WithConfigProviders(env.New(env.WithMap(nil))))

	if codes := exit.Codes(); !slices.Equal(codes, []int{1}) {
		t.Fatalf("exit codes = %v, want [1]", codes)
	}

	if got := entriesNamed(recorder, "serving http server"); len(got) != 0 {
		t.Fatal("it listened")
	}

	for _, e := range recorder.Entries() {
		panicked := false

		e.Record.Attrs(func(a slog.Attr) bool {
			panicked = panicked || a.Key == "stack"

			return true
		})

		if panicked {
			t.Fatal("a panic was recovered")
		}
	}

	if !mentions(errsLogged(recorder), "required") {
		t.Fatalf("no error said that the address is required: %v", errsLogged(recorder))
	}
}

// An empty address is not a way to turn the requirement off: it is as missing
// as no variable, whichever layer says it.
func TestAnEmptyAddressIsMissing(t *testing.T) {
	for name, opts := range map[string][]foundation.Option{
		"the environment": {foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{"SVC_HTTP_SERVER_ADDR": ""})))},
		"a flag":          {foundation.SetEnvironmentConfig(env.New(env.WithMap(nil))), foundation.WithArgs([]string{"--http", "--server", "--addr", ""})},
		"a dry run":       {foundation.WithDryRun(true), foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{"SVC_HTTP_SERVER_ADDR": ""})))},
	} {
		t.Run(name, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			recorder := foundationtest.NewLogRecorder()
			recorder.Install(t)

			foundation.Run("svc", func(f *foundation.F) {
				transporthttp.Server(f, hello)

				f.Shutdown("done")
			}, opts...)

			if codes := exit.Codes(); !slices.Equal(codes, []int{1}) {
				t.Fatalf("exit codes = %v, want [1]", codes)
			}

			if got := entriesNamed(recorder, "serving http server"); len(got) != 0 {
				t.Fatal("it listened")
			}
		})
	}
}

// tryGet is the body served at addr, or nothing if it could not be fetched, for
// a test that must not stop the run it is inside when the server is not there.
func tryGet(addr string) string {
	resp, err := (&http.Client{Timeout: time.Second}).Get("http://" + addr + "/")
	if err != nil {
		return ""
	}

	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	return string(body)
}

// parseSources parses the Go files of the package in the working directory that
// are not tests. It reads the files itself: parser.ParseDir is deprecated, and
// what is wanted is the declarations, not how files group into packages.
func parseSources(t *testing.T) []*ast.File {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	var files []*ast.File

	fset := token.NewFileSet()

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}

		files = append(files, file)
	}

	return files
}
