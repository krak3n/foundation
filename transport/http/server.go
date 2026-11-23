package http

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	pathpkg "path"
	"strings"
	"time"
	"unicode"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/health/monitor"
	"go.krak3n.dev/foundation/internal/nilcheck"
)

// ServerConfig holds the configuration for the HTTP server, including listen address,
// timeouts, TLS settings, and custom listeners.
// It is used to configure the server's behavior when creating a new server instance.
type ServerConfig struct {
	// Address to listen on, as host:port, for example "127.0.0.1:8080" for a
	// service that is reachable only from its own machine, ":8080" for every
	// interface, or "127.0.0.1:0" for a random port (the system chooses it when
	// the server listens, so another process can't take it in between). There is
	// no default: a server that listens somewhere nobody chose is not one a
	// mistake should be able to leave behind, so it is required, and a dry run
	// finds it missing. A [WithListener] stands in for it.
	Addr string `config:"addr,required" flag:"addr,short=a" help:"Address to listen on, as host:port."`

	// optional custom listener (not configurable via providers); [WithListener]
	// sets it, and Addr, so that the address, which is required, counts as given
	Listener net.Listener `json:"-"`

	// http.Server.ReadHeaderTimeout: how long a client has to send the request
	// headers, which stops one that sends them slowly holding a connection open.
	// Default 10s; 0 means no limit.
	ReadHeaderTimeout time.Duration `config:"read_header_timeout" flag:"read-header-timeout" help:"Time a client has to send request headers; 0 means no limit."`

	// http.Server.ReadTimeout, which includes the body. No limit by default, as
	// uploads can be large.
	ReadTimeout time.Duration `config:"read_timeout" flag:"read-timeout" help:"Time allowed to read a whole request, body included."`

	// http.Server.WriteTimeout. No limit by default, as responses can be streamed.
	WriteTimeout time.Duration `config:"write_timeout" flag:"write-timeout" help:"Time allowed to write a response."`

	// http.Server.IdleTimeout: how long a keep-alive connection waits for its next
	// request. Default 120s; 0 means no limit.
	IdleTimeout time.Duration `config:"idle_timeout" flag:"idle-timeout" help:"Time a keep-alive connection waits for its next request; 0 means no limit."`

	// Time allowed for open connections to finish, after which the rest are
	// closed. 0 means what is left of the function's timeout (f.Timeout()) after
	// ShutdownDelay and a margin, so that the server always closes its connections
	// before the framework gives up on it. A time that doesn't fit raises the
	// function's timeout.
	ShutdownTimeout time.Duration `config:"shutdown_timeout" flag:"shutdown-timeout" help:"Time allowed for connections to finish when stopping."`

	// Delay before starting shutdown
	ShutdownDelay time.Duration `config:"shutdown_delay" flag:"shutdown-delay" help:"Time to wait before starting to stop."`

	// TLS certificate file
	CertFile string `config:"tls_cert_file" flag:"tls-cert-file" help:"Path of the TLS certificate file; needs a key file too."`

	// TLS key file
	KeyFile string `config:"tls_key_file" flag:"tls-key-file" help:"Path of the TLS key file; needs a certificate file too."`

	// Block until the server is ready serve.
	Block bool `config:"block" flag:"block" help:"Wait until the server is accepting connections before going on."`

	// timeout for blocking until the server is ready, default is 15s
	BlockTimeout time.Duration `config:"block_timeout" flag:"block-timeout" help:"Time allowed to wait for the server to be ready."`

	// path for the health probe, default is "/_probe"
	ProbePath string `config:"probe_path" flag:"probe-path" help:"Path of the health probe."`
}

// A ServerOption configures what the HTTP server's configuration can't say: a
// listener ([WithListener]) and the program's own defaults ([WithDefaults]).
// Everything else (the address, the timeouts, TLS, blocking, the probe path) is
// configuration, set through any provider.
type ServerOption interface {
	applyServerConfig(*ServerConfig)
}

