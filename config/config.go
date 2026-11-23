// Package config defines the interface that configuration providers implement.
//
// A provider fills in a struct from some source, such as environment variables
// or a file. The framework hands it the hierarchical path of the function that
// is loading configuration, which is how each provider decides where to look.
// Providers are applied in order, later ones overriding earlier ones.
//
// All providers look in the equivalent place for the same path. A function
// name is split into keys by [Keys], on every character that isn't an ASCII
// letter, digit or underscore, so the path ["svc", "http.server"] means the keys
// svc, http and server. Each provider then lays the keys out in its own format:
// environment variables join them with underscores (SVC_HTTP_SERVER_ADDR) and
// structured files nest them (svc > http > server > addr). A provider for a new
// format should call [Keys] and follow the same rule.
//
// This package depends on nothing but the standard library, so providers can be
// implemented, and published, without depending on the rest of the framework.
// [Fields] and [SetString] hold the rules for which struct fields are loaded and
// how text becomes a value, so a provider that uses them accepts and rejects
// the same things as the built in ones. See the env and json sub packages for
// those.
//
// Providers belong to layers, which the framework applies in a fixed order:
// documents, remote, environment, then the command line (see [DocumentLayer] and
// the other layer interfaces). A provider implements the marker of its layer, and
// one that reads a file also implements [Pathed].
//
// Providers are loaded with [go.krak3n.dev/foundation.F.Load] and chosen with
// [go.krak3n.dev/foundation.WithConfigProviders].
package config

// Provider provides configuration loading.
//
// Unmarshal populates dst, a non-nil pointer to a struct, with the values the
// provider has for the function at path, the hierarchical path segments of the
// function loading the configuration, for example ["svc", "http.server"].
// Fields the provider has no value for must be left untouched, so that struct
// defaults and values from earlier providers survive.
//
// A provider should report every problem it finds, not stop at the first: keep
// going, set the fields that are fine, and return all the problems joined with
// [errors.Join]. A problem with a field is a [*FieldError]; anything else, such
// as a file that can't be read, can be any error.
//
// A provider is made once and used for as long as the program runs: by every
// instance of a program that restarts, and by the trial run of a restart, which
// runs beside the live instance. So Unmarshal must give the same answer each time
// it is asked about the same struct, and be safe to call at the same time from
// several goroutines. A provider that remembers what it was asked (to find two
// fields reading one name, say) must tell the same field asking again from a
// second field.
type Provider interface {
	Unmarshal(path []string, dst any) error
}

// ProviderFunc is an adapter to allow the use of ordinary functions as
// [Provider] values.
type ProviderFunc func(path []string, dst any) error

// Unmarshal calls fn(path, dst).
func (fn ProviderFunc) Unmarshal(path []string, dst any) error {
	return fn(path, dst)
}

// A Checker is a [Provider] that can also say, once everything has been loaded,
// which of the values it holds nothing asked for: a misspelt environment
// variable, a stray key in a file. root is the path of the root function, and
// the provider looks at what is under it. It returns a [*FieldError] wrapping
// [ErrUnknown] for each one, joined with [errors.Join].
//
// It only makes sense after every function has loaded its configuration, so
// providers record what they were asked for as they go, and
// [go.krak3n.dev/foundation.WithStrictConfig] calls Check when the root function
// returns.
type Checker interface {
	Provider

	Check(root []string) error
}

// A Validator is a configuration struct that can check itself. After
// [go.krak3n.dev/foundation.F.Load] has loaded it, and found no other problems,
// it calls Validate, so rules that span fields (a TLS key that needs its
// certificate, a port that must be above 1024) live next to the struct.
//
// Validate returns every problem it finds, joined with [errors.Join] if there
// are several; each is reported separately. Returning a [*FieldError] names the
// field it is about. Only the struct given to Load is validated: its Validate
// calls those of nested structs if they should run.
type Validator interface {
	Validate() error
}

// FieldKey says where a provider looks for one field.
type FieldKey struct {
	// Field is the Go name of the field, for nested structs the path to it:
	// "TLS.CertFile". An embedded struct's fields are named as if declared in the
	// parent.
	Field string

	// Key is where the provider looks the field up, in its own format: the
	// environment variable SVC_TLS_CERT_FILE, or the path in a document,
	// svc.tls.cert_file.
	Key string

	// Provider is the provider the key is for, by the name its problems use: env,
	// json, flags. It is what help labels where a field can be set.
	Provider string

	// Help is what the field is for, from its help tag, as written.
	Help string

	// Type is the type of the field as a person would write it: string, int,
	// duration, "list of string".
	Type string
}

// A Describer is a [Provider] that can say where it would look for each field of
// a struct, without looking: which environment variables a service reads, or
// which keys in its file. That is for documentation and tests, and it lets
// [go.krak3n.dev/foundation.F.Load] name the key to set when a required field is
// missing.
//
// Describe lists the fields of dst, a non-nil pointer to a struct, that the
// provider would load for the function at path, in declaration order. Only
// fields that hold a value are listed, so groups are expanded and fields that are
// skipped are left out. A field that can be set in more than one way, such as
// from a variable or from a file it names, is listed once for each.
//
// The Help, Type and Provider of each key are filled in by [Annotate].
//
// It must not read the source, change dst or count as having been asked for the
// keys (see [Checker]). Mistakes in the struct are returned as [*FieldError]s
// joined with [errors.Join], as [Provider.Unmarshal] does, alongside the fields
// that are fine.
type Describer interface {
	Provider

	Describe(path []string, dst any) ([]FieldKey, error)
}

// A FileBacked is a [Provider] that can read the value of a field from a file
// instead of from the source itself, as the env provider does for a field with
// the file option (Docker and Kubernetes secrets). What such a file holds is a
// secret to be used as it is, so [go.krak3n.dev/foundation.F.Load] refuses to
// expand ${NAME} references in those fields, whichever provider reads them.
//
// FileFields lists the Go names of the fields of dst, a non-nil pointer to a
// struct, that the provider may read from a file for the function at path, using
// the same names as [FieldError.Field]: "TLS.CertFile" for a nested field. It
// must not read the source or change dst. Mistakes in the struct are returned as
// [*FieldError]s joined with [errors.Join], as [Provider.Unmarshal] does.
type FileBacked interface {
	Provider

	FileFields(path []string, dst any) ([]string, error)
}
