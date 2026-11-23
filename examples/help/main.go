// This example shows help: what a program says about itself when it is run with
// --help, built from the functions that make it up.
//
//	go run ./examples/help --help               # the whole program
//	go run ./examples/help --api --help         # one function, with its full text
//	go run ./examples/help --http --help        # the functions under http
//	go run ./examples/help --api --workers 8    # starts the service (Ctrl-C to stop)
//
// Help is a dry run that describes instead of reading: every function loads, so
// every function says what it has, but nothing is read from the environment or a
// file, nothing is checked, and nothing starts, because a function that would
// start something skips (see examples/dry-run). So it works when the
// configuration is broken or missing, and it never shows a default or a value.
//
// A function says what it is for with f.Help, and a field with the help tag.
// Only fields with a flag tag are listed, so what appears is chosen by the
// program, and each one shows where else it can be set. --help can be given
// anywhere; after a function's marker it is help for that function.
package main

import (
	"log/slog"
	"os"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/interrupt"
)

// APIConfig is what the "api" function loads.
type APIConfig struct {
	Workers int `config:"workers" flag:"workers,short=w" help:"How many requests are handled at once."`
}

// ServerConfig is what "http.server" loads.
type ServerConfig struct {
	Port int `config:"port" flag:"port,short=p" help:"The port to listen on."`
}

func main() {
	foundation.Run("shop", func(f *foundation.F) {
		f.Help("The shop service.\n\nIt serves the API and the storefront.")

		interrupt.Signals(f)

		f.Run("api", func(f *foundation.F) {
			f.Help("Runs the API.\nRequests are handled by a pool of workers.")

			cfg := APIConfig{Workers: 4}

			f.Load(&cfg)

			if f.DryRun() {
				f.Skip("dry run")
			}

			f.Parallel()

			f.Log().Info("api started", slog.Int("workers", cfg.Workers))

			<-f.Context().Done()
		})

		// A name with a dot is two words on the command line: --http --server.
		f.Run("http.server", func(f *foundation.F) {
			f.Help("Serves the storefront.")

			cfg := ServerConfig{Port: 8080}

			f.Load(&cfg)

			if f.DryRun() {
				f.Skip("dry run")
			}

			f.Parallel()

			f.Log().Info("storefront started", slog.Int("port", cfg.Port))

			<-f.Context().Done()
		})
	}, foundation.WithArgs(os.Args[1:]))
}
