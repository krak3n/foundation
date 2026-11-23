package foundation_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/foundationtest"
	"go.krak3n.dev/foundation/health"
	"go.krak3n.dev/foundation/interrupt"
	transport "go.krak3n.dev/foundation/transport/http"
)

func TestRunExecutesFunction(t *testing.T) {
	called := false

	foundation.Run("test", func(f *foundation.F) {
		called = true
	})

	if !called {
		t.Fatal("expected function to be called")
	}
}

func TestRunSequentialOrder(t *testing.T) {
	var order []int

	foundation.Run("test", func(f *foundation.F) {
		f.Run("first", func(f *foundation.F) {
			order = append(order, 1)
		})

		f.Run("second", func(f *foundation.F) {
			order = append(order, 2)
		})

		f.Run("third", func(f *foundation.F) {
			order = append(order, 3)
		})
	})

	if len(order) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(order))
	}

	for i, v := range order {
		if v != i+1 {
			t.Fatalf("expected order[%d] = %d, got %d", i, i+1, v)
		}
	}
}

func TestCleanupCalledAfterFunctionExits(t *testing.T) {
	var order []string

	foundation.Run("test", func(f *foundation.F) {
		f.Run("child", func(f *foundation.F) {
			f.Cleanup(func() {
				order = append(order, "cleanup")
			})

			order = append(order, "run")
		})
	})

	expected := []string{"run", "cleanup"}

	if len(order) != len(expected) {
		t.Fatalf("expected %d events, got %d", len(expected), len(order))
	}

	for i, v := range order {
		if v != expected[i] {
			t.Fatalf("expected order[%d] = %q, got %q", i, expected[i], v)
		}
	}
}

func TestCleanupLIFOOrder(t *testing.T) {
	var order []int

	foundation.Run("test", func(f *foundation.F) {
		f.Run("child", func(f *foundation.F) {
			f.Cleanup(func() {
				order = append(order, 1)
			})

			f.Cleanup(func() {
				order = append(order, 2)
			})

			f.Cleanup(func() {
				order = append(order, 3)
			})
		})
	})

	expected := []int{3, 2, 1}

	if len(order) != len(expected) {
		t.Fatalf("expected %d cleanups, got %d", len(expected), len(order))
	}

	for i, v := range order {
		if v != expected[i] {
			t.Fatalf("expected order[%d] = %d, got %d", i, expected[i], v)
		}
	}
}

func TestNestedRun(t *testing.T) {
	var order []string

	foundation.Run("test", func(f *foundation.F) {
		f.Run("parent", func(f *foundation.F) {
			order = append(order, "parent-start")

			f.Run("child", func(f *foundation.F) {
				order = append(order, "child")
			})

			order = append(order, "parent-end")
		})
	})

	expected := []string{"parent-start", "child", "parent-end"}

	if len(order) != len(expected) {
		t.Fatalf("expected %d events, got %d", len(expected), len(order))
	}

	for i, v := range order {
		if v != expected[i] {
			t.Fatalf("expected order[%d] = %q, got %q", i, expected[i], v)
		}
	}
}

func TestNestedCleanupOrder(t *testing.T) {
	var order []string

	foundation.Run("test", func(f *foundation.F) {
		f.Run("parent", func(f *foundation.F) {
			f.Cleanup(func() {
				order = append(order, "parent-cleanup")
			})

			f.Run("child", func(f *foundation.F) {
				f.Cleanup(func() {
					order = append(order, "child-cleanup")
				})

				order = append(order, "child-run")
			})

			order = append(order, "parent-run")
		})
	})

	// Child runs and cleans up before parent continues, then parent cleans up
	expected := []string{"child-run", "child-cleanup", "parent-run", "parent-cleanup"}

	if len(order) != len(expected) {
		t.Fatalf("expected %d events, got %d", len(expected), len(order))
	}

	for i, v := range order {
		if v != expected[i] {
			t.Fatalf("expected order[%d] = %q, got %q", i, expected[i], v)
		}
	}
}

func TestContextCancelledWhenFunctionFinishes(t *testing.T) {
	var childCtx context.Context

	foundation.Run("test", func(f *foundation.F) {
		f.Run("child", func(f *foundation.F) {
			childCtx = f.Context()
		})
	})

	// A function's context is released as soon as it has finished, rather than
	// living on until the whole tree shuts down.
	if err := childCtx.Err(); err == nil {
		t.Fatal("expected child context to be cancelled once its function has finished")
	}
}

func TestShutdownReachesNestedLiveChildren(t *testing.T) {
	var saw atomic.Bool

	foundation.Run("test", func(f *foundation.F) {
		f.Run("parent", func(f *foundation.F) {
			f.Run("child", func(f *foundation.F) {
				f.Parallel()

				<-f.Context().Done()
				saw.Store(true)
			})

			f.Parallel()

			<-f.Context().Done()
		})

		f.Shutdown("done")
	})

	if !saw.Load() {
		t.Fatal("expected a live nested child to observe its context cancelled by Shutdown")
	}
}
func TestName(t *testing.T) {
	foundation.Run("service", func(f *foundation.F) {
		if f.Name() != "service" {
			t.Fatalf("expected root name %q, got %q", "service", f.Name())
		}

		f.Run("http.server", func(f *foundation.F) {
			if f.Name() != "http.server" {
				t.Fatalf("expected child name %q, got %q", "http.server", f.Name())
			}
		})
	})
}

