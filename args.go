package foundation

import (
	"go.krak3n.dev/foundation/config/flags"
)

// modes are the root function's own flags, which the framework defines with the
// same tags and rules as any other, so a program can't also use the words:
// claiming them is a collision like any other. They are read before the root
// function runs, because the mode of a run decides what every function in the
// tree does, so they come first among the arguments. --help (and -h) is not here:
// it can be given anywhere, after the marker of the function it is for, so the
// flags provider reads it itself and no function can claim it (see
// [flags.Provider.Help]).
type modes struct {
	DryRun bool `flag:"dry-run"`
}

// loadModes reads the root function's own flags, if there are arguments, and acts
// on them. A mistake in them stops the run before the root function starts.
func (root *F) loadModes(f *F) {
	if root.flags == nil {
		return
	}

	var m modes

	f.Load(&m, root.flags)

	_, helpAsked, err := root.flags.Help()
	if err != nil {
		f.failOnProblems(flatten(nil, err))
	}

	if m.DryRun {
		root.dryRun = true
	}

	// Help is a dry run that describes: every function loads and nothing starts.
	if helpAsked {
		root.dryRun = true
		root.helping = true
	}
}

// flagProviders are the providers that read arguments: the one chosen, which may
// not be among the providers when [WithConfigProviders] was used without it, and any
// others that were named.
func (root *F) flagProviders() []*flags.Provider {
	var out []*flags.Provider

	if root.flags != nil {
		out = append(out, root.flags)
	}

	for _, p := range root.providers {
		if fp, ok := p.(*flags.Provider); ok && fp != root.flags {
			out = append(out, fp)
		}
	}

	return out
}
