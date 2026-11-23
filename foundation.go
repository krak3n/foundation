package foundation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/flags"
	"go.krak3n.dev/foundation/health/monitor"
	"go.krak3n.dev/foundation/internal/config/ignore"
	"go.krak3n.dev/foundation/internal/exit"
	"go.krak3n.dev/foundation/internal/safetext"
)

// DefaultTimeout is the default shutdown timeout for each [F]. After the
// context is cancelled, if the function does not exit within this duration,
// it is abandoned and the parent continues shutdown.
const DefaultTimeout = 30 * time.Second

// Func is the signature for functions executed by [Run] and [F.Run].
// Each Func runs in its own goroutine and receives an [*F] that provides
// lifecycle control: context propagation, cleanup registration, parallel
// execution, and graceful shutdown.
type Func func(f *F)

// F is a function node in the foundation tree. Each call to [F.Run] creates a
// child F that executes in its own goroutine. Functions run sequentially by
// default — the parent waits for each child to complete before starting the next.
// Calling [F.Parallel] releases the parent to proceed to the next [F.Run] call
// without waiting.
type F struct {
	// runState is what the instance shares: every F of a tree holds the same one,
	// so its fields can be read from any function as well as from the root.
	*runState

	name string
	path []string // hierarchical path segments: ["service-name", "http.server", "listener"]

	parent *F
	root   *F

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	cleanups []func()

	// children are this F's live (started, not yet finished) children in start
	// order. Guarded by mu, as are closed and each child's stopRequested.
	children []*F

	// closed is set once the stop sequence has finished handing out children
	// to stop; after that no new children may be started.
	closed bool

	// stopRequested is set (under the parent's mu) when the parent's stop
	// sequence has picked this F to be stopped.
	stopRequested bool

	// Each function keeps what it said about itself for the help: its text, its
	// fields and, in the order they were run, its children. Guarded by mu.
	helpText   string
	helpFields []helpField
	helpKids   []*F

	// skipped is set when the function called [F.Skip].
	skipped atomic.Bool

	parallelCh   chan struct{} // closed by Parallel() to release the parent
	parallelOnce sync.Once
	wg           sync.WaitGroup // tracks this F's parallel children

	bodyDone  chan struct{} // closed when the function has returned
	finished  chan struct{} // closed when the function, its children and its cleanups are all done
	abandoned chan struct{} // closed when the stop sequence gives up waiting for this F

	abandonOnce sync.Once

	timeout time.Duration // shutdown timeout for this F
}

// runState is the state of one instance of the program that its functions share:
// the options it was given and what has become of it. Each instance has its own,
// made with its root; what outlives an instance is in the [process].
type runState struct {
	shutdownOnce sync.Once

	// stopping is set when shutdown begins. New children are not started once it
	// is set.
	stopping atomic.Bool

	// stopped is closed once the stop sequence has returned.
	stopped chan struct{}

	// proc is what outlives an instance: whether the run has failed, whether a
	// real shutdown was asked for, and which instance is current.
	proc *process

	// restarting is set when the shutdown is a restart, and hung when the stop
	// sequence abandoned a function, so that something may still be running.
	restarting    atomic.Bool
	restartPolicy RestartPolicy
	hung          atomic.Bool

	// candidate is set on a candidate, which tags what it logs, and completed when
	// it got to the end of startup without being stopped.
	candidate bool
	completed atomic.Bool

	// watchers counts the watchers started on this instance ([F.Watch]).
	watchers atomic.Int32

	// providers are the config providers used by [F.Load]. providersSet records that
	// [WithConfigProviders] chose them, so the defaults aren't applied on top.
	providers    []config.Provider
	providersSet bool

	// layerOps are the layer options in the order they were given, applied on top
	// of the default layers when the options are all known; layerUsed records that
	// one was used, even a mistaken one, and optionErrs are the mistakes. See
	// [F.chooseProviders].
	layerOps   []func(*layers)
	layerUsed  bool
	optionErrs []error

	// configWatch is what the providers asked to have watched, chosen once an
	// instance with the providers ([F.chooseWatch]).
	configWatch configWatch

	// args are the arguments given to [WithArgs], and argsSet records that it was
	// used, even with none. flags reads them: the provider named in
	// [WithConfigProviders] if there is one, or one made from args.
	args    []string
	argsSet bool
	flags   *flags.Provider

	// helping is set when help was asked for with --help, and helpOut is where it
	// is printed ([WithHelpOutput]; nil means standard output).
	helping bool
	helpOut io.Writer

	// strictConfig is set by [WithStrictConfig]: unknown configuration values
	// fail the run once the root function returns.
	strictConfig bool

	// strictIgnore are the patterns given to [WithStrictIgnore], and
	// strictIgnored what they compiled to.
	strictIgnore  []string
	strictIgnored *ignore.Matcher

	// dryRun is set by [WithDryRun], dryRunTimeout by [WithDryRunTimeout] (zero
	// means the default), and skips counts the calls to [F.Skip].
	dryRun        bool
	dryRunTimeout time.Duration
	skips         atomic.Int64

	// lookupEnv is where ${NAME} references are read from; nil means the process
	// environment. Set with [WithLookupEnv].
	lookupEnv func(string) (string, bool)

	// probes is the instance's registry, made by the first call to Probes, under
	// the root's mu.
	probes *probes
}

