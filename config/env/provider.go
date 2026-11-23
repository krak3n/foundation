// Package env provides a config.Provider that reads configuration from
// environment variables.
//
// The provider constructs a prefix from the keys of the path (see
// [config.Keys]), uppercased and joined with "_". Names are split into keys on
// anything that isn't a letter, digit or underscore, so the path
// ["app", "http", "server"] and the path ["app", "http.server"] both produce
// the prefix "APP_HTTP_SERVER". Fields are mapped using the "env" struct tag:
//
//	type Config struct {
//	    Addr string `env:"ADDR"`
//	}
//
// With the prefix above, the provider looks for the environment variable
// APP_HTTP_SERVER_ADDR.
//
// Names are matched exactly, so APP_addr is not APP_ADDR. By default variables
// come from the process environment; [WithLookup] reads them from anywhere
// else, which is how tests avoid sharing, and having to change, the real
// environment.
//
// Only fields with a matching environment variable are set; other fields are
// left untouched, preserving defaults or values from earlier providers. An empty
// variable counts as not set, except for a plain string, where empty is a value.
//
// Supported field types are strings, bools, integers (base 10), floats,
// time.Duration, url.URL, anything implementing encoding.TextUnmarshaler (such
// as time.Time and net.IP), pointers to those, slices of them (comma separated),
// and maps of them (comma separated key:value pairs). A struct with a tag is a
// nested group that extends the prefix, and an embedded struct without a tag has
// its fields loaded as if they were declared in the parent. A tagged field of
// any other type is an error, as is any tag option other than "file" and
// "alias"; use env:"-" to skip a field.
//
// Two fields can't read the same variable: that is an error, reported on the
// later one, and one of them has to be renamed.
//
// # Secrets in files
//
// A field with the file option can also be set from a file, which is how Docker
// and Kubernetes pass secrets:
//
//	type Config struct {
//	    Password string `env:"PASSWORD,file"`
//	}
//
// With the prefix APP, the provider reads APP_PASSWORD as usual, or, if
// APP_PASSWORD_FILE is set, the contents of the file it names, with trailing
// newlines removed. Setting both is an error, and so is a file that can't be read
// or a value in it that doesn't fit the field; those errors name the variable but
// never the path or what is in it. The file must be a regular file of at most 1 MiB:
// a named pipe or a device, which could block or never end, is refused. Symbolic
// links are followed, as a Kubernetes secret volume needs. A secret is only read when the field has the
// option, so APP_PASSWORD_FILE for a field without it is not recognised.
//
// # Names a platform chooses
//
// A platform such as Cloud Run or Heroku sets PORT or DATABASE_URL, not
// APP_PORT. A field with the alias option reads such a name as it is, when its
// own variable is not set:
//
//	type Config struct {
//	    Port int    `env:"PORT,alias=PORT"`
//	    DSN  string `env:"DSN,alias=DATABASE_URL"`
//	}
//
// APP_PORT wins when both are set, so the platform's value can still be
// overridden, and the alias is then not even parsed. Only a field that names an
// alias reads one, and it can't be combined with file. An alias is a name in
// capitals, at most 128 bytes, that isn't one the system reads (PATH, HOME,
// LD_PRELOAD) or that holds the credentials of another system
// (AWS_SECRET_ACCESS_KEY). Two fields can't share an alias.
package env

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/internal/config/decode"
	"go.krak3n.dev/foundation/internal/config/envname"
	"go.krak3n.dev/foundation/internal/config/safefile"
)

// An Option configures a [Provider].
type Option interface {
	applyProvider(*Provider)
}

// Options is a slice of [Option] that itself implements the [Option] interface.
// Each non-nil option in the slice is applied in order.
type Options []Option

func (o Options) applyProvider(p *Provider) {
	for _, opt := range o {
		if opt != nil {
			opt.applyProvider(p)
		}
	}
}

// OptionFunc is an adapter to allow the use of ordinary functions as [Option]
// values. If f is a function with the appropriate signature, OptionFunc(f) is
// an [Option] that calls f.
type OptionFunc func(*Provider)

func (fn OptionFunc) applyProvider(p *Provider) {
	fn(p)
}

