package http_test

import (
	"bytes"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/flags"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/foundationtest"
	"go.krak3n.dev/foundation/health/monitor"
	transporthttp "go.krak3n.dev/foundation/transport/http"
)

// rootProbes is how a test sees the probes of the program it starts: it is given to
// foundation.Run as an option, which is applied to the root of the instance, and
// reads the probes of that instance.
type rootProbes struct {
	p atomic.Pointer[foundation.Probes]
}

func (r *rootProbes) option() foundation.Option {
	return foundation.OptionFunc(func(f *foundation.F) {
		p := f.Probes()

		r.p.Store(&p)
	})
}

func (r *rootProbes) seq() iter.Seq[monitor.Probe] {
	if p := r.p.Load(); p != nil {
		return (*p).Seq()
	}

	return slices.Values([]monitor.Probe(nil))
}

// startServer starts an HTTP server on a random port using foundation.Run in a
// background goroutine. It returns the base URL, a shutdown function, and a
// done channel that closes when foundation.Run completes.
func startServer(t *testing.T, handler http.Handler, serverOpts []opt, foundationOpts ...foundation.Option) (baseURL string, shutdown func(), done chan struct{}) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	addr := ln.Addr().String()
	ln.Close()

	baseURL = fmt.Sprintf("http://%s", addr)
	done = make(chan struct{})

	shutdownCh := make(chan struct{})
	var shutdownOnce sync.Once

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			f.Run("shutdown-watcher", func(f *foundation.F) {
				f.Parallel()

				select {
				case <-shutdownCh:
					f.Shutdown("test complete")
				case <-f.Context().Done():
				}
			})

			opts := append([]opt{withAddr(addr)}, serverOpts...)
			server(f, handler, opts...)
		}, foundationOpts...)
	}()

	shutdown = func() {
		shutdownOnce.Do(func() {
			close(shutdownCh)
		})
	}

	// Wait for the server to be ready
	deadline := time.Now().Add(2 * time.Second)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			conn.Close()

			return baseURL, shutdown, done
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("server did not start within 2 seconds")

	return "", nil, nil
}

func TestServerServesRequests(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	})

	baseURL, shutdown, done := startServer(t, handler, nil)
	defer shutdown()

	resp, err := http.Get(baseURL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if string(body) != "hello" {
		t.Errorf("expected body %q, got %q", "hello", string(body))
	}

	shutdown()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for foundation.Run to complete")
	}
}

func TestServerRegistersProbe(t *testing.T) {
	var probes rootProbes

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	_, shutdown, done := startServer(t, handler, nil, probes.option())
	defer shutdown()

	var found bool

	for probe := range probes.seq() {
		if probe.Name() == "test/http.server" {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected probe with name \"test/http.server\" to be registered")
	}

	shutdown()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for foundation.Run to complete")
	}
}

func TestServerServesWithoutOptions(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	baseURL, shutdown, done := startServer(t, handler, nil)
	defer shutdown()

	resp, err := http.Get(baseURL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	shutdown()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for foundation.Run to complete")
	}
}

// freeAddr returns a loopback address with a free port.
func freeAddr(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer ln.Close()

	return ln.Addr().String()
}

// get requests the server at addr and returns the response body.
func get(t *testing.T, addr string) string {
	t.Helper()

	resp, err := http.Get(fmt.Sprintf("http://%s/", addr))
	if err != nil {
		t.Fatalf("expected a server listening on %s: %v", addr, err)
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}

var hello = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("hello"))
})

// The server reads its settings from the environment with no setup: the
// default providers are in place.
func TestServerReadsEnvConfigByDefault(t *testing.T) {
	t.Chdir(t.TempDir())

	addr := freeAddr(t)

	t.Setenv("TEST_HTTP_SERVER_ADDR", addr)

	var body string

	foundation.Run("test", func(f *foundation.F) {
		server(f, hello, withBlock())

		body = get(t, addr)

		f.Shutdown("done")
	})

	if body != "hello" {
		t.Fatalf("unexpected body %q", body)
	}
}

// What a program sets in code is a default: configuration wins over it.
func TestConfigurationOverridesWhatTheProgramSets(t *testing.T) {
	t.Chdir(t.TempDir())

	configured, inCode := freeAddr(t), freeAddr(t)

	t.Setenv("TEST_HTTP_SERVER_ADDR", configured)

	var body string

	foundation.Run("test", func(f *foundation.F) {
		server(f, hello, withAddr(inCode), withBlock())

		body = get(t, configured)

		f.Shutdown("done")
	})

	if body != "hello" {
		t.Fatalf("unexpected body %q", body)
	}
}

// The server's function is named "http.server", which a config file lays out as
// nested objects: http > server.
func TestServerReadsNestedJSONConfigFromANamedFile(t *testing.T) {
	dir := t.TempDir()

	t.Chdir(dir)

	addr := freeAddr(t)

	data := fmt.Sprintf(`{"test": {"http": {"server": {"addr": %q}}}}`, addr)

	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	var body string

	foundation.Run("test", func(f *foundation.F) {
		server(f, hello, withBlock())

		body = get(t, addr)

		f.Shutdown("done")
	}, foundation.WithDocumentConfig(json.File("config.json")))

	if body != "hello" {
		t.Fatalf("unexpected body %q", body)
	}
}

