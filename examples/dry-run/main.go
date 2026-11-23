// This example shows a dry run: the whole tree of functions is walked so that
// configuration is loaded and checked, but nothing is started.
//
//	go run ./examples/dry-run                          # starts the service (Ctrl-C to stop)
//	go run ./examples/dry-run --dry-run                # checks the configuration and exits
//	go run ./examples/dry-run --dry-run --api --workers 8
//
// Try a dry run with a mistake in the environment, which it reports without
// ever opening a port:
//
//	SHOP_API_WORKERS=many go run ./examples/dry-run --dry-run
//
// --dry-run is a flag of the root function, which the framework defines when the
// program gives it its arguments with WithArgs; it comes first. --api says that
// the flags after it are for the "api" function, which is how a flag for one
// function can't be mistaken for another's. An argument that nothing reads, such
// as --api --worker 8, fails the run rather than being ignored.
//
// The exit status is 0 if the configuration is good and 1 if it is not, so a
// dry run is a check to make before a deployment is rolled out.
package main

import (
	"log/slog"
	"os"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/health"
	"go.krak3n.dev/foundation/interrupt"
)

// Config is what the "api" function loads.
type Config struct {
	Workers int `config:"workers" flag:"workers,short=w"`
}

func main() {
	// The arguments are given to the framework, never read from os.Args behind the
	// program's back. Among them, --dry-run is how a dry run is chosen: by the
	// program's caller, and never from the environment or configuration, where a
	// service that quietly started nothing and exited 0 would look healthy.
	foundation.Run("shop", func(f *foundation.F) {
		// The built in components skip themselves in a dry run, after loading and
		// checking their own configuration.
		interrupt.Signals(f)
		health.Server(f)

		f.Run("api", func(f *foundation.F) {
			cfg := Config{Workers: 4}

			// Loading, required fields and Validate all run in a dry run. The default
			// providers read the environment, then flags.
			f.Load(&cfg)

			// Everything above only reads and checks. Everything below starts
			// something, so a dry run stops here.
			if f.DryRun() {
				f.Skip("dry run")
			}

			f.Parallel()

			f.Log().Info("api started", slog.Int("workers", cfg.Workers))

			<-f.Context().Done()
		})
	}, foundation.WithArgs(os.Args[1:]))
}