// WithLookup sets where the provider reads variables from. lookup is given the
// exact name of a variable, such as APP_HTTP_SERVER_ADDR, and reports its value
// and whether it is set. It has the same signature as [os.LookupEnv], which is
// the default, so a nil lookup means the process environment.
//
// A map backed lookup lets a test give the code under test its own environment,
// without t.Setenv and so with t.Parallel:
//
//	lookup := func(key string) (string, bool) { v, ok := vars[key]; return v, ok }
//
//	env.New(env.WithLookup(lookup))
func WithLookup(lookup func(key string) (value string, ok bool)) Option {
	return OptionFunc(func(p *Provider) {
		p.lookup = lookup
	})
}

// WithEnviron sets where the provider lists variables from, which it needs to
// find ones nothing asked for (see [Provider.Check]). environ returns entries of
// the form KEY=VALUE, as [os.Environ] does, which is the default, so a nil
// environ means the process environment.
//
// With a custom [WithLookup], supply this too: the provider can't list what a
// lookup holds. [WithMap] sets both.
func WithEnviron(environ func() []string) Option {
	return OptionFunc(func(p *Provider) {
		p.environ = environ
	})
}

// WithMap makes vars the whole environment: it answers lookups and is what
// [Provider.Check] lists. It is the one option a test needs. The map is copied,
// so it can be changed afterwards without effect.
func WithMap(vars map[string]string) Option {
	vars = maps.Clone(vars)

	return OptionFunc(func(p *Provider) {
		p.lookup = func(key string) (string, bool) {
			v, ok := vars[key]

			return v, ok
		}

		p.environ = func() []string {
			environ := make([]string, 0, len(vars))

			for k, v := range vars {
				environ = append(environ, k+"="+v)
			}

			return environ
		}
	})
}

// New returns a config.Provider that reads configuration from
// environment variables.
func New(opts ...Option) *Provider {
	p := &Provider{}

	Options(opts).applyProvider(p)

	return p
}

// Provider reads configuration from environment variables. Use [New] to
// create one. It is safe for concurrent use.
type Provider struct {
	lookup  func(key string) (string, bool)
	environ func() []string

	// requested is every variable name the provider has been asked for, whether
	// or not it was set: what [Provider.Check] treats as known.
	mu        sync.Mutex
	requested map[string]struct{}
}

// record notes that name was asked for.
func (p *Provider) record(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.requested == nil {
		p.requested = make(map[string]struct{})
	}

	p.requested[name] = struct{}{}
}

// Unmarshal reads environment variables with a prefix derived from path and
// populates dst. Only fields that have a matching environment variable are set.
func (p *Provider) Unmarshal(path []string, dst any) error {
	elem, err := structOf(dst)
	if err != nil {
		return err
	}

	lookup := p.lookup
	if lookup == nil {
		lookup = os.LookupEnv
	}

	prefix := strings.ToUpper(strings.Join(config.Keys(path), "_"))

	w := &walker{lookup: lookup, record: p.record, seen: make(map[string]string)}

	return errors.Join(w.walk(prefix, "", elem)...)
}

// Describe lists the variables the provider would read for dst at path, with the
// field each is for, in declaration order. It reads nothing: not the
// environment, and not dst, which is left as it is. Because it doesn't count as
// asking for the variables, [Provider.Check] still reports them if nothing
// loads. See [config.Describer].
func (p *Provider) Describe(path []string, dst any) ([]config.FieldKey, error) {
	elem, err := structOf(dst)
	if err != nil {
		return nil, err
	}

	var keys []config.FieldKey

	prefix := strings.ToUpper(strings.Join(config.Keys(path), "_"))

	w := &walker{
		lookup: func(string) (string, bool) { return "", false },
		record: func(string) {},
		leaf:   func(field, key string) { keys = append(keys, config.FieldKey{Field: field, Key: key}) },
		seen:   make(map[string]string),
	}

	errs := w.walk(prefix, "", elem)

	keys, err = config.Annotate("env", dst, keys)

	return keys, errors.Join(append(errs, err)...)
}