// WithListener provides a custom [net.Listener] for the server, for socket
// activation or a test that needs to know its port. The server uses it instead
// of creating its own, so no address is needed, and an address in configuration
// does not start a second listener. It is applied before configuration is loaded
// and stands in for the address, which keeps the address a required setting. An
// address that configuration sets as well is not used, and a warning says so,
// but one that configuration sets to an empty string is a mistake and still
// fails startup as a missing required setting. A nil listener, a typed nil too,
// is no listener.
func WithListener(ln net.Listener) ServerOption {
	return serverConfigFunc(func(c *ServerConfig) {
		if nilcheck.Is(ln) {
			return
		}

		c.Listener = ln
		c.Addr = listenerAddr(ln)
	})
}

// WithDefaults changes the server's defaults, which start as
// [DefaultServerConfig], before its configuration is loaded: whatever a provider
// says wins over what fn sets, so a deployment can still change it, and a
// program that must pin a value uses [ServerWithConfig], which loads nothing.
// It is how a program gives the server a default address, or one that blocks
// until it is ready, with no function of its own to write.
//
// fn also runs when help is asked for and in a dry run, which need the same
// shape of configuration, so it should only set fields. Defaults are checked as
// configuration is, and are never shown by help. A nil fn is ignored. A listener
// set in fn counts as the address, as with [WithListener].
func WithDefaults(fn func(*ServerConfig)) ServerOption {
	return serverConfigFunc(func(c *ServerConfig) {
		if fn != nil {
			fn(c)
		}
	})
}

// listenerAddr is the text of a listener's address, for it to count as the
// address: a listener whose address has no text still says where the server is.
func listenerAddr(ln net.Listener) string {
	if addr := ln.Addr().String(); addr != "" {
		return addr
	}

	return "listener"
}

// settleListener makes what the options did consistent: a nil listener, a typed
// nil too, is none (calling a method on it may panic), and a listener with no
// address stands in for it.
func settleListener(c *ServerConfig) {
	if nilcheck.Is(c.Listener) {
		c.Listener = nil

		return
	}

	if c.Addr == "" {
		c.Addr = listenerAddr(c.Listener)
	}
}

// DefaultServerConfig is the configuration before anything is loaded or set.
//
// The timeouts that protect against slow clients have defaults, and zero turns
// each off. ReadTimeout and WriteTimeout are left unlimited, as a limit that is
// right for a small API is wrong for uploads and streaming; set them if yours
// doesn't have those.
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		BlockTimeout:      15 * time.Second,
		ProbePath:         "/_probe",
	}
}

// newServer builds the [http.Server] for handler, with the probe injected.
func newServer(cfg ServerConfig, handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           InjectProbe(handler, cfg.ProbePath),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
}

// serverHelp is what --help says about the server. It is fixed text: it names no
// default, address or path, as help never shows values.
const serverHelp = "Serves the application over HTTP, or HTTPS when a certificate and key are given.\n\n" +
	"It starts listening once its configuration has been checked, and registers a probe so that the health server can report on it. " +
	"On shutdown it lets open connections finish within the shutdown timeout, then closes the rest."

// ServerFunc returns a [foundation.Func] that manages the full lifecycle of an
// HTTP server: listen, serve, and graceful shutdown.
//
// It runs a child function called "http.server", which loads the server's
// configuration (the options given here are applied before it, as defaults, so
// configuration wins over [WithDefaults]) and then runs [ServerWithConfig] in it.
func ServerFunc(handler http.Handler, opts ...ServerOption) foundation.Func {
	return func(f *foundation.F) {
		f.Run("http.server", func(f *foundation.F) {
			f.Help(serverHelp)

			cfg := DefaultServerConfig()

			serverOptions(opts).applyServerConfig(&cfg)
			settleListener(&cfg)

			f.Load(&cfg)

			ServerWithConfig(f, cfg, handler)
		})
	}
}

