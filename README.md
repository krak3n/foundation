<p align="center"><img src="logo.png" alt="Foundation" width="160"></p>

# Foundation

Foundation is a Go framework for building services. You write your program as a tree of functions, like `testing.T`: they start in order and stop in reverse, so shutdown is clean. It also gives you configuration, health checks and an HTTP server, safe by default.

- **Pre-1.0.** The API may still change.
- **Requires Go 1.26 or newer.**
- **No third-party dependencies** in the core.

## Quick start

```sh
go get go.krak3n.dev/foundation
```

```go
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
```

Run it, then open `http://127.0.0.1:8080/`. Press Ctrl-C and it stops in order.

```sh
go run .                                      # serve
go run . --help                               # list every flag
go run . --dry-run                            # check everything, start nothing
HELLO_HTTP_SERVER_ADDR=127.0.0.1:9090 go run . # change the address
```

## Features

### Ordered start and stop

`f.Run` starts a function and waits for it. `f.Parallel()` lets the parent carry on while the function keeps running. Functions start first to last and stop last to first, each one finishing before the next is told to stop. `f.Cleanup` runs after a function ends, like `defer`.

```go
foundation.Run("svc", func(f *foundation.F) {
	f.Run("database", func(f *foundation.F) {
		f.Cleanup(func() { /* close the connection */ })
	})

	f.Run("worker", func(f *foundation.F) {
		f.Parallel()

		<-f.Context().Done() // until it is this function's turn to stop
	})
})
```

### Graceful shutdown

`interrupt.Signals(f)` stops the program on Ctrl-C or SIGTERM. `f.Shutdown("done")` stops it and exits 0, and `f.Fail(err)` stops it and exits 1. A function that does not stop within its timeout (30 seconds, `f.SetTimeout`) is abandoned with a warning, and a second Ctrl-C forces the exit.

### Configuration

Describe your settings once, in a struct. Each setting has one name, and the environment, flags and files all read it.

```go
type Config struct {
	Addr    string        `config:"addr,required"`
	Timeout time.Duration `config:"timeout"`
}

var cfg Config

f.Load(&cfg) // SVC_ADDR=127.0.0.1:8080 ./svc
```

- Environment variables come from the path of the function (`SVC_API_ADDR` for `api` in `svc`). Flags need `foundation.WithArgs(os.Args[1:])`. Files are opt in.
- A missing `required` setting, a wrong type or a bad value stops startup and says which setting, never what its value was.
- Secrets can come from files (Docker and Kubernetes style), and unknown variables can be treated as typos.

More in [CONFIG.md](CONFIG.md): [loading](CONFIG.md#loading), [command line flags](CONFIG.md#command-line-flags), [secrets](CONFIG.md#secrets-from-files) and [strict mode](CONFIG.md#strict-mode).

### Transport

#### HTTP

`transport.Server` runs an HTTP server as one function of the tree. It starts when its configuration is valid and shuts down with everything else.

```go
transport.Server(f, handler, transport.WithDefaults(func(c *transport.ServerConfig) {
	c.Addr = "127.0.0.1:8080"
}))
```

- **Safe defaults:** header and idle timeouts are set, and there is no default address, so one must be given.
- **Configurable:** the address, timeouts and TLS files are settings like any other (`HELLO_HTTP_SERVER_ADDR`, `--http --server --addr`). What your code sets is a default, and configuration wins.
- **Ready check:** a `/_probe` endpoint, and it can wait until the server accepts connections.
- A certificate without its key is an error, never plain HTTP.

### Health checks

`health.Server(f)` serves liveness and readiness on its own port (3417, on every interface, so an orchestrator can reach it; change it with `SVC_HEALTH_ADDR`). A probe must say it is healthy: an error, a panic or a timeout counts as unhealthy. Responses are only the verdict, and the detail goes to your logs.

```go
f.Probes().Add(monitor.NewProbe("database", monitor.AllModes,
	monitor.StatusCheckerFunc(func(ctx context.Context) (monitor.Status, error) {
		return monitor.StatusHealthy, db.PingContext(ctx)
	})))

health.Server(f)
```

### Dry run and help

`--dry-run` walks the whole program, loads and checks every configuration, and starts nothing: use it in CI or before a deploy. `--help` lists every flag and what it is for. A function that starts something calls `f.Skip` when `f.DryRun()` is true.

```go
if f.DryRun() {
	f.Skip("not starting the worker")
}
```

### Reload and restart

A configuration file can restart the program when it changes. Before it stops anything, Foundation runs your program again as a trial: if the new configuration is wrong, the running service is left alone and the reason is logged.

```go
foundation.Run("svc", fn,
	foundation.WithDocumentConfig(json.File("/etc/svc.json", config.WithRestartOnChange())))
```

`f.Restart("reason")` does the same from your own code. See [reloading](CONFIG.md#reloading-when-a-file-changes) and `examples/restart`.

### Secure by default

- A mistake stops startup. It never changes what the service is (a certificate without a key is not plain HTTP).
- No configuration value is ever put in an error or a log line.
- Text that an outsider can influence is escaped before it is logged.
- Sizes, times and counts are bounded, and a health response is only a verdict.

See [SECURITY.md](SECURITY.md) to report a problem.

### Testing

`foundationtest` records log lines and the exit status, so a test can check what a program did.

```go
exit := foundationtest.NewExitRecorder()
exit.Install(t)

foundation.Run("svc", fn)

// exit.Codes() is empty after a clean run, and [1] after a failure.
```

## Integrations

Providers read configuration from somewhere. The core has none of them as dependencies, so one that needs a library is a module of its own.

| Module | What it does |
|---|---|
| [`config/yaml`](config/yaml) | Reads configuration from YAML files and directories. |

```sh
go get go.krak3n.dev/foundation/config/yaml
```

To write your own, implement `config.Provider`, which is one method: `Unmarshal(path []string, dst any) error`. Built something others could use? Open a pull request to add it here.

## Documentation

- [Configuration](CONFIG.md): providers, files, flags, secrets, validation, strict mode.
- [Security policy](SECURITY.md)
- [Examples](examples): small programs for each feature.
- [Design notes](docs/CONCEPT.md): the idea the project started from.

## License

Apache-2.0. See [LICENSE](LICENSE).
