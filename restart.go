package foundation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"go.krak3n.dev/foundation/internal/safetext"
)

// maxReason is the most of a restart's reason that is logged, in bytes. The reason
// comes from whatever noticed the change, such as a file name.
const maxReason = 200

// RestartPolicy controls how fast the program restarts, however often something
// asks it to: an editor saves a file in four writes, a deploy touches fifty, and
// a program that restarted each time would spend its life starting. It is chosen
// in code with [WithRestartPolicy], never by whatever is being watched.
//
// A restart does not begin the moment it is asked for. Requests that arrive close
// together are one restart, which begins once they have stopped for Debounce, but
// no later than MaxDebounce after the first, so a source that never goes quiet
// still gets a restart. It does not begin sooner than MinInterval after the
// instance started (after the program started, for the first), so a program that
// asks to restart as soon as it starts is slowed, not spun. And at most Limit
// restarts begin in any Window: one that is due when the limit has been reached
// is held, not dropped, until the oldest of them is a Window old, so the last
// change is never lost, and an error is logged once for the hold.
//
// A field left zero has its default, so a policy can't switch a control off (only
// SkipValidation, which is explicit, turns the candidate off), and the values are
// bounded (Limit at most 1000, durations at most 24 hours): a
// mistake stops startup.
type RestartPolicy struct {
	// Debounce is how long requests must have stopped for. Default 250ms.
	Debounce time.Duration

	// MaxDebounce is the longest a restart is put off by new requests. Default 2s,
	// and not less than Debounce.
	MaxDebounce time.Duration

	// MinInterval is the shortest time an instance runs before it is restarted.
	// Default 1s.
	MinInterval time.Duration

	// Limit is the most restarts that begin in a Window. Default 5.
	Limit int

	// Window is the period Limit applies to. Default one minute.
	Window time.Duration

	// SkipValidation restarts without first trying the new instance as a candidate
	// (see [F.Restart]). It is for a program that can't safely run its root
	// function a second time, even in a dry run, and a start that fails then fails
	// the run. Default false: the candidate is on.
	SkipValidation bool
}

const (
	defaultDebounce    = 250 * time.Millisecond
	defaultMaxDebounce = 2 * time.Second
	defaultMinInterval = time.Second
	defaultLimit       = 5
	defaultWindow      = time.Minute

	maxPolicyDuration = 24 * time.Hour
	maxPolicyLimit    = 1000
)

// resolve returns p with the defaults in place of zeros.
func (p RestartPolicy) resolve() RestartPolicy {
	if p.Debounce == 0 {
		p.Debounce = defaultDebounce
	}

	if p.MaxDebounce == 0 {
		p.MaxDebounce = defaultMaxDebounce
	}

	if p.MinInterval == 0 {
		p.MinInterval = defaultMinInterval
	}

	if p.Limit == 0 {
		p.Limit = defaultLimit
	}

	if p.Window == 0 {
		p.Window = defaultWindow
	}

	return p
}

// check reports what is wrong with p, if anything, in a fixed order.
func (p RestartPolicy) check() error {
	var errs []error

	for _, field := range []struct {
		name string
		d    time.Duration
	}{
		{"Debounce", p.Debounce}, {"MaxDebounce", p.MaxDebounce}, {"MinInterval", p.MinInterval}, {"Window", p.Window},
	} {
		if field.d < 0 || field.d > maxPolicyDuration {
			errs = append(errs, fmt.Errorf("%s must be between 0 and %v", field.name, maxPolicyDuration))
		}
	}

	if p.Limit < 0 || p.Limit > maxPolicyLimit {
		errs = append(errs, fmt.Errorf("RestartPolicy.Limit must be between 0 and %d", maxPolicyLimit))
	}

	if r := p.resolve(); r.Debounce > r.MaxDebounce {
		errs = append(errs, errors.New("RestartPolicy.Debounce is longer than MaxDebounce"))
	}

	return joinMistakes(errs...)
}

