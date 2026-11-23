package foundation

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/internal/nilcheck"
)

// An Option is a configuration option for an [F].
// Can only be applied to the root [F] in a tree; child [F]s inherit the configuration of their parent.
type Option interface {
	applyF(*F)
}

// Options is a slice of [Option] that itself implements the
// [Option] interface. Each non-nil option in the slice is applied
// in order.
type Options []Option

func (o Options) applyF(f *F) {
	for _, opt := range o {
		if opt != nil {
			opt.applyF(f)
		}
	}
}

// OptionFunc is an adapter to allow the use of ordinary functions as
// [Option] values. If f is a function with the appropriate signature,
// OptionFunc(f) is a [Option] that calls f.
type OptionFunc func(*F)

func (fn OptionFunc) applyF(f *F) {
	fn(f)
}

// mistakes are what is wrong with how the program was set up, all of it, said on
// one line: the text of a failure is escaped when it is logged, so a message of
// several lines would be shown with \n in it.
type mistakes []error

func (m mistakes) Error() string {
	texts := make([]string, len(m))

	for i, err := range m {
		texts[i] = err.Error()
	}

	return strings.Join(texts, "; ")
}

func (m mistakes) Unwrap() []error { return m }

// joinMistakes is [errors.Join] with the mistakes on one line, and nil for none.
func joinMistakes(errs ...error) error {
	var m mistakes

	for _, err := range errs {
		if err != nil {
			m = append(m, err)
		}
	}

	if len(m) == 0 {
		return nil
	}

	return m
}

// WithConfigProviders sets the [config.Provider]s that [F.Load] applies, in order,
// with later providers overriding earlier ones. It is the whole stack, chosen by
// the program: it replaces the layers and their defaults (environment variables,
// and flags with [WithArgs]), so to keep any of those, include it. Most programs
// want the layer options instead ([WithDocumentConfig] and the others), which
// change one part and keep the rest. It is a startup error to use both:
//
//	foundation.WithConfigProviders(env.New())            // environment variables only
//	foundation.WithConfigProviders(json.File("/etc/svc.json"), env.New())
//
// Calling it with no providers switches configuration loading off. A nil provider,
// a nil pointer in an interface too, is a startup error.
//
// The providers are the program's own objects, so a program that restarts uses
// the same ones for every instance, and for the trial run of a restart beside the
// live one: they are used again and at the same time, and the providers of this
// module are written for it.
func WithConfigProviders(providers ...config.Provider) Option {
	return OptionFunc(func(f *F) {
		f.providers = slices.Clone(providers)
		f.providersSet = true

		// A hole in a stack the program chose is a mistake, as in the layer options,
		// and a typed nil would panic when it was used.
		for i, p := range providers {
			if nilcheck.Is(p) {
				f.optionErrs = append(f.optionErrs, fmt.Errorf("WithConfigProviders: provider %d is nil", i+1))
			}
		}
	})
}

// WithArgs gives the run its command line arguments, without the program name:
//
//	foundation.Run("svc", fn, foundation.WithArgs(os.Args[1:]))
//
// They are copied, and never taken from os.Args by the framework itself: they are
// always a choice the program made. With them, fields tagged `flag:"name"` can be
// set from flags, which are read last by the default providers so they win over
// the environment and any documents (see package config/flags for the syntax), and the
// root function has two flags of its own, which must come first: --dry-run, which
// is [WithDryRun], and --help (or -h), which prints help for the whole program,
// or for one function when it follows that function's marker (--api --help); see
// [F.Help]. Help is a dry run that describes instead of reading: every function
// loads, but nothing is read from the environment or files, checked or started,
// and no default or value is shown.
//
// Every argument must be read by something: a flag nothing asked for, a flag
// before its function's marker, or a stray word fails the run when startup ends,
// whether or not [WithStrictConfig] is on, and [WithStrictIgnore] never covers
// one. With [WithConfigProviders] the flags provider is not in the list unless it is
// named there, in which case that one reads the arguments, so arguments that
// nothing reads are reported rather than ignored.
func WithArgs(args []string) Option {
	args = slices.Clone(args)

	return OptionFunc(func(f *F) {
		f.args = args
		f.argsSet = true
	})
}

// WithHelpOutput sets where help is written when the program is run with --help
// (see [WithArgs]). The default is standard output, looked up when the help is
// printed. If the help can't be written the run fails, as help that wasn't shown
// is not a success.
func WithHelpOutput(w io.Writer) Option {
	return OptionFunc(func(f *F) {
		f.helpOut = w
	})
}

