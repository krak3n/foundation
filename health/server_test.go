package health_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/flags"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/foundationtest"
	"go.krak3n.dev/foundation/health"
	"go.krak3n.dev/foundation/health/monitor"
	transporthttp "go.krak3n.dev/foundation/transport/http"
)

// startHealth runs a health server under the root function "test" and returns
// the address it is listening on, found from its log, and a function to stop it.
func startHealth(t *testing.T, setup func(f *foundation.F), opts []health.ServerOption, fopts ...foundation.Option) (addr string, stop func()) {
	t.Helper()

	foundationtest.NewExitRecorder().Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	stopCh := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)

		foundation.Run("test", func(f *foundation.F) {
			f.Run("watcher", func(f *foundation.F) {
				f.Parallel()

				select {
				case <-stopCh:
					f.Shutdown("test complete")
				case <-f.Context().Done():
				}
			})

			if setup != nil {
				setup(f)
			}

			health.Server(f, opts...)
		}, append([]foundation.Option{foundation.WithConfigProviders()}, fopts...)...)
	}()

	var once bool

	stop = func() {
		if !once {
			once = true

			close(stopCh)
			<-done
		}
	}

	t.Cleanup(stop)

	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for _, l := range loggedEntries(recorder) {
			if l.msg != "serving http server" {
				continue
			}

			host, port, err := net.SplitHostPort(l.attrs["addr"])
			if err != nil {
				t.Fatalf("unexpected address %q", l.attrs["addr"])
			}

			if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
				host = "127.0.0.1"
			}

			return net.JoinHostPort(host, port), stop
		}
	}

	t.Fatal("the health server did not start")

	return "", stop
}

func freeAddr(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer ln.Close()

	return ln.Addr().String()
}

func fetch(t *testing.T, url string) (int, string) {
	t.Helper()

	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(url)
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, string(b)
}

func TestServerListensWhereItIsToldTo(t *testing.T) {
	want := freeAddr(t)

	tests := map[string]struct {
		fopts []foundation.Option
	}{
		"env": {fopts: []foundation.Option{foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{
			"TEST_HEALTH_ADDR": want,
		})))}},
		"json": {fopts: []foundation.Option{foundation.WithConfigProviders(json.Raw([]byte(fmt.Sprintf(`{"test": {"health": {"addr": %q}}}`, want))))}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			addr, stop := startHealth(t, nil, nil, tt.fopts...)

			if addr != want {
				t.Fatalf("listening on %s, want %s", addr, want)
			}

			if code, body := fetch(t, "http://"+addr+"/healthz"); code != http.StatusOK || body != "healthz check passed\n" {
				t.Fatalf("got %d %q", code, body)
			}

			stop()
		})
	}
}

func failingProbe(f *foundation.F) {
	f.Probes().Add(monitor.NewProbe("shop/db.pool", monitor.AllModes, monitor.StatusCheckerFunc(func(context.Context) (monitor.Status, error) {
		return monitor.StatusUnhealthy, errors.New("connection refused")
	})))
}

func TestServerVerboseIsOffUnlessEnabled(t *testing.T) {
	tests := map[string]struct {
		opts    []health.ServerOption
		fopts   func(addr string) []foundation.Option
		verbose bool
	}{
		"default": {},
		"option":  {opts: []health.ServerOption{health.WithVerbose()}, verbose: true},
		"env": {fopts: func(addr string) []foundation.Option {
			return []foundation.Option{foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{
				"TEST_HEALTH_ADDR": addr, "TEST_HEALTH_VERBOSE": "true",
			})))}
		}, verbose: true},
		"json": {fopts: func(addr string) []foundation.Option {
			return []foundation.Option{foundation.WithConfigProviders(json.Raw([]byte(fmt.Sprintf(`{"test": {"health": {"addr": %q, "verbose": true}}}`, addr))))}
		}, verbose: true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			listen := freeAddr(t)

			fopts := []foundation.Option{foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{"TEST_HEALTH_ADDR": listen})))}
			if tt.fopts != nil {
				fopts = tt.fopts(listen)
			}

			addr, stop := startHealth(t, failingProbe, tt.opts, fopts...)

			defer stop()

			code, body := fetch(t, "http://"+addr+"/healthz?verbose")

			if code != http.StatusInternalServerError {
				t.Fatalf("code = %d", code)
			}

			if shown := strings.Contains(body, "connection refused"); shown != tt.verbose {
				t.Fatalf("verbose output shown = %v, want %v: %q", shown, tt.verbose, body)
			}
		})
	}
}