// process is what outlives an instance of the root function: the run as a whole.
type process struct {
	// failed decides the exit status, and is set by [F.Fail] and a recovered panic
	// in any instance.
	failed atomic.Bool

	// final is set by a shutdown or a failure, and means no new instance starts.
	final atomic.Bool

	// refused is set once the refusal to restart has been logged, so that a
	// trigger that fires again and again can't fill a log, and leaked when a
	// candidate left something running, after which no restart is tried.
	refused atomic.Bool
	leaked  atomic.Bool

	// cause is the first thing that went wrong in a candidate, for the line that
	// says its restart was rejected. It is only kept for a candidate's process, and
	// is already escaped and cut short.
	cause atomic.Pointer[string]

	// sig wakes Run when a restart has begun or has been given up, while it waits
	// for one that was asked for after its instance ended.
	sig chan struct{}

	// name, opts and fn are what Run was given, to make an instance and a candidate.
	name string
	opts []Option
	fn   Func

	// cwg counts candidates that are running, which Run waits for.
	cwg sync.WaitGroup

	mu  sync.Mutex
	cur *F // the root of the current instance

	// started is when the current instance started.
	started time.Time

	// The restart that has been asked for and not begun, which is one however
	// many requests there were: who asked, when the burst began and last grew, the
	// latest reason and how many requests. held is set once the hold was logged.
	// timer fires it. All guarded by mu.
	pend       bool
	pendRoot   *F
	pendFirst  time.Time
	pendLast   time.Time
	pendReason string
	pendCount  int
	held       bool
	timer      *time.Timer

	// validating is set while the candidate for the restart that was due is being
	// tried, and candProc is its process, so that a shutdown can stop it. Guarded
	// by mu.
	validating bool
	candProc   *process

	// retried is set when a candidate's result was set aside because the program
	// was asked again while it ran; a second one is not, so a source that never
	// goes quiet still gets its restart.
	retried bool

	// begins are when the last Limit restarts began, oldest first.
	begins []time.Time
}

// newRoot makes the root of a new instance, with the options applied, and makes
// it the current one. It returns nil if a shutdown has been asked for, and checks
// that under the lock a shutdown takes to find the current instance, so one that
// arrives while the instance is being made is never missed.
func (p *process) newRoot(name string, opts []Option) *F {
	ctx, cancel := context.WithCancel(context.Background())

	root := &F{
		runState: &runState{proc: p, restartPolicy: RestartPolicy{}.resolve()},
		name:     name,
		path:     []string{name},
		ctx:      ctx,
		cancel:   cancel,
		timeout:  DefaultTimeout,
	}

	root.init()
	root.root = root
	root.stopped = make(chan struct{})

	Options(opts).applyF(root)

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.final.Load() {
		cancel()

		return nil
	}

	p.cur = root
	p.started = time.Now()

	return root
}

// target is the instance that a shutdown applies to: the current one, or if there
// is none yet, the one asking.
func (p *process) target(own *F) *F {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cur == nil {
		return own
	}

	return p.cur
}

// again reports whether the instance that has just ended is to be followed by
// another: only if it ended because of a restart, nothing has asked for a real
// shutdown or failed, and nothing it started may still be running. A restart that
// was asked for but has not begun, because the root function returned first, is
// waited for.
func (p *process) again(root *F) bool {
	for {
		p.mu.Lock()
		waiting := (p.pend || p.validating) && p.pendRoot == root
		p.mu.Unlock()

		if root.restarting.Load() || p.final.Load() || p.failed.Load() || !waiting {
			break
		}

		<-p.sig
	}

	if !root.restarting.Load() || p.final.Load() || p.failed.Load() {
		return false
	}

	// The old instance is stopped completely before the next one starts.
	<-root.stopped

	if root.hung.Load() {
		// A function that wouldn't stop may still be running, and a second
		// instance beside it is two copies of the program.
		p.failed.Store(true)

		root.Log().Error("not restarting: a function did not stop in time, and a second instance beside it would not be safe")

		return false
	}

	return true
}

