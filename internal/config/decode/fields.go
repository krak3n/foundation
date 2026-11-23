package decode

import (
	"fmt"
	"reflect"
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

	// Options are the provider's own options from the tag, as it returned them,
	// such as "file". Fields doesn't interpret them.
	Options []string

	// Common is true if Name is the common name from the config tag, because the
	// provider's own tag gave none.
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
	// tag, handed back with the field for the provider to act on.
	Options []string
}

// Fields lists the fields of the struct rv that a provider reading the struct
// tag tagKey should load, in declaration order. It decides, the same way for
// every provider:
//
//   - Fields without the tag, and unexported fields, are left out.
//   - An embedded struct with no tag is flattened: its fields are loaded as if
//     they were declared in the parent. With a tag it is a group.
//   - A tagged field whose type can't be set is a problem, whether or not any
//     value exists for it, so that a mistake shows at startup.
//
// parse reads the tag's value. If it returns an error the field is a problem,
// and the Name in the Tag it returned, if any, is kept so the problem can be
// traced to the key it was meant to read. A tag that ends up with no name is a
// problem too.
//
// Every problem is returned, not just the first, alongside the fields that are
// fine.
func Fields(rv reflect.Value, tagKey string, parse func(tag string) (Tag, error)) ([]Field, []Problem) {
	return collect(rv, tagKey, parse, false)
}

// CommonFields is [Fields] for a provider that also reads the common name, the
// name in the config tag, of a field its own tag doesn't name:
//
//   - A field with no tag of its own but a common name is loaded by that name.
//   - A tag of its own with a name wins, and one that skips the field skips it for
//     this provider only.
//   - A tag of its own with only options takes the common name, and with no common
//     name either is a problem.
//   - A mistake in the config tag is a problem for the field whatever the
//     provider's own tag says, so that it can't wait for the day that tag is
//     removed.
//   - An embedded struct is flattened only if it has neither a tag nor a common
//     name.
func CommonFields(rv reflect.Value, tagKey string, parse func(tag string) (Tag, error)) ([]Field, []Problem) {
	return collect(rv, tagKey, parse, true)
}

func collect(rv reflect.Value, tagKey string, parse func(tag string) (Tag, error), common bool) ([]Field, []Problem) {
	rt := rv.Type()

	var (
		fields   []Field
		problems []Problem
	)

	for i := range rt.NumField() {
		sf, fv := rt.Field(i), rv.Field(i)

		// A tag that reflect would read wrongly is a problem for the field, for
		// every provider, before anything is taken from it.
		if mistake := TagMistake(sf.Tag); mistake != nil && (sf.IsExported() || sf.Anonymous) {
			problems = append(problems, Problem{GoName: sf.Name, Err: mistake})

			continue
		}

		tag, tagged := sf.Tag.Lookup(tagKey)

		var (
			name string
			err  error
		)

		if common {
			var ct configTag

			ct, err = parseConfigTag(sf.Tag.Get(configTagKey))
			name = ct.name
		}

		if sf.Anonymous && !tagged && name == "" && err == nil && sf.Type.Kind() == reflect.Struct {
			if IsGroup(sf.Type) {
				embedded, embeddedProblems := collect(fv, tagKey, parse, common)

				fields = append(fields, embedded...)
				problems = append(problems, embeddedProblems...)
			}

			continue
		}

		if !sf.IsExported() && !sf.Anonymous {
			continue
		}

		if !tagged && name == "" && err == nil {
			continue
		}

		var t Tag

		if tagged {
			var tagErr error

			// A problem in the tag is still told which name it was for.
			t, tagErr = parse(tag)

			if err == nil && tagErr != nil {
				err = tagErr
			}
		}

		if err != nil {
			if t.Name == "" {
				t.Name = name
			}

			problems = append(problems, Problem{GoName: sf.Name, Name: t.Name, Err: err})

			continue
		}

		if t.Skip {
			continue
		}

		fromCommon := false

		if t.Name == "" {
			if name == "" {
				problems = append(problems, Problem{GoName: sf.Name, Err: fmt.Errorf("%s tag has no name", tagKey)})

				continue
			}

			t.Name, fromCommon = name, true
		}

		if IsGroup(sf.Type) {
			fields = append(fields, Field{GoName: sf.Name, Name: t.Name, Value: fv, Group: true, Options: t.Options, Common: fromCommon})

			continue
		}

		if err := Check(sf.Type); err != nil {
			problems = append(problems, Problem{GoName: sf.Name, Name: t.Name, Err: err})

			continue
		}

		fields = append(fields, Field{GoName: sf.Name, Name: t.Name, Value: fv, Options: t.Options, Common: fromCommon})
	}

	return fields, problems
}
