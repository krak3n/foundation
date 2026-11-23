package watch

import (
	"context"
	"sync"
	"sync/atomic"
)

// Manual returns a trigger and the function that fires it, for code that decides
// for itself when to restart or shut down, and for tests. Every [Trigger.Watch] of the
// trigger is its own, and gets what is fired while it watches. Firing is safe for
// concurrent use, never blocks, and does nothing after a shutdown. A kind that is not
// [Restart] or [Shutdown] is dropped.
func Manual() (Trigger, func(kind Kind, reason string)) {
	m := &manualTrigger{subs: map[*Emitter]struct{}{}}

	return m, m.fire
}

type manualTrigger struct {
	mu   sync.Mutex
	subs map[*Emitter]struct{}
}

func (m *manualTrigger) Watch(ctx context.Context) (<-chan Event, error) {
	var (
		o    *Emitter
		stop atomic.Pointer[func() bool]
	)

	o = NewEmitter(func() {
		// The watch is over, by a shutdown or by the context: the context, which may
		// live as long as the process, has no more to do for it.
		if s := stop.Load(); s != nil {
			(*s)()
		}

		m.mu.Lock()
		delete(m.subs, o)
		m.mu.Unlock()
	})

	m.mu.Lock()
	m.subs[o] = struct{}{}
	m.mu.Unlock()

	release := context.AfterFunc(ctx, o.End)

	stop.Store(&release)

	// It may have ended before the registration was kept (a shutdown fired at once, a
	// context that was already done), and then nothing else will let it go.
	if o.isClosed() {
		release()
	}

	return o.out, nil
}

func (m *manualTrigger) fire(kind Kind, reason string) {
	m.mu.Lock()

	subs := make([]*Emitter, 0, len(m.subs))
	for o := range m.subs {
		subs = append(subs, o)
	}

	m.mu.Unlock()

	for _, o := range subs {
		o.Post(Event{Kind: kind, Reason: reason})
	}
}
