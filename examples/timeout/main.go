package main

import (
	"log/slog"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/interrupt"
)

func main() {
	slog.SetLogLoggerLevel(slog.LevelDebug)

	foundation.Run("timeout", func(f *foundation.F) {
		// Ctrl-C (SIGINT) or SIGTERM starts the shutdown, which then waits for each
		// function in turn, up to its timeout; see examples/interrupt.
		interrupt.Signals(f)

		f.Run("forever1", func(f *foundation.F) {
			f.Parallel()
			f.SetTimeout(10 * time.Second)

			ticker := time.NewTicker(time.Second)
			f.Cleanup(ticker.Stop)

			for tick := range ticker.C {
				f.Log().Debug("tick", slog.Time("time", tick))
			}
		})

		f.Run("forever2", func(f *foundation.F) {
			f.Parallel()
			f.SetTimeout(5 * time.Second)

			ticker := time.NewTicker(time.Second)
			f.Cleanup(ticker.Stop)

			for tick := range ticker.C {
				f.Log().Debug("tick", slog.Time("time", tick))
			}
		})
	})
}