func TestMultipleSequentialWithCleanups(t *testing.T) {
	var order []string

	foundation.Run("test", func(f *foundation.F) {
		f.Run("first", func(f *foundation.F) {
			f.Cleanup(func() {
				order = append(order, "first-cleanup")
			})

			order = append(order, "first-run")
		})

		f.Run("second", func(f *foundation.F) {
			f.Cleanup(func() {
				order = append(order, "second-cleanup")
			})

			order = append(order, "second-run")
		})
	})

	// Each function runs and cleans up fully before the next starts
	expected := []string{"first-run", "first-cleanup", "second-run", "second-cleanup"}

	if len(order) != len(expected) {
		t.Fatalf("expected %d events, got %d", len(expected), len(order))
	}

	for i, v := range order {
		if v != expected[i] {
			t.Fatalf("expected order[%d] = %q, got %q", i, expected[i], v)
		}
	}
}

func TestShutdownCancelsRootContext(t *testing.T) {
	var rootCtx context.Context

	foundation.Run("test", func(f *foundation.F) {
		rootCtx = f.Context()

		f.Run("child", func(f *foundation.F) {
			f.Shutdown("shutting down")
		})
	})

	if err := rootCtx.Err(); err == nil {
		t.Fatal("expected root context to be cancelled after Shutdown")
	}
}

func TestShutdownCancelsRootContextImmediately(t *testing.T) {
	foundation.Run("test", func(f *foundation.F) {
		f.Shutdown("shutting down")

		if err := f.Context().Err(); err == nil {
			t.Fatal("expected root context to be cancelled as soon as Shutdown returns")
		}
	})
}
func TestShutdownIsIdempotent(t *testing.T) {
	foundation.Run("test", func(f *foundation.F) {
		f.Run("child", func(f *foundation.F) {
			// Calling Shutdown multiple times should not panic
			f.Shutdown("first")
			f.Shutdown("second")
			f.Shutdown("third")
		})
	})

	// If we get here without panicking, the test passes
}

func TestShutdownFromChild(t *testing.T) {
	var rootCtx context.Context

	foundation.Run("test", func(f *foundation.F) {
		rootCtx = f.Context()

		f.Run("parent", func(f *foundation.F) {
			f.Run("child", func(f *foundation.F) {
				f.Run("grandchild", func(f *foundation.F) {
					f.Shutdown("deep shutdown")
				})
			})
		})
	})

	if err := rootCtx.Err(); err == nil {
		t.Fatal("expected root context to be cancelled from deep child Shutdown")
	}
}

func TestFailStopsCurrentFunction(t *testing.T) {
	installExit(t) // Fail ends the process unless the exit is recorded

	afterFail := false

	foundation.Run("test", func(f *foundation.F) {
		f.Run("child", func(f *foundation.F) {
			f.Fail(errors.New("something went wrong"))
			afterFail = true // This should never execute
		})
	})

	if afterFail {
		t.Fatal("expected code after Fail() to not execute")
	}
}

func TestFailCleanupsStillRun(t *testing.T) {
	installExit(t) // Fail ends the process unless the exit is recorded

	cleanupRan := false

	foundation.Run("test", func(f *foundation.F) {
		f.Run("child", func(f *foundation.F) {
			f.Cleanup(func() {
				cleanupRan = true
			})

			f.Fail(errors.New("something went wrong"))
		})
	})

	if !cleanupRan {
		t.Fatal("expected cleanup to run after Fail()")
	}
}

func TestFailCleanupsRunInLIFOOrder(t *testing.T) {
	installExit(t) // Fail ends the process unless the exit is recorded

	var order []int

	foundation.Run("test", func(f *foundation.F) {
		f.Run("child", func(f *foundation.F) {
			f.Cleanup(func() {
				order = append(order, 1)
			})

			f.Cleanup(func() {
				order = append(order, 2)
			})

			f.Cleanup(func() {
				order = append(order, 3)
			})

			f.Fail(errors.New("something went wrong"))
		})
	})

	expected := []int{3, 2, 1}

	if len(order) != len(expected) {
		t.Fatalf("expected %d cleanups, got %d", len(expected), len(order))
	}

	for i, v := range order {
		if v != expected[i] {
			t.Fatalf("expected order[%d] = %d, got %d", i, expected[i], v)
		}
	}
}

func TestFailCancelsRootContext(t *testing.T) {
	installExit(t) // Fail ends the process unless the exit is recorded

	var rootCtx context.Context

	foundation.Run("test", func(f *foundation.F) {
		rootCtx = f.Context()

		f.Run("child", func(f *foundation.F) {
			f.Fail(errors.New("something went wrong"))
		})
	})

	if err := rootCtx.Err(); err == nil {
		t.Fatal("expected root context to be cancelled after Fail()")
	}
}

func TestFailPreventsSubsequentRuns(t *testing.T) {
	installExit(t) // Fail ends the process unless the exit is recorded

	var order []string

	foundation.Run("test", func(f *foundation.F) {
		f.Run("first", func(f *foundation.F) {
			order = append(order, "first")
		})

		f.Run("failing", func(f *foundation.F) {
			order = append(order, "failing")
			f.Fail(errors.New("something went wrong"))
		})

		f.Run("skipped", func(f *foundation.F) {
			order = append(order, "skipped") // Should never execute
		})
	})

	expected := []string{"first", "failing"}

	if len(order) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(order), order)
	}

	for i, v := range order {
		if v != expected[i] {
			t.Fatalf("expected order[%d] = %q, got %q", i, expected[i], v)
		}
	}
}

func TestFailInNestedRun(t *testing.T) {
	installExit(t) // Fail ends the process unless the exit is recorded

	var rootCtx context.Context
	var order []string

	foundation.Run("test", func(f *foundation.F) {
		rootCtx = f.Context()

		f.Run("parent", func(f *foundation.F) {
			f.Run("child", func(f *foundation.F) {
				order = append(order, "child-before-fail")
				f.Fail(errors.New("deep failure"))
				order = append(order, "child-after-fail") // Should not execute
			})

			// This should still execute because the parent goroutine continues
			// after the child goroutine exits (sequential wait)
			order = append(order, "parent-after-child")
		})

		// This should be skipped because root context is cancelled
		f.Run("skipped", func(f *foundation.F) {
			order = append(order, "skipped")
		})
	})

	expected := []string{"child-before-fail", "parent-after-child"}

	if len(order) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(order), order)
	}

	for i, v := range order {
		if v != expected[i] {
			t.Fatalf("expected order[%d] = %q, got %q", i, expected[i], v)
		}
	}

	if err := rootCtx.Err(); err == nil {
		t.Fatal("expected root context to be cancelled from nested Fail()")
	}
}

