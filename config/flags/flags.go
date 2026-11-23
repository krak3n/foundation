// Package flags provides a config.Provider that reads configuration from command
// line arguments.
//
// Fields opt in with the "flag" struct tag; a field without it can't be set from
// the command line:
//
//	type Config struct {
//	    Addr string `flag:"addr"`
//	    Port int    `flag:"port,short=p"`
//	}
//
// # Writing flags
//
// A flag is --name=value or --name value, and a bool is bare (--verbose) or takes
// an equals sign (--verbose=false); a bool never takes the next argument. A short
// flag is -p=8080 or -p 8080 and needs the tag's short option, a single ASCII
// letter. There are no clusters (-abc), no attached values (-p8080), no "--", no
// abbreviations and no case folding: an argument is a flag exactly as it is
// written, or an error. A value that starts with a dash has to use the equals
// sign (--offset=-1), because otherwise it can't be told from a flag.
//
// # Scopes
//
// Every function still runs; a marker only says whose configuration the flags
// after it are for. The root function's flags come first, with no marker. A
// function's marker is its name, which is split into words as it is for
// environment variables and files (see [config.Keys]), lower cased, with
// underscores as hyphens, and a sub function's marker follows its parent's:
//
//	svc --addr=:80 --http --server --port 8080 --worker --count 4
//
// Here --http --server is the function "http.server" (or "http" with a child
// "server"), and --worker is another. A flag belongs to the nearest marker before
// it, and a scope ends at the first flag it doesn't have, so flags never reach
// the wrong function. The same name under different parents is different
// functions (--a --c and --b --c); when one marker could mean two functions, or a
// marker is also a flag, it is an error rather than a guess.
//
// A nested struct with a tag extends the name with a hyphen: a group tagged
// flag:"db" with a field tagged flag:"host" is --db-host.
//
// # What is checked
//
// Nothing is ignored. [Provider.Check], which the framework calls when startup
// ends, reports every argument that no function read: a misspelt flag, a flag
// given before its marker, a stray word. Reports name the flag as it was written
// and never repeat a value, because a value could be a password typed into the
// wrong flag. Arguments are bounded: see [MaxArgs], [MaxArgLength], [MaxDepth]
// and [MaxNameLength].
//
// # Secrets
//
// Arguments are visible to every user of the machine (ps, /proc) and end up in
// shell history, so they are no place for a secret: a field with an env tag that
// has the file option can't also be a flag.
package flags

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/internal/config/decode"
)

