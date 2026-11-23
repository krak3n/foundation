package foundation

import (
	"fmt"
	"iter"
	"slices"
	"sync"

	"go.krak3n.dev/foundation/health/monitor"
)

// Probes provides an interface for managing health probes.
// It allows adding new probes and retrieving the current sequence of registered probes.
//
// The probes are the instance's: they are taken out when the instance has stopped
// completely, and what is added after that is ignored.
type Probes interface {
	Add(probes ...monitor.Probe)
	Seq() iter.Seq[monitor.Probe]
}

type probes struct {
	r *monitor.Registry

	// ended is set when the instance is over: nothing is added after it, and it
	// shows no probes, so a function that was abandoned can't reach anything.
	mu    sync.Mutex
	ended bool
}

// probesOf is the instance's probes as one function sees them: the same registry,
// but a probe that can't be added fails the function that tried.
type probesOf struct {
	*probes
	f *F
}

func (v probesOf) Add(probes ...monitor.Probe) { v.probes.add(v.f, probes...) }

func (p *probes) add(f *F, probes ...monitor.Probe) {
	p.mu.Lock()

	if p.ended {
		p.mu.Unlock()

		return
	}

	err := p.r.Add(probes...)

	p.mu.Unlock()

	if err != nil {
		f.Fail(fmt.Errorf("failed to add probes: %w", err))
	}
}

func (p *probes) Seq() iter.Seq[monitor.Probe] {
	p.mu.Lock()
	ended := p.ended
	p.mu.Unlock()

	if ended {
		return slices.Values([]monitor.Probe(nil))
	}

	return p.r.Seq()
}

// endProbes is called when the instance has stopped completely, and before the
// next one starts: its probes are taken out and it is sealed, whether or not it
// ever asked for them.
func (root *F) endProbes() {
	root.mu.Lock()

	if root.probes == nil {
		root.probes = &probes{r: &monitor.Registry{}}
	}

	p := root.probes

	root.mu.Unlock()

	p.mu.Lock()
	p.ended = true
	p.mu.Unlock()

	p.r.Clear()
}