// ServerWithConfig runs the HTTP server in f, with a configuration that has
// already been loaded, and loads nothing itself. It is for a component that
// embeds the server's settings in its own configuration, such as the health
// server, where the settings sit under its own flags and variables: start from
// [DefaultServerConfig], change what the component needs, load, and pass the
// result. [ServerFunc] does the same under a function called "http.server".
//
// The configuration is checked as for any server, and a mistake fails f before
// anything listens, without repeating its values. It calls
// [foundation.F.Parallel] to release the parent (after the server is ready when
// cfg.Block is set), so it must be called from a function that is free to, and
// f.Skip is used in a dry run once the checks are done.
func ServerWithConfig(f *foundation.F, cfg ServerConfig, handler http.Handler) {
	// A typed nil listener is none, not something to call a method on.
	if nilcheck.Is(cfg.Listener) {
		cfg.Listener = nil
	}

	// Mistakes in the configuration fail the server here, before it listens,
	// rather than leaving it running as something else.
	if err := validateProbePath(cfg.ProbePath); err != nil {
		f.Fail(err)
	}

	shutdownTimeout := shutdownBudget(f, cfg)

	srv := newServer(cfg, handler)

	// TLS needs a certificate and its key. Having only one of them is a
	// mistake, such as a misspelt variable, and must not leave the server
	// quietly serving plain HTTP. A certificate that can't be loaded fails
	// here too. The one loaded is both served and probed, so a file
	// changing in between can't make them differ.
	var pin *x509.Certificate

	if cfg.CertFile != "" || cfg.KeyFile != "" {
		switch {
		case cfg.CertFile == "":
			f.Fail(errors.New("TLS needs both a certificate file and a key file, and the certificate file is not set"))
		case cfg.KeyFile == "":
			f.Fail(errors.New("TLS needs both a certificate file and a key file, and the key file is not set"))
		}

		pair, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			f.Fail(fmt.Errorf("loading TLS certificate: %w", err))
		}

		pin, err = x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			f.Fail(fmt.Errorf("loading TLS certificate: %w", err))
		}

		srv.TLSConfig = &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{pair},
		}
	}

	// A listener is what the server uses, so an address that configuration gave
	// as well is not. Say so, with no value: the address is configuration. The
	// listener's own address, which WithListener presets, is not that.
	if cfg.Listener != nil && cfg.Addr != "" && cfg.Addr != listenerAddr(cfg.Listener) {
		f.Log().Warn("a listener was given, so the configured address is not used")
	}

	// Everything that can be wrong with the configuration has been checked.
	// A dry run stops here, before anything listens.
	if f.DryRun() {
		f.Skip("dry run")
	}

	// With nothing to listen on there is nothing to start. Loading finds this for
	// a server that loads its own configuration (the address is required), so this
	// is for one built by hand and given to [ServerWithConfig]. It is after the
	// dry run's skip because help reads no configuration, so its address is always
	// empty.
	if cfg.Listener == nil && cfg.Addr == "" {
		f.Fail(errors.New("the server has no address to listen on: set its address (or a listener), as there is no default"))
	}

	// If the server is not configured to block until ready,
	// call [foundation.F.Parallel] to allow the parent to proceed immediately.
	if !cfg.Block {
		f.Parallel()
	}

	// If a listener was not provided, create one using the configured address.
	// This allows for flexibility in how the server is set up,
	// while still providing a default behavior that works out of the box.
	if cfg.Listener == nil {
		var err error

		cfg.Listener, err = net.Listen("tcp", cfg.Addr)
		if err != nil {
			f.Fail(listenError(err))
		}
	}

	// Create a health probe that checks the server's responsiveness by making an HTTP GET
	// request to the injected probe handler.
	probe := newProbe(f, probeURL(cfg.Listener.Addr(), pin != nil, cfg.ProbePath), pin, defaultProbeTimeout)

	// Register the probe before starting the server to ensure it is included in health
	// checks as soon as the server is ready.
	f.Probes().Add(probe)

	// Start the server in a child goroutine. This allows the parent to proceed immediately,
	// and the server will run until the context is cancelled.
	Serve(f, srv, cfg.Listener, "", "")

	// If the server is configured to block until ready, call the Ready function to wait for the server
	if cfg.Block {
		f.Log().Debug("waiting for server to be ready", slog.Duration("timeout", cfg.BlockTimeout))

		Ready(f, cfg.BlockTimeout, probe)

		f.Log().Debug("server is ready")

		f.Parallel() // Allow the parent to proceed after the server is ready.
	}

	<-f.Context().Done()

	if cfg.ShutdownDelay > 0 {
		f.Log().Debug("delay server shutdown", slog.Duration("delay", cfg.ShutdownDelay))

		<-time.After(cfg.ShutdownDelay)
	}

	f.Log().Info("shutting down", slog.Duration("timeout", shutdownTimeout))

	ctx, cancel := context.WithTimeout(context.WithoutCancel(f.Context()), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		f.Log().Error("shutdown error", slog.String("err", err.Error()))

		// Out of time: drop the connections still open rather than leave
		// them to outlive the server.
		if err := srv.Close(); err != nil {
			f.Log().Error("forced close error", slog.String("err", err.Error()))
		} else {
			f.Log().Warn("forced close", slog.String("reason", "graceful shutdown did not finish in time"))
		}
	} else {
		f.Log().Info("shutdown complete")
	}
}