// The limits on what is read. Past them the provider fails closed, since an
// argument list this size is not one a person wrote.
const (
	// MaxArgs is the most arguments accepted.
	MaxArgs = 1024

	// MaxArgLength is the longest single argument, in bytes.
	MaxArgLength = 4096

	// MaxDepth is how many markers deep a function can be.
	MaxDepth = 16

	// MaxNameLength is the longest flag or marker name, in bytes.
	MaxNameLength = 64
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
// values.
type OptionFunc func(*Provider)

func (fn OptionFunc) applyProvider(p *Provider) {
	fn(p)
}

// WithArgs sets the arguments to read, without the program name: os.Args[1:]. They
// are copied. The provider never reads os.Args itself, so the arguments are
// always a choice the program made.
func WithArgs(args []string) Option {
	args = slices.Clone(args)

	return OptionFunc(func(p *Provider) {
		p.args = args
	})
}

// New returns a config.Provider that reads configuration from the
// arguments given with [WithArgs]; with none it reads nothing.
func New(opts ...Option) *Provider {
	p := &Provider{}

	Options(opts).applyProvider(p)

	p.parse()

	return p
}

// Provider reads configuration from command line arguments. Use [New] to
// create one. It is safe for concurrent use.
type Provider struct {
	args []string

	tokens []token

	// argErr is why the arguments are refused as a whole, if they are.
	argErr error

	mu sync.Mutex

	// claimed is who has read each token, by index.
	claimed map[int]claim

	// names are the flags each scope has given out, by scope, and to whom, so that
	// two fields can't share one. The same field asking again (the provider is
	// used by every instance of a program that restarts, and by a restart's trial
	// run beside the live one) is not a second field.
	names map[string]map[string]owner
}

// owner is a field that has a flag: the struct it is in and its Go path, which is
// what tells two fields apart whatever they are called.
type owner struct {
	typ   reflect.Type
	field string
}

// claim says which function read a token, and whether as its marker.
type claim struct {
	scope  string
	marker bool

	// typ is the struct that read a flag or its value. The same struct reads it
	// again each time it is loaded again, which is not a second reader.
	typ reflect.Type
}

// helpScope is the scope of the claim on the argument that asks for help.
const helpScope = "-help"

// kind is what an argument is.
type kind int

const (
	// kindLong is --name or --name=value.
	kindLong kind = iota

	// kindShort is -x or -x=value.
	kindShort

	// kindOther is anything that isn't a flag: a word, or something malformed.
	kindOther
)

// token is one argument.
type token struct {
	kind kind

	// name is the long name or the short letter.
	name string

	value string

	// hasValue is set when the argument has an equals sign.
	hasValue bool

	// dash is set when the argument starts with a dash, so can't be a value.
	dash bool

	// label is how the argument is named in a report: never its value.
	label string

	// malformed is set for something that looked like a flag and isn't one.
	malformed bool
}

func (p *Provider) parse() {
	if len(p.args) > MaxArgs {
		p.argErr = fmt.Errorf("too many arguments (at most %d are read)", MaxArgs)

		return
	}

	for _, a := range p.args {
		if len(a) > MaxArgLength {
			p.argErr = fmt.Errorf("an argument is longer than %d bytes", MaxArgLength)

			return
		}
	}

	p.tokens = make([]token, len(p.args))

	for i, a := range p.args {
		p.tokens[i] = tokenize(i, a)
	}
}

func tokenize(i int, arg string) token {
	other := token{kind: kindOther, dash: strings.HasPrefix(arg, "-"), label: fmt.Sprintf("argument %d", i+1)}

	switch {
	case strings.HasPrefix(arg, "--"):
		rest := arg[2:]

		name, value, hasValue := strings.Cut(rest, "=")

		if !validName(name, true) {
			return token{kind: kindOther, dash: true, malformed: true, label: "--" + bounded(name)}
		}

		return token{kind: kindLong, name: name, value: value, hasValue: hasValue, dash: true, label: "--" + name}
	case strings.HasPrefix(arg, "-"):
		if len(arg) < 2 || !isLetter(arg[1]) {
			return other
		}

		letter := arg[1:2]

		switch {
		case len(arg) == 2:
			return token{kind: kindShort, name: letter, dash: true, label: "-" + letter}
		case arg[2] == '=':
			return token{kind: kindShort, name: letter, value: arg[3:], hasValue: true, dash: true, label: "-" + letter}
		default:
			// An attached value or a cluster. Only the letter is named, as the rest
			// could be a value.
			return token{kind: kindOther, dash: true, malformed: true, label: "-" + letter}
		}
	}

	return other
}

// bounded makes text safe to put in a report: valid UTF-8, and not long.
func bounded(s string) string {
	s = strings.ToValidUTF8(s, "�")

	if len(s) <= MaxNameLength {
		return s
	}

	cut := MaxNameLength

	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}

	return s[:cut] + "…"
}

func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// validName reports whether s is a name a flag or marker can have: lower case
// letters, digits and hyphens, not starting or ending with a hyphen, and short.
// Flag names also start with a letter; markers, which come from function names,
// may start with a digit.
func validName(s string, marker bool) bool {
	if s == "" || len(s) > MaxNameLength {
		return false
	}

	for i := 0; i < len(s); i++ {
		c := s[i]

		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
			if i == 0 && !marker {
				return false
			}
		case c == '-':
			if i == 0 || i == len(s)-1 {
				return false
			}
		default:
			return false
		}
	}

	return true
}

// problem is a FieldError from this provider.
func problem(field, key string, err error) error {
	return &config.FieldError{Provider: "flags", Field: field, Key: key, Err: err}
}

// spec is one field that can be read from a flag.
type spec struct {
	field  string // Go path: "DB.Port"
	name   string // the long name, with group names hyphenated on
	short  string // the short letter, or ""
	value  reflect.Value
	isBool bool
}