func TestParallelReleasesParent(t *testing.T) {
	// The parallel child blocks until shutdown, but the parent should be able
	// to continue to the next f.Run() call without waiting.
	var order []string
	var mu sync.Mutex

	append := func(s string) {
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
	}

	foundation.Run("test", func(f *foundation.F) {
		f.Run("parallel-child", func(f *foundation.F) {
			f.Parallel()
			append("parallel-start")

			// Block until context is cancelled
			<-f.Context().Done()
			append("parallel-end")
		})

		// This should run immediately without waiting for parallel-child to finish
		f.Run("sequential", func(f *foundation.F) {
			append("sequential")
			f.Shutdown("done")
		})
	})

	mu.Lock()
	defer mu.Unlock()

	// "parallel-start" should come before "sequential" since it runs first
	// "sequential" should run without waiting for "parallel-end"
	// "parallel-end" runs after shutdown cancels the context

	if len(order) != 3 {
		t.Fatalf("expected 3 events, got %d: %v", len(order), order)
	}

	if order[0] != "parallel-start" {
		t.Fatalf("expected order[0] = %q, got %q", "parallel-start", order[0])
	}

	if order[1] != "sequential" {
		t.Fatalf("expected order[1] = %q, got %q", "sequential", order[1])
	}

	if order[2] != "parallel-end" {
		t.Fatalf("expected order[2] = %q, got %q", "parallel-end", order[2])
	}
}

func TestParallelChildrenCompleteBeforeParentCleanup(t *testing.T) {
	var order []string
	var mu sync.Mutex

	appendOrder := func(s string) {
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
	}

	foundation.Run("test", func(f *foundation.F) {
		f.Run("parent", func(f *foundation.F) {
			f.Cleanup(func() {
				appendOrder("parent-cleanup")
			})

			f.Run("parallel-child", func(f *foundation.F) {
				f.Parallel()

				f.Cleanup(func() {
					appendOrder("child-cleanup")
				})

				// Simulate some work
				time.Sleep(50 * time.Millisecond)
				appendOrder("child-done")
			})
		})
	})

	mu.Lock()
	defer mu.Unlock()

	// Child must finish and clean up before parent cleans up
	expected := []string{"child-done", "child-cleanup", "parent-cleanup"}

	if len(order) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(order), order)
	}

	for i, v := range order {
		if v != expected[i] {
			t.Fatalf("expected order[%d] = %q, got %q", i, expected[i], v)
		}
	}
}

func TestParallelAndSequentialMixed(t *testing.T) {
	var order []string
	var mu sync.Mutex

	appendOrder := func(s string) {
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
	}

	foundation.Run("test", func(f *foundation.F) {
		f.Run("seq-1", func(f *foundation.F) {
			appendOrder("seq-1")
		})

		f.Run("par-1", func(f *foundation.F) {
			f.Parallel()
			appendOrder("par-1-start")
			time.Sleep(50 * time.Millisecond)
			appendOrder("par-1-end")
		})

		// This should start without waiting for par-1 to finish
		f.Run("seq-2", func(f *foundation.F) {
			appendOrder("seq-2")
		})
	})

	mu.Lock()
	defer mu.Unlock()

	// seq-1 completes first (sequential)
	// par-1-start happens, then parent continues
	// seq-2 runs while par-1 is still sleeping
	// par-1-end happens after the sleep

	if len(order) != 4 {
		t.Fatalf("expected 4 events, got %d: %v", len(order), order)
	}

	if order[0] != "seq-1" {
		t.Fatalf("expected order[0] = %q, got %q", "seq-1", order[0])
	}

	if order[1] != "par-1-start" {
		t.Fatalf("expected order[1] = %q, got %q", "par-1-start", order[1])
	}

	if order[2] != "seq-2" {
		t.Fatalf("expected order[2] = %q, got %q", "seq-2", order[2])
	}

	if order[3] != "par-1-end" {
		t.Fatalf("expected order[3] = %q, got %q", "par-1-end", order[3])
	}
}

func TestParallelMultiple(t *testing.T) {
	// Multiple parallel children should all run concurrently
	var running atomic.Int32
	var maxRunning atomic.Int32

	foundation.Run("test", func(f *foundation.F) {
		for i := range 3 {
			name := []string{"a", "b", "c"}[i]
			f.Run(name, func(f *foundation.F) {
				f.Parallel()

				cur := running.Add(1)
				// Track maximum concurrent goroutines
				for {
					old := maxRunning.Load()
					if cur <= old || maxRunning.CompareAndSwap(old, cur) {
						break
					}
				}

				time.Sleep(50 * time.Millisecond)
				running.Add(-1)
			})
		}
	})

	if max := maxRunning.Load(); max < 3 {
		t.Fatalf("expected at least 3 concurrent goroutines, got %d", max)
	}
}

func TestParallelWithShutdown(t *testing.T) {
	var childSawCancel atomic.Bool

	foundation.Run("test", func(f *foundation.F) {
		f.Run("parallel-child", func(f *foundation.F) {
			f.Parallel()

			<-f.Context().Done()
			childSawCancel.Store(true)
		})

		f.Run("trigger", func(f *foundation.F) {
			f.Shutdown("done")
		})
	})

	if !childSawCancel.Load() {
		t.Fatal("expected parallel child to observe context cancellation from Shutdown()")
	}
}

