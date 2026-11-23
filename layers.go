package foundation

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/flags"
	"go.krak3n.dev/foundation/internal/nilcheck"
)

// layers are the providers of each layer, in the order the framework fixes:
// documents, then the remote layer, then the environment, then the command line,
// each overriding the ones before it. Within a layer later providers win.
type layers struct {
	document    []config.DocumentLayer
	remote      []config.RemoteLayer
	environment []config.EnvironmentLayer
	command     []config.CommandLayer
}

// providers is every provider in the order [F.Load] applies them.
func (l *layers) providers() []config.Provider {
	var out []config.Provider

	for _, p := range l.document {
		out = append(out, p)
	}

	for _, p := range l.remote {
		out = append(out, p)
	}

	for _, p := range l.environment {
		out = append(out, p)
	}

	for _, p := range l.command {
		out = append(out, p)
	}

	return out
}

// layerOption adds providers to a layer, or replaces it, when the options have all
// been given: the options are recorded and applied in the order they were given,
// on top of the defaults, once the whole list is known (see
// [F.chooseProviders]). A mistake is recorded rather than acted on, as there is
// nowhere to return it from: it stops startup.
func layerOption[T config.Provider](what string, replace bool, entries []T, layer func(*layers) *[]T) Option {
	return OptionFunc(func(f *F) {
		f.layerUsed = true

		if len(entries) == 0 {
			f.optionErrs = append(f.optionErrs, fmt.Errorf("%s: no providers given", what))

			return
		}

		for i, e := range entries {
			if nilcheck.Is(e) {
				f.optionErrs = append(f.optionErrs, fmt.Errorf("%s: provider %d is nil", what, i+1))

				return
			}
		}

		entries := slices.Clone(entries)

		f.layerOps = append(f.layerOps, func(l *layers) {
			p := layer(l)

			if replace {
				*p = entries
			} else {
				*p = append(slices.Clone(*p), entries...)
			}
		})
	})
}

// WithDocumentConfig adds documents, such as files, to the document layer, after
// the ones it already has, so they win over them. The layer is empty by default:
// no file is read unless the program names it, in code, with the format it wants:
//
//	foundation.WithDocumentConfig(json.File("config.json"), yaml.File("config.yaml"))
//
// or, to let whoever runs the program choose the file, by the variable it names
// ([json.FileFromEnv]). Within the layer later providers override earlier ones.
// The layers have a fixed order whatever the order of the options: documents, the
// remote layer, the environment, then the command line. It is a startup error to
// give none, a nil one, or the same file twice. Options apply in the order they
// are given, and cannot be mixed with [WithConfigProviders].
func WithDocumentConfig(providers ...config.DocumentLayer) Option {
	return layerOption("WithDocumentConfig", false, providers, func(l *layers) *[]config.DocumentLayer { return &l.document })
}

// SetDocumentConfig replaces the document layer. As the layer is empty by
// default it differs from [WithDocumentConfig] only when an earlier option added
// to it:
//
//	foundation.SetDocumentConfig(yaml.File("config.yaml"))
//
// The format is the program's choice, never taken from a file's name or the
// environment.
func SetDocumentConfig(providers ...config.DocumentLayer) Option {
	return layerOption("SetDocumentConfig", true, providers, func(l *layers) *[]config.DocumentLayer { return &l.document })
}

// WithRemoteConfig adds providers to the remote layer, which is empty by default.
// It sits above the documents and below the environment: what a fleet shares
// overrides the file a build ships with, and what is set on one process overrides
// both.
func WithRemoteConfig(providers ...config.RemoteLayer) Option {
	return layerOption("WithRemoteConfig", false, providers, func(l *layers) *[]config.RemoteLayer { return &l.remote })
}

// SetRemoteConfig replaces the remote layer.
func SetRemoteConfig(providers ...config.RemoteLayer) Option {
	return layerOption("SetRemoteConfig", true, providers, func(l *layers) *[]config.RemoteLayer { return &l.remote })
}