// Run is the top-level entry point for a foundation application. It creates a
// root [F] with a background context and executes fn in a child goroutine,
// blocking until fn and all its children have completed.
//
// If any function in the tree called [F.Fail], or panicked, Run ends the process
// with exit status 1 once every function has stopped and every cleanup has run.
// A shutdown requested with [F.Shutdown], such as one triggered by an OS signal,
// is not a failure: Run returns normally. Because a failure ends the process,
// put cleanup in [F.Cleanup] rather than in deferred calls in main.
//
// A call to [F.Restart] makes Run stop the whole tree, as a shutdown does, and
// then run fn again as a new instance, with a tree, a context and probes of its
// own. What belongs to the process is kept: the options, and whether the run has
// failed. fn must therefore be safe to run more than once, and state it keeps
// outside the function (a package variable, a closure) is not reset.
func Run(name string, fn Func, opts ...Option) {
	proc := &process{sig: make(chan struct{}, 1), name: name, opts: opts, fn: fn}

	for {
		root := proc.newRoot(name, opts)
		if root == nil {
			break
		}

		if !root.instance(fn) {
			break
		}
	}

	proc.finish()

	if proc.failed.Load() {
		exit.Func(1)
	}
}

// instance runs one instance of the program and reports whether another should
// follow it, which is only after a restart that stopped everything.
func (root *F) instance(fn Func) bool {
	defer root.cancel()

	// Mistakes in how configuration was chosen stop startup, before anything is
	// read or started, as running on configuration the program didn't mean is
	// worse than not running.
	optionsErr := root.chooseProviders()
	if optionsErr == nil {
		optionsErr = root.chooseWatch()
	}

	var problems []error

	if optionsErr != nil {
		problems = append(problems, fmt.Errorf("invalid configuration options: %w", optionsErr))
	}

	// A pattern that is wrong stops startup, whether or not strict mode is on:
	// quietly ignoring nothing, or too much, is what it must not do.
	if len(root.strictIgnore) > 0 {
		matcher, err := ignore.Compile(root.strictIgnore)
		if err != nil {
			problems = append(problems, fmt.Errorf("invalid WithStrictIgnore: %w", err))
		}

		root.strictIgnored = matcher
	}

	// Every mistake is said at once, so that fixing one is not what shows the next.
	if len(problems) > 0 {
		failure := joinMistakes(problems...)

		fn = func(f *F) {
			f.Fail(failure)
		}
	}

	if optionsErr == nil && root.configWatch.asked {
		fn = root.watchConfig(fn)
	}

	if optionsErr == nil && (root.strictConfig || root.dryRun || root.flags != nil) {
		fn = root.startup(fn)
	}

	root.run(fn)

	// The probes are the instance's: they go when it has stopped completely, and
	// before the next one starts.
	root.endProbes()

	// Help is printed once every function has finished loading, which is not
	// when the root function returns if some of them started in parallel.
	if root.helping && !root.proc.failed.Load() {
		root.finishHelp()
	}

	return root.proc.again(root)
}

// init allocates the channels used to track the lifecycle of the F.
func (f *F) init() {
	f.parallelCh = make(chan struct{})
	f.bodyDone = make(chan struct{})
	f.finished = make(chan struct{})
	f.abandoned = make(chan struct{})
}