// wake tells Run that something it may be waiting for has happened.
func (p *process) wake() {
	select {
	case p.sig <- struct{}{}:
	default:
	}
}

// finish ends the process: nothing that is waiting will begin.
func (p *process) finish() {
	p.final.Store(true)
	p.cancelPending()
	p.abortCandidate()

	// A candidate is stopped like any instance, which takes as long as its
	// functions do, and Run does not return while it is running.
	p.cwg.Wait()
}

// abortCandidate stops the candidate that is running, if there is one: a real
// shutdown has been asked for, so there is nothing to try.
func (p *process) abortCandidate() {
	p.mu.Lock()
	cp := p.candProc
	p.mu.Unlock()

	if cp == nil {
		return
	}

	cp.final.Store(true)

	cur := cp.target(nil)
	if cur == nil {
		return
	}

	cur.begin(cur, false, slog.LevelInfo, "stopping the candidate: the program is shutting down")
}

// cancelPending gives up the restart that is waiting, if any, and wakes Run.
func (p *process) cancelPending() {
	p.mu.Lock()

	p.pend = false
	p.held = false
	p.retried = false

	if p.timer != nil {
		p.timer.Stop()
	}

	p.mu.Unlock()

	p.wake()
}

// due is when the pending restart may begin: after the requests have been quiet
// for the debounce, or the cap on that, and not before the minimum interval, nor
// while Limit have begun in the last Window. It reports whether it is the limit
// that is holding it. Called with mu held.
func (p *process) due(pol RestartPolicy) (due time.Time, held bool) {
	due = p.pendLast.Add(pol.Debounce)

	if limit := p.pendFirst.Add(pol.MaxDebounce); limit.Before(due) {
		due = limit
	}

	if floor := p.started.Add(pol.MinInterval); floor.After(due) {
		due = floor
	}

	if len(p.begins) >= pol.Limit {
		if free := p.begins[len(p.begins)-pol.Limit].Add(pol.Window); free.After(due) {
			return free, true
		}
	}

	return due, false
}

// request records a request to restart root, and sets the time the restart is due.
// It never blocks on the restart, and a request while one is waiting only moves
// the time and keeps the latest reason, so a flood costs nothing but the counting.
func (p *process) request(root *F, reason string) {
	reason = clipReason(reason)
	now := time.Now()
	pol := root.restartPolicy

	p.mu.Lock()

	if p.cur != root || p.final.Load() || root.stopping.Load() || root.restarting.Load() {
		p.mu.Unlock()

		return
	}

	if !p.pend {
		p.pend, p.pendRoot, p.pendFirst, p.pendCount, p.held = true, root, now, 0, false
	}

	p.pendLast, p.pendReason = now, reason
	p.pendCount++

	due, held := p.due(pol)
	logHeld := held && !p.held

	if held {
		p.held = true
	}

	if p.timer == nil {
		p.timer = time.AfterFunc(due.Sub(now), p.fire)
	} else {
		p.timer.Reset(due.Sub(now))
	}

	p.mu.Unlock()

	if logHeld {
		root.Log().Error("restart held",
			slog.Int("limit", pol.Limit),
			slog.Duration("window", pol.Window),
			slog.Duration("wait", due.Sub(now)),
		)
	}
}

// fire is the timer: it deals with the pending restart if it is due, by trying
// the new instance as a candidate, or by beginning at once if the policy says not
// to.
func (p *process) fire() {
	p.mu.Lock()

	if !p.pend || p.validating {
		// A candidate that is running re-arms the timer if it was asked again.
		p.mu.Unlock()

		return
	}

	root := p.pendRoot

	if p.final.Load() || p.cur != root || root.stopping.Load() {
		p.pend = false

		p.mu.Unlock()
		p.wake()

		return
	}

	now := time.Now()
	pol := root.restartPolicy

	// The request that moved the time may have come as the timer fired.
	if due, _ := p.due(pol); now.Before(due) {
		p.timer.Reset(due.Sub(now))
		p.mu.Unlock()

		return
	}

	reason, n := p.pendReason, p.pendCount

	p.pend, p.held = false, false

	// Every attempt counts against the limit, whether or not the candidate
	// passes: a rejected one cost as much to run.
	p.record(now, pol)

	if !pol.SkipValidation {
		p.validating = true
		p.cwg.Add(1)

		p.mu.Unlock()

		p.validate(root, reason, n)

		return
	}

	p.begin(root, reason, n)
}

