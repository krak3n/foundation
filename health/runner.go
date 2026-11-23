package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"go.krak3n.dev/foundation/health/monitor"
	"go.krak3n.dev/foundation/internal/safetext"
)

// defaultProbeTimeout is how long a probe is given to answer.
const defaultProbeTimeout = 5 * time.Second

// A runner runs probes for health requests, with three protections that a probe
// called directly from a request handler doesn't have:
//
//   - A probe is given a deadline, and a request stops waiting for it when that
//     passes, so a probe that never returns can't hold requests for ever.
//   - Only one run of each probe is in flight at a time. Requests that arrive
//     while it is running share its result, and a probe that never returns is
//     not started again, so a flood of requests can't leave a goroutine behind
//     for each one, or multiply the load on what the probe checks.
//   - A probe that panics is a failed probe, not a crashed process: panics in
//     goroutines other than the handler's are not recovered by net/http.
//
// It also fails closed: a probe has to say it is healthy, with no error.
type runner struct {
	timeout time.Duration

	mu       sync.Mutex
	inflight map[string]*run

	// last is the most recent status of each probe, so that a failure is logged
	// when it starts and not on every check.
	last map[string]monitor.Status
}

// A run is one execution of a probe, whose result is shared by every request
// that arrived while it was running.
type run struct {
	done   chan struct{}
	status monitor.Status
	err    error
}

func newRunner(timeout time.Duration) *runner {
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}

	return &runner{timeout: timeout, inflight: make(map[string]*run), last: make(map[string]monitor.Status)}
}

// check returns the result of probe, starting a run if there isn't one, and
// waiting for at most the timeout, or until ctx is done. The status is always
// either healthy or unhealthy.
func (r *runner) check(ctx context.Context, probe monitor.Probe) (monitor.Status, error) {
	name := probe.Name()

	r.mu.Lock()

	call, running := r.inflight[name]
	if !running {
		call = &run{done: make(chan struct{})}
		r.inflight[name] = call

		go r.execute(name, probe, call)
	}

	r.mu.Unlock()

	timer := time.NewTimer(r.timeout)
	defer timer.Stop()

	select {
	case <-call.done:
		return call.status, call.err
	case <-timer.C:
		err := fmt.Errorf("probe timed out after %s", r.timeout)

		r.note(name, monitor.StatusUnhealthy, err, "")

		return monitor.StatusUnhealthy, err
	case <-ctx.Done():
		return monitor.StatusUnhealthy, ctx.Err()
	}
}

// execute runs probe and records the outcome in call.
func (r *runner) execute(name string, probe monitor.Probe, call *run) {
	// What is reported if the probe doesn't get as far as returning, such as when
	// it calls runtime.Goexit.
	call.status, call.err = monitor.StatusUnhealthy, errors.New("probe did not complete")

	// The stack of a panic, for the log only: the response must not show it.
	var stack string

	defer func() {
		if rec := recover(); rec != nil {
			call.status, call.err = monitor.StatusUnhealthy, fmt.Errorf("probe panicked: %v", rec)
			stack = string(debug.Stack())
		}

		r.note(name, call.status, call.err, stack)

		// Removed first, so a request that arrives once the result is out starts a
		// fresh run.
		r.mu.Lock()
		delete(r.inflight, name)
		r.mu.Unlock()

		close(call.done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()

	status, err := probe.Status(ctx)

	switch {
	case status == monitor.StatusHealthy && err == nil:
		call.status, call.err = monitor.StatusHealthy, nil
	case err != nil:
		call.status, call.err = monitor.StatusUnhealthy, err
	case status == monitor.StatusUnhealthy:
		call.status, call.err = monitor.StatusUnhealthy, nil
	default:
		call.status, call.err = monitor.StatusUnhealthy, errors.New("status unknown")
	}
}

// maxErrText is the most of the reason a probe failed that is logged, in bytes.
const maxErrText = 8 << 10

// note records the latest status of a probe and logs when it changes for the
// worse or for the better: the reason a probe fails is kept out of the health
// response by default, so this is where an operator finds it.
func (r *runner) note(name string, status monitor.Status, err error, stack string) {
	r.mu.Lock()
	previous, seen := r.last[name]
	r.last[name] = status
	r.mu.Unlock()

	switch {
	case status != monitor.StatusHealthy && (!seen || previous == monitor.StatusHealthy):
		attrs := []any{slog.String("probe", name)}
		if err != nil {
			attrs = append(attrs, slog.String("err", safetext.Clip(err.Error(), maxErrText)))
		}

		if stack != "" {
			attrs = append(attrs, slog.String("stack", stack))
		}

		slog.Warn("health probe failed", attrs...)
	case status == monitor.StatusHealthy && seen && previous != monitor.StatusHealthy:
		slog.Info("health probe recovered", slog.String("probe", name))
	}
}