func TestParallelWithFail(t *testing.T) {
	installExit(t) // Fail ends the process unless the exit is recorded

	var rootCtx context.Context
	var parallelCleanupRan atomic.Bool

	foundation.Run("test", func(f *foundation.F) {
		rootCtx = f.Context()

		f.Run("parallel-child", func(f *foundation.F) {
			f.Parallel()

			f.Cleanup(func() {
				parallelCleanupRan.Store(true)
			})

			<-f.Context().Done()
		})

		f.Run("failing", func(f *foundation.F) {
			f.Fail(errors.New("something went wrong"))
		})
	})

	if err := rootCtx.Err(); err == nil {
		t.Fatal("expected root context to be cancelled after Fail() in sibling")
	}

	if !parallelCleanupRan.Load() {
		t.Fatal("expected parallel child cleanup to run after Fail()")
	}
}

func TestParallelCalledMidFunction(t *testing.T) {
	// Work done before Parallel() is called should complete before parent continues
	var order []string
	var mu sync.Mutex

	appendOrder := func(s string) {
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
	}

	foundation.Run("test", func(f *foundation.F) {
		f.Run("child", func(f *foundation.F) {
			// This work happens while parent is still waiting
			appendOrder("before-parallel")

			f.Parallel()

			// This work happens after parent is released
			time.Sleep(50 * time.Millisecond)
			appendOrder("after-parallel")
		})

		// Parent proceeds after Parallel() is called
		f.Run("next", func(f *foundation.F) {
			appendOrder("next")
		})
	})

	mu.Lock()
	defer mu.Unlock()

	if len(order) != 3 {
		t.Fatalf("expected 3 events, got %d: %v", len(order), order)
	}

	// "before-parallel" must be first — parent was still blocked
	if order[0] != "before-parallel" {
		t.Fatalf("expected order[0] = %q, got %q", "before-parallel", order[0])
	}

	// "next" should come before "after-parallel" since parent was released
	if order[1] != "next" {
		t.Fatalf("expected order[1] = %q, got %q", "next", order[1])
	}

	if order[2] != "after-parallel" {
		t.Fatalf("expected order[2] = %q, got %q", "after-parallel", order[2])
	}
}

func TestParallelOnRootPanics(t *testing.T) {
	exit := installExit(t)

	var rootCtx context.Context

	foundation.Run("test", func(f *foundation.F) {
		rootCtx = f.Context()

		// Calling Parallel() on root is a programming error.
		// The panic is recovered and converted into a Fail().
		f.Parallel()
	})

	if err := rootCtx.Err(); err == nil {
		t.Fatal("expected root context to be cancelled after Parallel() panic on root F")
	}

	assertExitCodes(t, exit, 1)
}

func TestLogReturnsLogger(t *testing.T) {
	foundation.Run("test", func(f *foundation.F) {
		logger := f.Log()
		if logger == nil {
			t.Fatal("expected Log() to return a non-nil logger")
		}
	})
}

func TestLogHierarchicalPath(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("service", func(f *foundation.F) {
		f.Log().Info("root log")

		f.Run("http.server", func(f *foundation.F) {
			f.Log().Info("server log")

			f.Run("listener", func(f *foundation.F) {
				f.Log().Info("listener log")
			})
		})
	})

	entries := recorder.Entries()

	if len(entries) != 3 {
		t.Fatalf("expected 3 log entries, got %d", len(entries))
	}

	// Verify messages
	expectedMsgs := []string{"root log", "server log", "listener log"}
	for i, e := range entries {
		if e.Record.Message != expectedMsgs[i] {
			t.Fatalf("expected entry[%d].Record.Message = %q, got %q", i, expectedMsgs[i], e.Record.Message)
		}
	}
}

func TestLogHierarchicalPathAttr(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("service", func(f *foundation.F) {
		f.Log().Info("root")

		f.Run("http.server", func(f *foundation.F) {
			f.Log().Info("child")

			f.Run("listener", func(f *foundation.F) {
				f.Log().Info("grandchild")
			})
		})
	})

	entries := recorder.Entries()

	if len(entries) != 3 {
		t.Fatalf("expected 3 log entries, got %d", len(entries))
	}

	expectedPaths := []string{"service", "service/http.server", "service/http.server/listener"}

	for i, entry := range entries {
		var fnValue string

		for _, attr := range entry.Attrs {
			if attr.Key == "fn" {
				fnValue = attr.Value.String()
			}
		}

		if fnValue != expectedPaths[i] {
			t.Fatalf("expected entry[%d] fn=%q, got fn=%q", i, expectedPaths[i], fnValue)
		}
	}
}

func TestLogUsesCurrentDefault(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()

	foundation.Run("test", func(f *foundation.F) {
		// Set the default logger mid-execution
		slog.SetDefault(slog.New(recorder))

		// This should use the new default
		f.Log().Info("after set default")
	})

	entries := recorder.Entries()

	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}

	if entries[0].Record.Message != "after set default" {
		t.Fatalf("expected message %q, got %q", "after set default", entries[0].Record.Message)
	}
}

func TestShutdownLogs(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("test", func(f *foundation.F) {
		f.Run("child", func(f *foundation.F) {
			f.Shutdown("received signal", slog.String("signal", "SIGTERM"))
		})
	})

	entries := recorder.Entries()

	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}

	e := entries[0]

	if e.Record.Level != slog.LevelInfo {
		t.Fatalf("expected INFO level, got %v", e.Record.Level)
	}

	if e.Record.Message != "received signal" {
		t.Fatalf("expected message %q, got %q", "received signal", e.Record.Message)
	}
}