// record notes an attempt at a restart, which begins a candidate or the restart
// itself, for the limit. Called with mu held.
func (p *process) record(now time.Time, pol RestartPolicy) {
	p.begins = append(p.begins, now)
	if extra := len(p.begins) - pol.Limit; extra > 0 {
		p.begins = slices.Delete(p.begins, 0, extra)
	}
}

// begin starts the restart: the instance is stopped. Called with mu held, which it
// releases.
func (p *process) begin(root *F, reason string, n int) {
	// Set before the lock is let go, so that a request made while the stop begins
	// is not the start of another.
	root.restarting.Store(true)

	p.mu.Unlock()

	root.begin(root, true, slog.LevelInfo, "restarting",
		slog.String("reason", reason), slog.Int("requests", n))

	p.wake()
}

// validate tries the new instance as a candidate, while the old one runs, and
// then begins the restart if it passed or says it was rejected if not. If the
// program was asked to restart again while the candidate was running, what it
// checked may be out of date, so its result is set aside and the new request
// takes its turn. That is done once: after a second candidate the restart goes
// ahead on a pass, since a source that never goes quiet would otherwise never
// get one, and if what changed since is bad the new instance fails the run.
func (p *process) validate(root *F, reason string, n int) {
	defer p.cwg.Done()

	pass, leaked, cause := p.try()

	p.mu.Lock()

	p.validating = false
	p.candProc = nil

	if leaked {
		// What the candidate left running is still running, and another would leave
		// another: nothing is tried again, and a request that came meanwhile is
		// refused with the rest (see Restart).
		p.leaked.Store(true)
		p.pend = false
	}

	if p.final.Load() || p.cur != root || root.stopping.Load() {
		p.pend = false

		p.mu.Unlock()
		p.wake()

		return
	}

	pol := root.restartPolicy

	if p.pend && !(pass && p.retried) {
		p.retried = true

		now := time.Now()
		due, _ := p.due(pol)

		p.timer.Reset(max(due.Sub(now), 0))
		p.mu.Unlock()
		p.wake()

		return
	}

	p.retried = false

	if !pass {
		p.mu.Unlock()

		root.Log().Error("restart rejected",
			slog.String("reason", reason),
			slog.Int("requests", n),
			slog.String("cause", cause),
			slog.String("why", "the new instance did not pass its trial run, so the running program was left as it was"),
			slog.String("hint", "the lines just above marked candidate=true say what was wrong"))

		p.wake()

		return
	}

	p.begin(root, reason, n)
}

// try runs the program as a candidate: a new instance, with the same options,
// forced to a dry run, with a process of its own so that nothing it does can fail
// or end the real one. It passes if it ran to the end of startup and everything
// it started was stopped; a failure, a panic, a shutdown by the program itself, a
// watchdog timeout or a function that had to be abandoned all fail it. It also
// reports whether it left something running, which is not the same as failing:
// a candidate that failed was stopped.
func (p *process) try() (pass, leaked bool, cause string) {
	cp := &process{sig: make(chan struct{}, 1), name: p.name, opts: p.opts, fn: p.fn}

	croot := cp.newRoot(p.name, append(slices.Clone(p.opts), WithDryRun(true)))
	if croot == nil {
		return false, false, "the new instance could not be made"
	}

	croot.candidate = true

	p.mu.Lock()

	if p.final.Load() {
		p.mu.Unlock()
		croot.cancel()

		return false, false, "the program is shutting down"
	}

	p.candProc = cp

	p.mu.Unlock()

	croot.instance(p.fn)

	leaked = croot.hung.Load()

	pass = !cp.failed.Load() && !leaked && croot.completed.Load()
	if pass {
		return true, false, ""
	}

	switch {
	case cp.cause.Load() != nil:
		cause = *cp.cause.Load()
	case leaked:
		cause = "a function did not stop when it was asked to"
	default:
		cause = "the new instance did not reach the end of startup"
	}

	return false, leaked, cause
}

