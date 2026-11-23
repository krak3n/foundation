package main

import (
	"fmt"
	"net/http"
	"os"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/interrupt"
	transport "go.krak3n.dev/foundation/transport/http"
)

func main() {
	foundation.Run("hello", func(f *foundation.F) {
		// Stop cleanly on Ctrl-C or SIGTERM.
		interrupt.Signals(f)

		// Serve HTTP. The address is a default: the environment
		// (HELLO_HTTP_SERVER_ADDR), a file or a flag can change it.
		transport.Server(f, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, "Hello, world!")
		}), transport.WithDefaults(func(c *transport.ServerConfig) {
			c.Addr = "127.0.0.1:8080"
		}))
	}, foundation.WithArgs(os.Args[1:]))
}