func TestServerResponsesHaveSafeHeaders(t *testing.T) {
	t.Setenv("TEST_HEALTH_ADDR", freeAddr(t))

	addr, stop := startHealth(t, failingProbe, nil)

	defer stop()

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("headers: %v", resp.Header)
	}
}

// WithProbeTimeout works through the server, not only with Handler.
func TestServerAcceptsAProbeTimeout(t *testing.T) {
	release := make(chan struct{})

	// Released on every path, so a failure doesn't leave the probe blocked.
	releaseProbe := sync.OnceFunc(func() { close(release) })

	defer releaseProbe()

	hung := func(f *foundation.F) {
		f.Probes().Add(monitor.NewProbe("hung", monitor.AllModes, monitor.StatusCheckerFunc(func(context.Context) (monitor.Status, error) {
			<-release

			return monitor.StatusHealthy, nil
		})))
	}

	t.Setenv("TEST_HEALTH_ADDR", freeAddr(t))

	addr, stop := startHealth(t, hung, []health.ServerOption{health.WithProbeTimeout(300 * time.Millisecond)})

	defer stop()

	start := time.Now()

	if code, _ := fetch(t, "http://"+addr+"/healthz"); code != http.StatusInternalServerError {
		t.Fatalf("code = %d", code)
	}

	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("took %v with a probe timeout of 300ms", took)
	}

	// Let the probe finish, and wait until it has: when it does it logs that it
	// recovered, and that must happen while this test's logger is installed, not
	// in whichever test runs next. A request after the release either joins the
	// run that was in flight or starts a fresh one, and returns healthy once the
	// outcome of that run, and its log line, are recorded.
	releaseProbe()

	if code, _ := fetch(t, "http://"+addr+"/healthz"); code != http.StatusOK {
		t.Fatalf("code = %d after the probe was released, want %d", code, http.StatusOK)
	}
}

func flagProvider(args ...string) foundation.Option {
	return foundation.WithConfigProviders(flags.New(flags.WithArgs(args)))
}

// The health server takes the settings of an HTTP server directly: they are its
// own, after --health, with no --http --server in between.
func TestHealthReadsItsAddressFromFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--health", "--addr", ""},
		{"--health", "--addr="},
		{"--health", "-a", ""},
	} {
		want := freeAddr(t)

		args[len(args)-1] += want

		addr, stop := startHealth(t, nil, nil, flagProvider(args...))

		if addr != want {
			t.Fatalf("%q: listening on %s, want %s", args, addr, want)
		}

		if code, _ := fetch(t, "http://"+addr+"/healthz"); code != http.StatusOK {
			t.Fatalf("code = %d", code)
		}

		stop()
	}
}

// Flags win over the environment.
func TestHealthFlagsWinOverTheEnvironment(t *testing.T) {
	flagged, other := freeAddr(t), freeAddr(t)

	addr, stop := startHealth(t, nil, nil, foundation.WithConfigProviders(
		env.New(env.WithMap(map[string]string{"TEST_HEALTH_ADDR": other})),
		flags.New(flags.WithArgs([]string{"--health", "--addr", flagged})),
	))

	defer stop()

	if addr != flagged {
		t.Fatalf("listening on %s, want %s", addr, flagged)
	}
}

// With nothing set the health server is on port 3417, on every interface, as an
// orchestrator probes it from outside. The default is the one that configuration
// can change, which is the point of it not being an option.
func TestHealthDefaultsToPort3417(t *testing.T) {
	ln, err := net.Listen("tcp", ":3417")
	if err != nil {
		t.Skip("port 3417 is in use")
	}

	ln.Close()

	addr, stop := startHealth(t, nil, nil)

	defer stop()

	if _, port, _ := net.SplitHostPort(addr); port != "3417" {
		t.Fatalf("listening on %s", addr)
	}
}

// --verbose is for the health server: the detail it lets a client ask for.
func TestHealthVerboseFlag(t *testing.T) {
	for _, tt := range []struct {
		args    []string
		verbose bool
	}{
		{[]string{"--health", "--verbose"}, true},
		{[]string{"--health", "--verbose=false"}, false},
	} {
		want := freeAddr(t)

		addr, stop := startHealth(t, failingProbe, nil, flagProvider(append(tt.args, "--addr", want)...))

		_, body := fetch(t, "http://"+addr+"/healthz?verbose")

		if shown := strings.Contains(body, "connection refused"); shown != tt.verbose {
			t.Errorf("%q: detail shown = %v, want %v", tt.args, shown, tt.verbose)
		}

		stop()
	}
}