// note keeps what went wrong first in a candidate, escaped and cut short. Only a
// candidate keeps it: the real run logs its failures as they happen, and nothing
// is held on to.
func (root *F) note(cause string) {
	if !root.candidate {
		return
	}

	if cause == "" {
		cause = "a failure with no message"
	}

	cause = clipReason(cause)

	root.proc.cause.CompareAndSwap(nil, &cause)
}

// Restart stops the whole program, as [F.Shutdown] does, and then runs the root
// function again: a new instance, with its own tree, context and probes. It is
// for a function that has noticed that what it was started with has changed, such
// as its configuration, and can be called from any function or goroutine. It does
// not halt the caller or wait for anything, so the caller should return when its
// context is done.
//
// The restart does not begin at once: requests close together are one restart,
// and its rate is limited, as set by [WithRestartPolicy]. The reason is logged
// when it begins (the latest of the requests), escaped and cut short, and is for
// people only. Nothing else about the restart depends on it. A restart that has
// been asked for is carried out even if the root function returns first, unless
// a shutdown or failure comes before it begins.
//
// Restart does nothing, and says nothing, when it can't apply:
//
//   - the program is already shutting down, has failed, or has begun to restart
//     (a real shutdown always wins),
//   - f belongs to an instance that has been replaced,
//   - the run is a dry run or help, which start nothing.
//
// It is refused, with one error that says so (and no more however often it is
// asked), once an earlier candidate left a function running: another would leave
// another, and the program has to be restarted from outside.
//
// Before anything is stopped the new instance is tried as a candidate: the root
// function is run again, in a dry run, beside the old one, which keeps running
// (see [WithDryRun]: a function that starts something skips when [F.DryRun] is
// true, and one that doesn't would start for real). If the candidate fails, panics,
// shuts the program down itself, doesn't finish within [WithDryRunTimeout] or
// leaves something that can't be stopped, the restart is rejected: nothing is
// stopped, the run does not fail, and an error says so. The candidate has a tree,
// context, probes and process state of its own, so nothing it does can fail or end
// the real run, and its log lines carry candidate=true. [RestartPolicy]'s
// SkipValidation turns it off. It is a check made at one moment: a configuration
// that changes after it can still fail the new instance.
//
// The new instance is started only once the old one has stopped completely. If a
// function in the old one doesn't stop in time, and is abandoned, the run fails
// rather than start a second instance beside it, and a new instance that fails
// fails the run: there is no retry.
func (f *F) Restart(reason string) {
	root := f.root
	p := root.proc

	if root.dryRun || root.helping {
		return
	}

	if p.final.Load() || p.target(root) != root {
		return
	}

	if p.leaked.Load() {
		p.refuse(f, "an earlier candidate left a function running, and another would leave another; restart the program")

		return
	}

	p.request(root, reason)
}

// refuse logs that restarts are refused, once, so that a trigger that fires again
// and again can't fill a log.
func (p *process) refuse(f *F, why string) {
	if p.refused.CompareAndSwap(false, true) {
		f.Log().Error("restart refused", slog.String("why", why))
	}
}

// clipReason makes a reason safe to log: at most maxReason bytes (cut at a
// character, and marked), valid UTF-8, and with control characters escaped. It is
// cut before anything else reads it, so a huge reason costs no more than a small.
func clipReason(s string) string {
	if len(s) > maxReason {
		cut := maxReason

		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}

		s = s[:cut] + "..."
	}

	return safetext.Escape(s, false)
}
