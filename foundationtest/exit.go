package foundationtest

import (
	"slices"
	"sync"
	"testing"

	"go.krak3n.dev/foundation/internal/exit"
)

// ExitRecorder captures the exit codes foundation would have ended the process
// with, so tests can assert on them without the test binary exiting.
type ExitRecorder struct {
	// OnExit, if set, is called with each exit code at the moment foundation
	// would have exited. It must be set before [ExitRecorder.Install].
	OnExit func(code int)

	mu    sync.Mutex
	codes []int
}

// NewExitRecorder creates a new [ExitRecorder].
func NewExitRecorder() *ExitRecorder {
	return &ExitRecorder{}
}

// Install replaces the process exit function used by foundation with the
// recorder and registers a [testing.TB.Cleanup] function to restore the
// original when the test completes. While installed, foundation does not exit.
func (r *ExitRecorder) Install(t testing.TB) {
	t.Helper()

	original := exit.Func

	exit.Func = func(code int) {
		r.mu.Lock()
		r.codes = append(r.codes, code)
		r.mu.Unlock()

		if r.OnExit != nil {
			r.OnExit(code)
		}
	}

	t.Cleanup(func() {
		exit.Func = original
	})
}

// Codes returns the exit codes recorded so far, in order.
func (r *ExitRecorder) Codes() []int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.codes)
}