// WithEnvironmentConfig adds providers to the environment layer, after the
// environment variables the layer reads by default.
func WithEnvironmentConfig(providers ...config.EnvironmentLayer) Option {
	return layerOption("WithEnvironmentConfig", false, providers, func(l *layers) *[]config.EnvironmentLayer { return &l.environment })
}

// SetEnvironmentConfig replaces the environment layer, for example to read
// variables from a map in a test, or with a prefix of its own.
func SetEnvironmentConfig(providers ...config.EnvironmentLayer) Option {
	return layerOption("SetEnvironmentConfig", true, providers, func(l *layers) *[]config.EnvironmentLayer { return &l.environment })
}

// WithCommandConfig adds providers to the command layer, the highest, after the
// flags that [WithArgs] gives it by default (the layer is empty without it).
func WithCommandConfig(providers ...config.CommandLayer) Option {
	return layerOption("WithCommandConfig", false, providers, func(l *layers) *[]config.CommandLayer { return &l.command })
}

// SetCommandConfig replaces the command layer. If [WithArgs] was used and no
// flags provider is named, nothing reads the arguments, which fails the run: an
// argument nobody reads is always a mistake.
func SetCommandConfig(providers ...config.CommandLayer) Option {
	return layerOption("SetCommandConfig", true, providers, func(l *layers) *[]config.CommandLayer { return &l.command })
}

// chooseProviders settles which providers [F.Load] uses and which one reads the
// arguments, and returns what is wrong with the options, if anything.
//
// With [WithConfigProviders] they are exactly those. Otherwise the layers start
// from their defaults (the environment, and flags if [WithArgs] was used; no
// file), the options are applied in the order they were given, and the layers are joined in their fixed order.
// Everything that looks at the providers (Load, help, the strict check, the keys
// named for a missing field) reads the one list this makes.
func (root *F) chooseProviders() error {
	errs := slices.Clone(root.optionErrs)

	if root.providersSet {
		if root.layerUsed {
			errs = append(errs, errors.New("WithConfigProviders can't be combined with the layer options (With/Set Document, Remote, Environment and Command Config): choose one"))
		}

		for _, p := range root.providers {
			if fp, ok := p.(*flags.Provider); ok && !nilcheck.Is(fp) {
				root.flags = fp

				break
			}
		}

		if root.flags == nil && root.argsSet {
			root.flags = flags.New(flags.WithArgs(root.args))
		}

		return joinMistakes(errs...)
	}

	l := layers{
		environment: []config.EnvironmentLayer{env.New()},
	}

	var defaultFlags *flags.Provider

	if root.argsSet {
		defaultFlags = flags.New(flags.WithArgs(root.args))
		l.command = []config.CommandLayer{defaultFlags}
	}

	for _, op := range root.layerOps {
		op(&l)
	}

	if err := noDuplicateFiles(l.document); err != nil {
		errs = append(errs, err)
	}

	// The provider that reads the arguments is the first flags provider in the
	// command layer. If it was replaced without one, the default still stands
	// guard: arguments nothing reads are reported, never ignored.
	for _, p := range l.command {
		if fp, ok := p.(*flags.Provider); ok {
			root.flags = fp

			break
		}
	}

	if root.flags == nil {
		root.flags = defaultFlags
	}

	if err := joinMistakes(errs...); err != nil {
		return err
	}

	root.providers = l.providers()

	return nil
}

// noDuplicateFiles refuses two documents that read the same file, however the
// path is spelt: both would be read and one would win by accident. Paths are
// compared as cleaned absolute paths, so symbolic links to one file are not
// noticed. The path is not in the error, only where in the list the entries are.
func noDuplicateFiles(docs []config.DocumentLayer) error {
	seen := map[string]int{}

	for i, d := range docs {
		p, ok := d.(config.Pathed)
		if !ok {
			continue
		}

		path := p.Path()
		if path == "" {
			// A file named by a variable that isn't set: nothing is read.
			continue
		}

		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}

		path = filepath.Clean(path)

		if first, dup := seen[path]; dup {
			return fmt.Errorf("the document layer reads the same file twice (documents %d and %d)", first+1, i+1)
		}

		seen[path] = i
	}

	return nil
}