// Run creates a child [F] and executes fn in a new goroutine. The parent waits
// for the child to complete before returning, unless the child calls [F.Parallel],
// in which case the parent proceeds to the next [F.Run] call immediately. Parallel
// children are still tracked — the parent will wait for all parallel children to
// complete before its own cleanup functions run.
//
// Cleanup functions registered on the child via [F.Cleanup] are called in LIFO
// order after fn exits, including when the goroutine is terminated via
// [runtime.Goexit].
//
// If the parent's context is already cancelled (e.g. due to a prior [F.Fail] or
// [F.Shutdown] call), the function is not executed.
func (f *F) Run(name string, fn Func) {
	if f.root.stopping.Load() {
		return
	}

	// The child's context carries the parent's values but not its
	// cancellation: during shutdown each F is cancelled explicitly, in order,
	// by [F.stop].
	ctx, cancel := context.WithCancel(context.WithoutCancel(f.ctx))

	child := &F{
		name:     name,
		path:     append(slices.Clone(f.path), name),
		runState: f.runState,
		parent:   f,
		root:     f.root,
		ctx:      ctx,
		cancel:   cancel,
		timeout:  DefaultTimeout,
	}

	child.init()

	f.mu.Lock()

	// Checked under the lock so that a child is either visible to the stop
	// sequence or not started at all.
	if f.closed || f.root.stopping.Load() {
		f.mu.Unlock()
		cancel()

		return
	}

	f.children = append(f.children, child)

	if f.root.helping {
		f.helpKids = append(f.helpKids, child)
	}

	f.mu.Unlock()

	child.exec(fn)

	// Wait for either: function completes, Parallel() is called, or the stop
	// sequence abandons the child after its shutdown timeout.
	select {
	case <-child.finished:
	case <-child.abandoned:
	case <-child.parallelCh:
		// Parallel() was called — track completion via parent's WaitGroup
		f.wg.Go(func() {
			select {
			case <-child.finished:
			case <-child.abandoned:
			}
		})
	}
}

// exec runs fn in a new goroutine. Cleanup functions are deferred so they run
// even on [runtime.Goexit]. The goroutine waits for all parallel children to
// complete before running cleanups.
func (f *F) exec(fn Func) {
	go func() {
		defer close(f.finished)

		if f.parent != nil {
			defer f.parent.removeChild(f)
		}

		// Release the context once the F is done with it, as the stop
		// sequence only cancels F's that are still live.
		defer f.cancel()
		defer f.runCleanups()
		defer f.wg.Wait()
		defer close(f.bodyDone)
		defer f.recoverPanic()

		fn(f)
	}()
}

// run executes fn in a new goroutine and waits for it to finish, or for the
// stop sequence to abandon it.
func (f *F) run(fn Func) {
	f.exec(fn)

	select {
	case <-f.finished:
	case <-f.abandoned:
	}

	// If shutdown began, let the stop sequence return before we do.
	if f.stopping.Load() {
		<-f.stopped
	}
}

// removeChild removes a finished child from the live children list.
func (f *F) removeChild(child *F) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for i, c := range f.children {
		if c == child {
			f.children = append(f.children[:i], f.children[i+1:]...)

			return
		}
	}
}

// Parallel releases the parent to proceed to the next [F.Run] call without
// waiting for this function to complete. The parent will still wait for this
// function to finish before running its own cleanup functions.
//
// Parallel may be called at any point during the function. Calling Parallel on
// the root F will panic.
func (f *F) Parallel() {
	if f.parent == nil {
		panic("foundation: Parallel called on root F")
	}

	f.parallelOnce.Do(func() { close(f.parallelCh) })
}

// SetTimeout sets the shutdown timeout for the current F. After the context is
// cancelled, if the function does not exit within d, it is abandoned and the
// parent continues shutdown. The default timeout is 30 seconds, and a d that is
// not positive means the default.
//
// The timeout is read when the shutdown sequence reaches f, which can be at any
// point once [F.Parallel] has released the parent, so call SetTimeout before
// Parallel.
func (f *F) SetTimeout(d time.Duration) {
	if d <= 0 {
		d = DefaultTimeout
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.timeout = d
}

// Timeout returns the shutdown timeout for the current F.
func (f *F) Timeout() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.timeout
}

// Cleanup registers a function to be called when the current F's function
// exits. Multiple cleanup functions are called in LIFO order (last registered,
// first called).
func (f *F) Cleanup(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.cleanups = append(f.cleanups, fn)
}

// Context returns the context for the current [F]. It carries the values of the
// parent's context but not its cancellation: in a shutdown each function's context
// is cancelled when its turn in the stop order comes (the root's at once), so a
// function that has not been asked to stop yet still has a live context. Outside a
// shutdown it is cancelled when the function has finished completely: it has
// returned, its parallel children have finished and its cleanups have run. A
// function that returns while its children carry on, as the root function usually
// does, keeps its context until then, and cleanups see it live unless a shutdown
// came first, so they should not depend on either.
func (f *F) Context() context.Context {
	if f.ctx != nil {
		return f.ctx
	}

	return context.Background()
}

// Name returns the name of the current F node.
func (f *F) Name() string {
	return f.name
}

// Path returns the hierarchical path segments of the current F node: the names
// given to [Run] and [F.Run] on the way down to it, for example
// ["svc", "http.server", "listener"]. The slice is a copy.
func (f *F) Path() []string {
	return slices.Clone(f.path)
}

// String returns the path of the current F node with its segments joined by
// "/", for example "svc/http.server/listener". It is the "fn" attribute of
// [F.Log], and means printing an F gives something readable and safe, rather
// than its internal state.
func (f *F) String() string {
	return strings.Join(f.path, "/")
}

