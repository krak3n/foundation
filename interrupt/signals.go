// Package interrupt asks the program to shut down, or to restart, when it receives
// an operating system signal: shutdown on SIGINT and SIGTERM by default, restart
// on the signals it is told to.
package interrupt

import (
	"errors"
	"os"
	"slices"
	"syscall"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/watch"
)

// An Option configures [Signals] and [SignalFunc], such as which signals to
// listen for. Options are applied in the order they are given.
type Option interface {
	apply(*config)
}

// Options is a slice of [Option] that itself implements the [Option] interface.
// Each non-nil option in the slice is applied in order.
type Options []Option

func (o Options) apply(c *config) {
	for _, opt := range o {
		if opt != nil {
			opt.apply(c)
		}
	}
}

type optionFunc func(*config)

func (f optionFunc) apply(c *config) { f(c) }

// A Kind is what a signal asks the program to do: [Shutdown] or [Restart]. It is
// the kind of a [watch.Event], so a program need not import watch for it.
type Kind = watch.Kind

const (
	// Shutdown ends the program, in the usual order.
	Shutdown = watch.Shutdown

	// Restart starts the program again (see [foundation.F.Restart]).
	Restart = watch.Restart
)

// WithSignals adds signals that ask for kind, after those already there. A kind
// that is neither [Shutdown] nor [Restart] stops startup.
func WithSignals(kind Kind, signals ...os.Signal) Option {
	// Copied now, so changing the slice later changes nothing.
	signals = slices.Clone(signals)

	return optionFunc(func(c *config) {
		if set := c.set(kind); set != nil {
			*set = append(*set, signals...)
		}
	})
}

// SetSignals replaces the signals that ask for kind, the defaults included, with
// signals.
func SetSignals(kind Kind, signals ...os.Signal) Option {
	// Copied now, so changing the slice later changes nothing.
	signals = slices.Clone(signals)

	return optionFunc(func(c *config) {
		if set := c.set(kind); set != nil {
			*set = slices.Clone(signals)
		}
	})
}

// WithoutSignals removes signals from the ones that ask for kind. A signal is
// matched by its value, so syscall.SIGINT removes syscall.SIGINT.
func WithoutSignals(kind Kind, signals ...os.Signal) Option {
	return optionFunc(func(c *config) {
		if set := c.set(kind); set != nil {
			*set = slices.DeleteFunc(*set, func(s os.Signal) bool {
				return slices.Contains(signals, s)
			})
		}
	})
}

// WithShutdownSignals is WithSignals(Shutdown, signals...).
func WithShutdownSignals(signals ...os.Signal) Option { return WithSignals(Shutdown, signals...) }

// WithRestartSignals is WithSignals(Restart, signals...).
func WithRestartSignals(signals ...os.Signal) Option { return WithSignals(Restart, signals...) }

// SetShutdownSignals is SetSignals(Shutdown, signals...).
func SetShutdownSignals(signals ...os.Signal) Option { return SetSignals(Shutdown, signals...) }

// SetRestartSignals is SetSignals(Restart, signals...).
func SetRestartSignals(signals ...os.Signal) Option { return SetSignals(Restart, signals...) }

// SignalFunc returns a [foundation.Func] that listens for signals and acts on
// them: a shutdown signal (SIGINT and SIGTERM unless the options say otherwise)
// shuts the program down, and a restart signal restarts it. There are no restart
// signals by default: SIGHUP is also what a closing terminal sends, so asking for
// a restart on it is a choice ([WithRestartSignals]). A restart is refused as
// [foundation.F.Restart] refuses it, for instance when it is a dry run.
//
// Each kind is a watcher (see [foundation.F.Watch]) for the signals, so it is
// stopped in the usual order, usually last as it is usually started first. A
// shutdown watcher gives its signals back as soon as the first arrives: a second
// Ctrl-C gets the default action and force quits a shutdown that is taking too
// long. A restart watcher keeps handling its signals for a short grace after it
// ends, so one that arrives between two instances is not the default action. A
// shutdown always wins over a restart. Nothing is started in a dry run, which
// logs that it skipped.
//
// A mistake stops startup, since a program that can't be asked to stop is better
// not started: no shutdown signals at all, a signal that can't be caught such as
// SIGKILL or SIGSTOP, one that isn't a [syscall.Signal], a signal in both kinds
// (so a restart on SIGINT or SIGTERM needs them moved out of the shutdown ones), or a kind that is
// neither.
func SignalFunc(opts ...Option) foundation.Func {
	cfg := newConfig()

	Options(opts).apply(&cfg)

	return func(f *foundation.F) {
		// A dry run installs no handler and waits for no signal.
		if f.DryRun() {
			f.Skip("dry run")
		}

		if cfg.badKind {
			f.Fail(errors.New("signals: a kind that is neither shutdown nor restart"))
		}

		// The same signal asking for both would fire both, and the shutdown would win:
		// a mistake, not a choice.
		for _, s := range cfg.restart {
			if slices.Contains(cfg.shutdown, s) {
				f.Fail(errors.New("signals: a signal can't be both a shutdown and a restart"))
			}
		}

		f.Watch(newTrigger(Shutdown, cfg.shutdown...))

		if len(cfg.restart) > 0 {
			f.Watch(newTrigger(Restart, cfg.restart...))
		}

		// The watchers are functions of their own, and keep the process from exiting
		// as this one did: the parent need not wait for this to return.
		f.Parallel()
	}
}

// Signals runs [SignalFunc] with the options, as a function called "signals", so
// that signal handling is set up in one line:
//
//	interrupt.Signals(f)
func Signals(f *foundation.F, opts ...Option) {
	f.Run("signals", SignalFunc(opts...))
}

type config struct {
	shutdown []os.Signal
	restart  []os.Signal

	// badKind is set by an option for a kind that is neither; it is reported when
	// the function starts.
	badKind bool
}

func newConfig() config {
	return config{shutdown: []os.Signal{syscall.SIGINT, syscall.SIGTERM}}
}

// set returns the signals of a kind, or nil, noting the mistake, for one that
// isn't.
func (c *config) set(kind Kind) *[]os.Signal {
	switch kind {
	case Shutdown:
		return &c.shutdown
	case Restart:
		return &c.restart
	}

	c.badKind = true

	return nil
}
