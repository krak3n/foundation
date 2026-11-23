package main

import (
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/interrupt"
)

// resource is a stand-in for something a service starts and later has to wind
// down: a database pool, a queue consumer, an API server. Draining takes a
// moment, so the order things stop in is visible in the output.
func resource(f *foundation.F, name string, drain time.Duration) {
	f.Run(name, func(f *foundation.F) {
		f.Cleanup(func() {
			f.Log().Info("closed")
		})

		f.Log().Info("started")

		// Release the parent once set up, so the next resource can start.
		f.Parallel()

		<-f.Context().Done()

		f.Log().Info("draining", "duration", drain)

		time.Sleep(drain)
	})
}

func main() {
	foundation.Run("ordered", func(f *foundation.F) {
		interrupt.Signals(f)

		// Started first, so stopped last: nothing may still be using the
		// database once it closes.
		resource(f, "database", 200*time.Millisecond)

		resource(f, "worker", 300*time.Millisecond)

		// Started last, so stopped first: the API stops taking requests before
		// the worker and database it depends on go away.
		resource(f, "api", 100*time.Millisecond)

		// Ends the demo after a couple of seconds if nothing else does. Press
		// Ctrl-C to shut down sooner; either way the output shows the same order.
		f.Run("demo", func(f *foundation.F) {
			f.Parallel()

			select {
			case <-time.After(2 * time.Second):
				f.Shutdown("demo finished")
			case <-f.Context().Done():
			}
		})
	})
}