// What is not a setting of the health server is not a flag of it: the old --addr
// and the test-only random port are refused, not ignored.
func TestHealthFlagMistakesFailAtStartup(t *testing.T) {
	const canary = "s3cr3t-canary"

	tests := map[string][]string{
		"the nested names":        {"--health", "--http", "--server", "--addr", canary},
		"random port":             {"--health", "--random-port"},
		"listener":                {"--health", "--listener", canary},
		"verbose not a bool":      {"--health", "--verbose=" + canary},
		"verbose with a value":    {"--health", "--verbose", canary},
		"verbose twice":           {"--health", "--verbose", "--verbose=false"},
		"addr with no value":      {"--health", "--addr"},
		"addr twice":              {"--health", "--addr", canary, "-a", canary + "2"},
		"block not a bool":        {"--health", "--block=" + canary},
		"timeout not a duration":  {"--health", "--shutdown-delay=" + canary},
		"probe path":              {"--health", "--probe-path", "/"},
		"probe path with control": {"--health", "--probe-path", "/" + canary + "\x1b[31m\nforged=1"},
		"certificate only":        {"--health", "--tls-cert-file", canary},
		"before its marker":       {"--verbose"},
		"under the wrong marker":  {"--verbose", "--health"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			recorder := foundationtest.NewLogRecorder()
			recorder.Install(t)

			foundation.Run("test", func(f *foundation.F) {
				health.Server(f)
			}, flagProvider(args...), foundation.WithDryRun(true))

			if codes := exit.Codes(); len(codes) != 1 || codes[0] != 1 {
				t.Fatalf("exit codes = %v", codes)
			}

			assertNothingLeaks(t, recorder, canary)
		})
	}
}

// An address that can't be listened on stops a real run, with a reason that
// doesn't repeat it.
func TestHealthBadAddressesAreRefusedWithoutBeingEchoed(t *testing.T) {
	const canary = "s3cr3t-canary"

	for _, addr := range []string{canary, canary + ":99999", canary + "\x1b[31m\nforged=1:80"} {
		exit := foundationtest.NewExitRecorder()
		exit.Install(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		foundation.Run("test", func(f *foundation.F) {
			health.Server(f)
		}, flagProvider("--health", "--addr", addr))

		if codes := exit.Codes(); len(codes) != 1 || codes[0] != 1 {
			t.Fatalf("%q: exit codes = %v", addr, codes)
		}

		assertNothingLeaks(t, recorder, canary)
	}
}

// A bad address is refused with a reason, and without being echoed.
func TestHealthABadAddressIsRefusedWithoutBeingEchoed(t *testing.T) {
	const canary = "s3cr3t-canary"

	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("test", func(f *foundation.F) {
		health.Server(f)
	}, foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{"TEST_HEALTH_ADDR": canary}))))

	if codes := exit.Codes(); len(codes) != 1 || codes[0] != 1 {
		t.Fatalf("exit codes = %v", codes)
	}

	assertNothingLeaks(t, recorder, canary)
}

// assertNothingLeaks fails if anything logged repeats the value, or has a control
// character in an err attribute.
func assertNothingLeaks(t *testing.T, r *foundationtest.LogRecorder, canary string) {
	t.Helper()

	for _, l := range loggedEntries(r) {
		for k, v := range l.attrs {
			if strings.Contains(v, canary) || (k == "err" && strings.ContainsAny(v, "\x1b\n")) {
				t.Fatalf("%q logged %s=%q", l.msg, k, v)
			}
		}
	}
}

// The names from the nested design are not read, and strict mode reports them,
// so nobody keeps setting one that does nothing.
func TestTheNestedNamesAreReportedInStrictMode(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("test", func(f *foundation.F) {
		health.Server(f)
	},
		foundation.WithStrictConfig(),
		foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{
			"TEST_HEALTH_ADDR":             freeAddr(t),
			"TEST_HEALTH_HTTP_SERVER_ADDR": ":1",
		}))),
	)

	if codes := exit.Codes(); len(codes) != 1 || codes[0] != 1 {
		t.Fatalf("exit codes = %v", codes)
	}
}

// With the app's own server and the health server in one program, each is set
// with its own flags in whichever order they are given: the words after --http
// are the app's, and the health server has no --http to be mixed up with.
func TestAnAppServerAndTheHealthServerTakeFlagsInAnyOrder(t *testing.T) {
	for name, order := range map[string]func(app, hl string) []string{
		"app first": func(app, hl string) []string {
			return []string{"--http", "--server", "--block", "--addr", app, "--health", "--addr", hl}
		},
		"health first": func(app, hl string) []string {
			return []string{"--health", "--addr", hl, "--http", "--server", "--block", "--addr", app}
		},
	} {
		t.Run(name, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			app, hl := freeAddr(t), freeAddr(t)

			var appCode, healthCode int

			foundation.Run("test", func(f *foundation.F) {
				transporthttp.Server(f, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
				health.Server(f)

				appCode, _ = fetch(t, "http://"+app+"/")
				healthCode, _ = fetch(t, "http://"+hl+"/healthz")

				f.Shutdown("done")
			}, flagProvider(order(app, hl)...))

			if len(exit.Codes()) != 0 || appCode != http.StatusOK || healthCode != http.StatusOK {
				t.Fatalf("exit codes %v, app %d, health %d", exit.Codes(), appCode, healthCode)
			}
		})
	}
}

