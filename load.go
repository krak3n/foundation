package foundation

import (
	"errors"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/flags"
	"go.krak3n.dev/foundation/internal/config/decode"
	"go.krak3n.dev/foundation/internal/nilcheck"
	"go.krak3n.dev/foundation/internal/safetext"
)

// Load populates dst, a pointer to a struct, from configuration providers,
// looking up values by the hierarchical path of f. Fields no provider has a
// value for are left as they are, so set defaults on dst before calling Load.
//
// Providers are applied in order and later ones override earlier ones. With no
// providers given Load uses those chosen with [WithConfigProviders] on [Run], which
// by default read environment variables (and flags with [WithArgs]), and any
// documents named with [WithDocumentConfig]. Passing providers
// uses only those, ignoring the ones set on Run, and they are not part of the
// program's configuration: [WithStrictConfig] does not check them, and one that
// asks to restart the program is refused. Nil providers are skipped.
//
// Fields tagged `config:"expand"`, which must be strings, are templates: any
// ${NAME} in a value a provider supplied is replaced with the variable NAME, or
// ${NAME:-default} with its default if it is unset or empty, and $$ is a literal
// $. A variable that is unset and has no default is a problem. Values are read
// from the process environment, or the function given to [WithLookupEnv]. Only
// values supplied by providers are expanded, not defaults set on dst, and a value
// is never expanded twice: what a variable holds is not scanned again.
//
// Fields tagged `config:"required"` must hold a value once the providers have
// run, whether from a provider or a default set beforehand; zero, an empty
// string, a nil pointer and an empty slice or map count as not set, so use a
// pointer for a field that can be explicitly zero. Then, if dst implements
// [config.Validator] and there have been no problems, its Validate is called.
//
// Every provider runs, even after one has failed, so that all the problems are
// found in one go. If there were any, Load logs each one as an error, with the
// provider, field and key where the provider says them, and then f fails via
// [F.Fail].
func (f *F) Load(dst any, providers ...config.Provider) {
	if len(providers) == 0 {
		// Checked when the options were applied: none of these is nil.
		providers = f.root.providers
	} else {
		// A nil provider, a nil pointer inside the interface too, is skipped. This is
		// the one place one can get in, so nothing below looks for it.
		providers = slices.DeleteFunc(slices.Clone(providers), func(p config.Provider) bool { return nilcheck.Is(p) })

		f.refuseUnwatched(providers)
	}

	if f.root.helping {
		f.describeLoad(dst, providers)

		return
	}

	// Which fields are templates, and what they hold before the providers run, so
	// that afterwards only what a provider supplied is expanded.
	templates := newTemplates(dst)
	templates.refuseFileFields(f.Path(), dst, providers)

	var problems []error

	for _, p := range providers {
		if err := p.Unmarshal(f.Path(), dst); err != nil {
			problems = flatten(problems, err)
		}
	}

	problems = append(problems, templates.expand(f.root.lookupEnv, problems)...)

	problems = append(problems, requiredProblems(f.Path(), dst, providers, problems)...)

	// Rules about the whole struct make no sense while parts of it are missing
	// or wrong.
	if v, ok := dst.(config.Validator); ok && len(problems) == 0 {
		if err := v.Validate(); err != nil {
			problems = flatten(problems, err)
		}
	}

	f.failOnProblems(problems)
}

// templates are the fields of a struct marked `config:"expand"`, with what they
// held before the providers ran, and the mistakes in how they are marked.
type templates struct {
	fields   []decode.ExpandField
	before   []templateValue
	mistakes []error
}

// templateValue is what a template field held: whether it was set, and its text.
type templateValue struct {
	set  bool
	text string
}

func newTemplates(dst any) *templates {
	t := &templates{}

	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return t
	}

	fields, problems := decode.Expandable(rv.Elem())

	t.fields = fields

	for _, p := range problems {
		t.mistakes = append(t.mistakes, &config.FieldError{Field: p.GoName, Err: p.Err})
	}

	for _, f := range fields {
		t.before = append(t.before, textOf(f.Value))
	}

	return t
}