// collect lists the fields of the struct rv that can be flags, in declaration
// order, and the mistakes in how they are tagged.
func collect(rv reflect.Value) ([]spec, []error) {
	var (
		specs []spec
		errs  []error
	)

	walk(rv, "", "", &specs, &errs)

	return specs, errs
}

func walk(rv reflect.Value, namePrefix, goPrefix string, specs *[]spec, errs *[]error) {
	fields, problems := decode.Fields(rv, "flag", parseTag)

	for _, p := range problems {
		key := ""
		if p.Name != "" {
			key = "--" + join(namePrefix, p.Name)
		}

		*errs = append(*errs, problem(goPath(goPrefix, p.GoName), key, p.Err))
	}

	for _, f := range fields {
		field := goPath(goPrefix, f.GoName)
		name := join(namePrefix, f.Name)
		short := shortOf(f.Options)

		if len(name) > MaxNameLength {
			*errs = append(*errs, problem(field, "", fmt.Errorf("the flag name is longer than %d bytes once its groups are added", MaxNameLength)))

			continue
		}

		if f.Group {
			if short != "" {
				*errs = append(*errs, problem(field, "", errors.New("the short option can't be used on a struct")))
			}

			walk(f.Value, name, field, specs, errs)

			continue
		}

		if readsFile(rv.Type(), f.GoName) {
			*errs = append(*errs, problem(field, "--"+name, errors.New("can't be a flag: the field reads a secret from a file, and arguments are visible to other users")))

			continue
		}

		if name == helpName || short == helpShort {
			*errs = append(*errs, problem(field, "--"+name, errors.New("--help and -h are reserved: they ask for help, wherever they are given")))

			continue
		}

		t := f.Value.Type()
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}

		*specs = append(*specs, spec{
			field:  field,
			name:   name,
			short:  short,
			value:  f.Value,
			isBool: t.Kind() == reflect.Bool,
		})
	}
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}

	return prefix + "-" + name
}

func goPath(prefix, name string) string {
	if prefix == "" {
		return name
	}

	return prefix + "." + name
}

// The words that ask for help. They mean the same wherever they are given, so no
// field can be one, and no function can be called help.
const (
	helpName  = "help"
	helpShort = "h"
)

// isHelp reports whether t is --help or -h, with or without a value.
func isHelp(t token) bool {
	return (t.kind == kindLong && t.name == helpName) || (t.kind == kindShort && t.name == helpShort)
}

// optionShort is the tag option that gives a flag a short form.
const optionShort = "short"

func shortOf(options []string) string {
	for _, o := range options {
		if v, ok := strings.CutPrefix(o, optionShort+"="); ok {
			return v
		}
	}

	return ""
}

// parseTag reads a flag tag: a name, or "-" to skip the field, then options. The
// only option is short=x; anything else is an error rather than ignored.
func parseTag(tag string) (decode.Tag, error) {
	name, options, _ := strings.Cut(tag, ",")

	name = strings.TrimSpace(name)

	if name == "-" {
		if options != "" {
			return decode.Tag{}, errors.New(`flag tag "-" takes no options`)
		}

		return decode.Tag{Skip: true}, nil
	}

	if name == "" {
		return decode.Tag{}, errors.New("flag tag has no name")
	}

	if !validName(name, false) {
		return decode.Tag{}, fmt.Errorf("%s is not a flag name: use lower case letters, digits and hyphens, starting with a letter, at most %d bytes", quote(name), MaxNameLength)
	}

	var opts []string

	for _, option := range strings.Split(options, ",") {
		option = strings.TrimSpace(option)

		switch {
		case option == "":
		case strings.HasPrefix(option, optionShort+"="):
			if shortOf(opts) != "" {
				return decode.Tag{}, errors.New("the short option is given twice")
			}

			letter := strings.TrimPrefix(option, optionShort+"=")

			if len(letter) != 1 || !isLetter(letter[0]) {
				return decode.Tag{}, fmt.Errorf("short=%s is not a letter: a short flag is a single ASCII letter", quote(letter))
			}

			opts = append(opts, option)
		default:
			return decode.Tag{}, fmt.Errorf("unknown flag tag option %s", quote(option))
		}
	}

	return decode.Tag{Name: name, Options: opts}, nil
}