// A component that embeds the server loads its configuration itself, and where
// it likes, then runs the server on it. The defaults are the server's own.
func TestDefaultServerConfig(t *testing.T) {
	cfg := transporthttp.DefaultServerConfig()

	if cfg.Addr != "" || cfg.ReadHeaderTimeout != 10*time.Second || cfg.IdleTimeout != 120*time.Second ||
		cfg.BlockTimeout != 15*time.Second || cfg.ProbePath != "/_probe" ||
		cfg.ReadTimeout != 0 || cfg.WriteTimeout != 0 || cfg.Block || cfg.CertFile != "" || cfg.KeyFile != "" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

// Each call gets its own: a caller changing one doesn't change the next.
func TestDefaultServerConfigIsACopy(t *testing.T) {
	a := transporthttp.DefaultServerConfig()
	a.Addr = "changed"

	if b := transporthttp.DefaultServerConfig(); b.Addr != "" {
		t.Fatalf("Addr = %q", b.Addr)
	}
}

// The server runs in the function it is given, with the configuration it is
// given, and loads nothing of its own: a variable for a node that doesn't exist
// is not read.
func TestServerWithConfigRunsWhereItIsGivenAndLoadsNothing(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	addr := freeAddr(t)

	var body string

	foundation.Run("test", func(f *foundation.F) {
		cfg := transporthttp.DefaultServerConfig()
		cfg.Addr = addr
		cfg.Block = true

		f.Run("srv", func(f *foundation.F) {
			transporthttp.ServerWithConfig(f, cfg, hello)
		})

		body = get(t, addr)

		f.Shutdown("done")
	}, foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{
		"TEST_HTTP_SERVER_ADDR": "256.0.0.1:1",
	}))))

	if body != "hello" {
		t.Fatalf("body %q", body)
	}

	for _, e := range recorder.Entries() {
		for _, a := range e.Attrs {
			if a.Key == "fn" && strings.Contains(a.Value.String(), "http.server") {
				t.Fatalf("a node called http.server was started: %v", a.Value)
			}
		}
	}
}

// With the block option the server is accepting connections when it returns, and
// without it the parent is released first.
func TestServerWithConfigBlocks(t *testing.T) {
	foundationtest.NewExitRecorder().Install(t)

	addr := freeAddr(t)

	foundation.Run("test", func(f *foundation.F) {
		cfg := transporthttp.DefaultServerConfig()
		cfg.Addr = addr
		cfg.Block = true

		f.Run("srv", func(f *foundation.F) {
			transporthttp.ServerWithConfig(f, cfg, hello)
		})

		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Errorf("not listening when it returned: %v", err)
		} else {
			conn.Close()
		}

		f.Shutdown("done")
	}, foundation.WithConfigProviders())
}

// A configuration is checked as it is for the server that loads its own.
func TestServerWithConfigRefusesMistakes(t *testing.T) {
	const canary = "s3cr3t-canary"

	tests := map[string]struct {
		change func(*transporthttp.ServerConfig)
		word   string
	}{
		"probe path":               {func(c *transporthttp.ServerConfig) { c.ProbePath = "/" }, "probe path"},
		"probe path with a canary": {func(c *transporthttp.ServerConfig) { c.ProbePath = canary + "\x1b[31m\n" }, "probe path"},
		"certificate only":         {func(c *transporthttp.ServerConfig) { c.CertFile = canary }, "key file"},
		"key only":                 {func(c *transporthttp.ServerConfig) { c.KeyFile = canary }, "certificate file"},
		"no certificate there":     {func(c *transporthttp.ServerConfig) { c.CertFile, c.KeyFile = canary, canary }, "certificate"},
		"an address that is not":   {func(c *transporthttp.ServerConfig) { c.Addr = canary }, "listening"},
		"nothing at all":           {func(c *transporthttp.ServerConfig) { *c = transporthttp.ServerConfig{} }, "probe path"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())

			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			recorder := foundationtest.NewLogRecorder()
			recorder.Install(t)

			foundation.Run("test", func(f *foundation.F) {
				cfg := transporthttp.DefaultServerConfig()
				cfg.Addr = "127.0.0.1:0"
				tt.change(&cfg)

				f.Run("srv", func(f *foundation.F) {
					transporthttp.ServerWithConfig(f, cfg, hello)
				})
			}, foundation.WithConfigProviders())

			errs := errsLogged(recorder)

			if codes := exit.Codes(); len(codes) != 1 || codes[0] != 1 || !mentions(errs, tt.word) {
				t.Fatalf("codes=%v errs=%v", codes, errs)
			}

			// The name of a certificate file that can't be opened is in the error, as
			// for any file a provider is told to read: it is a path, and says what to fix.
			if name == "no certificate there" {
				return
			}

			for _, e := range errs {
				if strings.Contains(e, canary) || strings.ContainsAny(e, "\x1b\n") {
					t.Fatalf("an error repeats the value: %q", e)
				}
			}
		})
	}
}