// FileFields lists the fields that can be read from a file, those with the file
// option, by Go name. It implements [config.FileBacked].
func (p *Provider) FileFields(path []string, dst any) ([]string, error) {
	elem, err := structOf(dst)
	if err != nil {
		return nil, err
	}

	var fields []string

	prefix := strings.ToUpper(strings.Join(config.Keys(path), "_"))

	w := &walker{
		lookup: func(string) (string, bool) { return "", false },
		record: func(string) {},
		file:   func(field string) { fields = append(fields, field) },
		seen:   make(map[string]string),
	}

	return fields, errors.Join(w.walk(prefix, "", elem)...)
}

// structOf returns the struct dst points to, or an error if it isn't a non-nil
// pointer to one.
func structOf(dst any) (reflect.Value, error) {
	rv := reflect.ValueOf(dst)

	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return reflect.Value{}, &InvalidUnmarshalError{Type: reflect.TypeOf(dst)}
	}

	return rv.Elem(), nil
}

// Check lists the variables under the prefix of root, the path of the root
// function, that nothing has asked for, and returns a [*config.FieldError]
// wrapping [config.ErrUnknown] for each, sorted by name and joined with
// [errors.Join]. It is a typo detector: SVC_ADRR is not read by anything that
// reads SVC_ADDR. Call it once every function has loaded its configuration, as
// [go.krak3n.dev/foundation.WithStrictConfig] does, as a variable that
// belongs to a function that hasn't loaded yet looks unknown.
//
// It needs a source to list. That is the process environment unless set with
// [WithEnviron] or [WithMap]; a custom [WithLookup] without one is an error,
// as the provider can't know what the lookup holds.
func (p *Provider) Check(root []string) error {
	if p.lookup != nil && p.environ == nil {
		return errors.New("env: checking for unknown variables needs WithEnviron (or WithMap) when WithLookup is used, as the provider can't list what a lookup holds")
	}

	environ := p.environ
	if environ == nil {
		environ = os.Environ
	}

	prefix := strings.ToUpper(strings.Join(config.Keys(root), "_")) + "_"

	p.mu.Lock()
	requested := maps.Clone(p.requested)
	p.mu.Unlock()

	var unknown []string

	for _, entry := range environ() {
		name, _, _ := strings.Cut(entry, "=")

		if name == "" || !strings.HasPrefix(name, prefix) {
			continue
		}

		if _, ok := requested[name]; !ok {
			unknown = append(unknown, name)
		}
	}

	slices.Sort(unknown)

	var errs []error

	for _, name := range slices.Compact(unknown) {
		errs = append(errs, &config.FieldError{Provider: "env", Key: name, Err: config.ErrUnknown})
	}

	return errors.Join(errs...)
}

// fileSuffix is added to a variable's name to get the one that holds the path of
// a file with the value, for fields with the file option.
const fileSuffix = "_FILE"

// walker loads a struct tree. seen is every variable a field has claimed, with
// the Go path of the field, so that two fields can't read the same one.
type walker struct {
	lookup func(string) (string, bool)
	record func(string)

	// file, if not nil, is told each field that can be read from a file.
	file func(field string)

	// leaf, if not nil, is told each field that holds a value and the variable it
	// reads; a field that can be read from a file is told twice.
	leaf func(field, key string)

	seen map[string]string
}

