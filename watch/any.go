package watch

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Any returns a trigger that is all of the triggers at once: what any of them says
// is said, the strongest of what is waiting wins, and a [Shutdown] from any of them
// ends the whole and tells the others to stop watching. It is over when they all
// are. Events that are not a valid kind are dropped.
//
// It is an error to give none, a nil one, or more than [MaxTriggers]. If one fails
// to start the others that had are stopped and nothing is left running.
func Any(triggers ...Trigger) Trigger {
	return &anyTrigger{triggers: append([]Trigger(nil), triggers...)}
}

type anyTrigger struct {
	triggers []Trigger
}

func (a *anyTrigger) Watch(ctx context.Context) (<-chan Event, error) {
	switch {
	case len(a.triggers) == 0:
		return nil, errors.New("watch: Any was given no triggers")
	case len(a.triggers) > MaxTriggers:
		return nil, fmt.Errorf("watch: Any was given %d triggers, the limit is %d", len(a.triggers), MaxTriggers)
	}

	for i, t := range a.triggers {
		if isNil(t) {
			return nil, fmt.Errorf("watch: trigger %d is nil", i+1)
		}
	}

	ctx, cancel := context.WithCancel(ctx)

	sources := make([]<-chan Event, 0, len(a.triggers))

	for i, t := range a.triggers {
		src, err := t.Watch(ctx)

		switch {
		case err != nil:
			cancel()

			return nil, fmt.Errorf("watch: trigger %d: %w", i+1, err)
		case src == nil:
			cancel()

			return nil, fmt.Errorf("watch: trigger %d returned no channel", i+1)
		}

		sources = append(sources, src)
	}

	o := NewEmitter(cancel)

	var wg sync.WaitGroup

	for _, src := range sources {
		wg.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-src:
					if !ok {
						return
					}

					o.Post(ev)

					if ev.Kind == Shutdown {
						cancel()

						return
					}
				}
			}
		})
	}

	go func() {
		wg.Wait()
		o.End()
	}()

	return o.out, nil
}