// A dry run checks the configuration and listens on nothing.
func TestServerWithConfigDoesNotListenInADryRun(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	addr := freeAddr(t)

	foundation.Run("test", func(f *foundation.F) {
		cfg := transporthttp.DefaultServerConfig()
		cfg.Addr = addr

		f.Run("srv", func(f *foundation.F) {
			transporthttp.ServerWithConfig(f, cfg, hello)
		})
	}, foundation.WithDryRun(true), foundation.WithConfigProviders())

	if len(exit.Codes()) != 0 {
		t.Fatalf("exit codes = %v", exit.Codes())
	}

	if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		conn.Close()
		t.Fatalf("something is listening on %s", addr)
	}

	if got := skippedFns(recorder); len(got) != 1 || got[0] != "test/srv" {
		t.Fatalf("skipped %v", got)
	}
}

// flagProviders gives the run only flags. The server's function is "http.server",
// so its settings follow --http --server.
func flagProviders(args ...string) foundation.Option {
	return foundation.WithConfigProviders(flags.New(flags.WithArgs(args)))
}

func TestServerReadsItsAddressFromFlags(t *testing.T) {
	for _, form := range []func(addr string) []string{
		func(addr string) []string { return []string{"--http", "--server", "--addr", addr, "--block"} },
		func(addr string) []string { return []string{"--http", "--server", "--addr=" + addr, "--block=true"} },
		func(addr string) []string { return []string{"--http", "--server", "-a", addr} },
	} {
		addr := freeAddr(t)

		exit := foundationtest.NewExitRecorder()
		exit.Install(t)

		var body string

		foundation.Run("test", func(f *foundation.F) {
			server(f, hello, withBlock())

			body = get(t, addr)

			f.Shutdown("done")
		}, flagProviders(form(addr)...))

		// The address is the one given, so the flag was read.
		if body != "hello" || len(exit.Codes()) != 0 {
			t.Fatalf("body %q, exit codes %v", body, exit.Codes())
		}
	}
}

// Flags win over the environment, as they come later in the default order.
func TestServerFlagsWinOverTheEnvironment(t *testing.T) {
	flagged := freeAddr(t)

	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	var body string

	foundation.Run("test", func(f *foundation.F) {
		server(f, hello, withBlock())

		body = get(t, flagged)

		f.Shutdown("done")
	}, foundation.WithConfigProviders(
		env.New(env.WithMap(map[string]string{"TEST_HTTP_SERVER_ADDR": "256.0.0.1:1"})),
		flags.New(flags.WithArgs([]string{"--http", "--server", "--addr", flagged})),
	))

	if body != "hello" || len(exit.Codes()) != 0 {
		t.Fatalf("body %q, exit codes %v", body, exit.Codes())
	}
}

// A flag wins over what the program sets in code, as any configuration does.
func TestFlagsOverrideWhatTheProgramSets(t *testing.T) {
	flagged, inCode := freeAddr(t), freeAddr(t)

	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	var body string

	foundation.Run("test", func(f *foundation.F) {
		server(f, hello, withAddr(inCode), withBlock())

		body = get(t, flagged)

		f.Shutdown("done")
	}, flagProviders("--http", "--server", "--addr", flagged))

	if body != "hello" || len(exit.Codes()) != 0 {
		t.Fatalf("body %q, exit codes %v", body, exit.Codes())
	}
}

// The checks that apply to a value from the environment apply to a flag: a bad
// one stops startup, and nothing changes what the server is.
func TestServerFlagMistakesFailAtStartup(t *testing.T) {
	tests := []struct {
		name string
		args []string
		word string
	}{
		{"probe path", []string{"--http", "--server", "--probe-path", "/"}, "probe path"},
		{"probe path with a space", []string{"--http", "--server", "--probe-path=/a b"}, "probe path"},
		{"certificate without a key", []string{"--http", "--server", "--tls-cert-file", "cert.pem"}, "key file"},
		{"key without a certificate", []string{"--http", "--server", "--tls-key-file", "key.pem"}, "certificate file"},
		{"certificate that is not there", []string{"--http", "--server", "--tls-cert-file", "nope.pem", "--tls-key-file", "nope.key"}, "certificate"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())

			codes, errs := startupFailure(t, nil, flagProviders(tt.args...))

			if len(codes) != 1 || codes[0] != 1 || !mentions(errs, tt.word) {
				t.Fatalf("codes=%v errs=%v", codes, errs)
			}
		})
	}
}