// walk loads the struct rv, whose variable names start with prefix and whose Go
// field names are reached through goPrefix, and returns every problem it finds:
// mistakes in the struct first, then problems with values. Fields that are fine
// are still set.
func (w *walker) walk(prefix, goPrefix string, rv reflect.Value) []error {
	fields, problems := decode.CommonFields(rv, "env", parseTag)

	var errs []error

	for _, p := range problems {
		key := ""
		if p.Name != "" {
			key = prefix + "_" + strings.ToUpper(p.Name)

			// A field with a mistake in it still owns its variable.
			w.record(key)
		}

		errs = append(errs, &config.FieldError{Provider: "env", Field: goPath(goPrefix, p.GoName), Key: key, Err: p.Err})
	}

	for _, f := range fields {
		key := prefix + "_" + strings.ToUpper(f.Name)
		field := goPath(goPrefix, f.GoName)
		file := slices.Contains(f.Options, optionFile)
		alias := aliasOf(f.Options)

		// Nested struct — recurse with extended prefix.
		if f.Group {
			if file {
				errs = append(errs, &config.FieldError{Provider: "env", Field: field, Key: key, Err: errors.New("the file option can't be used on a struct")})
			}

			if alias != "" {
				errs = append(errs, &config.FieldError{Provider: "env", Field: field, Key: key, Err: errors.New("the alias option can't be used on a struct")})
			}

			errs = append(errs, w.walk(key, field, f.Value)...)

			continue
		}

		if alias == key {
			errs = append(errs, &config.FieldError{
				Provider: "env",
				Field:    field,
				Key:      key,
				Err:      fmt.Errorf("variable %s is also this field's alias; remove the alias or rename one of them", key),
			})

			continue
		}

		names := []string{key}
		if alias != "" {
			names = append(names, alias)
		}

		if file {
			names = append(names, key+fileSuffix)

			if w.file != nil {
				w.file(field)
			}
		}

		if name, other, clash := w.claim(names, field); clash {
			errs = append(errs, &config.FieldError{
				Provider: "env",
				Field:    field,
				Key:      name,
				Err:      fmt.Errorf("variable %s is also read by field %s; rename one of them", name, other),
			})

			continue
		}

		for _, name := range names {
			w.record(name)

			if w.leaf != nil {
				w.leaf(field, name)
			}
		}

		if err := w.load(f.Value, field, key, alias, file); err != nil {
			errs = append(errs, err)
		}
	}

	return errs
}

// claim records that the field with Go path field reads every variable in names.
// If one of them is already read by another field it claims none and reports
// which, and by whom.
func (w *walker) claim(names []string, field string) (name, other string, clash bool) {
	for _, n := range names {
		if o, ok := w.seen[n]; ok {
			return n, o, true
		}
	}

	for _, n := range names {
		w.seen[n] = field
	}

	return "", "", false
}

// load sets the field fv, found by Go path field, from the variable key or, if
// file is set and there is one, the file named by key_FILE. If key isn't set and
// there is an alias, that variable is read instead.
func (w *walker) load(fv reflect.Value, field, key, alias string, file bool) error {
	val, ok := w.lookup(key)

	if file {
		fileKey := key + fileSuffix

		path, _ := w.lookup(fileKey)

		if path != "" {
			// An empty plain variable is no value here: nobody means an empty secret.
			if ok && val != "" {
				return &config.FieldError{
					Provider: "env",
					Field:    field,
					Key:      key,
					Err:      fmt.Errorf("both %s and %s are set; use one", key, fileKey),
				}
			}

			return loadFile(fv, field, fileKey, path)
		}
	}

	// A stray "FOO=" shouldn't wipe out a default or fail to parse.
	set := ok && (val != "" || decode.IsPlainString(fv.Type()))

	if alias != "" {
		aliasVal, aliasOK := w.lookup(alias)
		aliasSet := aliasOK && (aliasVal != "" || decode.IsPlainString(fv.Type()))

		switch {
		case set && aliasSet:
			// Names only, never values: the prefixed variable is used, which is
			// worth knowing when it is not the one that was meant.
			slog.Debug("env alias ignored", slog.String("field", field), slog.String("key", key), slog.String("alias", alias))
		case aliasSet:
			key, val, set = alias, aliasVal, true
		}
	}

	if !set {
		return nil
	}

	if err := decode.SetString(fv, val); err != nil {
		return &config.FieldError{Provider: "env", Field: field, Key: key, Err: err}
	}

	return nil
}

// maxSecretFile is the largest file a secret can be read from. A secret is a line
// or a certificate; anything larger is a mistake, and is refused rather than read
// into memory.
const maxSecretFile = 1 << 20

