package http_test

import (
	"bytes"
	"log/slog"
	"net"
	"slices"
	"strings"
	"testing"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/foundationtest"
	transporthttp "go.krak3n.dev/foundation/transport/http"
)

// withDefaults runs the server with fn as its defaults, blocking until it is
// ready, and returns the address it served on ("" if it did not), the exit codes
// and what was logged.
func withDefaults(t *testing.T, fn func(*transporthttp.ServerConfig), fopts ...foundation.Option) (addr string, codes []int, recorder *foundationtest.LogRecorder) {
	t.Helper()

	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder = foundationtest.NewLogRecorder()
	recorder.Install(t)

	// With no configuration of its own, a test is isolated from the real environment.
	if len(fopts) == 0 {
		fopts = []foundation.Option{foundation.WithConfigProviders(env.New(env.WithMap(nil)))}
	}

	foundation.Run("svc", func(f *foundation.F) {
		transporthttp.Server(f, hello, transporthttp.WithDefaults(fn))

		for _, e := range entriesNamed(recorder, "serving http server") {
			e.Record.Attrs(func(a slog.Attr) bool {
				if a.Key == "addr" {
					addr = a.Value.String()
				}

				return true
			})
		}

		f.Shutdown("done")
	}, fopts...)

	return addr, exit.Codes(), recorder
}

func blockOn(addr string) func(*transporthttp.ServerConfig) {
	return func(c *transporthttp.ServerConfig) {
		c.Addr = addr
		c.Block = true
	}
}

// What the program sets in code is used when configuration says nothing.
func TestWithDefaultsAreUsedWhenConfigurationSaysNothing(t *testing.T) {
	addr, codes, _ := withDefaults(t, blockOn("127.0.0.1:0"))

	if len(codes) != 0 {
		t.Fatalf("exit codes = %v, want none", codes)
	}

	if host, port, err := net.SplitHostPort(addr); err != nil || host != "127.0.0.1" || port == "0" {
		t.Fatalf("listening on %q (%v)", addr, err)
	}
}

// Configuration has the last word over a default, whichever layer says it.
func TestConfigurationOverridesWithDefaults(t *testing.T) {
	for name, opt := range map[string]func(addr string) []foundation.Option{
		"the environment": func(addr string) []foundation.Option {
			return []foundation.Option{foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{"SVC_HTTP_SERVER_ADDR": addr})))}
		},
		"a flag": func(addr string) []foundation.Option {
			return []foundation.Option{
				foundation.SetEnvironmentConfig(env.New(env.WithMap(nil))),
				foundation.WithArgs([]string{"--http", "--server", "--addr", addr}),
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			inCode, configured := freeAddr(t), freeAddr(t)

			addr, codes, _ := withDefaults(t, blockOn(inCode), opt(configured)...)

			if len(codes) != 0 || addr != configured {
				t.Fatalf("listening on %q, want %q; exit codes %v", addr, configured, codes)
			}

			if c, err := net.Dial("tcp", inCode); err == nil {
				c.Close()

				t.Fatal("the default address was listened on as well")
			}
		})
	}
}

// Help describes the server, which is what going through Server and not a
// hand made function is for, and shows nothing that was set in code.
func TestHelpDescribesTheServerWhenThereAreDefaults(t *testing.T) {
	const canary = "s3cr3t-canary.internal:9"

	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	var out bytes.Buffer

	foundation.Run("svc", func(f *foundation.F) {
		transporthttp.Server(f, hello, transporthttp.WithDefaults(func(c *transporthttp.ServerConfig) { c.Addr = canary }))
	}, foundation.WithArgs([]string{"--help"}), foundation.WithHelpOutput(&out))

	if codes := exit.Codes(); len(codes) != 0 {
		t.Fatalf("exit codes = %v, want none", codes)
	}

	if !strings.Contains(out.String(), "Serves the application over HTTP") {
		t.Fatalf("help does not describe the server:\n%s", out.String())
	}

	if strings.Contains(out.String(), canary) {
		t.Fatalf("help shows a default:\n%s", out.String())
	}
}

// A nil function changes nothing.
func TestWithDefaultsNilIsSkipped(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	foundation.Run("svc", func(f *foundation.F) {
		transporthttp.Server(f, hello, transporthttp.WithDefaults(nil))

		f.Shutdown("done")
	}, foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{"SVC_HTTP_SERVER_ADDR": "127.0.0.1:0"}))))

	if codes := exit.Codes(); len(codes) != 0 {
		t.Fatalf("exit codes = %v, want none", codes)
	}
}

