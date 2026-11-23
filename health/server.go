package health

import (
	"errors"
	"fmt"
	"slices"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/health/monitor"
	"go.krak3n.dev/foundation/transport/http"
)

// DefaultPort is the port the health server listens on unless its configuration
// says otherwise, on every interface, as an orchestrator probes the pod address.
const DefaultPort = 3417

// ServerConfig holds the configuration for the health server: its own setting
// and, embedded, all of an HTTP server's, which are therefore its own flags,
// variables and keys too (--health --addr, SVC_HEALTH_ADDR, health.addr). See
// [go.krak3n.dev/foundation/transport/http.ServerConfig] for them.
type ServerConfig struct {
	http.ServerConfig

	// Verbose lets a client ask for the detail of each probe with ?verbose. See
	// [WithVerbose].
	Verbose bool `config:"verbose" flag:"verbose" help:"Let clients ask for probe detail with ?verbose; this shows internals to anyone who can reach the port."`

	// Registry is the monitor registry for the server (not configurable via providers).
	Registry Registry `json:"-"`

	// HandlerOptions are options for the health handler (not configurable via providers).
	HandlerOptions []HandlerOption `json:"-"`
}

// A ServerOption configures a [ServerConfig].
type ServerOption interface {
	applyServerConfig(*ServerConfig)
}

// WithRegistry sets the monitor registry for the server.
// The default uses the probes registry from the [*foundation.F], which is suitable for most applications.
// This option is useful for tests that need to use a custom registry.
//
// The registry is the program's: the framework only reads it, and never clears it.
// A program that restarts should make it in the root function, so that each
// instance has its own and none holds a probe of one that has gone.
func WithRegistry(r monitor.Registrar) ServerOption {
	return serverConfigFunc(func(c *ServerConfig) {
		c.Registry = r
	})
}

// defaultServerConfig is the configuration before anything is loaded or set: an
// HTTP server's defaults, on port 3417, that blocks until it is ready, as a
// service waits for its health server before it goes on. Configuration can
// change all of it, and an empty address is refused (the address is required),
// not taken to mean the default.
func defaultServerConfig() ServerConfig {
	cfg := ServerConfig{ServerConfig: http.DefaultServerConfig()}
	cfg.Addr = fmt.Sprintf(":%d", DefaultPort)
	cfg.Block = true

	return cfg
}

// serverHelp is what --help says about the server. It is fixed text: it names no
// default, address or path, as help never shows values.
const serverHelp = "Serves the health endpoints that an orchestrator probes: startup, readiness and liveness.\n\n" +
	"Responses carry only a verdict. Detail about each probe is shown only with --verbose, which gives it to anyone who can reach the port."

// Server starts a health server using the provided [*foundation.F] and options.
func Server(f *foundation.F, opts ...ServerOption) {
	f.Run("health", func(f *foundation.F) {
		f.Help(serverHelp)

		cfg := defaultServerConfig()
		cfg.Registry = f.Probes()

		f.Load(&cfg)

		serverOptions(opts).applyServerConfig(&cfg)

		handlerOptions := slices.Clone(cfg.HandlerOptions)
		if cfg.Verbose {
			handlerOptions = append(handlerOptions, WithVerbose())
		}

		// The server puts its own probe at ProbePath. It must not be where an
		// endpoint of this server is, which it would hide. The path is not in the
		// error, as it is configuration.
		hc := newHandlerConfig(handlerOptions)

		if slices.Contains([]string{hc.healthPath, hc.startupPath, hc.readyPath, hc.livePath}, cfg.ProbePath) {
			f.Fail(errors.New("probe path is the path of an endpoint of the health server, which it would hide"))
		}

		// The server runs in this function and calls f.Parallel once it is ready (or
		// at once, if configuration says not to wait), which unblocks the parent.
		http.ServerWithConfig(f, cfg.ServerConfig, Handler(cfg.Registry, handlerOptions...))
	})
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