// Hostile values are refused with a reason, and the value is never in the error:
// whatever was typed may be a password in the wrong place.
func TestServerFlagValuesAreRefusedWithoutBeingEchoed(t *testing.T) {
	const canary = "s3cr3t-canary"

	tests := []struct {
		name string
		args []string
	}{
		{"duration", []string{"--http", "--server", "--shutdown-delay=" + canary}},
		{"every duration", []string{"--http", "--server", "--idle-timeout=" + canary, "--read-timeout=" + canary, "--write-timeout=" + canary}},
		{"bool", []string{"--http", "--server", "--block=" + canary}},
		{"value for a bool", []string{"--http", "--server", "--block", canary}},
		{"no value", []string{"--http", "--server", "--addr"}},
		{"twice", []string{"--http", "--server", "--addr", canary, "--addr", canary + "2"}},
		{"short and long", []string{"--http", "--server", "-a", canary, "--addr", canary + "2"}},
		{"too long", []string{"--http", "--server", "--addr", canary + strings.Repeat("x", flags.MaxArgLength)}},
		{"control characters", []string{"--http", "--server", "--probe-path", "/" + canary + "\x1b[31m\nforged=1"}},
		{"listener is not a flag", []string{"--http", "--server", "--listener", canary}},
		{"wrong marker", []string{"--http", "--addr", canary}},
		{"before its marker", []string{"--addr", canary}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			codes, errs := startupFailure(t, nil, flagProviders(tt.args...))

			if len(codes) == 0 || codes[0] != 1 {
				t.Fatalf("exit codes = %v, want a failure; errs %v", codes, errs)
			}

			for _, e := range errs {
				if strings.Contains(e, canary) || strings.ContainsAny(e, "\x1b\n") {
					t.Fatalf("an error repeats the value or an unescaped one: %q", e)
				}
			}
		})
	}
}

// A dry run checks the flags too, and still listens on nothing.
func TestDryRunChecksServerFlags(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("test", func(f *foundation.F) {
		transporthttp.Server(f, http.NotFoundHandler())
	}, foundation.WithDryRun(true), flagProviders("--http", "--server", "--addr", "127.0.0.1:0", "--probe-path", "/"))

	if codes := exit.Codes(); len(codes) != 1 || codes[0] != 1 || !mentions(errsLogged(recorder), "probe path") {
		t.Fatalf("codes=%v errs=%v", exit.Codes(), errsLogged(recorder))
	}
}

// Every setting that a provider can give has a flag and says what it is for, and
// the listener, which is code, has neither: help is generated from the tags, so a
// field added without them would be missing from it.
func TestEverySettingHasAFlagAndHelp(t *testing.T) {
	typ := reflect.TypeFor[transporthttp.ServerConfig]()

	seen := map[string]string{}

	for i := range typ.NumField() {
		field := typ.Field(i)

		// Set in code: not on the command line.
		if field.Tag.Get("json") == "-" {
			if field.Tag.Get("flag") != "" && field.Tag.Get("flag") != "-" {
				t.Errorf("%s is set in code, so it must not be a flag", field.Name)
			}

			continue
		}

		name, _, _ := strings.Cut(field.Tag.Get("flag"), ",")
		if name == "" || name == "-" {
			t.Errorf("%s has no flag", field.Name)

			continue
		}

		if prev, ok := seen[name]; ok {
			t.Errorf("%s and %s share --%s", prev, field.Name, name)
		}

		seen[name] = field.Name

		if strings.TrimSpace(field.Tag.Get("help")) == "" {
			t.Errorf("%s has no help", field.Name)
		}
	}

	// Only the address has a short flag: the surface stays small.
	if got := typ.Field(0).Tag.Get("flag"); got != "addr,short=a" {
		t.Errorf("Addr flag = %q", got)
	}

	for i := range typ.NumField() {
		field := typ.Field(i)

		if _, short, _ := strings.Cut(field.Tag.Get("flag"), ","); short != "" && field.Name != "Addr" {
			t.Errorf("%s has a short flag", field.Name)
		}
	}
}

// --http --server --help lists the server's flags, and shows no default or value,
// and no environment or JSON names.
func TestHelpDescribesTheServer(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	var out bytes.Buffer

	foundation.Run("test", func(f *foundation.F) {
		transporthttp.Server(f, http.NotFoundHandler())
	},
		foundation.WithConfigProviders(
			env.New(env.WithMap(map[string]string{"TEST_HTTP_SERVER_ADDR": "canary.internal:1"})),
			flags.New(flags.WithArgs([]string{"--http", "--server", "--help"})),
		),
		foundation.WithHelpOutput(&out),
	)

	if len(exit.Codes()) != 0 {
		t.Fatalf("exit codes = %v", exit.Codes())
	}

	help := out.String()

	for _, want := range []string{
		"--addr", "-a", "--read-header-timeout", "--read-timeout", "--write-timeout", "--idle-timeout",
		"--shutdown-timeout", "--shutdown-delay", "--block", "--block-timeout", "--probe-path",
		"--tls-cert-file", "--tls-key-file",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("help does not mention %q:\n%s", want, help)
		}
	}

	for _, banned := range []string{"canary.internal", ":http", "/_probe", "listener", "TEST_HTTP_SERVER", "(env", "json "} {
		if strings.Contains(help, banned) {
			t.Errorf("help shows %q:\n%s", banned, help)
		}
	}
}

