package interrupt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"go.krak3n.dev/foundation/watch"
)

// newTrigger returns a [watch.Trigger] that sends an event of the kind when the
// process receives one of the signals. It has no opinion about which signals mean
// what: that SIGINT and SIGTERM shut a program down by default, and that a signal
// can't be in both kinds, are decided by [SignalFunc], which knows what it listens
// for. It refuses what is wrong for any signal watch: a kind that is not one, no
// signals, a signal that can't be caught (SIGKILL, SIGSTOP) and one that is not a
// [syscall.Signal]. On Windows only some signals are ever delivered.
//
// For a [Shutdown] the watch ends with the event and gives the signals back, so
// that a second one gets the default action of the system: the first asks
// politely, and the second force quits a shutdown that is slow. The same is true
// when the context is done.
//
// A signal that nothing is watching for gets its default action, which for SIGHUP
// and the others that are used to restart is to end the process. A program that
// restarts stops its watch and starts it again, so for a [Restart] the signals
// are still handled, and discarded, for a grace period of ten seconds after the
// watch ends because its context is done, and then given back. A watch started
// during the grace gets the signals as it would have, since they go to every
// handler, and each watch that ends extends the grace, so one that comes and goes
// for ever keeps one handler for each signal and no more. What arrives in the gap
// before the next watch starts is not kept, as the restart that is under way reads
// afresh anyway. A [Shutdown] gets no grace.
//
// It delivers through a [watch.Emitter], so it holds at most one event: a second
// restart while one waits is dropped, a shutdown replaces a waiting restart, and a
// shutdown is the last.
func newTrigger(kind Kind, sigs ...os.Signal) watch.Trigger {
	return &trigger{kind: kind, sigs: append([]os.Signal(nil), sigs...)}
}

type trigger struct {
	kind Kind
	sigs []os.Signal
}

func (s *trigger) Watch(ctx context.Context) (<-chan watch.Event, error) {
	if !s.kind.Valid() {
		return nil, errors.New("signals: a kind that is neither restart nor shutdown")
	}

	if len(s.sigs) == 0 {
		return nil, errors.New("signals: no signals")
	}

	for i, sig := range s.sigs {
		sys, ok := sig.(syscall.Signal)

		switch {
		case !ok:
			return nil, fmt.Errorf("signals: signal %d is not a syscall.Signal", i+1)
		case uncatchable(sys):
			return nil, fmt.Errorf("signals: signal %d can't be caught", i+1)
		}
	}

	ch := make(chan os.Signal, 8)

	signal.Notify(ch, s.sigs...)

	e := watch.NewEmitter(nil)

	go func() {
		defer e.End()
		defer signal.Stop(ch)

		for {
			select {
			case <-ctx.Done():
				// Held before the signals are given back, so there is no moment with
				// nothing handling them.
				if s.kind == Restart {
					holdSignals(s.sigs)
				}

				return
			case sig := <-ch:
				if s.kind == Shutdown {
					// The signals are given back before the shutdown is delivered, so that
					// by the time anyone has seen it a second signal gets the default
					// action: there is no window in which it is swallowed.
					signal.Stop(ch)
				}

				e.Post(watch.Event{Kind: s.kind, Reason: "signal: " + sig.String()})

				if s.kind == Shutdown {
					return
				}
			}
		}
	}()

	return e.Events(), nil
}

// signalsGrace is how long a restart trigger that has ended keeps handling its
// signals. It is a variable only so that the tests can shorten it.
var signalsGrace = 10 * time.Second

// graces are the signals being held after a restart trigger ended, one handler for
// each however many there have been, guarded by graceMu.
var (
	graceMu sync.Mutex
	graces  = map[syscall.Signal]*grace{}
)

// A grace is the handler for one signal, which discards what it gets until time
// has passed with no watch extending it.
type grace struct {
	ch    chan os.Signal
	until time.Time
}

// holdSignals keeps handling sigs, discarding them, for the grace period from now.
func holdSignals(sigs []os.Signal) {
	graceMu.Lock()
	defer graceMu.Unlock()

	until := time.Now().Add(signalsGrace)

	for _, sig := range sigs {
		sys, ok := sig.(syscall.Signal)
		if !ok {
			continue
		}

		g := graces[sys]
		if g == nil {
			g = &grace{ch: make(chan os.Signal, 8)}

			signal.Notify(g.ch, sys)

			graces[sys] = g

			go g.run(sys)
		}

		g.until = until
	}
}

// run discards the signal until the grace is over, which a later watch ending can
// put off, and then gives the signal back.
func (g *grace) run(sig syscall.Signal) {
	timer := time.NewTimer(signalsGrace)
	defer timer.Stop()

	for {
		graceMu.Lock()
		wait := time.Until(g.until)

		if wait <= 0 {
			delete(graces, sig)
			signal.Stop(g.ch)
			graceMu.Unlock()

			return
		}

		graceMu.Unlock()

		timer.Reset(wait)

		select {
		case <-g.ch:
			// Discarded: a restart is under way.
		case <-timer.C:
		}
	}
}
