package foundation

import (
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/internal/config/confdir"
	"go.krak3n.dev/foundation/internal/nilcheck"
	"go.krak3n.dev/foundation/watch"
)

// What a provider says it reads is checked before anything is watched. The limits
// keep a provider, which may be written elsewhere, from making the framework do
// unbounded work or log something it should not.
const (
	maxSourcePath = 4096
	maxExtensions = 8
	maxExtension  = 16
)

// configWatch is what the program asked to be watched: what the providers that
// asked say they read. It is chosen once an instance, with the providers.
type configWatch struct {
	sources []config.Source

	// asked says a provider asked and may have nothing to read.
	asked bool
}

// chooseWatch looks at the providers for what is to be watched and checks it. The
// reasons it gives are fixed: what a provider said is never in them, since it may
// be a path that came from the environment.
func (root *F) chooseWatch() error {
	var w configWatch

	for i, p := range root.providers {
		r, ok := p.(config.Restartable)
		if !ok || nilcheck.Is(p) {
			continue
		}

		sources, asked, err := restartSources(r)
		if err != nil {
			return fmt.Errorf("provider %d: %w", i+1, err)
		}

		w.asked = w.asked || asked || len(sources) > 0

		for j, s := range sources {
			if err := checkSource(s); err != nil {
				return fmt.Errorf("provider %d, source %d: %w", i+1, j+1, err)
			}
		}

		if len(w.sources)+len(sources) > watch.MaxTriggers {
			return fmt.Errorf("the providers want more than %d files or directories watched", watch.MaxTriggers)
		}

		w.sources = append(w.sources, sources...)
	}

	root.configWatch = w

	return nil
}

// restartSources asks a provider, which is code we don't control, and turns a
// panic into an error that says nothing of what it panicked with.
func restartSources(r config.Restartable) (sources []config.Source, asked bool, err error) {
	defer func() {
		if recover() != nil {
			sources, asked, err = nil, false, errors.New("the provider panicked when asked what it reads")
		}
	}()

	if a, ok := r.(config.RestartAsker); ok {
		asked = a.RestartAsked()
	}

	return r.RestartSources(), asked, nil
}

// asksToRestart says whether the provider asked to have the program restart on
// change, by saying so or by having something to read.
func asksToRestart(p config.Provider) bool {
	r, ok := p.(config.Restartable)
	if !ok || nilcheck.Is(p) {
		return false
	}

	sources, asked, err := restartSources(r)

	return asked || len(sources) > 0 || err != nil
}

func checkSource(s config.Source) error {
	switch {
	case s.Path == "":
		return errors.New("the path is empty")
	case len(s.Path) > maxSourcePath:
		return fmt.Errorf("the path is longer than %d bytes", maxSourcePath)
	case strings.ContainsRune(s.Path, 0):
		return errors.New("the path has a NUL in it")
	}

	if s.Label != "" && !validLabel(s.Label) {
		return fmt.Errorf("the label must be printable text of at most %d bytes", watch.MaxLabel)
	}

	if !s.Dir {
		if len(s.Extensions) > 0 {
			return errors.New("a file has no extensions")
		}

		return nil
	}

	if len(s.Extensions) == 0 || len(s.Extensions) > maxExtensions {
		return fmt.Errorf("a directory needs from 1 to %d extensions", maxExtensions)
	}

	for _, ext := range s.Extensions {
		if len(ext) < 2 || len(ext) > maxExtension || ext[0] != '.' || ext[1] == '.' || strings.ContainsAny(ext, "/\\") || !validLabel(ext) {
			return fmt.Errorf("an extension is a dot and a name of at most %d bytes, such as .json", maxExtension)
		}
	}

	return nil
}

// validLabel says text is fit for a log: valid UTF-8, no control, formatting or
// separator characters, and not longer than a label may be.
func validLabel(s string) bool {
	if len(s) > watch.MaxLabel || !utf8.ValidString(s) {
		return false
	}

	for _, r := range s {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
	}

	return true
}

// trigger is the watch for what a provider reads.
func trigger(s config.Source) watch.Trigger {
	var opts []watch.Option

	if s.Label != "" {
		opts = append(opts, watch.WithLabel(s.Label))
	}

	if !s.Dir {
		return watch.Files([]string{s.Path}, opts...)
	}

	exts := append([]string(nil), s.Extensions...)

	// A directory is read by the names the provider selects, so the watch looks at
	// the same ones: an editor's swap file is not a change.
	return watch.Dir(s.Path, append(opts,
		watch.WithExtensions(exts...),
		watch.WithFilter(func(name string) bool { return confdir.Match(name, exts...) }),
	)...)
}

// watchConfig wraps the root function so that what the providers asked to have
// watched is, before it runs: nothing it loads can then change without being seen.
func (root *F) watchConfig(setup Func) Func {
	return func(f *F) {
		w := root.configWatch

		// A dry run, help and a candidate start nothing, as for any watch.
		if !root.dryRun && !root.helping {
			if len(w.sources) == 0 {
				f.Log().Info("no configuration to watch", slog.String("why", "the providers that asked to restart on change have nothing to read"))
			} else {
				triggers := make([]watch.Trigger, 0, len(w.sources))
				names := make([]string, 0, len(w.sources))

				for _, s := range w.sources {
					triggers = append(triggers, trigger(s))

					if s.Label != "" {
						names = append(names, s.Label)
					} else {
						names = append(names, s.Path)
					}
				}

				f.Log().Info("watching configuration",
					slog.Int("sources", len(w.sources)),
					slog.String("names", clipReason(strings.Join(names, ", "))))

				f.Watch(watch.Any(triggers...))
			}
		}

		setup(f)
	}
}

// refuseUnwatched fails when a provider that asked to restart the program is given
// to Load without being part of the program's configuration, as nothing would
// watch it and the program would believe it reloads.
func (f *F) refuseUnwatched(providers []config.Provider) {
	for _, p := range providers {
		if !asksToRestart(p) || inStack(f.root.providers, p) {
			continue
		}

		f.Fail(errors.New("a provider that asked to restart the program when its files change is not part of the configuration of the program, so nothing would watch it: give it to WithDocumentConfig or WithConfigProviders"))
	}
}

// inStack says whether p is one of the providers, by identity. A provider that
// can't be compared, which includes one whose fields hold something that can't be
// (comparing would panic), is not found.
func inStack(stack []config.Provider, p config.Provider) bool {
	if !reflect.ValueOf(p).Comparable() {
		return false
	}

	for _, q := range stack {
		if q != nil && reflect.TypeOf(q) == reflect.TypeOf(p) && reflect.ValueOf(q).Comparable() && q == p {
			return true
		}
	}

	return false
}
