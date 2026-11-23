package health

import (
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"go.krak3n.dev/foundation/health/monitor"
)

// A HandlerOption configures the behavior of the health handler.
type HandlerOption interface {
	applyHandlerConfig(*handlerConfig)
}

// HandlerOptionFunc is an adapter to allow the use of ordinary functions as
// [HandlerOption] values. If f is a function with the appropriate signature,
// HandlerOptionFunc(f) is a [HandlerOption] that calls f.
type HandlerOptionFunc func(*handlerConfig)

func (f HandlerOptionFunc) applyHandlerConfig(cfg *handlerConfig) {
	f(cfg)
}

// HandlerOptions is a slice of HandlerOption that can be applied to a handler configuration.
type HandlerOptions []HandlerOption

func (opts HandlerOptions) applyHandlerConfig(cfg *handlerConfig) {
	for _, opt := range opts {
		if opt != nil {
			opt.applyHandlerConfig(cfg)
		}
	}
}

// WithHealthPath sets the path for the health endpoint. The default is "/healthz".
func WithHealthPath(p string) HandlerOption {
	return HandlerOptionFunc(func(cfg *handlerConfig) {
		cfg.healthPath = p
	})
}

// WithStartupPath sets the path for the startup endpoint. The default is "/startup".
func WithStartupPath(p string) HandlerOption {
	return HandlerOptionFunc(func(cfg *handlerConfig) {
		cfg.startupPath = p
	})
}

// WithReadyPath sets the path for the readiness endpoint. The default is "/readyz".
func WithReadyPath(p string) HandlerOption {
	return HandlerOptionFunc(func(cfg *handlerConfig) {
		cfg.readyPath = p
	})
}

// WithLivePath sets the path for the liveness endpoint. The default is "/livez".
func WithLivePath(p string) HandlerOption {
	return HandlerOptionFunc(func(cfg *handlerConfig) {
		cfg.livePath = p
	})
}

// A Registry provides access to the registered health probes.
type Registry interface {
	Seq() iter.Seq[monitor.Probe]
}

// newHandlerConfig is the configuration of a handler with the options applied to
// its defaults.
func newHandlerConfig(opts []HandlerOption) handlerConfig {
	cfg := handlerConfig{
		marshaler:   monitor.KubernetesResultsMarshaler(),
		healthPath:  "/healthz",
		startupPath: "/startup",
		readyPath:   "/readyz",
		livePath:    "/livez",
	}

	HandlerOptions(opts).applyHandlerConfig(&cfg)

	return cfg
}

// Handler returns an [http.Handler] that serves the health endpoints based on the provided registry and options.
func Handler(registry Registry, opts ...HandlerOption) http.Handler {
	mux := http.NewServeMux()

	cfg := newHandlerConfig(opts)

	// One runner for every endpoint, so a probe is run once at a time however it
	// is asked for.
	run := newRunner(cfg.probeTimeout)

	mux.HandleFunc(fmt.Sprintf("GET %s", cfg.startupPath), handlerFunc(registry, cfg.marshaler, monitor.StartupMode, run, cfg.verbose))
	mux.HandleFunc(fmt.Sprintf("GET %s", cfg.livePath), handlerFunc(registry, cfg.marshaler, monitor.LivenessMode, run, cfg.verbose))
	mux.HandleFunc(fmt.Sprintf("GET %s", cfg.readyPath), handlerFunc(registry, cfg.marshaler, monitor.ReadinessMode, run, cfg.verbose))
	mux.HandleFunc(fmt.Sprintf("GET %s", cfg.healthPath), handlerFunc(registry, cfg.marshaler, monitor.AllModes, run, cfg.verbose))

	return mux
}

// HandlerFunc returns an [http.HandlerFunc] that serves the health endpoint for the specified mode.
//
// It is healthy only if every probe for the mode says it is, with no error: a
// probe that returns an error, an unknown status, or panics is failed. Each probe
// has the default timeout, and only one run of a probe is in flight at a time;
// see [WithProbeTimeout]. The response is only the verdict unless [WithVerbose]
// is given and the client asks with ?verbose.
func HandlerFunc(registry Registry, marshaler monitor.ResultsMarshaler, mode monitor.Mode, opts ...HandlerOption) http.HandlerFunc {
	var cfg handlerConfig

	HandlerOptions(opts).applyHandlerConfig(&cfg)

	return handlerFunc(registry, marshaler, mode, newRunner(cfg.probeTimeout), cfg.verbose)
}

func handlerFunc(registry Registry, marshaler monitor.ResultsMarshaler, mode monitor.Mode, run *runner, allowVerbose bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var (
			wg  sync.WaitGroup
			mtx sync.Mutex
		)

		var results []monitor.Result

		healthy := true

		_, asked := r.URL.Query()["verbose"]

		verbose := allowVerbose && asked

		for probe := range FilterProbes(registry.Seq(), mode) {
			wg.Go(func() {
				status, err := run.check(r.Context(), probe)

				mtx.Lock()
				defer mtx.Unlock()

				if status != monitor.StatusHealthy {
					healthy = false
				}

				if verbose {
					results = append(results, monitor.Result{
						Name:   probe.Name(),
						Status: status,
						Error:  err,
					})
				}
			})
		}

		wg.Wait()

		endpoint := strings.TrimPrefix(r.URL.Path, "/")

		code := http.StatusOK
		foot := fmt.Sprintf("%s check passed\n", endpoint)

		if !healthy {
			code = http.StatusInternalServerError
			foot = fmt.Sprintf("%s check failed\n", endpoint)
		}

		// Plain text, and said to be: a probe's error text is not markup, and a
		// stale verdict is no use to anyone.
		header := w.Header()
		header.Set("Content-Type", "text/plain; charset=utf-8")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Cache-Control", "no-store")

		w.WriteHeader(code)

		if verbose {
			slices.SortFunc(results, func(a, b monitor.Result) int {
				return strings.Compare(a.Name, b.Name)
			})

			b, err := marshaler.MarshalResults(results)
			if err != nil {
				slog.Error("failed to marshal health results", slog.String("err", err.Error()))
			}

			if _, err := w.Write(b); err != nil {
				slog.Error("failed to write verbose health results", slog.String("err", err.Error()))
			}
		}

		if _, err := w.Write([]byte(foot)); err != nil {
			slog.Error("failed to write health result", slog.String("err", err.Error()))
		}
	}
}

// FilterProbes filters the provided probes based on the specified mode and returns a sequence of matching probes.
func FilterProbes(probes iter.Seq[monitor.Probe], mode monitor.Mode) iter.Seq[monitor.Probe] {
	return func(yield func(monitor.Probe) bool) {
		for probe := range probes {
			if probe.Mode()&mode == 0 {
				// doesn't match -> skip
				continue
			}

			// matches -> yield
			if !yield(probe) {
				return
			}
		}
	}
}

type handlerConfig struct {
	verbose      bool
	probeTimeout time.Duration
	marshaler    monitor.ResultsMarshaler
	healthPath   string
	startupPath  string
	readyPath    string
	livePath     string
}
