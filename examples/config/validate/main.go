// This example shows a service that refuses to start with a configuration that
// is incomplete or doesn't make sense, and says everything that is wrong in one
// go.
//
// Two things catch this:
//
//   - `config:"required"` on a field: some value must end up in it, from a
//     provider or a default. Zero counts as not set, so use a pointer for a field
//     that may legitimately be zero.
//   - A Validate method on the struct, for rules across fields. It is called by
//     Load once the fields are in, and only if nothing was missing or
//     unparseable, so it never has to cope with a half loaded struct.
//
// The problems show in two stages: first what is missing, then, once that is
// fixed, the rules between values.
//
// Run it, then give it what it asks for: set the variables or put the values in
// the file in the code below, and run it again.
package main

import (
	"errors"
	"log/slog"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/json"
)

// Config is loaded by the "api" function below.
type Config struct {
	// Addr has no sensible default, so it must be given.
	Addr string `config:"addr,required"`

	// Port has a default, set before Load, so it is satisfied already.
	Port int `config:"port,required"`

	Timeout time.Duration `config:"timeout"`

	// Burst may be 0 on purpose, which a plain int couldn't tell from "not
	// given", so it is a pointer.
	Burst *int `config:"burst,required"`

	TLS struct {
		CertFile string `config:"cert_file,required"`
		KeyFile  string `config:"key_file"`
	} `config:"tls"`
}

// Validate holds the rules that are about the values together. Each error it
// returns is reported on its own line; a *config.FieldError names the field.
func (c *Config) Validate() error {
	var errs []error

	if c.Port < 1024 {
		errs = append(errs, &config.FieldError{Field: "Port", Err: errors.New("must be 1024 or above")})
	}

	if c.TLS.KeyFile != "" && c.TLS.CertFile == "" {
		errs = append(errs, &config.FieldError{Field: "TLS.CertFile", Err: errors.New("needed when TLS.KeyFile is set")})
	}

	if c.Timeout < 0 {
		errs = append(errs, errors.New("timeout can't be negative"))
	}

	return errors.Join(errs...)
}

func main() {
	slog.SetLogLoggerLevel(slog.LevelDebug)

	// Providers are spelled out so that the example needs no files or variables
	// set. A real service would use the default providers.
	file := json.Raw([]byte(`{
		"shop": {
			"api": {
				"port": 80,
				"tls": {"key_file": "/etc/tls/key.pem"}
			}
		}
	}`))

	vars := map[string]string{
		"SHOP_API_TIMEOUT": "5s",
	}

	foundation.Run("shop", func(f *foundation.F) {
		f.Run("api", func(f *foundation.F) {
			cfg := Config{Port: 8080}

			// Load fails this function, and so the whole process, if a required
			// field is missing or Validate objects.
			f.Load(&cfg)

			// Log the fields you need, not the whole struct: a struct that grows a
			// password or a DSN one day would be logged with it.
			f.Log().Info("configuration loaded", slog.String("addr", cfg.Addr), slog.Int("port", cfg.Port))
		})
	}, foundation.WithConfigProviders(file, env.New(env.WithMap(vars))))
}
