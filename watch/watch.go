// Package watch says when something has changed, so that something else can act on
// it: restart, or shut down. A [Trigger] is a source of [Event]s, which are advice and
// nothing more. An event carries no data to trust, so whoever receives one reads
// again and checks for themselves, and an attacker who can cause events can only
// cause that.
//
// The built in triggers are [Files] and [Dir], which poll for changes to files and
// to the files in a directory, [Manual], for code and tests, and [Any], which joins
// others. They are chosen by the program in code and read nothing from the
// environment or from configuration. Operating system signals are not here: which
// ones shut a program down and which restart it is decided through package
// interrupt, which has the rules for them.
//
// The package is a leaf: it imports only the standard library, so a trigger can be
// written and published without the framework. It knows nothing of configuration
// either: a caller that reads some of the files of a directory says which with
// [WithFilter], and one whose paths are not for a log names them with [WithLabel].
//
// # Events
//
// An event is a [Restart] or a [Shutdown]. When more than one is waiting the stronger
// wins, so a shutdown is never lost behind a restart, and a shutdown is the end of a watch:
// it is delivered, the channel is closed, and what the watch held (a signal
// handler, a ticker) is released. A watch never blocks on a slow reader and never
// queues: what is waiting is at most one event.
//
// # Polling
//
// [Files] and [Dir] only ever look at files with stat. Nothing in them is opened
// or read, so a named pipe or a device where a file should be can't block them
// (the one thing opened is the directory [Dir] lists, after it has been seen to be
// one). A file is
// changed if it exists or not, its type and permissions, its size, its
// modification time, the file it is (so that one renamed over it counts, though it
// looks the same) or, where the system has it, the time its status changed, which
// a write can't set back.
package watch

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// A Kind says what to do.
type Kind int

const (
	// Restart asks for what is being watched to be started again, afresh.
	Restart Kind = iota + 1

	// Shutdown asks for it to be shut down, and is the end of a watch.
	Shutdown
)

// String names the kind. Anything else is shown as its number.
func (k Kind) String() string {
	switch k {
	case Restart:
		return "restart"
	case Shutdown:
		return "shutdown"
	}

	return fmt.Sprintf("kind(%d)", int(k))
}

// Valid reports whether k is [Restart] or [Shutdown]. The zero value is not a kind, and
// an event of any other kind is dropped by the triggers in this package.
func (k Kind) Valid() bool { return k == Restart || k == Shutdown }

// An Event says that something may have changed, and what to do about it.
type Event struct {
	Kind Kind

	// Reason says why, for a log: it is at most [MaxReason] bytes of UTF-8. It is
	// text the program chose, or, for a directory, names that passed the rules for
	// a name of a file of configuration; the receiver escapes it all the same.
	Reason string
}

// A Trigger is a source of events.
type Trigger interface {
	// Watch starts watching and returns once it has started, with a baseline: a
	// change made after it returns is not missed. Events come on the channel, which
	// is closed when ctx is done or after a [Shutdown]. A trigger that can't start
	// returns an error and no channel, and has nothing running.
	Watch(ctx context.Context) (<-chan Event, error)
}

// TriggerFunc is an adapter to allow the use of ordinary functions as [Trigger]s.
type TriggerFunc func(ctx context.Context) (<-chan Event, error)

// Watch calls f.
func (f TriggerFunc) Watch(ctx context.Context) (<-chan Event, error) {
	if f == nil {
		return nil, errors.New("watch: the trigger function is nil")
	}

	return f(ctx)
}