// A setting is named once, for the environment and for files, so the two can't
// drift apart: the server's settings have a config name and no tag of a provider's
// own, apart from the flag, which is a separate decision.
func TestEverySettingHasOneCommonName(t *testing.T) {
	typ := reflect.TypeFor[transporthttp.ServerConfig]()

	seen := map[string]string{}

	for i := range typ.NumField() {
		field := typ.Field(i)

		if field.Tag.Get("json") == "-" {
			continue
		}

		name, _, _ := strings.Cut(field.Tag.Get("config"), ",")
		if name == "" {
			t.Errorf("%s has no config name", field.Name)

			continue
		}

		if prev, ok := seen[strings.ToLower(name)]; ok {
			t.Errorf("%s and %s share the name %s", prev, field.Name, name)
		}

		seen[strings.ToLower(name)] = field.Name

		for _, own := range []string{"env", "json"} {
			if tag, ok := field.Tag.Lookup(own); ok {
				t.Errorf("%s has its own %s tag %q beside the config name", field.Name, own, tag)
			}
		}
	}
}

// A default is not a value that anyone set, so help doesn't show what the code
// gave, and nothing is listening after it.
func TestHelpDoesNotShowTheAddressInCode(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	var out bytes.Buffer

	foundation.Run("test", func(f *foundation.F) {
		server(f, hello, withAddr("canary.internal:1"))
	},
		foundation.WithConfigProviders(flags.New(flags.WithArgs([]string{"--http", "--server", "--help"}))),
		foundation.WithHelpOutput(&out),
	)

	if len(exit.Codes()) != 0 || !strings.Contains(out.String(), "--addr") || strings.Contains(out.String(), "canary.internal") {
		t.Fatalf("exit codes %v, help:\n%s", exit.Codes(), out.String())
	}
}

// startedAddr runs the server with the options and providers, and returns the
// address it listened on, from the log, after answering a request there.
func startedAddr(t *testing.T, opts []opt, fopts ...foundation.Option) string {
	t.Helper()

	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var addr string

	foundation.Run("test", func(f *foundation.F) {
		server(f, hello, append([]opt{withBlock()}, opts...)...)

		for _, e := range recorder.Entries() {
			if e.Record.Message != "serving http server" {
				continue
			}

			e.Record.Attrs(func(a slog.Attr) bool {
				if a.Key == "addr" {
					addr = a.Value.String()
				}

				return true
			})
		}

		if addr != "" {
			if body := get(t, addr); body != "hello" {
				t.Errorf("body %q", body)
			}
		}

		f.Shutdown("done")
	}, append([]foundation.Option{foundation.WithConfigProviders()}, fopts...)...)

	if len(exit.Codes()) != 0 {
		t.Fatalf("exit codes = %v", exit.Codes())
	}

	if addr == "" {
		t.Fatal("the server did not say where it listened")
	}

	return addr
}

// An address with port 0 and no host listens on every interface, on a port the
// system chooses.
func TestPortZeroWithNoHost(t *testing.T) {
	addr := startedAddr(t, []opt{withAddr(":0")})

	if _, port, _ := net.SplitHostPort(addr); port == "0" || port == "" {
		t.Fatalf("listening on %s", addr)
	}
}

// A mistake with the address stops startup with a reason, and the address, which
// may be anything that was typed, is not in it.
func TestAddressMistakesAreRefusedWithoutBeingEchoed(t *testing.T) {
	const canary = "s3cr3t-canary"

	tests := map[string][]opt{
		"no port":            {withAddr(canary)},
		"bad port":           {withAddr(canary + ":99999")},
		"control characters": {withAddr(canary + "\x1b[31m\nforged=1:80")},
	}

	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			recorder := foundationtest.NewLogRecorder()
			recorder.Install(t)

			foundation.Run("test", func(f *foundation.F) {
				server(f, hello, opts...)
			}, foundation.WithConfigProviders())

			if codes := exit.Codes(); len(codes) != 1 || codes[0] != 1 {
				t.Fatalf("exit codes = %v", codes)
			}

			errs := errsLogged(recorder)
			if len(errs) == 0 {
				t.Fatal("nothing explained the failure")
			}

			for _, e := range errs {
				if strings.Contains(e, canary) || strings.ContainsAny(e, "\x1b\n") {
					t.Fatalf("an error repeats the address: %q", e)
				}
			}
		})
	}
}

// When the graceful shutdown runs out of time, the connections still open are
// closed, instead of being left to outlive the server.
func TestShutdownTimeoutClosesRemainingConnections(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	started := make(chan struct{})
	release := make(chan struct{})

	t.Cleanup(func() { close(release) })

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
	})

	baseURL, shutdown, done := startServer(t, handler, []opt{
		withShutdownTimeout(100 * time.Millisecond),
	})

	result := make(chan error, 1)

	go func() {
		resp, err := http.Get(baseURL + "/")
		if err == nil {
			resp.Body.Close()
		}

		result <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the request never reached the handler")
	}

	shutdown()

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected the connection to be closed on the client")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the connection was left open after the shutdown timed out")
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("foundation.Run did not return")
	}

	var forced bool

	for _, e := range recorder.Entries() {
		if e.Record.Message == "forced close" {
			forced = true
		}
	}

	if !forced {
		t.Fatal("expected the forced close to be logged")
	}
}

