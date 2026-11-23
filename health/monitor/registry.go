package monitor

import (
	"fmt"
	"iter"
	"maps"
	"slices"
	"sync"
)

// A Registrar is responsible for registering probes.
type Registrar interface {
	// Add registers probes, all or none of them.
	Add(probes ...Probe) error

	// Seq returns the probes registered when it is called.
	Seq() iter.Seq[Probe]
}

// A Registry holds probes by name. It is safe for concurrent use, and its zero
// value is ready to use.
type Registry struct {
	mtx    sync.RWMutex
	probes map[string]Probe
}

// NewRegistry returns a Registry holding probes, which must have different names.
func NewRegistry(probes ...Probe) (*Registry, error) {
	r := &Registry{
		probes: make(map[string]Probe),
	}

	return r, r.Add(probes...)
}

// Add registers probes. Names must be unique, within probes and against those
// already registered. It is all or nothing: if any name clashes an error is
// returned and none are added.
func (r *Registry) Add(probes ...Probe) error {
	// Names are read before the lock, as they are the caller's code.
	names := make([]string, len(probes))

	for i, probe := range probes {
		names[i] = probe.Name()
	}

	r.mtx.Lock()
	defer r.mtx.Unlock()

	seen := make(map[string]struct{}, len(names))

	for _, name := range names {
		_, registered := r.probes[name]
		_, repeated := seen[name]

		if registered || repeated {
			return fmt.Errorf("probe with name %q already registered", name)
		}

		seen[name] = struct{}{}
	}

	if r.probes == nil {
		r.probes = make(map[string]Probe)
	}

	for i, probe := range probes {
		r.probes[names[i]] = probe
	}

	return nil
}

// Clear removes every probe, so the names can be used again. A sequence taken
// before is not changed.
func (r *Registry) Clear() {
	r.mtx.Lock()
	defer r.mtx.Unlock()

	clear(r.probes)
}

// Seq returns the probes registered when it is called, sorted by name. It is a
// snapshot, so probes added later, or while it is being ranged over, don't
// affect it, and it can be ranged over without holding any lock.
func (r *Registry) Seq() iter.Seq[Probe] {
	r.mtx.RLock()
	names := slices.Sorted(maps.Keys(r.probes))

	snapshot := make([]Probe, len(names))

	for i, name := range names {
		snapshot[i] = r.probes[name]
	}

	r.mtx.RUnlock()

	return slices.Values(snapshot)
}