// The function runs in help and in a dry run as well, so it has to be free of
// side effects: they need the same shape of configuration.
func TestWithDefaultsRunsInHelpAndInADryRun(t *testing.T) {
	for name, opt := range map[string]foundation.Option{
		"help":    foundation.WithArgs([]string{"--help"}),
		"dry run": foundation.WithDryRun(true),
	} {
		t.Run(name, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			var calls int

			foundation.Run("svc", func(f *foundation.F) {
				transporthttp.Server(f, hello, transporthttp.WithDefaults(func(c *transporthttp.ServerConfig) {
					calls++

					c.Addr = "127.0.0.1:0"
				}))
			}, opt, foundation.WithHelpOutput(&bytes.Buffer{}))

			if calls != 1 {
				t.Fatalf("the function ran %d times, want 1", calls)
			}

			if codes := exit.Codes(); len(codes) != 0 {
				t.Fatalf("exit codes = %v, want none", codes)
			}
		})
	}
}

// A function that panics fails startup, with the stack of the panic and nothing
// listening, and does not end the process.
func TestWithDefaultsThatPanicsFailsStartup(t *testing.T) {
	addr, codes, recorder := withDefaults(t, func(*transporthttp.ServerConfig) { panic("boom") })

	if !slices.Equal(codes, []int{1}) || addr != "" {
		t.Fatalf("exit codes = %v, listening on %q", codes, addr)
	}

	var stack bool

	for _, e := range recorder.Entries() {
		e.Record.Attrs(func(a slog.Attr) bool {
			stack = stack || a.Key == "stack"

			return true
		})
	}

	if !stack {
		t.Fatal("the panic was not logged with its stack")
	}
}

// A listener set through the function counts as the address, as WithListener
// does, and a nil one of a pointer type is cleared and not called.
func TestWithDefaultsCanSetAListener(t *testing.T) {
	t.Run("a listener", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}

		defer ln.Close()

		addr, codes, _ := withDefaults(t, func(c *transporthttp.ServerConfig) {
			c.Listener = ln
			c.Block = true
		})

		if len(codes) != 0 || addr != ln.Addr().String() {
			t.Fatalf("listening on %q, want %q; exit codes %v", addr, ln.Addr(), codes)
		}
	})

	t.Run("a typed nil", func(t *testing.T) {
		var ln *net.TCPListener

		addr, codes, recorder := withDefaults(t, func(c *transporthttp.ServerConfig) { c.Listener = ln })

		if !slices.Equal(codes, []int{1}) || addr != "" {
			t.Fatalf("exit codes = %v, listening on %q", codes, addr)
		}

		if !mentions(errsLogged(recorder), "required") {
			t.Fatalf("no error said that the address is required: %v", errsLogged(recorder))
		}

		for _, e := range recorder.Entries() {
			e.Record.Attrs(func(a slog.Attr) bool {
				if a.Key == "stack" {
					t.Fatal("a panic was recovered")
				}

				return true
			})
		}
	})
}

// A hand built configuration with a typed nil listener is as one with none: it
// listens on its address, and does not panic when the listener is used.
func TestServerWithConfigClearsATypedNilListener(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	cfg := transporthttp.DefaultServerConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.Block = true

	var ln *net.TCPListener

	cfg.Listener = ln

	foundation.Run("svc", func(f *foundation.F) {
		f.Run("http.server", func(f *foundation.F) {
			transporthttp.ServerWithConfig(f, cfg, hello)
		})

		f.Shutdown("done")
	})

	if codes := exit.Codes(); len(codes) != 0 {
		t.Fatalf("exit codes = %v, want none", codes)
	}

	if len(entriesNamed(recorder, "serving http server")) != 1 {
		t.Fatal("it did not listen on its address")
	}
}

// A default goes through the same checks as any configuration: a mistake stops
// startup, and what was set in code is not repeated in the reason.
func TestWithDefaultsThatAreWrongFailStartupWithoutBeingEchoed(t *testing.T) {
	const canary = "s3cr3t-canary"

	for name, fn := range map[string]func(*transporthttp.ServerConfig){
		"probe path":       func(c *transporthttp.ServerConfig) { c.ProbePath = "/" + canary + " x" },
		"certificate only": func(c *transporthttp.ServerConfig) { c.CertFile = canary },
		"a bad address":    func(c *transporthttp.ServerConfig) { c.Addr = canary },
	} {
		t.Run(name, func(t *testing.T) {
			addr, codes, recorder := withDefaults(t, func(c *transporthttp.ServerConfig) {
				c.Addr = "127.0.0.1:0"
				fn(c)
			})

			if !slices.Equal(codes, []int{1}) || addr != "" {
				t.Fatalf("exit codes = %v, listening on %q", codes, addr)
			}

			errs := errsLogged(recorder)
			if len(errs) == 0 {
				t.Fatal("nothing explained the failure")
			}

			for _, e := range errs {
				if strings.Contains(e, canary) {
					t.Fatalf("an error repeats what was set: %q", e)
				}
			}
		})
	}
}