func TestFailLogs(t *testing.T) {
	installExit(t) // Fail ends the process unless the exit is recorded

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("test", func(f *foundation.F) {
		f.Run("child", func(f *foundation.F) {
			f.Fail(errors.New("something broke"), slog.String("detail", "bad thing"))
		})
	})

	entries := recorder.Entries()

	// Fail() produces a single ERROR log via shutdown()
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}

	e := entries[0]

	if e.Record.Level != slog.LevelError {
		t.Fatalf("expected ERROR level, got %v", e.Record.Level)
	}

	if e.Record.Message != "shutting down due to failure" {
		t.Fatalf("expected message %q, got %q", "shutting down due to failure", e.Record.Message)
	}

	// Verify attrs: err and detail should both be present
	var errVal, detailVal string

	e.Record.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "err":
			errVal = a.Value.String()
		case "detail":
			detailVal = a.Value.String()
		}

		return true
	})

	if errVal != "something broke" {
		t.Fatalf("expected err attr %q, got %q", "something broke", errVal)
	}

	if detailVal != "bad thing" {
		t.Fatalf("expected detail attr %q, got %q", "bad thing", detailVal)
	}
}

func TestSetTimeout(t *testing.T) {
	foundation.Run("test", func(f *foundation.F) {
		f.Run("child", func(f *foundation.F) {
			f.SetTimeout(5 * time.Second)

			if f.Timeout() != 5*time.Second {
				t.Fatalf("expected timeout 5s, got %v", f.Timeout())
			}
		})
	})
}

func TestTimeoutReturnsDefault(t *testing.T) {
	foundation.Run("test", func(f *foundation.F) {
		f.Run("child", func(f *foundation.F) {
			if f.Timeout() != foundation.DefaultTimeout {
				t.Fatalf("expected default timeout %v, got %v", foundation.DefaultTimeout, f.Timeout())
			}
		})
	})
}

func TestTimeoutOnShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Released when the test ends: every goroutine in a synctest bubble must
		// exit, including the ones the framework abandoned.
		hang := make(chan struct{})
		defer close(hang)

		// A function that ignores context cancellation should be timed out.
		// We use a parallel trigger to initiate shutdown while "hung" is blocking.
		start := time.Now()

		foundation.Run("test", func(f *foundation.F) {
			f.Run("trigger", func(f *foundation.F) {
				f.Parallel()

				// Give "hung" time to start, then trigger shutdown
				time.Sleep(10 * time.Millisecond)
				f.Shutdown("shutting down")
			})

			f.Run("hung", func(f *foundation.F) {
				f.SetTimeout(100 * time.Millisecond)

				// Intentionally ignore context cancellation — block forever
				<-hang
			})
		})

		elapsed := time.Since(start)

		// 10ms until the trigger fires, then the 100ms timeout. The clock is fake so
		// this is exact.
		if want := 110 * time.Millisecond; elapsed != want {
			t.Fatalf("expected shutdown to take %v, took %v", want, elapsed)
		}
	})
}

func TestTimeoutLogsWarning(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("test", func(f *foundation.F) {
		f.Run("trigger", func(f *foundation.F) {
			f.Parallel()

			time.Sleep(10 * time.Millisecond)
			f.Shutdown("shutting down")
		})

		f.Run("hung", func(f *foundation.F) {
			f.SetTimeout(50 * time.Millisecond)

			select {}
		})
	})

	entries := recorder.Entries()

	// Should have: INFO (shutdown) + WARN (timeout)
	var warnFound bool

	for _, e := range entries {
		if e.Record.Level == slog.LevelWarn && e.Record.Message == "function did not exit within shutdown timeout" {
			warnFound = true
		}
	}

	if !warnFound {
		t.Fatal("expected a WARN log for shutdown timeout")
	}
}

func TestTimeoutParallelChild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Released when the test ends: every goroutine in a synctest bubble must
		// exit, including the ones the framework abandoned.
		hang := make(chan struct{})
		defer close(hang)

		// A parallel child that ignores context cancellation should be timed out
		start := time.Now()

		foundation.Run("test", func(f *foundation.F) {
			f.Run("hung-parallel", func(f *foundation.F) {
				// Set before Parallel: once the parent is released, shutdown can
				// begin at any moment and reads the timeout when it reaches us.
				f.SetTimeout(100 * time.Millisecond)
				f.Parallel()

				// Intentionally ignore context cancellation
				<-hang
			})

			// This runs immediately because hung-parallel called Parallel()
			f.Shutdown("shutting down")
		})

		elapsed := time.Since(start)

		if want := 100 * time.Millisecond; elapsed != want {
			t.Fatalf("expected shutdown to take %v, took %v", want, elapsed)
		}
	})
}

func TestTimeoutDoesNotFireBeforeShutdown(t *testing.T) {
	// A function that completes normally should not trigger a timeout,
	// even with a very short timeout value.
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("test", func(f *foundation.F) {
		f.Run("fast", func(f *foundation.F) {
			f.SetTimeout(1 * time.Millisecond)

			// Do some work that takes longer than the timeout
			time.Sleep(50 * time.Millisecond)
		})
	})

	entries := recorder.Entries()

	// No timeout warning should be logged since the context was never cancelled
	for _, e := range entries {
		if e.Record.Level == slog.LevelWarn {
			t.Fatalf("unexpected WARN log: %q", e.Record.Message)
		}
	}
}

func TestTimeoutCustomValue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Released when the test ends: every goroutine in a synctest bubble must
		// exit, including the ones the framework abandoned.
		hang := make(chan struct{})
		defer close(hang)

		// Verify that a custom timeout is respected — the function should be
		// abandoned after the custom timeout, not the default 30s.
		start := time.Now()

		foundation.Run("test", func(f *foundation.F) {
			f.Run("trigger", func(f *foundation.F) {
				f.Parallel()

				time.Sleep(10 * time.Millisecond)
				f.Shutdown("done")
			})

			f.Run("hung", func(f *foundation.F) {
				f.SetTimeout(50 * time.Millisecond)

				// Block forever
				<-hang
			})
		})

		elapsed := time.Since(start)

		// 10ms until the trigger fires, then the 50ms timeout, not the 30s default.
		if want := 60 * time.Millisecond; elapsed != want {
			t.Fatalf("expected shutdown to take %v, took %v", want, elapsed)
		}
	})
}

