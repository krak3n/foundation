// This example shows what a service with several mistakes in its configuration
// looks like when it starts. Every provider reports every problem it finds, so
// all the mistakes show up in a single run, each as its own log line naming the
// provider, the field and the key to fix, and the process then exits with
// status 1.
//
// Try it, then correct a value or two and run it again.
package main

import (
	"log/slog"
	"net/url"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/json"
)

// Config is loaded by the "api" function below. Each field has a tag for each
// provider that can supply it.
type Config struct {
	Port    uint16        `config:"port"`
	Timeout time.Duration `config:"timeout"`
	Home    url.URL       `config:"home"`
	Workers int           `config:"workers"`

	TLS struct {
		CertFile string `config:"cert_file"`
		Port     int    `config:"port"`
	} `config:"tls"`
}

func main() {
	slog.SetLogLoggerLevel(slog.LevelDebug)

	// Two sources of configuration. A real service would use the default
	// providers, which read the process environment and any file named with
	// WithDocumentConfig; they are spelled out here so that the example needs no
	// files or variables set.
	//
	// The names follow the path of the function loading the config: "shop" then
	// "api", so SHOP_API_PORT in the environment and shop > api > port in JSON.
	file := json.Raw([]byte(`{
		"shop": {
			"api": {
				"port": 8080,
				"home": "http://[::1",
				"tls": {"cert_file": "/etc/tls/cert.pem", "port": "https"}
			}
		}
	}`))

	vars := map[string]string{
		"SHOP_API_PORT":    "70000", // too big for a port (uint16); it would override the file's 8080
		"SHOP_API_TIMEOUT": "soon",  // not a duration
		"SHOP_API_WORKERS": "4",     // fine
	}

	lookup := func(key string) (string, bool) {
		v, ok := vars[key]

		return v, ok
	}

	foundation.Run("shop", func(f *foundation.F) {
		f.Run("api", func(f *foundation.F) {
			cfg := Config{Workers: 1}

			// Load fails this function, and so the whole process, if there is
			// any problem: nothing after it runs.
			f.Load(&cfg)

			// Log the fields you need, not the whole struct: a struct that grows a
			// password or a DSN one day would be logged with it.
			f.Log().Info("configuration loaded", slog.Int("port", int(cfg.Port)), slog.Int("workers", cfg.Workers))
		})
	}, foundation.WithConfigProviders(file, env.New(env.WithLookup(lookup))))
}
