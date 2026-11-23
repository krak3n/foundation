package interrupt

import (
	"os"
	"time"

	"go.krak3n.dev/foundation/watch"
)

// Resolve returns the signals that options ask for, per kind, starting from the
// defaults, so that tests can compare what options mean without sending signals.
func Resolve(opts ...Option) (shutdown, restart []os.Signal) {
	cfg := newConfig()

	Options(opts).apply(&cfg)

	return cfg.shutdown, cfg.restart
}

// NewTrigger is the trigger for the signals that the shutdown and restart watchers
// are built on, so that tests can drive it without a program around it. It has no
// opinion about which signals mean what.
func NewTrigger(kind Kind, sigs ...os.Signal) watch.Trigger { return newTrigger(kind, sigs...) }

// SetSignalsGrace changes how long a restart trigger keeps handling its signals
// after it ends, for the tests that would otherwise wait for the real time. It is
// for a process of its own that sets it before anything watches.
func SetSignalsGrace(d time.Duration) { signalsGrace = d }