func TestTimeoutsOfSiblingsAreSequential(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Released when the test ends: every goroutine in a synctest bubble must
		// exit, including the ones the framework abandoned.
		hang := make(chan struct{})
		defer close(hang)

		// Each sibling is stopped in turn and gets its own timeout, so two hung
		// siblings take roughly the sum of their timeouts, not the longest.
		start := time.Now()

		foundation.Run("test", func(f *foundation.F) {
			for _, name := range []string{"hung-a", "hung-b"} {
				f.Run(name, func(f *foundation.F) {
					f.SetTimeout(50 * time.Millisecond)
					f.Parallel()

					<-hang
				})
			}

			f.Shutdown("done")
		})

		elapsed := time.Since(start)

		if want := 100 * time.Millisecond; elapsed != want {
			t.Fatalf("expected shutdown to take %v (2 x 50ms), took %v", want, elapsed)
		}
	})
}

func TestRunDoesNotExitOnNormalCompletion(t *testing.T) {
	exit := installExit(t)

	foundation.Run("test", func(f *foundation.F) {
		f.Run("child", func(f *foundation.F) {})
	})

	assertExitCodes(t, exit)
}

func TestRunDoesNotExitAfterShutdown(t *testing.T) {
	exit := installExit(t)

	foundation.Run("test", func(f *foundation.F) {
		f.Run("parallel", func(f *foundation.F) {
			f.Parallel()

			<-f.Context().Done()
		})

		f.Shutdown("done")
	})

	assertExitCodes(t, exit)
}

func TestRunExitsWithOneAfterFail(t *testing.T) {
	exit := installExit(t)

	foundation.Run("test", func(f *foundation.F) {
		f.Run("failing", func(f *foundation.F) {
			f.Fail(errors.New("boom"))
		})
	})

	assertExitCodes(t, exit, 1)
}

func TestRunExitsWithOneAfterFailInRoot(t *testing.T) {
	exit := installExit(t)

	foundation.Run("test", func(f *foundation.F) {
		f.Fail(errors.New("boom"))
	})

	assertExitCodes(t, exit, 1)
}

func TestRunExitsWithOneAfterFailInNestedRun(t *testing.T) {
	exit := installExit(t)

	foundation.Run("test", func(f *foundation.F) {
		f.Run("a", func(f *foundation.F) {
			f.Run("b", func(f *foundation.F) {
				f.Fail(errors.New("boom"))
			})
		})
	})

	assertExitCodes(t, exit, 1)
}

func TestRunExitsWithOneAfterPanic(t *testing.T) {
	exit := installExit(t)

	foundation.Run("test", func(f *foundation.F) {
		f.Run("panicking", func(f *foundation.F) {
			panic("boom")
		})
	})

	assertExitCodes(t, exit, 1)
}

func TestRunExitsWithOneAfterFailInParallelChild(t *testing.T) {
	exit := installExit(t)

	foundation.Run("test", func(f *foundation.F) {
		f.Run("parallel", func(f *foundation.F) {
			f.Parallel()

			f.Fail(errors.New("boom"))
		})

		// Keeps the root alive until the parallel child has failed.
		<-f.Context().Done()
	})

	assertExitCodes(t, exit, 1)
}

func TestRunExitsWithOneAfterFailDuringShutdown(t *testing.T) {
	exit := installExit(t)

	// The shutdown is intentional, so on its own it would exit 0. A node that
	// fails while draining must still turn it into a failure, even though it
	// isn't the failure that triggered the shutdown.
	foundation.Run("test", func(f *foundation.F) {
		f.Run("drains badly", func(f *foundation.F) {
			f.Parallel()

			<-f.Context().Done()

			f.Fail(errors.New("failed to drain"))
		})

		f.Shutdown("done")
	})

	assertExitCodes(t, exit, 1)
}

func TestRunExitsOnceAfterMultipleFailures(t *testing.T) {
	exit := installExit(t)

	foundation.Run("test", func(f *foundation.F) {
		for _, name := range []string{"a", "b", "c"} {
			f.Run(name, func(f *foundation.F) {
				f.Parallel()

				f.Fail(errors.New("boom"))
			})
		}

		<-f.Context().Done()
	})

	assertExitCodes(t, exit, 1)
}

func TestRunExitsAfterEverythingHasStopped(t *testing.T) {
	var ev events

	exit := foundationtest.NewExitRecorder()
	exit.OnExit = func(int) { ev.add("exit") }
	exit.Install(t)

	foundation.Run("test", func(f *foundation.F) {
		f.Cleanup(func() { ev.add("root:cleanup") })

		stoppable(f, &ev, "a", nil)

		f.Fail(errors.New("boom"))
	})

	// The process must only be ended once every function has stopped and every
	// cleanup has run.
	assertEvents(t, ev.get(), []string{"a:stop", "a:cleanup", "root:cleanup", "exit"})
}

func TestShutdownTimeoutDoesNotChangeExitCode(t *testing.T) {
	exit := installExit(t)

	foundation.Run("test", func(f *foundation.F) {
		f.Run("hung", func(f *foundation.F) {
			f.SetTimeout(20 * time.Millisecond)
			f.Parallel()

			select {}
		})

		f.Shutdown("done")
	})

	// A slow drain is logged as a warning, it is not a failure.
	assertExitCodes(t, exit)
}

