package http_test

import (
	"net"
	"net/http"
	"time"

	"go.krak3n.dev/foundation"
	transporthttp "go.krak3n.dev/foundation/transport/http"
)

// opt is a change a test makes to the server's configuration before it is
// loaded, as the defaults of a program would. It is what the options that only
// set a setting used to be: the server has no such options now, as configuration
// says the same.
type opt func(*transporthttp.ServerConfig)

func withAddr(addr string) opt { return func(c *transporthttp.ServerConfig) { c.Addr = addr } }
func withBlock() opt           { return func(c *transporthttp.ServerConfig) { c.Block = true } }

func withTLS(cert, key string) opt {
	return func(c *transporthttp.ServerConfig) { c.CertFile, c.KeyFile = cert, key }
}

func withProbePath(path string) opt {
	return func(c *transporthttp.ServerConfig) { c.ProbePath = path }
}

func withShutdownTimeout(d time.Duration) opt {
	return func(c *transporthttp.ServerConfig) { c.ShutdownTimeout = d }
}

func withReadHeaderTimeout(d time.Duration) opt {
	return func(c *transporthttp.ServerConfig) { c.ReadHeaderTimeout = d }
}

// withListener is as [transporthttp.WithListener]: the listener stands in for the
// address.
func withListener(ln net.Listener) opt {
	return func(c *transporthttp.ServerConfig) {
		c.Listener = ln
		c.Addr = ln.Addr().String()
	}
}

// server runs the HTTP server through [transporthttp.Server], with the changes
// as its defaults.
func server(f *foundation.F, handler http.Handler, opts ...opt) {
	transporthttp.Server(f, handler, transporthttp.WithDefaults(func(c *transporthttp.ServerConfig) {
		for _, o := range opts {
			o(c)
		}
	}))
}