// shutdownBudget is the time the server gives open connections to finish, which
// has to end, with the delay before it and a margin to close what is left, before
// the framework abandons the function: a client that holds a request open could
// otherwise make the function miss its timeout, which fails a restart. With no
// time set it is what is left; a time that doesn't fit raises the function's
// timeout to make room; and a delay that leaves nothing is a mistake.
func shutdownBudget(f *foundation.F, cfg ServerConfig) time.Duration {
	if cfg.ShutdownTimeout < 0 || cfg.ShutdownDelay < 0 {
		f.Fail(errors.New("the shutdown timeout and delay must not be negative"))
	}

	timeout := f.Timeout()
	margin := min(max(timeout/10, 100*time.Millisecond), 5*time.Second)

	if cfg.ShutdownTimeout == 0 {
		budget := timeout - cfg.ShutdownDelay - margin
		if budget <= 0 {
			f.Fail(errors.New("the shutdown delay leaves no time to stop within the function's timeout: raise the timeout or shorten the delay"))
		}

		return budget
	}

	if need := cfg.ShutdownDelay + cfg.ShutdownTimeout + margin; need > timeout {
		f.SetTimeout(need)
	}

	return cfg.ShutdownTimeout
}

// Server is a convenience function that runs [ServerFunc] with the provided
// handler and options. It manages the full HTTP server lifecycle: listen, serve,
// and graceful shutdown.
func Server(f *foundation.F, handler http.Handler, opts ...ServerOption) {
	ServerFunc(handler, opts...)(f)
}

// Serve starts the HTTP server in a child function of f called "serve", and
// returns at once: the child serves until the server is closed, and fails f if it
// stops for any other reason. [ServerWithConfig] is what stops it.
//
// The server uses TLS if certFile or keyFile is set, loading the pair from the
// files, or if srv.TLSConfig already has a certificate. Giving only one of the
// files is an error, not a reason to serve plain HTTP.
func Serve(f *foundation.F, srv *http.Server, ln net.Listener, certFile, keyFile string) {
	f.Run("serve", func(f *foundation.F) {
		f.Parallel()

		useTLS := certFile != "" || keyFile != "" || (srv.TLSConfig != nil && len(srv.TLSConfig.Certificates) > 0)

		f.Log().Info("serving http server", slog.String("addr", ln.Addr().String()), slog.Bool("tls", useTLS))

		var err error

		if useTLS {
			err = srv.ServeTLS(ln, certFile, keyFile)
		} else {
			err = srv.Serve(ln)
		}

		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			f.Fail(err)
		}
	})
}

