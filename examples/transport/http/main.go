package main

import (
	"log/slog"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/interrupt"
	"go.krak3n.dev/foundation/transport/http"
)

func main() {
	slog.SetLogLoggerLevel(slog.LevelDebug)

	foundation.Run("service-a", func(f *foundation.F) {
		// Add signal os interrupt handling to the process.
		/// The process will be gracefully shut down when any of the specified signals are received.
		interrupt.Signals(f)

		// Start a HTTP server with the given handler.
		// The server will be gracefully shut down when the process is shut down.
		// The settings are configuration: what WithDefaults sets is what this
		// service wants, and the environment (SERVICE_A_HTTP_SERVER_ADDR=...), a
		// file or a flag can change it. The address has no default of its own, so
		// one has to be given, here or there.
		http.Server(f, Handler(), http.WithDefaults(func(c *http.ServerConfig) {
			c.Addr = ":8080"
			c.Block = true
			c.ShutdownDelay = 15 * time.Second
		}))

		f.Run("runs-after-http-server-is-ready", func(f *foundation.F) {
			f.Log().Info("do something after the http server is ready")
		})
	})
}
