package main

import (
	"errors"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/interrupt"
)

func main() {
	foundation.Run("fail", func(f *foundation.F) {
		// Listen for interrupt signals and gracefully shut down the process when any of those signals are received.
		interrupt.Signals(f)

		f.Run("ticker1", func(f *foundation.F) {
			f.Parallel()

			tickker := time.NewTicker(time.Second)
			f.Cleanup(tickker.Stop)

			var counter int

			for {
				select {
				case <-f.Context().Done():
					return
				case <-tickker.C:
					counter++

					f.Log().Info("tick")

					if counter == 5 {
						f.Fail(errors.New("reached 5 ticks"))
					}
				}
			}
		})

		f.Run("ticker2", func(f *foundation.F) {
			f.Parallel()

			tickker := time.NewTicker(time.Millisecond * 500)
			f.Cleanup(tickker.Stop)

			for {
				select {
				case <-f.Context().Done():
					return
				case <-tickker.C:
					f.Log().Info("tick")
				}
			}
		})
	})
}