// A shutdown that finishes in time doesn't force anything.
func TestGracefulShutdownDoesNotForceClose(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	_, shutdown, done := startServer(t, http.NotFoundHandler(), nil)

	shutdown()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("foundation.Run did not return")
	}

	for _, e := range recorder.Entries() {
		if e.Record.Message == "forced close" {
			t.Fatal("a clean shutdown should not be forced")
		}
	}
}

// dryRunServer runs the HTTP server under the root "test" in a dry run, and
// returns the exit codes, what was logged, the address it was given and how many
// probes it registered.
func dryRunServer(t *testing.T, vars map[string]string, opts ...opt) (codes []int, recorder *foundationtest.LogRecorder, addr string, probes int) {
	t.Helper()

	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder = foundationtest.NewLogRecorder()
	recorder.Install(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	addr = ln.Addr().String()
	ln.Close()

	foundation.Run("test", func(f *foundation.F) {
		server(f, http.NotFoundHandler(), append([]opt{withAddr(addr)}, opts...)...)

		for range f.Probes().Seq() {
			probes++
		}
	}, foundation.WithDryRun(true), foundation.WithConfigProviders(env.New(env.WithMap(vars))))

	return exit.Codes(), recorder, addr, probes
}

func skippedFns(r *foundationtest.LogRecorder) []string {
	var out []string

	for _, e := range r.Entries() {
		if e.Record.Message != "skipped" {
			continue
		}

		for _, a := range e.Attrs {
			if a.Key == "fn" {
				out = append(out, a.Value.String())
			}
		}
	}

	return out
}

// In a dry run the server loads and checks its configuration and then stops
// before it listens: the port is never opened, and no probe is registered.
func TestDryRunDoesNotListen(t *testing.T) {
	codes, recorder, addr, probes := dryRunServer(t, nil)

	if len(codes) != 0 {
		t.Fatalf("exit codes = %v, want none", codes)
	}

	if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		conn.Close()
		t.Fatalf("something is listening on %s", addr)
	}

	// The address is free for anyone else to use.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("the port should not have been taken: %v", err)
	}

	ln.Close()

	if probes != 0 {
		t.Fatalf("%d probes registered, want none", probes)
	}

	if got := skippedFns(recorder); !slices.Equal(got, []string{"test/http.server"}) {
		t.Fatalf("skipped %v, want the server", got)
	}

	for _, e := range recorder.Entries() {
		if e.Record.Message == "serving http server" {
			t.Fatal("the server must not log that it is serving")
		}
	}
}

// The server's configuration is still loaded and validated, so a mistake in it is
// found by a dry run.
func TestDryRunStillLoadsTheServerConfiguration(t *testing.T) {
	t.Run("a bad value", func(t *testing.T) {
		codes, _, _, _ := dryRunServer(t, map[string]string{"TEST_HTTP_SERVER_READ_HEADER_TIMEOUT": "soon"})

		if !slices.Equal(codes, []int{1}) {
			t.Fatalf("exit codes = %v, want [1]", codes)
		}
	})

	t.Run("a bad probe path", func(t *testing.T) {
		codes, _, _, _ := dryRunServer(t, map[string]string{"TEST_HTTP_SERVER_PROBE_PATH": "no-slash"})

		if !slices.Equal(codes, []int{1}) {
			t.Fatalf("exit codes = %v, want [1]", codes)
		}
	})

	t.Run("a certificate without a key", func(t *testing.T) {
		codes, _, _, _ := dryRunServer(t, map[string]string{"TEST_HTTP_SERVER_TLS_CERT_FILE": "/nonexistent/cert.pem"})

		if !slices.Equal(codes, []int{1}) {
			t.Fatalf("exit codes = %v, want [1]", codes)
		}
	})

	t.Run("a certificate that can't be loaded", func(t *testing.T) {
		codes, _, _, _ := dryRunServer(t, nil, withTLS("/nonexistent/cert.pem", "/nonexistent/key.pem"))

		if !slices.Equal(codes, []int{1}) {
			t.Fatalf("exit codes = %v, want [1]", codes)
		}
	})

	t.Run("a good configuration", func(t *testing.T) {
		codes, _, _, _ := dryRunServer(t, map[string]string{"TEST_HTTP_SERVER_READ_HEADER_TIMEOUT": "5s", "TEST_HTTP_SERVER_PROBE_PATH": "/_up"})

		if len(codes) != 0 {
			t.Fatalf("exit codes = %v, want none", codes)
		}
	})
}

// Outside a dry run nothing changes: the server still listens.
func TestServerStillListensOutsideADryRun(t *testing.T) {
	baseURL, shutdown, done := startServer(t, http.NotFoundHandler(), nil)

	resp, err := http.Get(baseURL + "/")
	if err != nil {
		t.Fatal(err)
	}

	resp.Body.Close()

	shutdown()
	<-done
}

// The server says what it is for, as any function can with f.Help: its first line
// is on its row in the tree, and the whole text is the heading of its own help.
// "--http" is only the first word of the function's name, "http.server", so the
// text is on "--server", the function.
const serverSummary = "Serves the application over HTTP, or HTTPS when a certificate and key are given."