const (
	// MaxPaths is the most files that one [Files] trigger watches.
	MaxPaths = 1024

	// MinInterval is the shortest time between polls. A shorter one is an error,
	// not a longer one: a program that asked for it should know.
	MinInterval = 100 * time.Millisecond

	// DefaultInterval is the time between polls when none is given.
	DefaultInterval = time.Second

	// MaxInterval is the longest time between polls.
	MaxInterval = 7 * 24 * time.Hour

	// MaxDirFiles is the most files [Dir] looks at. A directory with more is one
	// state, which is reported when it is entered and left, not for every file.
	MaxDirFiles = 256

	// MaxReason is the most bytes of an [Event]'s Reason.
	MaxReason = 200

	// MaxLabel is the most bytes of a label given with [WithLabel].
	MaxLabel = 128

	// MaxTriggers is the most triggers [Any] joins.
	MaxTriggers = 64
)

// settings are what the options of [Files] and [Dir] set.
type settings struct {
	interval    time.Duration
	intervalSet bool

	exts    []string
	extsSet bool

	filter    func(name string) bool
	filterSet bool

	label    string
	labelSet bool
}

// An Option configures [Files] and [Dir]. It can only be made by this package:
// there is no OptionFunc, as the options are few and the set is closed.
type Option interface {
	apply(*settings)
}

type optionFunc func(*settings)

func (f optionFunc) apply(s *settings) { f(s) }

// WithInterval sets the time between polls, from [MinInterval] to [MaxInterval].
// The default is [DefaultInterval]. Watching more often costs a stat of every file
// each time.
func WithInterval(d time.Duration) Option {
	return optionFunc(func(s *settings) {
		s.interval = d
		s.intervalSet = true
	})
}

// WithExtensions restricts [Dir] to the files that end in one of the extensions,
// each with its dot (".json"), matched exactly. Without it every file that is not
// hidden counts. It has no effect on [Files].
func WithExtensions(exts ...string) Option {
	return optionFunc(func(s *settings) {
		s.exts = append([]string(nil), exts...)
		s.extsSet = true
	})
}

// WithFilter restricts [Dir] to the names that filter accepts, in addition to
// [WithExtensions]: it is how a caller says which files are its own, such as the
// ones a provider reads, which this package knows nothing about. It is given the
// name of an entry, never a path, for each visible entry of one listing of the
// directory (at most a bounded number), and it must be quick and not block. If it
// panics the watch ends. It has no effect on [Files].
func WithFilter(filter func(name string) bool) Option {
	return optionFunc(func(s *settings) {
		s.filter = filter
		s.filterSet = true
	})
}

// WithLabel names what is watched in a reason, in place of its path: a path that
// came from a configuration value, such as an environment variable, is not for
// a log, so whoever asked for the watch says what to call it ($SVC_CONFIG). The
// label is text of the caller's own, in code, and is checked when the watch starts:
// it must be from 1 to [MaxLabel] bytes of valid, printable UTF-8 (no control or
// formatting characters, no line or paragraph separators). With [Files] it needs
// exactly one path, as it could not say which of several changed.
func WithLabel(label string) Option {
	return optionFunc(func(s *settings) {
		s.label = label
		s.labelSet = true
	})
}

// checkLabel checks the label, if one was given. The reason it is wrong never
// repeats it, which may be what was wrong.
func (s settings) checkLabel() error {
	if !s.labelSet {
		return nil
	}

	if s.label == "" || len(s.label) > MaxLabel || !utf8.ValidString(s.label) {
		return fmt.Errorf("watch: the label must be from 1 to %d bytes of valid text", MaxLabel)
	}

	for _, r := range s.label {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return errors.New("watch: the label must not have control, formatting or separator characters in it")
		}
	}

	return nil
}

// named is how a reason says what changed: the label if there is one, else the path.
func (s settings) named(path string) string {
	if s.labelSet {
		return s.label
	}

	return path
}

func newSettings(opts []Option) settings {
	var s settings

	for _, opt := range opts {
		if !isNil(opt) {
			opt.apply(&s)
		}
	}

	return s
}

// pollInterval checks the interval and returns the one to use.
func (s settings) pollInterval() (time.Duration, error) {
	if !s.intervalSet {
		return DefaultInterval, nil
	}

	if s.interval < MinInterval || s.interval > MaxInterval {
		return 0, fmt.Errorf("watch: the interval must be from %v to %v", MinInterval, MaxInterval)
	}

	return s.interval, nil
}

