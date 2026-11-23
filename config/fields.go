package config

import (
	"errors"
	"reflect"

	"go.krak3n.dev/foundation/internal/config/decode"
)

// Field is a struct field a provider should load.
type Field struct {
	// GoName is the name of the struct field. A field of an embedded struct has
	// its own name, as it is loaded as if declared in the parent.
	GoName string

	// Name is the key from the field's struct tag.
	Name string

	// Value is the field, settable.
	Value reflect.Value

	// Group is true if the field is a nested struct to walk into, with Name
	// extending the path, rather than a value to set.
	Group bool

	// Options are the provider's own options from the tag, as parse returned them
	// in the [Tag]. Fields doesn't interpret them.
	Options []string

	// Common is true if Name is the common name from the field's config tag,
	// because the provider's own tag gave none. A provider that changes the case
	// of names, as the environment's does, can leave the names it was given alone.
	Common bool
}

// Problem is a field that can't be loaded, however it is configured.
type Problem struct {
	// GoName is the name of the struct field.
	GoName string

	// Name is the key from the field's struct tag, empty if the tag has none.
	Name string

	Err error
}

// Tag is a provider's reading of a struct tag.
type Tag struct {
	// Name is the key to look the field up by.
	Name string

	// Skip leaves the field alone.
	Skip bool

	// Options are options of the provider's own that come after the name in the
	// tag, such as "file". They are handed back on the [Field] for the provider to
	// act on, so a provider can have options of its own without Fields knowing
	// what they mean.
	Options []string
}

// These fail to compile if the types above and their internal counterparts, which
// the conversions below rely on, ever drift apart.
var (
	_ = decode.Field(Field{})
	_ = decode.Problem(Problem{})
	_ = decode.Tag(Tag{})
)

// Fields lists the fields of the struct rv that a provider reading the struct
// tag tagKey should load, in declaration order. It is the rule every built in
// provider follows, so a provider that uses it accepts and rejects the same
// struct definitions:
//
//   - Fields with neither the tag nor a common name (see below), and unexported
//     fields, are left out.
//   - An embedded struct with no tag is flattened: its fields are loaded as if
//     they were declared in the parent. With a tag it is a group.
//   - A tagged field whose type can't be set from text is a problem, whether or
//     not any value exists for it, so that a mistake shows at startup.
//
// A field can also be named once for every provider, in the first element of its
// config tag, `config:"addr"` or `config:"addr,required"`. A provider that calls
// Fields reads a field by that name when its own tag doesn't give one. Its own
// tag wins: a name in it is used instead, and "-" skips the field for this
// provider only. A tag with only options, `env:",file"`, takes the common name.
// An embedded struct is flattened only if it has neither a tag of its own nor a
// common name. A common name is letters, digits and underscores, starts with a
// letter, and is at most 64 bytes; a mistake in it, or a word that looks like a
// misspelt option ("requried"), is a problem for the field.
//
// parse reads the tag's value. If it returns an error the field is a problem,
// and the Name in the [Tag] it returned, if any, is kept so the problem can be
// traced to the key it was meant to read.
//
// Every problem is returned, not just the first, alongside the fields that are
// fine; report them all, as [Provider] asks.
func Fields(rv reflect.Value, tagKey string, parse func(tag string) (Tag, error)) ([]Field, []Problem) {
	fs, ps := decode.CommonFields(rv, tagKey, func(tag string) (decode.Tag, error) {
		t, err := parse(tag)

		return decode.Tag(t), err
	})

	var (
		fields   []Field
		problems []Problem
	)

	for _, f := range fs {
		fields = append(fields, Field(f))
	}

	for _, p := range ps {
		problems = append(problems, Problem(p))
	}

	return fields, problems
}

// SetString sets rv, a settable field of a type [Fields] returned, from the text
// s: the value of an environment variable, or of a string in a file. It accepts
// strings, bools, integers (base 10), floats, [time.Duration], [net/url.URL],
// anything implementing [encoding.TextUnmarshaler], pointers to those, slices of
// them (comma separated) and maps of them (comma separated key:value pairs).
// Integers that don't fit the field are an error, never wrapped. A pointer is
// given a new value rather than changing the one it points to, and slices and
// maps replace what was there.
func SetString(rv reflect.Value, s string) error {
	return decode.SetString(rv, s)
}

// IsPlainString reports whether rt is a string, or a pointer to one, that has no
// parsing of its own. For these an empty value is a real value, while for every
// other type a provider should treat an empty value as not set, so that a stray
// "PORT=" doesn't wipe out a default or fail to parse.
func IsPlainString(rt reflect.Type) bool {
	return decode.IsPlainString(rt)
}

// Annotate fills in the Provider, Help and Type of keys, which a provider's
// Describe has built from dst, so that every provider says the same about a
// field and a new one needn't know how. provider is the name its problems use.
// Help is the field's help tag, as written, and Type its type as a person would
// write it.
//
// A help tag longer than 1024 bytes is a mistake in the struct: the keys of that
// field are left out and one [*FieldError] for it is returned, which doesn't
// contain the tag, alongside the keys of the fields that are fine.
func Annotate(provider string, dst any, keys []FieldKey) ([]FieldKey, error) {
	rt := reflect.TypeOf(dst)
	if rt == nil || rt.Kind() != reflect.Pointer || rt.Elem().Kind() != reflect.Struct {
		return keys, nil
	}

	var (
		out    = make([]FieldKey, 0, len(keys))
		errs   []error
		failed = map[string]bool{}
	)

	for _, k := range keys {
		if failed[k.Field] {
			continue
		}

		k.Provider = provider

		m, found, err := decode.MetaOf(rt, k.Field)
		if err != nil {
			failed[k.Field] = true

			errs = append(errs, &FieldError{Provider: provider, Field: k.Field, Err: err})

			continue
		}

		if found {
			k.Help, k.Type = m.Help, m.Type
		}

		out = append(out, k)
	}

	return out, errors.Join(errs...)
}