// quote shows tag text in an error, bounded.
func quote(s string) string {
	if len(s) > 32 {
		s = s[:32] + "…"
	}

	return fmt.Sprintf("%q", s)
}

// readsFile reports whether the field goName of the struct type rt has an env tag
// with the file option: its value is a secret that comes from a file.
func readsFile(rt reflect.Type, goName string) bool {
	sf, ok := structField(rt, goName)
	if !ok {
		return false
	}

	tag, ok := sf.Tag.Lookup("env")
	if !ok {
		return false
	}

	_, options, _ := strings.Cut(tag, ",")

	for _, o := range strings.Split(options, ",") {
		if strings.TrimSpace(o) == "file" {
			return true
		}
	}

	return false
}

// structField finds a field by Go name, looking into embedded structs, which
// providers load as if their fields were declared in the parent.
func structField(rt reflect.Type, goName string) (reflect.StructField, bool) {
	for i := range rt.NumField() {
		sf := rt.Field(i)

		if sf.Name == goName {
			return sf, true
		}

		if sf.Anonymous && sf.Type.Kind() == reflect.Struct {
			if got, ok := structField(sf.Type, goName); ok {
				return got, true
			}
		}
	}

	return reflect.StructField{}, false
}

// structOf returns the struct dst points to, or an error if it isn't a non-nil
// pointer to one.
func structOf(dst any) (reflect.Value, error) {
	rv := reflect.ValueOf(dst)

	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("flags: Unmarshal(%T): need a non-nil pointer to a struct", dst)
	}

	return rv.Elem(), nil
}

// markers are the words that say a function's flags follow: the keys of its path
// after the root function's, in the form a flag is written in.
func markers(path []string) ([]string, error) {
	if len(path) == 0 {
		return nil, errors.New("the path of the function is empty")
	}

	keys := config.Keys(path[1:])

	if len(keys) > MaxDepth {
		return nil, fmt.Errorf("the function is more than %d names deep", MaxDepth)
	}

	out := make([]string, 0, len(keys))

	for _, k := range keys {
		m := strings.ToLower(strings.ReplaceAll(k, "_", "-"))

		if m == helpName {
			return nil, errors.New("a function can't be called help: --help is reserved, so it could never be addressed")
		}

		if !validName(m, true) {
			return nil, errors.New("a name in the path can't be written as a flag marker: use letters, digits and underscores, starting with one, at most 64 bytes")
		}

		out = append(out, m)
	}

	return out, nil
}

// chainOf names a function by its markers, up to and including the first n.
func chainOf(ms []string, n int) string {
	return strings.Join(ms[:n], "/")
}

