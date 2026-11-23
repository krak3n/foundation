// This example shows strict mode. By default a misspelt setting is silently
// ignored: the service starts, and runs on the default, and nobody finds out
// until it matters. With foundation.WithStrictConfig, a value that nothing asked
// for is a failure, whether it was in a JSON document or in the environment.
//
// It also shows foundation.WithStrictIgnore, for keys that are not typos: a
// "_comment" in the document, and a variable that belongs to another program.
//
// Try it, then fix the two typos and run it again.
package main

import (
	"log/slog"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/json"
)

// Config is loaded by the "api" function below.
type Config struct {
	Addr    string        `config:"addr"`
	Workers int           `config:"workers"`
	Timeout time.Duration `config:"timeout"`
}

func main() {
	slog.SetLogLoggerLevel(slog.LevelDebug)

	// A real service would use the default providers, which read the process environment and any file
	// named with WithDocumentConfig. They are spelled out here so that the example
	// needs no files or variables set.
	file := json.Raw([]byte(`{
		"shop": {
			"api": {
				"_comment": "JSON has no comments, so this is how one is written",
				"addr": ":8080",
				"timeuot": "5s"
			}
		}
	}`))

	vars := map[string]string{
		"SHOP_API_WORKERS": "4",
		"SHOP_API_WORKRS":  "8", // a typo: nothing reads this, so Workers stays 4
		"SHOP_LEGACY_MODE": "1", // read by another program that shares the prefix
	}

	foundation.Run("shop", func(f *foundation.F) {
		f.Run("api", func(f *foundation.F) {
			cfg := Config{Timeout: 30 * time.Second}

			// Everything must be loaded before the root function returns, which is
			// when strict mode checks. The built in components load before they call
			// f.Parallel() for the same reason.
			f.Load(&cfg)

			// Log the fields you need, not the whole struct: a struct that grows a
			// password or a DSN one day would be logged with it.
			f.Log().Info("configuration loaded", slog.String("addr", cfg.Addr), slog.Int("workers", cfg.Workers))
		})
	},
		foundation.WithConfigProviders(file, env.New(env.WithMap(vars))),

		// Fail if anything in the document or the environment, under "shop",
		// was never asked for.
		foundation.WithStrictConfig(),

		// ...except what is listed here. The patterns are part of the program, not
		// of its configuration, "*" stands for any run of characters, and the
		// typos above are still reported. What was ignored is logged.
		foundation.WithStrictIgnore("shop.*._comment", "SHOP_LEGACY_*"),
	)
}