// Block is a default, changed by configuration like any other setting, so that no
// flag is accepted and then ignored. By default the server is accepting
// connections when health.Server returns.
func TestHealthBlocksByDefaultAndConfigurationCanChangeIt(t *testing.T) {
	foundationtest.NewExitRecorder().Install(t)

	for _, tt := range []struct {
		name  string
		args  []string
		ready bool
	}{
		{"default", []string{"--health"}, true},
		{"on", []string{"--health", "--block"}, true},
	} {
		addr := freeAddr(t)

		var accepting bool

		foundation.Run("test", func(f *foundation.F) {
			health.Server(f)

			conn, err := net.Dial("tcp", addr)
			if err == nil {
				conn.Close()
			}

			accepting = err == nil

			f.Shutdown("done")
		}, flagProvider(append(tt.args, "--addr", addr)...))

		if accepting != tt.ready {
			t.Errorf("%s: accepting = %v, want %v", tt.name, accepting, tt.ready)
		}
	}

	// Switching it off is honoured and still serves.
	addr, stop := startHealth(t, nil, nil, flagProvider("--health", "--block=false", "--addr", freeAddr(t)))

	defer stop()

	if code, _ := fetch(t, "http://"+addr+"/healthz"); code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
}

// The probe path is where the server's own probe is put. It can't be where an
// endpoint of the health server is, which it would hide: the mistake stops
// startup, and the path is not in the error.
func TestProbePathCanNotHideAHealthEndpoint(t *testing.T) {
	for _, path := range []string{"/healthz", "/readyz", "/livez", "/startup"} {
		t.Run(path, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			recorder := foundationtest.NewLogRecorder()
			recorder.Install(t)

			foundation.Run("test", func(f *foundation.F) {
				health.Server(f)
			}, flagProvider("--health", "--probe-path", path), foundation.WithDryRun(true))

			if codes := exit.Codes(); len(codes) != 1 || codes[0] != 1 {
				t.Fatalf("exit codes = %v", codes)
			}

			var found bool

			for _, l := range loggedEntries(recorder) {
				if strings.Contains(strings.ToLower(l.attrs["err"]), "probe path") {
					found = true
				}

				if l.attrs["err"] != "" && strings.Contains(l.attrs["err"], path) {
					t.Errorf("the error repeats the path: %q", l.attrs["err"])
				}
			}

			if !found {
				t.Fatalf("no error about the probe path: %v", loggedEntries(recorder))
			}
		})
	}

	// Another path is fine.
	addr, stop := startHealth(t, nil, nil, flagProvider("--health", "--probe-path", "/probe", "--addr", freeAddr(t)))

	defer stop()

	if code, _ := fetch(t, "http://"+addr+"/probe"); code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
}

// --health --help lists everything the health server takes in one place, shows
// no default, and says what verbose gives away.
func TestHealthHelp(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	var out bytes.Buffer

	foundation.Run("test", func(f *foundation.F) {
		health.Server(f)
	},
		foundation.WithConfigProviders(env.New(env.WithMap(nil)), flags.New(flags.WithArgs([]string{"--health", "--help"}))),
		foundation.WithHelpOutput(&out),
	)

	if len(exit.Codes()) != 0 {
		t.Fatalf("exit codes = %v", exit.Codes())
	}

	help := out.String()

	for _, want := range []string{
		"--verbose", "anyone who can reach",
		"--addr", "-a", "--probe-path", "--tls-cert-file", "--shutdown-timeout", "--block",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("help does not mention %q:\n%s", want, help)
		}
	}

	for _, banned := range []string{":3417", "--random-port", "RANDOM_PORT", "--http", "--server", "listener", "TEST_HEALTH", "(env", "json "} {
		if strings.Contains(help, banned) {
			t.Errorf("help shows %q:\n%s", banned, help)
		}
	}
}

// The health server says what it is for, as any function can with f.Help: its
// first line is on its row in the tree, and the whole text is the heading of its
// own help.
const healthSummary = "Serves the health endpoints that an orchestrator probes: startup, readiness and liveness."

