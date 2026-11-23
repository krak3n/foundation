package main

import (
	"fmt"
	"time"

	"go.krak3n.dev/foundation"
)

func main() {
	foundation.Run("shutdown", func(f *foundation.F) {
		f.Cleanup(func() {
			fmt.Println("cleanup shutdown")
		})

		f.Run("blocking", func(f *foundation.F) {
			f.Cleanup(func() {
				fmt.Println("cleanup blocking")
			})

			ticker := time.NewTicker(time.Second)
			f.Cleanup(func() {
				ticker.Stop()
			})

			var counter int

			for {
				select {
				case <-f.Context().Done():
					return
				case <-ticker.C:
					counter++

					fmt.Println("tick", counter)

					if counter == 5 {
						f.Shutdown("reached 5 ticks")
					}
				}
			}
		})

		f.Run("after shutdown", func(f *foundation.F) {
			// Never run as Shutdown is called in the blocking func above.
			fmt.Println("after shutdown")
		})
	})
}