// Unmarshal reads the flags that belong to the function at path into dst. See the
// package documentation for which those are.
func (p *Provider) Unmarshal(path []string, dst any) error {
	elem, err := structOf(dst)
	if err != nil {
		return err
	}

	specs, errs := collect(elem)

	if len(specs) == 0 {
		return errors.Join(errs...)
	}

	if p.argErr != nil {
		return errors.Join(append(errs, problem("", "", p.argErr))...)
	}

	ms, err := markers(path)
	if err != nil {
		return errors.Join(append(errs, problem("", "", err))...)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	scope := chainOf(ms, len(ms))

	specs, errs = p.register(scope, elem.Type(), specs, errs)
	if len(specs) == 0 {
		return errors.Join(errs...)
	}

	// Find the markers: each one after the one before.
	prev := -1

	for i, m := range ms {
		idx := p.findMarker(m, prev)
		if idx < 0 {
			return errors.Join(errs...)
		}

		if err := p.claimMarker(idx, chainOf(ms, i+1)); err != nil {
			return errors.Join(append(errs, err)...)
		}

		prev = idx
	}

	got, errs := p.scan(scope, elem.Type(), prev+1, specs, errs)

	for _, s := range specs {
		as := got[s.field]

		switch {
		case len(as) == 0:
		case len(as) > 1:
			errs = append(errs, problem(s.field, as[0].label, fmt.Errorf("%w: the flag is given more than once", config.ErrAmbiguous)))
		default:
			if err := set(s, as[0]); err != nil {
				errs = append(errs, problem(s.field, as[0].label, err))
			}
		}
	}

	return errors.Join(errs...)
}

// register gives the scope the flags of specs, which are fields of the struct
// type typ, dropping, and reporting, any that another field of the scope has.
func (p *Provider) register(scope string, typ reflect.Type, specs []spec, errs []error) ([]spec, []error) {
	if p.names == nil {
		p.names = make(map[string]map[string]owner)
	}

	taken := p.names[scope]
	if taken == nil {
		taken = make(map[string]owner)
		p.names[scope] = taken
	}

	kept := specs[:0:0]

	for _, s := range specs {
		keys := []string{"--" + s.name}
		if s.short != "" {
			keys = append(keys, "-"+s.short)
		}

		var (
			clash bool
			me    = owner{typ: typ, field: s.field}
		)

		for _, k := range keys {
			if other, ok := taken[k]; ok && other != me {
				errs = append(errs, problem(s.field, k, fmt.Errorf("%s is also read by field %s; rename one of them", k, other.field)))

				clash = true

				break
			}
		}

		if clash {
			continue
		}

		for _, k := range keys {
			taken[k] = me
		}

		kept = append(kept, s)
	}

	return kept, errs
}

// findMarker returns the index of the first --name after index prev, with no
// value, or -1.
func (p *Provider) findMarker(name string, prev int) int {
	for i := prev + 1; i < len(p.tokens); i++ {
		t := p.tokens[i]

		if t.kind == kindLong && !t.hasValue && t.name == name {
			return i
		}
	}

	return -1
}

func (p *Provider) claimMarker(idx int, scope string) error {
	if c, ok := p.claimed[idx]; ok && !(c.marker && c.scope == scope) {
		return problem("", p.tokens[idx].label, fmt.Errorf("%w: the argument could mean more than one function or flag", config.ErrAmbiguous))
	}

	p.mark(idx, claim{scope: scope, marker: true})

	return nil
}

func (p *Provider) mark(idx int, c claim) {
	if p.claimed == nil {
		p.claimed = make(map[int]claim)
	}

	p.claimed[idx] = c
}

// assignment is a value a flag was given.
type assignment struct {
	label string
	value string
	bare  bool
}

// scan reads the flags of specs from index start on, until the first flag that
// isn't theirs. It returns the values found for each field, by Go name.
func (p *Provider) scan(scope string, typ reflect.Type, start int, specs []spec, errs []error) (map[string][]assignment, []error) {
	got := make(map[string][]assignment)

	for i := start; i < len(p.tokens); i++ {
		t := p.tokens[i]

		if t.kind == kindOther || isHelp(t) {
			continue
		}

		// Flags this scope has read already, for another struct, are not its end.
		if c, ok := p.claimed[i]; ok && !c.marker && c.scope == scope && c.typ != typ {
			continue
		}

		s, ok := find(specs, t)
		if !ok {
			break
		}

		if p.claimedAt(i) && !p.readBy(i, scope, typ) {
			errs = append(errs, problem(s.field, t.label, fmt.Errorf("%w: the argument could mean more than one function or flag", config.ErrAmbiguous)))

			break
		}

		p.mark(i, claim{scope: scope, typ: typ})

		a := assignment{label: t.label, value: t.value}

		switch {
		case t.hasValue:
		case s.isBool:
			a.bare = true
		default:
			if i+1 < len(p.tokens) && !p.tokens[i+1].dash && (!p.claimedAt(i+1) || p.readBy(i+1, scope, typ)) {
				i++

				a.value = p.args[i]

				p.mark(i, claim{scope: scope, typ: typ})
			} else {
				errs = append(errs, problem(s.field, t.label, errors.New("needs a value: give it as --name=value or --name value (a value that starts with - needs the equals sign)")))

				continue
			}
		}

		got[s.field] = append(got[s.field], a)
	}

	return got, errs
}

// readBy says whether the struct typ, loading in scope, is the one that read token
// i, as it did the last time it was loaded.
func (p *Provider) readBy(i int, scope string, typ reflect.Type) bool {
	c, ok := p.claimed[i]

	return ok && !c.marker && c.scope == scope && c.typ == typ
}

func (p *Provider) claimedAt(i int) bool {
	_, ok := p.claimed[i]

	return ok
}

// find returns the spec token t is a flag of.
func find(specs []spec, t token) (spec, bool) {
	for _, s := range specs {
		switch {
		case t.kind == kindLong && t.name == s.name:
			return s, true
		case t.kind == kindShort && s.short != "" && t.name == s.short:
			return s, true
		}
	}

	return spec{}, false
}

// set sets the field of s from a.
func set(s spec, a assignment) error {
	text := a.value

	if a.bare {
		text = "true"
	}

	if text == "" && !decode.IsPlainString(s.value.Type()) {
		return errors.New("the value is empty")
	}

	return decode.SetString(s.value, text)
}

// Describe lists the flags the provider would read for dst at path, with the
// field each is for, in declaration order: the long form, then the short one. It
// reads nothing, and doesn't count as asking for them: see [config.Describer].
func (p *Provider) Describe(path []string, dst any) ([]config.FieldKey, error) {
	elem, err := structOf(dst)
	if err != nil {
		return nil, err
	}

	specs, errs := collect(elem)

	if len(specs) == 0 {
		return nil, errors.Join(errs...)
	}

	ms, err := markers(path)
	if err != nil {
		return nil, errors.Join(append(errs, problem("", "", err))...)
	}

	words := ""
	for _, m := range ms {
		words += "--" + m + " "
	}

	var keys []config.FieldKey

	for _, s := range specs {
		keys = append(keys, config.FieldKey{Field: s.field, Key: words + "--" + s.name})

		if s.short != "" {
			keys = append(keys, config.FieldKey{Field: s.field, Key: words + "-" + s.short})
		}
	}

	keys, err = config.Annotate("flags", dst, keys)

	return keys, errors.Join(append(errs, err)...)
}

// Help reports whether help was asked for, with --help or -h, and for which
// function: scope is the markers of the function in force where it was given,
// such as ["http", "server"] for --http --server --help, or none for the root. It
// is the markers that functions claimed, so the scope is only known once they
// have loaded; asked before that it says only whether help was asked for.
//
// Asking is not enough to be help: --help given twice, or with a value, is a
// problem (and ok is false), because nothing is guessed. The argument counts as
// read, so [Provider.Check] doesn't report it. --help doesn't end the scope of
// the function in front of it, so it can be given after that function's flags.
func (p *Provider) Help() (scope []string, ok bool, err error) {
	if p.argErr != nil {
		return nil, false, problem("", "", p.argErr)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	var asked []int

	for i, t := range p.tokens {
		if isHelp(t) {
			asked = append(asked, i)
		}
	}

	switch {
	case len(asked) == 0:
		return nil, false, nil
	case len(asked) > 1:
		return nil, false, problem("", p.tokens[asked[1]].label, fmt.Errorf("%w: help is asked for more than once", config.ErrAmbiguous))
	}

	idx := asked[0]

	if p.tokens[idx].hasValue {
		return nil, false, problem("", p.tokens[idx].label, errors.New("help takes no value"))
	}

	p.mark(idx, claim{scope: helpScope})

	for i := idx - 1; i >= 0; i-- {
		if c, found := p.claimed[i]; found && c.marker {
			return strings.Split(c.scope, "/"), true, nil
		}
	}

	return nil, true, nil
}

// Check reports every argument that nothing read, as a [*config.FieldError]
// wrapping [config.ErrUnknown], in the order they were given, joined with
// [errors.Join]. It names a flag as it was written and a word by its position,
// never a value. Call it once every function has loaded its configuration, as the
// framework does when startup ends.
func (p *Provider) Check(root []string) error {
	if p.argErr != nil {
		return problem("", "", p.argErr)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	var errs []error

	for i, t := range p.tokens {
		if _, ok := p.claimed[i]; ok {
			continue
		}

		err := config.ErrUnknown

		if t.malformed {
			err = fmt.Errorf("%w: not a flag this provider can read", config.ErrUnknown)
		}

		errs = append(errs, problem("", t.label, err))
	}

	return errors.Join(errs...)
}

// CommandLayer marks the provider as one for the command layer.
func (p *Provider) CommandLayer() {}
