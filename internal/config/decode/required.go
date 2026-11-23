package decode

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// configTagKey is the struct tag that marks fields as required or expanded,
// whichever provider loads them.
const configTagKey = "config"

// configTag is a field's `config` tag.
type configTag struct {
	// name is the common name: the key every provider that reads names reads the
	// field by, unless its own tag says otherwise. Empty if the tag has none.
	name string

	required bool
	expand   bool
}

// maxNameLength is the longest a common name can be.
const maxNameLength = 64

// parseConfigTag reads a config tag: a comma separated list whose first element is
// the common name, unless it is an option. After the name there are only options
// ("required" and "expand"), and these two words are options wherever they stand,
// so `config:"required"` still means what it did before names existed, and
// neither can be a name.
//
// A mistake is an error that doesn't contain more of the tag than a short, escaped
// excerpt: the tag is part of the program, but may be anything.
func parseConfigTag(tag string) (configTag, error) {
	var t configTag

	for i, element := range strings.Split(tag, ",") {
		switch element = strings.TrimSpace(element); element {
		case "":
		case "required":
			t.required = true
		case "expand":
			t.expand = true
		default:
			if i != 0 {
				return configTag{}, fmt.Errorf("unknown config tag option %s", excerpt(element))
			}

			if err := checkName(element); err != nil {
				return configTag{}, err
			}

			t.name = element
		}
	}

	return t, nil
}

// options are the words that are options in a config tag, with how far a name may
// be from each before it is taken for a misspelling of it: "requried" read as a
// name would quietly stop a field being required.
var options = []struct {
	word     string
	distance int
}{
	{"required", 2},
	{"expand", 1},
}

// checkName reports why name can't be a common name. A name ends up in variable
// names, in keys and in logs, so what it may hold is narrow.
func checkName(name string) error {
	switch {
	case len(name) > maxNameLength:
		return fmt.Errorf("config name %s is too long (the limit is %d bytes)", excerpt(name), maxNameLength)
	case !validName(name):
		return fmt.Errorf("config name %s is not a name: use letters, digits and underscores, starting with a letter", excerpt(name))
	}

	lower := strings.ToLower(name)

	for _, o := range options {
		if distance(lower, o.word) <= o.distance {
			return fmt.Errorf("config name %s looks like the option %s: options are written after the name, and a name can't be spelt like one", excerpt(name), o.word)
		}
	}

	return nil
}

// validName reports whether name is ASCII letters, digits and underscores, and
// starts with a letter.
func validName(name string) bool {
	if name == "" {
		return false
	}

	for i := range len(name) {
		c := name[i]

		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '_'):
		default:
			return false
		}
	}

	return true
}

// distance is the number of edits (inserting, removing or changing a byte, or
// swapping two next to each other) that turn a into b.
func distance(a, b string) int {
	prev2 := make([]int, len(b)+1)
	prev := make([]int, len(b)+1)
	row := make([]int, len(b)+1)

	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		row[0] = i

		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}

			row[j] = min(prev[j]+1, row[j-1]+1, prev[j-1]+cost)

			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				row[j] = min(row[j], prev2[j-2]+1)
			}
		}

		prev2, prev, row = prev, row, prev2
	}

	return prev[len(b)]
}

// excerpt quotes s for an error: escaped to printable ASCII, so that it can't
// forge a log line, and cut short.
func excerpt(s string) string {
	const limit = 40

	quoted := strconv.QuoteToASCII(s)

	if len(quoted) > limit {
		return quoted[:limit] + `..."`
	}

	return quoted
}

// configField is a struct field met while walking a struct for config tags.
type configField struct {
	// path is the Go field path, such as "TLS.CertFile". An embedded struct's
	// fields are named as if declared in the parent, as providers name them.
	path string

	sf    reflect.StructField
	value reflect.Value
	tag   configTag

	// err is a mistake in the tag.
	err error

	// group is true for a struct to walk into, as opposed to a value.
	group bool
}

