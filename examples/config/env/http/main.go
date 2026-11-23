package main

import (
	"log/slog"
	"os"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/health"
	"go.krak3n.dev/foundation/interrupt"
	"go.krak3n.dev/foundation/transport/http"
)

func main() {
	slog.SetLogLoggerLevel(slog.LevelDebug)

	os.Setenv("FOO_HEALTH_ADDR", "127.0.0.1:0")
	os.Setenv("FOO_HTTP_SERVER_ADDR", ":8080")

	foundation.Run("foo", func(f *foundation.F) {
		// Signal interrupt
		interrupt.Signals(f)

		// Run http server with environment config overrides
		// The address is not defaulted: here it comes from FOO_HTTP_SERVER_ADDR above.
		http.Server(f, Handler())

		// Run healthcheck server
		health.Server(f)
	})
}