// refuseFileFields takes out of the templates, and reports, every field that one
// of the providers says it reads from a file: what such a file holds is a secret
// to be used as it is, not a template, and expanding it would alter or replace
// parts of it. Each provider says so itself (see [config.FileBacked]), so the
// rule holds for providers that don't use the env tag. Problems with the struct
// are left to the provider's own Unmarshal to report.
func (t *templates) refuseFileFields(path []string, dst any, providers []config.Provider) {
	if len(t.fields) == 0 {
		return
	}

	fromFile := map[string]bool{}

	for _, p := range providers {
		fb, ok := p.(config.FileBacked)
		if !ok {
			continue
		}

		fields, _ := fb.FileFields(path, dst)

		for _, field := range fields {
			fromFile[field] = true
		}
	}

	if len(fromFile) == 0 {
		return
	}

	var (
		kept   []decode.ExpandField
		before []templateValue
	)

	for i, field := range t.fields {
		if fromFile[field.Path] {
			t.mistakes = append(t.mistakes, &config.FieldError{
				Field: field.Path,
				Err:   errors.New("expand can't be used on a field read from a file: its contents are a secret, not a template"),
			})

			continue
		}

		kept = append(kept, field)
		before = append(before, t.before[i])
	}

	t.fields, t.before = kept, before
}

// textOf reads a string field, or a pointer to one.
func textOf(v reflect.Value) templateValue {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return templateValue{}
		}

		v = v.Elem()
	}

	return templateValue{set: true, text: v.String()}
}

// expand replaces the references in every template field a provider changed, and
// returns the problems: mistakes in how fields are marked, then variables that
// aren't defined and syntax errors, field by field. A field that couldn't be
// expanded is left as it was, and one a provider has already reported is skipped.
func (t *templates) expand(lookup func(string) (string, bool), reported []error) []error {
	errs := slices.Clone(t.mistakes)

	if lookup == nil {
		lookup = os.LookupEnv
	}

	for i, field := range t.fields {
		now := textOf(field.Value)

		// Unchanged is a default, or a value an earlier Load already expanded.
		if !now.set || now == t.before[i] {
			continue
		}

		if reportedField(reported, field.Path) {
			continue
		}

		out, undefined, err := decode.Expand(now.text, lookup)

		for _, name := range undefined {
			errs = append(errs, &config.FieldError{Field: field.Path, Key: name, Err: config.ErrUndefined})
		}

		if err != nil {
			errs = append(errs, &config.FieldError{Field: field.Path, Err: err})
		}

		if err != nil || len(undefined) > 0 {
			continue
		}

		setText(field.Value, out)
	}

	return errs
}

// setText sets a string field, or points a pointer field at a new string, as the
// old one may be shared.
func setText(v reflect.Value, text string) {
	if v.Kind() == reflect.Pointer {
		p := reflect.New(v.Type().Elem())

		p.Elem().SetString(text)
		v.Set(p)

		return
	}

	v.SetString(text)
}

// reportedField says whether one of the problems is about the field: it has been
// said, so it is not said again in other words.
func reportedField(problems []error, field string) bool {
	return slices.ContainsFunc(problems, func(err error) bool {
		var fe *config.FieldError

		return errors.As(err, &fe) && fe.Field == field
	})
}

// requiredProblems reports the fields of dst marked `config:"required"` that
// are still unset, and mistakes in how they are marked. A field a provider has
// already reported is not also reported as missing, as that problem has been
// said.
func requiredProblems(path []string, dst any, providers []config.Provider, reported []error) []error {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return nil
	}

	missing, mistakes := decode.Required(rv.Elem())

	var errs []error

	for _, m := range mistakes {
		// A provider reads the same tag, and has said so if it is wrong.
		if reportedField(reported, m.GoName) {
			continue
		}

		errs = append(errs, &config.FieldError{Field: m.GoName, Err: m.Err})
	}

	keys := describeKeys(path, dst, providers, len(missing) > 0)

	for _, field := range missing {
		if !reportedField(reported, field) {
			errs = append(errs, &config.FieldError{Field: field, Key: keys[field], Err: config.ErrRequired})
		}
	}

	return errs
}