// extensions checks the extensions and the filter of a [Dir].
func (s settings) extensions() error {
	if s.filterSet && s.filter == nil {
		return errors.New("watch: WithFilter was given no filter")
	}

	if !s.extsSet {
		return nil
	}

	if len(s.exts) == 0 {
		return errors.New("watch: WithExtensions was given no extensions")
	}

	for i, ext := range s.exts {
		if len(ext) < 2 || len(ext) > 16 || ext[0] != '.' || ext[1] == '.' || strings.ContainsAny(ext, "/\\\x00") || !printable(ext) {
			return fmt.Errorf("watch: extension %d is not one: write the dot and a name, such as .json", i+1)
		}
	}

	return nil
}

func printable(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}

	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}

	return true
}

// clip cuts a reason to [MaxReason] bytes, at a character boundary, and makes it
// valid UTF-8.
func clip(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}

	if len(s) <= MaxReason {
		return s
	}

	i := MaxReason

	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}

	return s[:i]
}

// An Emitter is the channel a trigger delivers on, and the rules every trigger
// shares, so that one written elsewhere need not write them again. It holds at most
// one event, so a trigger that posts as fast as it likes costs one event of memory
// and no goroutine: another restart while one is waiting is dropped, a shutdown
// replaces a restart that is waiting, and a shutdown is the end, after which the
// channel is closed and nothing more is delivered. An event that is not a valid kind
// is dropped, and a reason is made valid UTF-8 and cut at [MaxReason]. It is safe for
// concurrent use. Make one with [NewEmitter].
type Emitter struct {
	out chan Event

	mu     sync.Mutex
	closed bool

	// release is called once, without the lock, when the Emitter closes.
	release func()
}

// NewEmitter returns an Emitter. release, which may be nil, is called once, without
// the emitter's lock, when the emitter closes, whether by a shutdown or by [Emitter.End],
// so a trigger can let go of what it holds; it may use the emitter.
func NewEmitter(release func()) *Emitter {
	return &Emitter{out: make(chan Event, 1), release: release}
}

// Post delivers ev under the rules above. An event that is not a valid kind is
// dropped.
func (o *Emitter) Post(ev Event) {
	if !ev.Kind.Valid() {
		return
	}

	ev.Reason = clip(ev.Reason)

	o.mu.Lock()

	if o.closed {
		o.mu.Unlock()

		return
	}

	select {
	case o.out <- ev:
	default:
		// One is waiting, and it can only be a restart: a shutdown closes the Emitter.
		if ev.Kind == Shutdown {
			select {
			case <-o.out:
			default:
			}

			o.out <- ev
		}
	}

	if ev.Kind == Shutdown {
		o.closed = true

		close(o.out)
	}

	o.mu.Unlock()

	if ev.Kind == Shutdown {
		o.done()
	}
}

// End closes the emitter, if a shutdown hasn't, and does nothing if it has. Events
// posted after it are dropped.
func (o *Emitter) End() {
	o.mu.Lock()

	if o.closed {
		o.mu.Unlock()

		return
	}

	o.closed = true

	close(o.out)

	o.mu.Unlock()

	o.done()
}

// Events is the channel to return from a trigger's Watch. It is the same channel
// every time.
func (o *Emitter) Events() <-chan Event { return o.out }

// isClosed reports whether the Emitter has closed.
func (o *Emitter) isClosed() bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.closed
}

func (o *Emitter) done() {
	if o.release != nil {
		o.release()
	}
}

// isNil reports whether v is nil, including a nil pointer or function inside an
// interface, which would panic when it was used. It is a copy of
// internal/nilcheck.Is, kept here because this package imports only the standard
// library: a change to one is a change to the other.
func isNil(v any) bool {
	if v == nil {
		return true
	}

	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return rv.IsNil()
	default:
		return false
	}
}