// configFields lists the fields of the struct rv with their config tags, in
// declaration order, walking into nested and embedded structs. Structs that are
// single values, such as time.Time and url.URL, are not walked into, and nor are
// unexported fields. A struct with a mistake in its tag, or with an option that
// doesn't apply to structs, is listed but not walked into.
func configFields(rv reflect.Value, prefix string, out []configField) []configField {
	rt := rv.Type()

	for i := range rt.NumField() {
		sf, fv := rt.Field(i), rv.Field(i)

		embedded := sf.Anonymous && IsGroup(sf.Type)

		if !sf.IsExported() && !embedded {
			continue
		}

		path := sf.Name
		if prefix != "" {
			path = prefix + "." + sf.Name
		}

		if embedded {
			path = prefix
		}

		tag, err := parseConfigTag(sf.Tag.Get(configTagKey))
		if mistake := TagMistake(sf.Tag); mistake != nil {
			tag, err = configTag{}, mistake
		}

		group := IsGroup(sf.Type)

		out = append(out, configField{path: path, sf: sf, value: fv, tag: tag, err: err, group: group})

		if group && err == nil && !tag.required && !tag.expand {
			out = configFields(fv, path, out)
		}
	}

	return out
}

// Required lists the fields of the struct rv marked `config:"required"` that
// still hold their zero value, in declaration order, as Go field paths such as
// "TLS.CertFile". Run it after every provider has loaded, so a default or a
// value from any source counts.
//
// Zero is "not set": a number that is 0, a false bool, an empty string, a nil
// pointer, an empty slice or map. A pointer to a zero value is set, which is how
// a field can be explicitly zero.
//
// Nested structs are walked into, and an embedded struct's fields are named as
// if declared in the parent, as providers name them. Structs that are single
// values, such as time.Time and url.URL, are not walked into. Unexported fields
// are ignored.
//
// Mistakes in the tag are returned as problems, with the same paths: an option
// other than "required" and "expand", or "required" on a struct, which has no
// single value to be missing. A field with a mistake is not also checked.
func Required(rv reflect.Value) (missing []string, problems []Problem) {
	for _, cf := range configFields(rv, "", nil) {
		switch {
		case cf.err != nil:
			problems = append(problems, Problem{GoName: cf.path, Err: cf.err})
		case cf.group:
			if cf.tag.required {
				problems = append(problems, Problem{GoName: cf.path, Err: fmt.Errorf("required can't be used on a struct; mark its fields instead")})
			}
		case cf.tag.required && isUnset(cf.value):
			missing = append(missing, cf.path)
		}
	}

	return missing, problems
}

// isUnset reports whether rv holds nothing: its zero value, or an empty slice or
// map.
func isUnset(rv reflect.Value) bool {
	switch rv.Kind() {
	case reflect.Slice, reflect.Map:
		return rv.Len() == 0
	default:
		return rv.IsZero()
	}
}

// ExpandField is a string field marked `config:"expand"`.
type ExpandField struct {
	// Path is the Go field path, such as "TLS.CertFile".
	Path string

	// Value is the field, settable: a string, or a pointer to one.
	Value reflect.Value
}

// Expandable lists the fields of the struct rv marked `config:"expand"`, in
// declaration order, walking as [Required] does.
//
// Only strings, and pointers to strings, can be expanded. A field of any other
// type, or a struct, is a problem. Whether a provider reads a field from a file,
// which makes it a secret rather than a template, is for the provider to say (see
// [config.FileBacked]). Mistakes in the tag itself are left to
// [Required], so that each is reported once; a field with one is not listed.
func Expandable(rv reflect.Value) (fields []ExpandField, problems []Problem) {
	for _, cf := range configFields(rv, "", nil) {
		if cf.err != nil || !cf.tag.expand {
			continue
		}

		switch {
		case cf.group:
			problems = append(problems, Problem{GoName: cf.path, Err: fmt.Errorf("expand can't be used on a struct; it is for strings")})
		case !isString(cf.sf.Type):
			problems = append(problems, Problem{GoName: cf.path, Err: fmt.Errorf("expand is for strings, not %s", cf.sf.Type)})
		default:
			fields = append(fields, ExpandField{Path: cf.path, Value: cf.value})
		}
	}

	return fields, problems
}

// isString reports whether rt is a string kind, or a pointer to one.
func isString(rt reflect.Type) bool {
	if rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}

	return rt.Kind() == reflect.String
}
