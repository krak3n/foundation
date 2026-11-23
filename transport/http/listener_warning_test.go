package http_test

import (
	"bytes"
	"log/slog"
	"net"
	"strings"
	"testing"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/foundationtest"
	transporthttp "go.krak3n.dev/foundation/transport/http"
)

// ignoredAddress is what is logged when an address in configuration is not used
// because a listener was given. It is fixed text, with no attributes: the address
// is configuration, and is never repeated.
const ignoredAddress = "a listener was given, so the configured address is not used"

// ignoredAddressWarnings is the entries of that warning.
func ignoredAddressWarnings(r *foundationtest.LogRecorder) []foundationtest.LogEntry {
	return entriesNamed(r, ignoredAddress)
}

func listenOn(t *testing.T) net.Listener {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { ln.Close() })

	return ln
}

// An address in configuration that a listener makes pointless is said so, once,
// whether it is a real run or a dry run, and the listener is what is served on.
func TestAConfiguredAddressBesideAListenerIsWarnedAbout(t *testing.T) {
	const canary = "s3cr3t-canary.internal:1"

	for name, opt := range map[string]foundation.Option{
		"a real run": foundation.OptionFunc(func(*foundation.F) {}),
		"a dry run":  foundation.WithDryRun(true),
	} {
		t.Run(name, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			recorder := foundationtest.NewLogRecorder()
			recorder.Install(t)

			ln := listenOn(t)

			var body string

			foundation.Run("svc", func(f *foundation.F) {
				transporthttp.Server(f, hello, transporthttp.WithListener(ln))

				if !f.DryRun() {
					body = tryGet(ln.Addr().String())
				}

				f.Shutdown("done")
			}, opt, foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{
				"SVC_HTTP_SERVER_ADDR":  canary,
				"SVC_HTTP_SERVER_BLOCK": "true",
			}))))

			if codes := exit.Codes(); len(codes) != 0 {
				t.Fatalf("exit codes = %v, want none", codes)
			}

			if name == "a real run" && body != "hello" {
				t.Fatalf("body = %q: the listener was not served on", body)
			}

			got := ignoredAddressWarnings(recorder)
			if len(got) != 1 {
				t.Fatalf("%d warnings, want 1", len(got))
			}

			if got[0].Record.Level != slog.LevelWarn {
				t.Fatalf("level = %v, want warn", got[0].Record.Level)
			}

			if got[0].Record.NumAttrs() != 0 {
				t.Fatal("the warning has attributes of its own: it would repeat configuration")
			}

			for _, e := range recorder.Entries() {
				if strings.Contains(e.Record.Message, canary) {
					t.Fatalf("the address is in a message: %q", e.Record.Message)
				}

				e.Record.Attrs(func(a slog.Attr) bool {
					if strings.Contains(a.Value.String(), canary) {
						t.Fatalf("the address is in %s=%q", a.Key, a.Value)
					}

					return true
				})
			}
		})
	}
}

// Nothing is warned about when nothing was ignored: no address in configuration,
// or one that is the listener's own.
func TestNoWarningWhenTheAddressIsNotIgnored(t *testing.T) {
	for name, vars := range map[string]func(ln net.Listener) map[string]string{
		"no address": func(net.Listener) map[string]string { return nil },
		"the listener's own": func(ln net.Listener) map[string]string {
			return map[string]string{"SVC_HTTP_SERVER_ADDR": ln.Addr().String()}
		},
	} {
		t.Run(name, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			recorder := foundationtest.NewLogRecorder()
			recorder.Install(t)

			ln := listenOn(t)

			foundation.Run("svc", func(f *foundation.F) {
				transporthttp.Server(f, hello, transporthttp.WithListener(ln))

				f.Shutdown("done")
			}, foundation.WithConfigProviders(env.New(env.WithMap(vars(ln)))))

			if codes := exit.Codes(); len(codes) != 0 {
				t.Fatalf("exit codes = %v, want none", codes)
			}

			if got := ignoredAddressWarnings(recorder); len(got) != 0 {
				t.Fatalf("%d warnings, want none", len(got))
			}
		})
	}
}

// A configuration built by hand is checked in the same place: both an address and
// a listener is a warning if they differ, and not if there is no address.
func TestServerWithConfigWarnsAboutAnAddressBesideAListener(t *testing.T) {
	tests := map[string]struct {
		addr func(ln net.Listener) string
		warn bool
	}{
		"a different address": {func(net.Listener) string { return "canary.internal:1" }, true},
		"its own address":     {func(ln net.Listener) string { return ln.Addr().String() }, false},
		"no address":          {func(net.Listener) string { return "" }, false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			exit := foundationtest.NewExitRecorder()
			exit.Install(t)

			recorder := foundationtest.NewLogRecorder()
			recorder.Install(t)

			ln := listenOn(t)

			cfg := transporthttp.DefaultServerConfig()
			cfg.Listener = ln
			cfg.Addr = tt.addr(ln)
			cfg.Block = true

			foundation.Run("svc", func(f *foundation.F) {
				f.Run("http.server", func(f *foundation.F) {
					transporthttp.ServerWithConfig(f, cfg, hello)
				})

				f.Shutdown("done")
			})

			if codes := exit.Codes(); len(codes) != 0 {
				t.Fatalf("exit codes = %v, want none", codes)
			}

			if got := len(ignoredAddressWarnings(recorder)); (got == 1) != tt.warn || got > 1 {
				t.Fatalf("%d warnings, want warning = %v", got, tt.warn)
			}
		})
	}
}

// Help reads no configuration, so it has no address to say was ignored.
func TestHelpDoesNotWarnAboutAnIgnoredAddress(t *testing.T) {
	exit := foundationtest.NewExitRecorder()
	exit.Install(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	ln := listenOn(t)

	foundation.Run("svc", func(f *foundation.F) {
		transporthttp.Server(f, hello, transporthttp.WithListener(ln))
	}, foundation.WithArgs([]string{"--help"}), foundation.WithHelpOutput(&bytes.Buffer{}),
		foundation.SetEnvironmentConfig(env.New(env.WithMap(map[string]string{"SVC_HTTP_SERVER_ADDR": "canary.internal:1"}))))

	if codes := exit.Codes(); len(codes) != 0 {
		t.Fatalf("exit codes = %v, want none", codes)
	}

	if got := ignoredAddressWarnings(recorder); len(got) != 0 {
		t.Fatalf("%d warnings, want none", len(got))
	}
}