// loadFile sets fv from the contents of the file at path, which key named. The
// contents are a secret, so no error repeats them, and nor the path, which is
// configuration that may have been put in the wrong variable: errors name the
// variable (key), which is what an operator needs.
func loadFile(fv reflect.Value, field, key, path string) error {
	data, err := safefile.ReadFile(path, maxSecretFile)
	if err != nil {
		// The error names the path itself, so keep only the cause.
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			err = pathErr.Err
		}

		return &config.FieldError{Provider: "env", Field: field, Key: key, Err: fmt.Errorf("reading $%s: %w", key, err)}
	}

	// A secret file holds one line, and editors and echo end it with a newline.
	val := strings.TrimRight(string(data), "\r\n")

	if val == "" && !decode.IsPlainString(fv.Type()) {
		return nil
	}

	if err := decode.SetString(fv, val); err != nil {
		return &config.FieldError{Provider: "env", Field: field, Key: key, Err: fmt.Errorf("the value in $%s is not a valid %s", key, fv.Type())}
	}

	return nil
}

func goPath(prefix, name string) string {
	if prefix == "" {
		return name
	}

	return prefix + "." + name
}

// optionFile is the tag option that lets a field be read from a file.
const optionFile = "file"

// parseTag reads an env tag: a name, or "-" to skip the field, then options. The
// name may be left out when the field has a common name (see [config.Fields]).
// The options are "file" and "alias"; anything else is an error rather than
// silently ignored.
func parseTag(tag string) (decode.Tag, error) {
	name, options, _ := strings.Cut(tag, ",")

	name = strings.TrimSpace(name)

	if name == "-" {
		if options != "" {
			return decode.Tag{}, errors.New(`env tag "-" takes no options`)
		}

		return decode.Tag{Skip: true}, nil
	}

	// A tag with only options, env:",file", takes the name from the field's config
	// tag; one with no name from anywhere is a problem for the walker to report.
	var opts []string

	for _, option := range strings.Split(options, ",") {
		option = strings.TrimSpace(option)

		switch {
		case option == "":
		case option == optionFile:
			if !slices.Contains(opts, option) {
				opts = append(opts, option)
			}
		case option == optionAlias || strings.HasPrefix(option, optionAlias+"="):
			if aliasOf(opts) != "" {
				return decode.Tag{Name: name}, errors.New("the alias option is given twice")
			}

			alias := strings.TrimPrefix(option, optionAlias)
			alias = strings.TrimPrefix(alias, "=")

			if err := checkAlias(alias); err != nil {
				return decode.Tag{Name: name}, err
			}

			opts = append(opts, optionAlias+"="+alias)
		default:
			// The name is returned too, so the problem can say which variable it was for.
			return decode.Tag{Name: name}, fmt.Errorf("unknown env tag option %s", envname.Quote(option))
		}
	}

	if aliasOf(opts) != "" && slices.Contains(opts, optionFile) {
		return decode.Tag{Name: name}, errors.New("the alias option can't be combined with the file option")
	}

	return decode.Tag{Name: name, Options: opts}, nil
}

// optionAlias is the tag option that names a second variable to read, as it is,
// when the prefixed one isn't set: env:"PORT,alias=PORT".
const optionAlias = "alias"

// aliasOf returns the alias in a field's options, or "".
func aliasOf(options []string) string {
	for _, o := range options {
		if name, ok := strings.CutPrefix(o, optionAlias+"="); ok {
			return name
		}
	}

	return ""
}

// checkAlias reports why name can't be an alias, by the shared rule for the
// names of variables.
func checkAlias(name string) error {
	if name == "" {
		return errors.New("the alias option has no name; write alias=NAME")
	}

	if err := envname.Check(name); err != nil {
		return fmt.Errorf("alias: %w", err)
	}

	return nil
}

// InvalidUnmarshalError describes an invalid argument passed to Unmarshal.
type InvalidUnmarshalError struct {
	Type reflect.Type
}

func (e *InvalidUnmarshalError) Error() string {
	if e.Type == nil {
		return "env: Unmarshal(nil)"
	}

	if e.Type.Kind() != reflect.Pointer {
		return "env: Unmarshal(non-pointer " + e.Type.String() + ")"
	}

	return "env: Unmarshal(" + e.Type.String() + ")"
}

// EnvironmentLayer marks the provider as one for the environment layer.
func (p *Provider) EnvironmentLayer() {}
