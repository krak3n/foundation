package monitor

import "context"

// A Probe is a named health check that participates in one or more health check modes.
// Each probe has a [StatusChecker] that produces its Status when asked.
type Probe interface {
	Name() string
	Mode() Mode
	Status(ctx context.Context) (Status, error)
}

// A StatusChecker produces a Status when called.
// The health handler calls it when a health endpoint is requested, within a
// deadline, to find out the current status of the probe.
type StatusChecker interface {
	Status(context.Context) (Status, error)
}

// The StatusCheckerFunc type is an adapter to allow the use of ordinary functions
// as a [StatusChecker]. If f is a function with the appropriate signature,
// StatusCheckerFunc(f) is a [StatusChecker] that calls f.
type StatusCheckerFunc func(ctx context.Context) (Status, error)

// Status calls f(ctx).
func (f StatusCheckerFunc) Status(ctx context.Context) (Status, error) {
	return f(ctx)
}

// NewProbe creates a new Probe with the given name, mode and checker.
func NewProbe(name string, mode Mode, checker StatusChecker) Probe {
	return &probe{
		name:    name,
		mode:    mode,
		checker: checker,
	}
}

type probe struct {
	name    string
	mode    Mode
	checker StatusChecker
}

func (s *probe) Name() string {
	return s.name
}

func (s *probe) Mode() Mode {
	return s.mode
}

func (s *probe) Status(ctx context.Context) (Status, error) {
	return s.checker.Status(ctx)
}
