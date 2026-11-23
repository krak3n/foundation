package config

// Configuration comes from layers, in an order the framework fixes: the document
// layer (files and documents), the remote layer (a configuration service), the
// environment layer and the command layer (flags). Each layer overrides the ones
// before it, so what is closest to the process and most deliberate wins.
//
// A [Provider] says which layer it belongs to by implementing one of the
// interfaces below, which add a marker method and nothing else. The framework's
// options take the matching interface, so a provider can't be put in a layer it
// doesn't belong to, and a program can't place a weak source above a strong one.
// Embed or implement the marker in the provider's own type: it is exported so
// that providers in other modules can.

// A DocumentLayer is a [Provider] that reads a document, such as a JSON or YAML
// file or bytes held in memory.
type DocumentLayer interface {
	Provider

	DocumentLayer()
}

// A RemoteLayer is a [Provider] that reads from a configuration service. It sits
// above the documents, as it is shared by a fleet, and below the environment, as
// what is set on one process is more specific.
type RemoteLayer interface {
	Provider

	RemoteLayer()
}

// An EnvironmentLayer is a [Provider] that reads environment variables.
type EnvironmentLayer interface {
	Provider

	EnvironmentLayer()
}

// A CommandLayer is a [Provider] that reads command line arguments.
type CommandLayer interface {
	Provider

	CommandLayer()
}

// Pathed is a [DocumentLayer] that reads a file, so the framework can notice the
// same file given twice. A document that isn't a file, such as bytes in memory,
// has no path and does not implement it. Path is empty when there is no file to
// read, for example when the variable that names it is not set.
type Pathed interface {
	DocumentLayer

	Path() string
}