// listenError is the reason that an address could not be used, without the
// address. net's errors repeat it, and it is configuration, so it must not reach
// a log: it may have been typed into the wrong field.
func listenError(err error) error {
	var (
		addrErr *net.AddrError
		dnsErr  *net.DNSError
		opErr   *net.OpError
	)

	switch {
	case errors.As(err, &addrErr):
		return fmt.Errorf("listening: %s", addrErr.Err)
	case errors.As(err, &dnsErr):
		return errors.New("listening: the host could not be found")
	case errors.As(err, &opErr) && opErr.Err != nil:
		return fmt.Errorf("listening: %w", opErr.Err)
	}

	return errors.New("listening failed")
}

// validateProbePath checks that path is somewhere a probe can be put. It is
// formatted into a [http.ServeMux] pattern, where some paths panic and others
// would take over requests that belong to the application. Its errors never
// repeat the path, which may have come from anywhere.
func validateProbePath(path string) error {
	switch {
	case path == "":
		return errors.New("probe path is empty")
	case path[0] != '/':
		return errors.New("probe path must start with /")
	case path == "/":
		return errors.New(`probe path can't be "/": it would answer every GET request, so the application would never see one`)
	}

	for _, r := range path {
		switch {
		case unicode.IsSpace(r) || unicode.IsControl(r):
			return errors.New("probe path can't contain spaces or control characters")
		case r == '{' || r == '}':
			return errors.New("probe path can't contain { or }, which are wildcards in a pattern")
		case r == '?' || r == '#':
			return errors.New("probe path can't contain a query or fragment")
		}
	}

	if strings.HasSuffix(path, "/") {
		return errors.New("probe path can't end in /, which would match everything beneath it")
	}

	if pathpkg.Clean(path) != path {
		return errors.New("probe path is not a clean path: it has //, . or .. in it")
	}

	return nil
}

// InjectProbe wraps the provided HTTP handler with a new handler that includes a
// health probe endpoint at the specified path.
//
// The path must be a valid probe path, as [ServerFunc] checks for. Like
// [http.ServeMux.Handle], InjectProbe panics on one that isn't a valid pattern.
func InjectProbe(handler http.Handler, path string) http.Handler {
	mux := http.NewServeMux()

	mux.Handle(fmt.Sprintf("GET %s", path), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	mux.Handle("/", handler)

	return mux
}

// Ready blocks until the provided health probe reports a healthy status or the timeout is reached.
//
// A probe that errors, or isn't healthy yet, is tried again on the next tick, as
// a server that is still starting up is expected to fail its first checks. If it
// is still not healthy when the timeout passes, the run fails. Ready returns
// without failing if the process starts shutting down while it waits.
func Ready(f *foundation.F, timeout time.Duration, probe monitor.Probe) {
	ctx, cancel := context.WithTimeout(f.Context(), timeout)
	defer cancel()

	ticker := time.NewTicker(time.Millisecond * 100)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// If the context is done we are either shutting down or we
			// timed out waiting for the server to be ready.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				f.Fail(fmt.Errorf("http server did not become ready in time %w", ctx.Err()), slog.String("timeout", timeout.String()))
			}

			// Normal shutdown
			return
		case <-ticker.C:
			status, err := probe.Status(ctx)
			if status == monitor.StatusHealthy && err == nil {
				return
			}

			if err != nil {
				f.Log().Debug("server is not ready yet", slog.String("err", err.Error()))
			}
		}
	}
}

// serverOptions is a slice of [ServerOption] that itself implements the
// [ServerOption] interface. Each non-nil option in the slice is applied
// in order.
type serverOptions []ServerOption

func (o serverOptions) applyServerConfig(c *ServerConfig) {
	for _, opt := range o {
		if opt != nil {
			opt.applyServerConfig(c)
		}
	}
}

// serverConfigFunc is an adapter to allow the use of ordinary functions as
// [ServerOption] values. If f is a function with the appropriate signature,
// serverConfigFunc(f) is a [ServerOption] that calls f.
type serverConfigFunc func(*ServerConfig)

func (f serverConfigFunc) applyServerConfig(c *ServerConfig) {
	f(c)
}
