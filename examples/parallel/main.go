package main

import (
	"fmt"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/interrupt"
)

func main() {
	foundation.Run("fail", func(f *foundation.F) {
		// Ctrl-C (SIGINT) or SIGTERM shuts the whole process down gracefully; see
		// examples/interrupt for how.
		interrupt.Signals(f)

		f.Run("ticker", func(f *foundation.F) {
			tickker := time.NewTicker(time.Second)
			f.Cleanup(tickker.Stop)

			for {
				select {
				case <-f.Context().Done():
					return
				case <-tickker.C:
					fmt.Println("tick")
				}
			}
		})
	})
}