// TestHelperProcess is not a real test: it is the program
// TestRunEndsProcessWithStatusOneOnFail runs in a subprocess, where the real
// exit function is in place.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("FOUNDATION_EXIT_HELPER") != "1" {
		t.Skip("helper process for TestRunEndsProcessWithStatusOneOnFail")
	}

	foundation.Run("helper", func(f *foundation.F) {
		f.Cleanup(func() { os.Stdout.WriteString("cleanup\n") })

		f.Fail(errors.New("boom"))
	})

	// Only reached if Run returned instead of ending the process.
	os.Stdout.WriteString("returned\n")
}

func TestRunEndsProcessWithStatusOneOnFail(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "FOUNDATION_EXIT_HELPER=1")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = cmd.Process.Kill() })

	var lines []string

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	err = cmd.Wait()

	var exitErr interface{ ExitCode() int }
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("expected the process to exit with status 1, got err=%v", err)
	}

	// Cleanups ran, and nothing after Run did.
	if !slices.Equal(lines, []string{"cleanup"}) {
		t.Fatalf("unexpected output: %q", lines)
	}

}

func TestFailDuringShutdownIsLogged(t *testing.T) {
	installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("test", func(f *foundation.F) {
		f.Run("drains badly", func(f *foundation.F) {
			f.Parallel()

			<-f.Context().Done()

			f.Fail(errors.New("failed to drain"))
		})

		f.Shutdown("done")
	})

	// Only the first shutdown trigger is logged by shutdown itself, so a
	// failure while draining needs its own log line or it would vanish.
	var logged bool

	for _, e := range recorder.Entries() {
		if e.Record.Message != "failure during shutdown" {
			continue
		}

		e.Record.Attrs(func(a slog.Attr) bool {
			if a.Key == "err" && a.Value.String() == "failed to drain" {
				logged = true
			}

			return true
		})
	}

	if !logged {
		t.Fatal("expected the failure during shutdown to be logged")
	}
}

func explode() {
	panic("boom")
}

// A panic is logged with the stack it happened on, so it can be found, not only
// the value it carried.
func TestAPanicIsLoggedWithItsStack(t *testing.T) {
	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("svc", func(f *foundation.F) {
		f.Run("worker", func(*foundation.F) {
			explode()
		})
	})

	assertExitCodes(t, exit, 1)

	var found bool

	for _, l := range loggedEntries(recorder) {
		if l.msg != "shutting down due to failure" {
			continue
		}

		found = true

		if !strings.Contains(l.attrs["err"], "panic: boom") {
			t.Errorf("err = %q, want it to keep the panic value", l.attrs["err"])
		}

		if !strings.Contains(l.attrs["stack"], "explode") {
			t.Errorf("stack should name the panicking function, got %q", l.attrs["stack"])
		}
	}

	if !found {
		t.Fatal("expected the failure to be logged")
	}
}

// A failure that isn't a panic has no stack to log.
func TestAFailureHasNoStack(t *testing.T) {
	installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("svc", func(f *foundation.F) {
		f.Fail(errors.New("broken"))
	})

	for _, l := range loggedEntries(recorder) {
		if _, ok := l.attrs["stack"]; ok {
			t.Fatalf("Fail should not log a stack, got %q", l.attrs["stack"])
		}
	}
}

func TestStringIsThePathOfTheFunction(t *testing.T) {
	var root, nested, dotted string

	foundation.Run("svc", func(f *foundation.F) {
		root = f.String()

		f.Run("http.server", func(f *foundation.F) {
			dotted = f.String()

			f.Run("listener", func(f *foundation.F) {
				nested = f.String()
			})
		})
	})

	if root != "svc" {
		t.Fatalf("expected the root to print as its name, got %q", root)
	}

	if dotted != "svc/http.server" {
		t.Fatalf("expected a child to print its whole path, got %q", dotted)
	}

	if nested != "svc/http.server/listener" {
		t.Fatalf("expected a nested function to print its whole path, got %q", nested)
	}
}

func TestStringIsUsedWhenPrinting(t *testing.T) {
	var got []string

	foundation.Run("svc", func(f *foundation.F) {
		f.Run("http", func(f *foundation.F) {
			got = []string{
				fmt.Sprint(f),
				fmt.Sprintf("%v", f),
				//lint:ignore S1025 the %s verb is what is under test
				fmt.Sprintf("%s", f),
			}
		})
	})

	for _, s := range got {
		if s != "svc/http" {
			t.Fatalf("expected printing a function to give its path, got %q", s)
		}
	}
}

// Without a String method fmt prints every field of the struct, which reads
// state other goroutines are writing: the race detector catches it here.
func TestPrintingAFunctionIsSafeWhileItRuns(t *testing.T) {
	foundation.Run("svc", func(f *foundation.F) {
		var wg sync.WaitGroup

		wg.Go(func() {
			for range 200 {
				f.Cleanup(func() {})
			}
		})

		for range 200 {
			_ = fmt.Sprint(f)
		}

		wg.Wait()
	})
}

func TestPathIsTheSegmentsOfTheFunction(t *testing.T) {
	var root, nested []string

	foundation.Run("svc", func(f *foundation.F) {
		root = f.Path()

		f.Run("http.server", func(f *foundation.F) {
			nested = f.Path()
		})
	})

	if want := []string{"svc"}; !slices.Equal(root, want) {
		t.Fatalf("expected %v, got %v", want, root)
	}

	// Segments are the names given to Run, so a dot stays inside one segment.
	if want := []string{"svc", "http.server"}; !slices.Equal(nested, want) {
		t.Fatalf("expected %v, got %v", want, nested)
	}
}

func TestPathReturnsACopy(t *testing.T) {
	foundation.Run("svc", func(f *foundation.F) {
		f.Run("http", func(f *foundation.F) {
			f.Path()[0] = "changed"

			if got := f.String(); got != "svc/http" {
				t.Fatalf("expected changing the returned path to leave the function alone, got %q", got)
			}

			if got := f.Path(); !slices.Equal(got, []string{"svc", "http"}) {
				t.Fatalf("expected the path to be unchanged, got %v", got)
			}
		})
	})
}

