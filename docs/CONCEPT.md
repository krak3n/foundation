# Concept of Foundation

> **History.** This is the idea the project started from, kept as it was written. The code and the [README](../README.md) are the source of truth, and parts of the API below have changed since.

The inspiration for this SDK comes from the built in testing framework in Go where tests are just functions that run, in parallel or synchronously. Essentially using the same principle we can achieve a service framework that can work in the same way with the exception that it needs to block (in some way) for long running processes like HTTP servers etc.

All functions start and stop in a deterministic order: first to last for starting and last to first for stopping. Each function finishes stopping (or times out) before the next one is told to stop.

Minimum Go version: **1.26**

## Basic API

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"go.krak3n.dev/foundation"
)

func main() {
	foundation.Run("service-name", func(f *foundation.F) {
		// OS signal handling
		f.Run("signals", func(f *foundation.F) {
			f.Parallel() // Runs the function as a go routine but will still block the process from exiting

			sigC := make(chan os.Signal, 1)

			signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)

			// Similar to t.Cleanup these are functions which are called after function exit to perform cleanup, like defer
			f.Cleanup(func() {
				signal.Stop(sigC)
			})

			// Block until os.Signal received OR the context propagated on f.Context() is done
			// e.g something else has caused us to exit
			for {
				select {
				// f.Context() can be used to know if we are shutting down in case f.Shutdown() is called elsewhere
				// the f.Shutdown() below when a normal os signal is received would also then trigger this case on the next loop
				case <-f.Context().Done():
					return
				case sig := <-sigC:
					// This function was started first so it is stopped last. Stop handling signals now so a
					// second signal force quits a shutdown that is taking too long.
					signal.Stop(sigC)

					// Gracefully shutdown the entire process stopping all running functions
					// Shutdown would call f.Log().Info() under the hood to log what happened
					f.Shutdown("received OS signal", slog.String("signal", sig.String()))
				}
			}
		})

		// Run another function - this can do some processing and then just exit normally but would
		// not trigger the process as a whole to exit
		f.Run("processing", func(f *foundation.F) {
			fmt.Println("some processing")

			f.Cleanup(func() {
				fmt.Println("done processing")
			})
		})

		// Errors can also cause the whole process to shutdown
		f.Run("error-example", func(f *foundation.F) {
			// Fail() immediately halts the current function (via runtime.Goexit()), runs Cleanup
			// functions, and propagates the failure up to the root F which cancels all children.
			// f.Fail() would also call f.Log().Error() to log the error message and any log attributes
			f.Fail(errors.New("something went wrong"), slog.String("some", "attribute"))
		})

		// HTTP server example
		// Because the two functions above this are not run with f.Parallel()
		// this function would only begin to execute once those functions have completed.
		// Because the function directly above fails this function should never actually execute.
		f.Run("http.server", func(f *foundation.F) {
			f.Parallel()

			srv := &http.Server{
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Write([]byte("hello world"))
				}),
			}

			f.Run("listener", func(f *foundation.F) {
				f.Parallel()

				if err := srv.ListenAndServe(); err != nil {
					f.Fail(err)
				}
			})

			<-f.Context().Done()

			if err := srv.Shutdown(context.WithoutCancel(f.Context())); err != nil {
				// Because we are already shutting down, Fail() would just log the error.
				// Shutdown() is idempotent (via sync.Once), so calling it again is a no-op.
				f.Fail(err)
			}
		})
	})
}
```

## Design Decisions

### Tree-structured `F` instances

Each `F` is a node in a tree. The root `F` is created by `foundation.Run()`, and each call to `f.Run()` creates a child `F` node. This tree structure enables:

- **Upward propagation**: When `f.Fail()` is called on any node, the failure propagates up to the root, which then starts an orderly shutdown of every node.
- **Downward cancellation**: Shutdown (via `Shutdown()` or `Fail()`) walks the tree and cancels each node's context in turn, in reverse start order (see [Deterministic ordering](#deterministic-ordering)). A node's context is not cancelled just because its parent's is; the shutdown sequence cancels it when its turn comes. A node's context is also cancelled once its function has finished.
- **State tracking**: Each `F` node tracks whether it is already in a shutdown/failed state. This allows `Fail()` and `Shutdown()` to behave correctly when called during an already-in-progress shutdown (they become no-ops for triggering shutdown, but still log).

### `Fail()` semantics (FailNow-style)

`f.Fail(err, ...slog.Attr)` follows the `testing.T.FailNow()` pattern:

1. Logs the error via `f.Log().Error()`
2. Marks the run as failed and starts the shutdown, signalling all functions to stop (in order)
3. Immediately halts the current goroutine via `runtime.Goexit()` (cleanup functions still run)

No code after `f.Fail()` will execute in the current function. Cleanup functions registered via `f.Cleanup()` are still called, in LIFO order.

A failed run ends with exit status 1: once every function has stopped and every cleanup has run, `foundation.Run` ends the process itself, so callers do not handle an error. Every `Fail`, including ones while an earlier shutdown is draining and including recovered panics, counts. Because the process ends inside `Run`, put cleanup in `f.Cleanup` rather than in deferred calls in `main`.

### `Shutdown()` semantics

`f.Shutdown(msg, ...slog.Attr)` is for intentional, graceful shutdowns (e.g. receiving an OS signal):

1. Logs via `f.Log().Info()`
2. Cancels the root context and starts stopping the remaining functions, in reverse start order
3. Is **idempotent** via `sync.Once` - multiple calls are safe, only the first triggers shutdown
4. Does **not** halt the current function — the caller continues running and should return when it observes `f.Context().Done()`

An intentional shutdown is not a failure: `Run` returns normally and the process exits 0.

### Per-`F` shutdown timeout

Each `F` has a configurable shutdown timeout (default: 30 seconds). The timeout only applies **once it is the function's turn to stop** (i.e., its context has been cancelled during shutdown). If the function does not exit within the timeout, a `WARN` log is produced and the goroutine is abandoned — the shutdown continues without waiting further, and everything still running beneath the abandoned function has its context cancelled at once. Timeouts of siblings add up, and a timeout does not change the exit status.

The timeout covers the entire goroutine lifecycle: function body, waiting for parallel children, and cleanup functions.

```go
f.Run("http.server", func(f *foundation.F) {
    f.SetTimeout(10 * time.Second) // Override the default 30s timeout
    f.Parallel()                   // Set the timeout first: shutdown can begin as soon as the parent is released
    // ...
})
```

The timeout does **not** fire during normal operation — a function can block indefinitely before shutdown is initiated.

### Deterministic ordering

- **Start order**: Functions passed to `f.Run()` start in the order they are defined (FIFO).
- **Stop order**: Functions stop in reverse order (LIFO) - the last function started is the first to be stopped, and each function has finished stopping (or timed out) before the next is signalled. Within a function the order is: its context is cancelled and its function returns, then its children are stopped in reverse order, then its cleanups run (LIFO). If a function is blocked waiting on a sequential child, that child is stopped first. New `f.Run` calls made after shutdown has begun are ignored. See `examples/ordered-shutdown`.
- **`Parallel()`**: All functions execute in their own goroutine. By default, the parent waits for each function to complete before starting the next. Calling `f.Parallel()` releases the parent to proceed to the next `f.Run()` call without waiting. The ordering of the *calls* to start/stop is still deterministic, but parallel functions run concurrently.

### Logging

`f.Log()` returns a `*slog.Logger` derived from `slog.Default()` with a `"fn"` attribute set to the hierarchical function path. The logger is created on each call, so changes to the default logger via `slog.SetDefault()` are always reflected.

For example, a function at `service-name/http.server/listener` produces log output like:

```
2024/01/15 10:30:00 INFO listening fn=service-name/http.server/listener addr=:8080
```

- `Shutdown()` logs at `INFO` level with the provided message and attributes.
- `Fail()` logs at `ERROR` level with the error message and attributes, then calls `Shutdown()` which logs at `INFO` level. This produces two log lines per `Fail()` call.
- Panics recovered by the framework are logged via `Fail()`.

The default logger can be replaced globally via `slog.SetDefault()` at any point (e.g. to configure a JSON handler or OpenTelemetry integration).

### High-level abstractions

The framework will provide pre-built components (HTTP servers, signal handlers, etc.) out of the box. Engineers using the framework will typically not be exposed to deeply nested `f.Run()` calls - they will interact with high-level APIs that handle the lifecycle complexity internally. The nested `f` pattern is an implementation detail of the framework, not something end users need to manage.

## `F` API Surface

```go
type F struct {
    // unexported fields: name, parent *F, children []*F, context, cancel, once, etc.
}