func helpFor(t *testing.T, args ...string) string {
	t.Helper()

	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	var out bytes.Buffer

	foundation.Run("test", func(f *foundation.F) {
		transporthttp.Server(f, http.NotFoundHandler())
	}, flagProviders(args...), foundation.WithHelpOutput(&out))

	if len(exit.Codes()) != 0 {
		t.Fatalf("exit codes = %v", exit.Codes())
	}

	return out.String()
}

func TestHelpTreeDescribesTheServerOnItsRow(t *testing.T) {
	help := helpFor(t, "--help")

	var server, word string

	for _, line := range strings.Split(help, "\n") {
		switch {
		case strings.HasPrefix(strings.TrimSpace(line), "--server"):
			server = line
		case strings.TrimSpace(line) == "--http":
			word = line
		}
	}

	if !strings.Contains(server, serverSummary) {
		t.Errorf("the --server row has no description:\n%s", help)
	}

	// The first word of the name is not a function: it has nothing to say.
	if word == "" {
		t.Errorf("no --http row without text:\n%s", help)
	}

	// Only the first line is in the tree.
	if strings.Contains(help, "shutdown timeout") {
		t.Errorf("the long text is in the tree:\n%s", help)
	}
}

func TestHelpForTheWordShowsTheServerRow(t *testing.T) {
	help := helpFor(t, "--http", "--help")

	if !strings.Contains(help, "--server") || !strings.Contains(help, serverSummary) {
		t.Errorf("--http --help doesn't describe --server:\n%s", help)
	}
}

func TestHelpForTheServerStartsWithItsWholeText(t *testing.T) {
	help := helpFor(t, "--http", "--server", "--help")

	if !strings.HasPrefix(help, serverSummary+"\n") {
		t.Fatalf("the text is not the heading:\n%s", help)
	}

	// The rest of it says what it does with the settings below, in words and
	// not values.
	for _, want := range []string{"probe", "shutdown timeout", "--addr"} {
		if !strings.Contains(help, want) {
			t.Errorf("help does not mention %q:\n%s", want, help)
		}
	}

	// The text is followed by the flags, after a blank line.
	if i := strings.Index(help, "--addr"); i < 0 || !strings.Contains(help[:i], "\n\n") {
		t.Errorf("no blank line between the text and the flags:\n%s", help)
	}
}

// The text is fixed in code. It shows no default, port, path or name of a
// variable or key, and nothing that could forge a line.
func TestServerHelpTextShowsNoValues(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"--http", "--help"},
		{"--http", "--server", "--help"},
	} {
		help := helpFor(t, args...)

		for _, banned := range []string{
			":http", ":80", "/_probe", "listener", "TEST_HTTP_SERVER", "SVC_", "(env", "json ",
			"10s", "120s", "30s",
		} {
			if strings.Contains(help, banned) {
				t.Errorf("%q: help shows %q:\n%s", args, banned, help)
			}
		}

		for _, r := range help {
			if r != '\n' && (r < ' ' || r == 0x7f) {
				t.Errorf("%q: help has the control character %q", args, r)
			}
		}
	}
}

// heldOpen runs a server in a function with the given shutdown timeout, holds a
// request open on it, and asks the program to shut down. It returns what was
// logged, the exit codes, the function the server ran in and how long the
// shutdown took.
func heldOpen(t *testing.T, timeout time.Duration, change func(*transporthttp.ServerConfig)) (recorder *foundationtest.LogRecorder, codes []int, srv *foundation.F, took time.Duration) {
	t.Helper()

	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder = foundationtest.NewLogRecorder()
	recorder.Install(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	finish := make(chan struct{})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)

		select {
		case <-r.Context().Done():
		case <-finish:
		}
	})

	cfg := transporthttp.DefaultServerConfig()
	cfg.Listener = ln

	if change != nil {
		change(&cfg)
	}

	url := "http://" + ln.Addr().String() + "/"

	var shutdownAt time.Time

	var mu sync.Mutex

	foundation.Run("svc", func(f *foundation.F) {
		f.Run("http.server", func(f *foundation.F) {
			f.SetTimeout(timeout)

			mu.Lock()
			srv = f
			mu.Unlock()

			transporthttp.ServerWithConfig(f, cfg, handler)
		})

		go func() {
			resp, err := http.Get(url)
			if err == nil {
				resp.Body.Close()
			}
		}()

		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Error("the request never reached the handler")
		}

		mu.Lock()
		shutdownAt = time.Now()
		mu.Unlock()

		f.Shutdown("test")
	})

	close(finish)

	mu.Lock()
	defer mu.Unlock()

	return recorder, exit.Codes(), srv, time.Since(shutdownAt)
}