// Probes returns the probes of the program for the current F. They are shared
// across the whole foundation tree, so a probe registered in any F is visible to
// all. The registry is made on the first call to Probes.
//
// The registry belongs to the instance of the program: it is taken out of when the
// instance has stopped completely, and a program that restarts registers its probes
// from the root function, which runs again for each instance. A function that was
// abandoned and runs after that adds nothing and sees nothing.
//
// Use [Probes.Add] to add probes. Any failure to add probes (e.g. duplicate
// names) will cause the current F to fail via [F.Fail].
func (f *F) Probes() Probes {
	f.root.mu.Lock()
	defer f.root.mu.Unlock()

	if f.root.probes == nil {
		r, err := monitor.NewRegistry()
		if err != nil {
			f.Fail(fmt.Errorf("failed to create probe registry: %w", err))
		}

		f.root.probes = &probes{r: r}
	}

	return probesOf{f.root.probes, f}
}

// Log returns a [*slog.Logger] for the current F with the hierarchical function
// path set as the "fn" attribute. The logger is derived from [slog.Default] on
// each call, so changes via [slog.SetDefault] are always reflected.
func (f *F) Log() *slog.Logger {
	logger := slog.Default()

	// Help is the output of a help run, so only what is wrong is logged.
	if f.root.helping {
		logger = slog.New(warningsOnly{logger.Handler()})
	}

	logger = logger.With("fn", safetext.Escape(f.String(), false))

	// A candidate says so on every line, so that what it reports is not taken
	// for the program's own.
	if f.root.candidate {
		logger = logger.With("candidate", true)
	}

	return logger
}

// Shutdown begins a graceful shutdown of the entire process. The root context is
// cancelled at once, so it is done when Shutdown returns, and every other function
// follows in the reverse of the order they started, each finishing (or timing out)
// before the next is told to stop. Shutdown is idempotent — multiple calls are
// safe, only the first begins it.
//
// Shutdown does not halt the current function. The caller should return when it
// observes the Done of its own f.Context(), which is cancelled at its turn.
func (f *F) Shutdown(msg string, attrs ...slog.Attr) {
	// A candidate that shuts the program down before the end of startup is rejected;
	// the one that ends a dry run is how a candidate passes.
	if !f.root.completed.Load() {
		f.root.note("the new instance asked for the program to shut down")
	}

	f.shutdown(slog.LevelInfo, msg, attrs...)
}

// maxFailText is the most of the text of a failure that is logged, in bytes.
const maxFailText = 8 << 10

// Fail logs the error, triggers a graceful [F.Shutdown] of the entire process,
// and immediately halts the current goroutine via [runtime.Goexit]. Cleanup
// functions registered via [F.Cleanup] are still called in LIFO order.
//
// The text of err can carry what an outsider chose, so it is logged escaped (a
// newline is shown as \n) and cut at 8 KiB, whatever the log handler does. The
// stack of a recovered panic is the runtime's and is logged as it is.
//
// No code after Fail will execute in the current function.
func (f *F) Fail(err error, attrs ...slog.Attr) {
	if err == nil {
		err = errors.New("failure with no error")
	}

	attrs = append([]slog.Attr{slog.String("err", safetext.Clip(err.Error(), maxFailText))}, attrs...)

	f.root.note(err.Error())

	// Set before shutdown so that a failure while already shutting down, which
	// is not the one that triggered it, still counts.
	f.root.proc.failed.Store(true)

	if !f.shutdown(slog.LevelError, "shutting down due to failure", attrs...) {
		// Shutdown was already under way, so it did not log this failure.
		f.Log().LogAttrs(f.Context(), slog.LevelError, "failure during shutdown", attrs...)
	}

	runtime.Goexit()
}

// recoverPanic recovers from a panic in the current goroutine and converts it
// into a [F.Fail] call, triggering a graceful shutdown of the entire process.
func (f *F) recoverPanic() {
	if r := recover(); r != nil {
		f.Fail(fmt.Errorf("panic: %v", r), slog.String("stack", string(debug.Stack())))
	}
}

// runCleanups executes all registered cleanup functions in LIFO order.
func (f *F) runCleanups() {
	f.mu.Lock()
	cleanups := f.cleanups
	f.mu.Unlock()

	// Each cleanup runs in a goroutine of its own, so that one that panics or
	// calls Fail (which ends its goroutine) affects only itself: the failure is
	// logged and fails the run, and the cleanups registered before it still run.
	for _, c := range slices.Backward(cleanups) {
		done := make(chan struct{})

		go func() {
			defer close(done)
			defer f.recoverPanic()

			c()
		}()

		<-done
	}
}
