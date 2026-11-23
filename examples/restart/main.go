// This example restarts itself when its configuration file changes, or when it is
// sent SIGHUP.
//
//	cd examples/restart
//	go run .
//
// Then, from another terminal, change a value in config.json (workers, say) and
// save: the program notices, tries the new configuration as a candidate (a dry
// run, beside the running program, which is left alone), and if it passes stops
// and starts again with it. Break the file (workers: "many") and the candidate is
// rejected: an error says so and the program carries on as it was. Fix it, and the
// fix is applied. Or send the program SIGHUP, which is the habit of daemons:
//
//	kill -HUP <pid>
//
// Ctrl-C shuts it down as usual. SIGHUP is a restart because the program says so,
// with interrupt.WithRestartSignals; it is not a default (see examples/interrupt).
//
// f.Watch starts a watcher for a trigger. A Restart event is f.Restart and a
// Shutdown event is f.Shutdown; the triggers come from package watch, which says
// when something has changed and nothing else. They are chosen here, in code:
// nothing in the environment or the file can make the program restart. How fast it
// restarts is the restart policy (foundation.WithRestartPolicy, defaults: a
// quarter of a second of quiet, at most five a minute).
//
// A restart runs the root function again, so the program must be safe to run more
// than once: anything it starts must be stopped through its context, and anything
// that starts something skips when f.DryRun() is true, as the candidate is a dry
// run (see examples/dry-run).
package main

import (
	"log/slog"
	"syscall"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/interrupt"
	"go.krak3n.dev/foundation/watch"
)

// Config is what the "api" function loads.
type Config struct {
	Greeting string `config:"greeting"`
	Workers  int    `config:"workers"`
}

const file = "config.json"

func main() {
	foundation.Run("shop", func(f *foundation.F) {
		// A watcher is a function like any other, stopped in reverse order, so one
		// started first is stopped last: the program is watching for as long as it
		// is running.
		f.Watch(watch.Files([]string{file}))

		// SIGINT and SIGTERM shut down; SIGHUP is asked for, it is not a default.
		interrupt.Signals(f, interrupt.WithRestartSignals(syscall.SIGHUP))

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
		foundation.WithDocumentConfig(json.File(file)),
		foundation.WithStrictConfig(),
	)
}