// Run is the top-level entry point. Creates a root F and executes fn.
func Run(name string, fn func(f *F))

// Run executes fn as a child of the current F. Sequential by default.
// The name is used for identification in logs and error messages.
// Names are hierarchical: "service-name/http.server/listener".
func (f *F) Run(name string, fn func(f *F))

// Parallel releases the parent to proceed to the next f.Run() call
// without waiting for this function to complete.
// May be called at any point during the function.
// Panics if called on the root F.
func (f *F) Parallel()

// Context returns the context for the current F. Cancelled on Shutdown.
func (f *F) Context() context.Context

// Cleanup registers a function to be called when the current F's function exits.
// Multiple cleanups are called in LIFO order.
func (f *F) Cleanup(fn func())

// Name returns the name of the current F node.
func (f *F) Name() string

// SetTimeout sets the shutdown timeout for the current F.
// After context cancellation, if the function does not exit within d,
// it is abandoned and the parent continues. Default is 30 seconds.
func (f *F) SetTimeout(d time.Duration)

// Shutdown triggers a graceful shutdown of the entire process.
// Idempotent - safe to call multiple times. Does not halt the current function.
func (f *F) Shutdown(msg string, attrs ...slog.Attr)

// Fail logs the error and immediately halts the current function via runtime.Goexit().
// Calls Shutdown() to propagate failure to the root, triggering shutdown of all functions.
// Cleanup functions still run.
func (f *F) Fail(err error, attrs ...slog.Attr)

// Log returns a *slog.Logger derived from slog.Default() with the
// hierarchical function path set as the "fn" attribute.
// Always uses the current default logger, so slog.SetDefault() changes
// are reflected immediately.
func (f *F) Log() *slog.Logger
```