// checkConfig asks each provider that can check which of its values nothing
// asked for, and fails f if there are any. It does nothing if the run is
// already failing or shutting down, as that has said what is wrong.
func (f *F) checkConfig() {
	if f.root.stopping.Load() {
		return
	}

	var problems []error

	if f.root.strictConfig {
		for _, p := range f.root.providers {
			checker, ok := p.(config.Checker)
			if !ok {
				continue
			}

			// Arguments are checked below, always, and strict mode's ignore list is
			// not for them.
			if _, isFlags := p.(*flags.Provider); isFlags {
				continue
			}

			if err := checker.Check(f.Path()); err != nil {
				problems = flatten(problems, err)
			}
		}

		problems = f.withoutIgnored(problems)
	}

	// A flag is typed by a person, so one nothing read is a mistake whether or not
	// strict mode is on.
	for _, p := range f.root.flagProviders() {
		if err := p.Check(f.Path()); err != nil {
			problems = flatten(problems, err)
		}
	}

	f.failOnProblems(problems)
}

// withoutIgnored drops the problems that [WithStrictIgnore] covers and says what
// it dropped. Only a key reported as not recognised can be covered: a problem
// with a field, or one that isn't about a key at all, is kept whatever its text.
func (f *F) withoutIgnored(problems []error) []error {
	matcher := f.root.strictIgnored
	if len(matcher.Patterns()) == 0 {
		return problems
	}

	var kept []error

	ignored := 0

	for _, err := range problems {
		// A direct assertion, not errors.As, on purpose: a problem that has been
		// wrapped is not the plain "not recognised" a pattern is meant for, so it is
		// kept and reported. Looser matching here would hide more, never less.
		fe, ok := err.(*config.FieldError)
		if !ok || fe.Field != "" || fe.Key == "" || !errors.Is(fe.Err, config.ErrUnknown) || !matcher.Match(fe.Key) {
			kept = append(kept, err)

			continue
		}

		ignored++

		f.Log().LogAttrs(f.Context(), slog.LevelDebug, "ignored unknown key",
			slog.String("provider", safetext.Escape(fe.Provider, false)), slog.String("key", safetext.Escape(fe.Key, false)))
	}

	f.Log().LogAttrs(f.Context(), slog.LevelInfo, "ignored unknown configuration",
		slog.Int("count", ignored), slog.String("patterns", strings.Join(matcher.Patterns(), ", ")))

	return kept
}

// describeKeys maps each field of dst to where the providers that can say would
// look for it, separated by commas in provider order. It does nothing unless
// needed, and a provider that can't describe itself is left out. A problem with
// the struct has already been reported by the provider, so is ignored here.
func describeKeys(path []string, dst any, providers []config.Provider, needed bool) map[string]string {
	if !needed {
		return nil
	}

	byField := make(map[string][]string)

	for _, p := range providers {
		d, ok := p.(config.Describer)
		if !ok {
			continue
		}

		keys, _ := d.Describe(path, dst)

		for _, k := range keys {
			byField[k.Field] = append(byField[k.Field], k.Key)
		}
	}

	out := make(map[string]string, len(byField))

	for field, keys := range byField {
		out[field] = strings.Join(keys, ", ")
	}

	return out
}

// failOnProblems logs each configuration problem as an error, then fails f with
// a summary. It does nothing if there are none.
func (f *F) failOnProblems(problems []error) {
	if len(problems) == 0 {
		return
	}

	f.logProblems(problems)

	f.Fail(errors.New("invalid configuration"), slog.Int("count", len(problems)))
}

// logProblems logs each configuration problem as an error.
func (f *F) logProblems(problems []error) {
	for _, err := range problems {
		f.logProblem(err)
	}
}

// logProblem logs one configuration problem as an error, naming the provider,
// field and key when it is a [*config.FieldError].
func (f *F) logProblem(err error) {
	var attrs []slog.Attr

	var fe *config.FieldError

	if errors.As(err, &fe) {
		for _, a := range []struct{ key, value string }{{"provider", fe.Provider}, {"field", fe.Field}, {"key", fe.Key}} {
			if a.value != "" {
				attrs = append(attrs, slog.String(a.key, safetext.Escape(a.value, false)))
			}
		}

		err = fe.Err
	}

	msg := "unknown error"
	if err != nil {
		msg = err.Error()
	}

	attrs = append(attrs, slog.String("err", safetext.Escape(msg, false)))

	f.Log().LogAttrs(f.Context(), slog.LevelError, "invalid configuration", attrs...)
}

// flatten appends the individual errors in err, however deeply joined, to errs.
func flatten(errs []error, err error) []error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			errs = flatten(errs, e)
		}

		return errs
	}

	return append(errs, err)
}
