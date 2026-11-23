package foundation

import (
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"time"

	"go.krak3n.dev/foundation/internal/safetext"
)

// DefaultDryRunTimeout is how long a dry run is given before it is failed for not
// finishing: see [WithDryRunTimeout].
const DefaultDryRunTimeout = 10 * time.Second

// startup wraps the root function with what happens at the end of startup: the
// strict configuration check, and for a dry run the announcement, the watchdog
// and the shutdown.
//
// The end of startup is also reached when the root function calls [F.Skip], but
// not when it fails, panics or shuts down: those have their own way out.
func (root *F) startup(setup Func) Func {
	return func(f *F) {
		// The mode of the run is chosen by the arguments before anything starts.
		root.loadModes(f)

		var watchdog *time.Timer

		if root.dryRun {
			// Help prints help and nothing else, and its log is quiet anyway.
			if !root.helping {
				f.Log().Info("dry run: nothing will be started")
			}

			timeout := root.dryRunTimeout
			if timeout <= 0 {
				timeout = DefaultDryRunTimeout
			}

			watchdog = time.AfterFunc(timeout, root.dryRunHung)
		}

		returned := false

		defer func() {
			if watchdog != nil {
				watchdog.Stop()
			}

			if !returned && !f.skipped.Load() {
				return
			}

			// Help has its own check, once everything has loaded, and has read nothing
			// that strict mode could look at.
			if !root.helping && (root.strictConfig || root.flags != nil) {
				f.checkConfig()
			}

			if root.dryRun && !root.stopping.Load() {
				root.completed.Store(true)

				// What is still running forgot to skip: stop it, in the usual order.
				f.Shutdown("dry run complete", slog.Int64("skipped", root.skips.Load()))
			}
		}()

		setup(f)

		returned = true
	}
}

// dryRunHung is called when a dry run has not finished in time. It fails the run
// and names every function still running, which is where the f.Skip is missing.
func (root *F) dryRunHung() {
	root.note("the dry run did not finish")
	root.proc.failed.Store(true)

	root.shutdown(slog.LevelError, "dry run did not finish",
		slog.String("running", strings.Join(root.live(), ", ")),
		slog.String("hint", "a function that starts something should call f.Skip when f.DryRun() is true"))
}

// live returns the paths of the functions that have started and not finished, other
// than the root, sorted.
func (root *F) live() []string {
	var paths []string

	var walk func(f *F)

	walk = func(f *F) {
		f.mu.Lock()
		children := slices.Clone(f.children)
		f.mu.Unlock()

		for _, c := range children {
			paths = append(paths, safetext.Escape(c.String(), false))

			walk(c)
		}
	}

	walk(root)
	slices.Sort(paths)

	return paths
}

// DryRun reports whether the run is a dry run, set with [WithDryRun]. In a dry
// run the whole tree is walked so that configuration is loaded and checked, and
// functions that would start something, such as a server, call [F.Skip]:
//
//	f.Load(&cfg)
//
//	if f.DryRun() {
//		f.Skip("dry run")
//	}
//
//	startServer(cfg)
func (f *F) DryRun() bool {
	return f.root.dryRun
}

// Skip stops the current function, as [F.Fail] does but without failing: it logs
// the reason at info level, runs cleanups and lets the parent carry on. It halts
// the goroutine with [runtime.Goexit], so no code after it runs.
//
// It is for a function that has nothing to do, such as a feature that is turned
// off, or that must not start in a dry run (see [F.DryRun]). Attributes are added
// to the log line. Control characters in msg are escaped.
func (f *F) Skip(msg string, attrs ...slog.Attr) {
	f.skipped.Store(true)
	f.root.skips.Add(1)

	attrs = append([]slog.Attr{slog.String("reason", safetext.Escape(msg, false))}, attrs...)

	f.Log().LogAttrs(f.Context(), slog.LevelInfo, "skipped", attrs...)

	runtime.Goexit()
}
