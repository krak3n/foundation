package config

// A FileOption configures a provider that reads a file or a directory of files,
// of any format. The options are defined here, once, so that every such provider
// means the same by them, and so that a provider written elsewhere can take them.
//
// Only this package can make an option, and an option is made in code: nothing in
// the environment or a document can set one. A provider reads the result with
// [ResolveFile].
type FileOption interface {
	applyFile(*FileSettings)
}

// FileSettings is what [FileOption]s ask of a provider. The zero value is the
// strict one, so a provider made without options is never the weaker for it.
type FileSettings struct {
	// Required says a file or directory that does not exist is an error, not
	// something to skip.
	Required bool

	// NoPermissionCheck turns off the check that refuses a file, and the directory
	// that holds it, which the group or others can write.
	NoPermissionCheck bool

	// RestartOnChange says the program is to restart when what the provider reads
	// changes. See [WithRestartOnChange] and [Restartable].
	RestartOnChange bool
}

// FileOptions is a slice of [FileOption] that is itself one. Each non-nil option
// is applied in order.
type FileOptions []FileOption

func (opts FileOptions) applyFile(s *FileSettings) {
	for _, opt := range opts {
		if opt != nil {
			opt.applyFile(s)
		}
	}
}

type fileOptionFunc func(*FileSettings)

func (f fileOptionFunc) applyFile(s *FileSettings) { f(s) }

// ResolveFile applies opts, in order, to the strict defaults and returns the
// result. A nil option is skipped. It is for providers: nothing is kept, so what
// one call resolves never reaches the next.
func ResolveFile(opts ...FileOption) FileSettings {
	var s FileSettings

	FileOptions(opts).applyFile(&s)

	return s
}

// WithRequired says the file, or the directory, must exist: a missing one is an
// error instead of being silently skipped. It is about the document and has
// nothing to do with the "required" tag on a field.
func WithRequired() FileOption {
	return fileOptionFunc(func(s *FileSettings) {
		s.Required = true
	})
}

// WithoutPermissionCheck turns off the check that refuses a file, and the
// directory that holds it, that the group or others can write. By default a
// provider refuses one, because whoever can write to a configuration file can
// change what the service does. Use it where the deployment can't give the file
// tighter modes, for example a Kubernetes volume with a loose defaultMode. It is a
// choice the program makes in code for the provider it gives it to, and no other:
// nothing in the environment or a document can turn the check off. The check is
// only made on Unix, where files have modes.
func WithoutPermissionCheck() FileOption {
	return fileOptionFunc(func(s *FileSettings) {
		s.NoPermissionCheck = true
	})
}

// WithRestartOnChange asks for the program to restart when what the provider reads
// changes. It is the program's choice, made in code for the provider it gives it to
// and no other: nothing in the environment or a document can ask for it, and without
// it a provider is never watched.
//
// The framework does the watching, when the provider is part of the program's
// configuration: before the root function runs it starts one watch over the
// [Source]s the provider reports (see [Restartable]). A change is a restart, and a
// restart only goes ahead if the new configuration loads (see the restart policy of
// the framework), so a bad edit is rejected and the running service is left alone.
func WithRestartOnChange() FileOption {
	return fileOptionFunc(func(s *FileSettings) {
		s.RestartOnChange = true
	})
}