// WithStrictConfig makes a configuration value that nothing asked for a failure,
// which catches a misspelt environment variable such as APP_ADRR that would
// otherwise be silently ignored.
//
// When the root function returns, each provider of the program that can check
// (see [config.Checker]) is asked for the values it holds under the root's name
// that no [F.Load] asked for. Only the providers of the program are checked: one
// given straight to [F.Load] is a narrower, deliberate use, and is not. Any are logged as errors, one each, and the run fails.
// The check is skipped if the run is already failing or shutting down.
//
// It treats the end of the root function as the end of startup, so every
// function must load its configuration before then: before it calls
// [F.Parallel], as the built in components do. Variables belonging to a function
// that loads later would be reported as unknown.
func WithStrictConfig() Option {
	return OptionFunc(func(f *F) {
		f.strictConfig = true
	})
}

// WithStrictIgnore lists keys that [WithStrictConfig] should not report as
// unknown, such as "_comment" keys in a JSON document, or variables that share
// the root's prefix but belong to another program. Without strict mode it does
// nothing.
//
// A pattern is matched against the whole key as it is reported, "SVC_LEGACY_FLAG"
// for a variable and "svc.http._comment" for a path in a document, and "*" stands
// for any run of characters, so "SVC_LEGACY_*" covers every such variable and
// "*._comment" a comment at any depth. Nothing else is special, and matching is
// case sensitive: a pattern that doesn't match reports more, never less. An object
// in a document is reported once, so ignoring it ignores what is inside it.
//
// Only a key reported as not recognised can be ignored; a value that is wrong, or
// a file that can't be read, is reported whatever the patterns say. What was
// ignored is not hidden: one line at the end of startup says which patterns are
// in force and how many keys they covered, and each key is logged at debug level.
//
// The patterns are part of the program, not of its configuration, so nobody who
// can change the environment or the file can add to them. A pattern that is
// empty, or only stars, or has control characters or invalid UTF-8, or is longer
// than 256 bytes, or more than 64 patterns, stops [Run] before the root function
// starts.
func WithStrictIgnore(patterns ...string) Option {
	return OptionFunc(func(f *F) {
		f.strictIgnore = slices.Clone(patterns)
	})
}

// WithDryRun makes the run a dry run when enabled is true: the tree of functions
// is walked so that every [F.Load] runs and checks its configuration (providers,
// required fields, Validate, strict mode), but functions that would start
// something skip themselves (see [F.DryRun] and [F.Skip]). The built in
// components do. When the root function returns, anything still running is
// stopped and Run returns, with exit status 0 if the configuration is good, or 1
// with the usual reports if it is not.
//
// It is meant for checking a deployment before it is rolled out and for printing
// help, and is chosen by the program, usually from a command line flag. It is
// never read from the environment or from configuration: a service that quietly
// started nothing and exited 0 would look healthy to an orchestrator.
//
// A function that forgets to skip would start for real, so a dry run that has
// not finished after the timeout (see [WithDryRunTimeout]) is failed, naming the
// functions still running.
func WithDryRun(enabled bool) Option {
	return OptionFunc(func(f *F) {
		f.dryRun = enabled
	})
}

// WithDryRunTimeout sets how long a dry run is given to finish before it is
// failed. The default is [DefaultDryRunTimeout]; a timeout that is not positive
// means the default, so it can't switch the watchdog off. It has no effect
// unless the run is a dry run.
func WithDryRunTimeout(d time.Duration) Option {
	return OptionFunc(func(f *F) {
		f.dryRunTimeout = d
	})
}

// WithLookupEnv sets where [F.Load] reads the variables that ${NAME} references
// in fields tagged `config:"expand"` refer to. lookup has the signature of
// [os.LookupEnv], which is the default, so a nil lookup means the process
// environment.
//
// It is separate from the environment provider's own source on purpose, as that
// reads configuration and this reads values to substitute into it. A test that
// gives the environment provider its own variables with env.WithMap will want to
// give expansion the same, so that nothing reaches the real environment:
//
//	vars := map[string]string{"DB_HOST": "db.internal"}
//
//	foundation.WithLookupEnv(func(name string) (string, bool) { v, ok := vars[name]; return v, ok })
//
// A service that wants to limit what a configuration file can read can pass a
// lookup that answers only for names it allows.
func WithLookupEnv(lookup func(name string) (value string, ok bool)) Option {
	return OptionFunc(func(f *F) {
		f.lookupEnv = lookup
	})
}

// WithRestartPolicy sets how fast the program restarts when [F.Restart] is called:
// the debounce, the minimum time between restarts and the limit in a window. See
// [RestartPolicy]. A field left zero has its default, so with no policy the
// defaults apply, and a policy with values that are negative, absurd (more than
// 1000 restarts a window, or an interval over 24 hours) or inconsistent stops
// startup. It is chosen in code, never from the environment or configuration.
func WithRestartPolicy(p RestartPolicy) Option {
	return OptionFunc(func(f *F) {
		if err := p.check(); err != nil {
			f.optionErrs = append(f.optionErrs, fmt.Errorf("WithRestartPolicy: %w", err))

			return
		}

		f.restartPolicy = p.resolve()
	})
}
