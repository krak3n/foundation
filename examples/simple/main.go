package main

import (
	"fmt"

	"go.krak3n.dev/foundation"
)

func main() {
	foundation.Run("simple", func(f *foundation.F) {
		fmt.Println("A")

		f.Cleanup(func() {
			fmt.Println("cleanup A")
		})

		f.Run("A.A", func(f *foundation.F) {
			fmt.Println("A.A")

			f.Cleanup(func() {
				fmt.Println("cleanup A.A")
			})

			f.Run("A.A.A", func(f *foundation.F) {
				fmt.Println("A.A.A")

				f.Cleanup(func() {
					fmt.Println("cleanup A.A.A")
				})
			})
		})

		f.Run("A.B", func(f *foundation.F) {
			fmt.Println("A.B")

			f.Cleanup(func() {
				fmt.Println("cleanup A.B")
			})
		})

		f.Run("A.C", func(f *foundation.F) {
			fmt.Println("A.C")

			f.Cleanup(func() {
				fmt.Println("cleanup A.C")
			})

			f.Run("A.C.A", func(f *foundation.F) {
				fmt.Println("A.C.A")

				f.Cleanup(func() {
					fmt.Println("cleanup A.C.A")
				})
			})
		})
	})
}
