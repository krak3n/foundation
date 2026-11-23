// This example reloads when its configuration file changes, with one option on the
// provider that reads it.
//
//	cd examples/config/reload
//	go run .
//
// Then, from another terminal, change a value in config.json (workers, say) and
// save: the program notices, tries the new configuration as a candidate (a dry
// run, beside the running program, which is left alone), and if it loads stops and
// starts again with it. Break the file (workers: "many") and the candidate is
// rejected: an error says so and the program carries on as it was. Fix it, and the
// fix is applied.
//
// The option is config.WithRestartOnChange(), given to the provider in code. The
// framework starts one watcher for everything that asked, before the root function
// runs, so nothing is loaded that is not also watched: there is no f.Watch call to
// make or to forget. Nothing in the environment or the file can ask for it, and a
// provider that did not ask is not watched.
//
// Try the same with the path in a variable:
//
//	SHOP_CONFIG=config.json go run . --env
//
// Logs then say $SHOP_CONFIG, never the path, which is a configuration value.
//
// A restart runs the root function again, so the program must be safe to run more
// than once: anything it starts is stopped through its context, and anything that
// starts something skips when f.DryRun() is true, as the candidate is a dry run
// (see examples/dry-run).
package main

import (
	"log/slog"
	"os"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/interrupt"
)

// Config is what the "api" function loads.
type Config struct {
	Greeting string `config:"greeting"`
	Workers  int    `config:"workers"`
}

func main() {
	provider := json.File("config.json", config.WithRestartOnChange())

	if len(os.Args) > 1 && os.Args[1] == "--env" {
		provider = json.FileFromEnv("SHOP_CONFIG", config.WithRestartOnChange())
	}

	foundation.Run("shop", func(f *foundation.F) {
		interrupt.Signals(f)

		f.Run("api", func(f *foundation.F) {
			cfg := Config{Greeting: "hello", Workers: 1}

			// Strict mode is on, so a key in the file that nothing reads, such as a
			// typo, fails the candidate and the edit is rejected.
			f.Load(&cfg)

			if f.DryRun() {
				f.Skip("dry run")
			}

			f.Parallel()

			f.Log().Info("running",
				slog.String("greeting", cfg.Greeting),
				slog.Int("workers", cfg.Workers),
			)

			<-f.Context().Done()
		})
	},
		foundation.WithDocumentConfig(provider),
		foundation.WithStrictConfig(),
	)
}
