package foundation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"go.krak3n.dev/foundation/internal/nilcheck"
	"go.krak3n.dev/foundation/watch"
)

// maxWatchers is the most watchers one instance may start. A watcher is a
// goroutine and whatever its trigger holds, so it is bounded.
const maxWatchers = 64

// Watch starts a watcher for trigger as a function of its own, a child of f, and
// turns what the trigger says into calls on the program: a [watch.Restart] event
// is [F.Restart], and a [watch.Shutdown] event is [F.Shutdown]:
//
//	f.Watch(watch.Files([]string{"/etc/svc.json"}))
//
// Watch returns once the trigger has begun to watch, so what changes after it
// returns is not missed, and a watcher is not stopped when the function that
// started it returns: it is stopped with the rest, in the usual order, which for
// one that was started first is last. The trigger's context is cancelled when its
// turn comes, and it should release what it holds then.
//
// What the trigger says is advice. The reason is escaped and cut short, and is
// never the message of a log line, since it may have come from a file name that
// anyone who can write the directory chose. Events that are not a valid kind are
// dropped. A watch that ends on its own, which nothing asked for, is logged once
// as a warning and the program carries on.
//
// If the trigger can't start, or panics, or is nil, the program fails, as a
// program that thinks it is watching and isn't is worse than one that doesn't
// start. Nothing is started, and the trigger is not asked to watch, in a dry run
// or help (so one that could not start is not found by a dry run, but a nil one
// is), in a candidate, from an instance that has been replaced, or once the
// program is shutting down. There are at most 64 watchers in an instance.
func (f *F) Watch(trigger watch.Trigger) {
	if nilcheck.Is(trigger) {
		f.Fail(errors.New("watch: no trigger given"))
	}

	root := f.root
	p := root.proc

	if root.dryRun || root.helping {
		return
	}

	if p.final.Load() || root.stopping.Load() || p.target(root) != root {
		return
	}

	if root.watchers.Add(1) > maxWatchers {
		f.Fail(fmt.Errorf("watch: more than %d watchers in one instance", maxWatchers))
	}

	f.Run("watch", func(w *F) {
		events, err := startWatch(w.Context(), trigger)
		if err != nil {
			w.Fail(err)
		}

		// The parent carries on once the watch has begun.
		w.Parallel()

		w.dispatch(events)
	})
}

// dispatch turns the events of a watch into calls until the watch ends.
func (w *F) dispatch(events <-chan watch.Event) {
	ctx := w.Context()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				// Closed with nothing asking it to: the watch has gone away.
				if ctx.Err() == nil {
					w.Log().Warn("watch ended", slog.String("why", "the trigger stopped sending events"))
				}

				return
			}

			switch ev.Kind {
			case watch.Restart:
				w.Restart(ev.Reason)
			case watch.Shutdown:
				w.Shutdown("shutdown requested by a watch", slog.String("reason", clipReason(ev.Reason)))

				// A shutdown is the end of a watch.
				return
			}
		}
	}
}

// startWatch asks trigger to watch, and turns a failure of any kind, including a
// panic in code that isn't ours, into an error that is safe to log.
func startWatch(ctx context.Context, trigger watch.Trigger) (events <-chan watch.Event, err error) {
	defer func() {
		if r := recover(); r != nil {
			events, err = nil, errors.New("watch: the trigger panicked")
		}
	}()

	events, err = trigger.Watch(ctx)
	if err != nil {
		return nil, fmt.Errorf("watch: the trigger could not start: %s", clipReason(err.Error()))
	}

	if events == nil {
		return nil, errors.New("watch: the trigger returned no channel")
	}

	return events, nil
}