func healthHelp(t *testing.T, args ...string) string {
	t.Helper()

	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	var out bytes.Buffer

	foundation.Run("test", func(f *foundation.F) {
		health.Server(f)
	}, flagProvider(args...), foundation.WithHelpOutput(&out))

	if len(exit.Codes()) != 0 {
		t.Fatalf("exit codes = %v", exit.Codes())
	}

	return out.String()
}

func TestHelpTreeDescribesTheHealthServerOnItsRow(t *testing.T) {
	help := healthHelp(t, "--help")

	var row string

	for _, line := range strings.Split(help, "\n") {
		if strings.HasPrefix(line, "--health") {
			row = line
		}
	}

	if !strings.Contains(row, healthSummary) {
		t.Errorf("the --health row has no description:\n%s", help)
	}

	// Only the first line is in the tree.
	if strings.Contains(help, "only a verdict") {
		t.Errorf("the long text is in the tree:\n%s", help)
	}
}

func TestHelpForTheHealthServerStartsWithItsWholeText(t *testing.T) {
	help := healthHelp(t, "--health", "--help")

	if !strings.HasPrefix(help, healthSummary+"\n") {
		t.Fatalf("the text is not the heading:\n%s", help)
	}

	// It says what a response holds, and that detail has to be asked for and
	// what that gives away.
	for _, want := range []string{"only a verdict", "--verbose", "anyone who can reach"} {
		if !strings.Contains(help, want) {
			t.Errorf("help does not mention %q:\n%s", want, help)
		}
	}

	if i := strings.Index(help, "--addr"); i < 0 || !strings.Contains(help[:i], "\n\n") {
		t.Errorf("no blank line between the text and the flags:\n%s", help)
	}
}

// With an application server beside it, each has its own text, and neither
// borrows the other's.
func TestHelpDescribesBothServersSeparately(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	var out bytes.Buffer

	foundation.Run("test", func(f *foundation.F) {
		health.Server(f)
		transporthttp.Server(f, http.NotFoundHandler())
	}, flagProvider("--help"), foundation.WithHelpOutput(&out))

	if len(exit.Codes()) != 0 {
		t.Fatalf("exit codes = %v", exit.Codes())
	}

	help := out.String()

	for _, want := range []string{healthSummary, "Serves the application over HTTP, or HTTPS when a certificate and key are given."} {
		if strings.Count(help, want) != 1 {
			t.Errorf("expected %q once:\n%s", want, help)
		}
	}
}

// The text is fixed in code. It shows no default, port, path or name of a
// variable or key, and nothing that could forge a line.
func TestHealthHelpTextShowsNoValues(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--health", "--help"}} {
		help := healthHelp(t, args...)

		for _, banned := range []string{
			":3417", "3417", "/healthz", "/readyz", "/livez", "/startup", "TEST_HEALTH", "SVC_",
			"(env", "json ", "--http", "--server", "listener", "5s",
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

func dryRunHealth(t *testing.T, vars map[string]string) (codes []int, addr string) {
	t.Helper()

	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	foundationtest.NewLogRecorder().Install(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	addr = ln.Addr().String()
	ln.Close()

	foundation.Run("test", func(f *foundation.F) {
		health.Server(f)
	}, foundation.WithDryRun(true), foundation.WithConfigProviders(env.New(env.WithMap(withAddr(vars, addr)))))

	return exit.Codes(), addr
}

// A dry run loads the health server's configuration, and its HTTP server's, and
// stops before anything listens.
func TestDryRunDoesNotListenForHealth(t *testing.T) {
	codes, addr := dryRunHealth(t, nil)

	if len(codes) != 0 {
		t.Fatalf("exit codes = %v, want none", codes)
	}

	if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		conn.Close()
		t.Fatalf("something is listening on %s", addr)
	}
}

func TestDryRunStillLoadsTheHealthConfiguration(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"the health server's own":    {"TEST_HEALTH_VERBOSE": "maybe"},
		"the settings of its server": {"TEST_HEALTH_READ_HEADER_TIMEOUT": "soon"},
	} {
		t.Run(name, func(t *testing.T) {
			codes, _ := dryRunHealth(t, vars)

			if !slices.Equal(codes, []int{1}) {
				t.Fatalf("exit codes = %v, want [1]", codes)
			}
		})
	}
}

// withAddr is vars with the health server's address added.
func withAddr(vars map[string]string, addr string) map[string]string {
	out := map[string]string{"TEST_HEALTH_ADDR": addr}

	for k, v := range vars {
		out[k] = v
	}

	return out
}