// The pre-built components stop in the reverse of the order they were started:
// the application server drains first, then the health server (so probes stay
// answerable for the whole drain), and the signal handler goes last.
func TestComponentsStopInReverseOrder(t *testing.T) {
	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("SVC_HEALTH_ADDR", "127.0.0.1:0")
	t.Setenv("SVC_HTTP_SERVER_BLOCK", "true")

	foundation.Run("svc", func(f *foundation.F) {
		interrupt.Signals(f)
		health.Server(f)
		transport.Server(f, http.NewServeMux(), transport.WithListener(ln))

		f.Shutdown("test")
	})

	// The "shutdown complete" log is written by each http.server node as it
	// finishes stopping, tagged with the node's path.
	var done []string

	for _, e := range recorder.Entries() {
		if e.Record.Message != "shutdown complete" {
			continue
		}

		// "fn" is attached by F.Log via With, so it is a handler-level attr.
		for _, a := range e.Attrs {
			if a.Key == "fn" {
				done = append(done, a.Value.String())
			}
		}
	}

	want := []string{"svc/http.server", "svc/health"}

	if !slices.Equal(done, want) {
		t.Fatalf("unexpected stop order\n got: %v\nwant: %v", done, want)
	}
}

// A cleanup that panics is a failure like a function that panics: it is logged
// with its stack and fails the run, and the cleanups registered before it still
// run, so one bad cleanup can't leave the rest of what the program holds open.
func TestPanickingCleanupFailsTheRunAndTheRestStillRun(t *testing.T) {
	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var order []string

	foundation.Run("svc", func(f *foundation.F) {
		f.Cleanup(func() { order = append(order, "first") })
		f.Cleanup(func() { panic("cleanup went wrong") })
		f.Cleanup(func() { order = append(order, "last") })
	})

	if want := []string{"last", "first"}; !slices.Equal(order, want) {
		t.Fatalf("cleanups ran %v, want %v", order, want)
	}

	if got := exit.Codes(); !slices.Equal(got, []int{1}) {
		t.Fatalf("exit codes = %v, want [1]", got)
	}

	var stacked bool

	for _, l := range loggedEntries(recorder) {
		if l.level == slog.LevelError && l.attrs["stack"] != "" && strings.Contains(l.attrs["err"], "cleanup went wrong") {
			stacked = true
		}
	}

	if !stacked {
		t.Fatal("the panic in a cleanup was not logged as an error with its stack")
	}
}

// A cleanup that calls Fail (which ends its goroutine) fails the run, and the
// cleanups registered before it still run.
func TestFailingCleanupDoesNotSkipTheOthers(t *testing.T) {
	exit := installExit(t)

	var order []string

	foundation.Run("svc", func(f *foundation.F) {
		f.Cleanup(func() { order = append(order, "A") })
		f.Cleanup(func() { order = append(order, "B") })
		f.Cleanup(func() { f.Fail(errors.New("cleanup failed")) })
	})

	if want := []string{"B", "A"}; !slices.Equal(order, want) {
		t.Fatalf("cleanups ran %v, want %v", order, want)
	}

	if got := exit.Codes(); !slices.Equal(got, []int{1}) {
		t.Fatalf("exit codes = %v, want [1]", got)
	}
}

// A cleanup that panics in a child doesn't take the parent's cleanups with it.
func TestPanickingCleanupInAChildDoesNotSkipTheParents(t *testing.T) {
	installExit(t)

	var ran bool

	foundation.Run("svc", func(f *foundation.F) {
		f.Cleanup(func() { ran = true })

		f.Run("child", func(f *foundation.F) {
			f.Cleanup(func() { panic("boom") })
		})
	})

	if !ran {
		t.Fatal("the parent's cleanup did not run after a child's cleanup panicked")
	}
}

// Calling Parallel twice is the same as calling it once.
func TestParallelTwiceIsHarmless(t *testing.T) {
	exit := installExit(t)

	var after bool

	foundation.Run("svc", func(f *foundation.F) {
		f.Run("child", func(f *foundation.F) {
			f.Parallel()
			f.Parallel()
		})

		after = true
	})

	if !after {
		t.Fatal("the parent did not carry on")
	}

	// It is not a panic that the framework recovered and counted as a failure.
	if got := exit.Codes(); len(got) != 0 {
		t.Fatalf("exit codes = %v, want none", got)
	}
}

// A failure with no error is still a failure, not a crash.
func TestFailWithNilErrorIsAFailure(t *testing.T) {
	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("svc", func(f *foundation.F) {
		f.Fail(nil)
	})

	if got := exit.Codes(); !slices.Equal(got, []int{1}) {
		t.Fatalf("exit codes = %v, want [1]", got)
	}

	failures := entriesNamed(recorder, "shutting down due to failure")
	if len(failures) != 1 {
		t.Fatal("the failure was not logged")
	}

	// A failure that says what happened, not a nil dereference that was recovered.
	if _, panicked := failures[0].attrs["stack"]; panicked || strings.Contains(failures[0].attrs["err"], "panic") {
		t.Fatalf("the failure was a panic: %v", failures[0].attrs)
	}
}

// A timeout that is not positive means the default, as it does for the other
// timeouts, rather than abandoning the function at once.
func TestSetTimeoutNotPositiveMeansTheDefault(t *testing.T) {
	installExit(t)

	for _, d := range []time.Duration{0, -1, -time.Hour} {
		foundation.Run("svc", func(f *foundation.F) {
			f.SetTimeout(d)

			if got := f.Timeout(); got != foundation.DefaultTimeout {
				t.Errorf("SetTimeout(%v): Timeout() = %v, want %v", d, got, foundation.DefaultTimeout)
			}
		})
	}
}
