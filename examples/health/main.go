package main

import (
	"context"
	"errors"
	"log/slog"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/health"
	"go.krak3n.dev/foundation/health/monitor"
	"go.krak3n.dev/foundation/interrupt"
)

func main() {
	slog.SetLogLoggerLevel(slog.LevelDebug)

	foundation.Run("service", func(f *foundation.F) {
		// Listen of interrupt signals and gracefully shut down the process when any of the specified signals are received.
		interrupt.Signals(f)

		// Define probes
		probe1 := monitor.NewProbe("probe1", monitor.AllModes, monitor.StatusCheckerFunc(func(context.Context) (monitor.Status, error) {
			return monitor.StatusHealthy, nil
		}))

		// An error is not required to be returned when the status is unhealthy,
		// but it can be helpful to provide more context about the failure.
		probe2 := monitor.NewProbe("probe2", monitor.AllModes, monitor.StatusCheckerFunc(func(context.Context) (monitor.Status, error) {
			return monitor.StatusUnhealthy, errors.New("some error occured")
		}))

		// Register probes before starting the health check server.
		f.Probes().Add(probe1, probe2)

		// Start health check server last, but doesn't have too. It listens on :3417
		// unless its configuration says otherwise, for example
		// SERVICE_HEALTH_ADDR=127.0.0.1:0 for a random port on loopback.
		health.Server(f)
	})
}
