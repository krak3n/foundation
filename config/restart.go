package config

// A Source is something a provider reads from the file system and wants to be
// watched. It is plain data: this package does not watch anything, and a provider
// does not need the framework to say what it reads.
type Source struct {
	// Path is where it is, as the provider reads it.
	Path string

	// Label is what a log calls it in place of Path, for a path that is not for
	// a log, such as one that came from an environment variable ("$SVC_CONFIG").
	// Empty means the path is shown.
	Label string

	// Dir says Path is a directory of documents rather than one file.
	Dir bool

	// Extensions are, for a directory, the endings of the names of its documents
	// (".json"), each with its dot. A directory needs at least one.
	Extensions []string
}

// Restartable is a [DocumentLayer] provider that was asked, with
// [WithRestartOnChange], to have the program restart when what it reads changes,
// and says what that is. A provider that was not asked reports none, so the
// framework can ask every provider and watch only what the program chose.
//
// What is reported is checked before anything is watched (how many, how long, what
// a label and an extension may be), and a provider that gets it wrong stops
// startup with a fixed reason, never changing what the service is.
type Restartable interface {
	DocumentLayer

	// RestartSources returns what the provider reads, or none. It is called before
	// the root function runs, may be called more than once, and returns a copy.
	RestartSources() []Source
}

// A RestartAsker is a [Restartable] that can say it was asked to have the program
// restart on change even when it has nothing to read right now, for example because
// the variable that names its file is not set. The framework uses it to say so in
// the log instead of staying silent. It is optional: a provider that reports its
// sources and not this is only noticed when it has some.
type RestartAsker interface {
	Restartable

	// RestartAsked says whether [WithRestartOnChange] was given.
	RestartAsked() bool
}