// A client that holds a request open must not be able to turn a stop into a
// failure. The server's own time to finish connections has to end before the
// framework gives up on the function that runs it, or the function is abandoned
// (flagged as one that did not stop, which fails a restart) and the connection is
// left open instead of being closed.
func TestAHeldOpenRequestIsForceClosedBeforeTheFunctionIsAbandoned(t *testing.T) {
	recorder, codes, _, took := heldOpen(t, time.Second, nil)

	if got := entriesNamed(recorder, "function did not exit within shutdown timeout"); len(got) != 0 {
		t.Fatalf("the server was abandoned instead of closed: %v", got)
	}

	if got := entriesNamed(recorder, "forced close"); len(got) != 1 {
		t.Fatalf("expected one forced close, got %d", len(got))
	}

	if len(codes) != 0 {
		t.Fatalf("exit codes = %v, want none", codes)
	}

	if took > 2*time.Second {
		t.Fatalf("the shutdown took %v with a timeout of 1s", took)
	}
}

// The delay before stopping is part of what the function has, not time on top of
// it.
func TestTheShutdownDelayIsInsideTheFunctionsTimeout(t *testing.T) {
	recorder, codes, _, _ := heldOpen(t, 1500*time.Millisecond, func(cfg *transporthttp.ServerConfig) {
		cfg.ShutdownDelay = 500 * time.Millisecond
	})

	if got := entriesNamed(recorder, "function did not exit within shutdown timeout"); len(got) != 0 {
		t.Fatalf("the server was abandoned instead of closed: %v", got)
	}

	if got := entriesNamed(recorder, "forced close"); len(got) != 1 || len(codes) != 0 {
		t.Fatalf("forced closes = %d, exit codes = %v", len(got), codes)
	}
}

// A time to finish connections that the function's timeout can't hold is given
// the room it asked for, rather than being cut short: the function's own timeout
// is raised to fit it, delay and a margin included.
func TestAnExplicitShutdownTimeoutThatDoesNotFitRaisesTheFunctionsTimeout(t *testing.T) {
	_, _, srv, _ := heldOpen(t, 200*time.Millisecond, func(cfg *transporthttp.ServerConfig) {
		cfg.ShutdownTimeout = 600 * time.Millisecond
		cfg.ShutdownDelay = 100 * time.Millisecond
	})

	if got := srv.Timeout(); got < 700*time.Millisecond {
		t.Fatalf("the function's timeout is %v, want at least the delay and the shutdown time (700ms)", got)
	}
}

// With no time left for the server at all, startup fails, without naming a value.
func TestADelayThatLeavesNoTimeFailsStartup(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	cfg := transporthttp.DefaultServerConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.ShutdownDelay = time.Hour

	foundation.Run("svc", func(f *foundation.F) {
		// So that a server that does start can't hang the test.
		time.AfterFunc(3*time.Second, func() { f.Shutdown("watchdog") })

		f.Run("http.server", func(f *foundation.F) {
			f.SetTimeout(time.Second)

			transporthttp.ServerWithConfig(f, cfg, hello)
		})
	})

	if codes := exit.Codes(); !slices.Equal(codes, []int{1}) {
		t.Fatalf("exit codes = %v, want [1]", codes)
	}

	if got := entriesNamed(recorder, "serving http server"); len(got) != 0 {
		t.Fatal("it listened before it checked")
	}
}

// There is no default address: a server that quietly listens on every interface
// on port 80 is not what a mistake such as a misspelt variable should leave
// behind. Without an address or a listener the required address is missing and
// nothing listens.
func TestAServerWithNoAddressFailsStartup(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("svc", func(f *foundation.F) {
		// So that a server that does start can't hang the test.
		time.AfterFunc(time.Second, func() { f.Shutdown("watchdog") })

		transporthttp.Server(f, hello)
	}, foundation.WithConfigProviders())

	if codes := exit.Codes(); !slices.Equal(codes, []int{1}) {
		t.Fatalf("exit codes = %v, want [1]", codes)
	}

	if got := entriesNamed(recorder, "serving http server"); len(got) != 0 {
		t.Fatal("it listened without an address")
	}

	if !mentions(errsLogged(recorder), "required") {
		t.Fatalf("no error said that the address is required: %v", errsLogged(recorder))
	}
}

// An address or a listener is each enough.
func TestAServerNeedsOnlyOneWayToKnowWhereToListen(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer ln.Close()

	for name, change := range map[string]func(*transporthttp.ServerConfig){
		"an address": func(c *transporthttp.ServerConfig) { c.Addr = "127.0.0.1:0" },
		"a listener": func(c *transporthttp.ServerConfig) { c.Listener = ln },
	} {
		t.Run(name, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			cfg := transporthttp.DefaultServerConfig()
			change(&cfg)

			foundation.Run("svc", func(f *foundation.F) {
				f.Run("http.server", func(f *foundation.F) {
					transporthttp.ServerWithConfig(f, cfg, hello)
				})

				f.Shutdown("done")
			})

			if codes := exit.Codes(); len(codes) != 0 {
				t.Fatalf("exit codes = %v, want none", codes)
			}
		})
	}
}

// entriesNamed is what was logged with message msg.
func entriesNamed(r *foundationtest.LogRecorder, msg string) []foundationtest.LogEntry {
	var out []foundationtest.LogEntry

	for _, e := range r.Entries() {
		if e.Record.Message == msg {
			out = append(out, e)
		}
	}

	return out
}
