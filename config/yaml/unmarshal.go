package yaml

import (
	"encoding"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	yamllib "go.yaml.in/yaml/v3"

	"go.krak3n.dev/foundation/config"
)

// InvalidUnmarshalError describes an invalid argument passed to Unmarshal.
type InvalidUnmarshalError struct {
	Type reflect.Type
}

func (e *InvalidUnmarshalError) Error() string {
	if e.Type == nil {
		return "yaml: Unmarshal(nil)"
	}

	if e.Type.Kind() != reflect.Pointer {
		return "yaml: Unmarshal(non-pointer " + e.Type.String() + ")"
	}

	return "yaml: Unmarshal(" + e.Type.String() + ")"
}

// A pair is one key and value of a mapping, as the document spells them.
type entry struct {
	key   string
	value *yamllib.Node
}

// entries lists the pairs of the mapping m, in document order. A nil m has none.
func entries(m *yamllib.Node) []entry {
	if m == nil {
		return nil
	}

	out := make([]entry, 0, len(m.Content)/2)

	for i := 0; i+1 < len(m.Content); i += 2 {
		out = append(out, entry{key: m.Content[i].Value, value: m.Content[i+1]})
	}

	return out
}

// find returns the pairs whose key is name, without regard to case.
func find(es []entry, name string) []entry {
	lower := strings.ToLower(name)

	var out []entry

	for _, e := range es {
		if strings.ToLower(e.key) == lower {
			out = append(out, e)
		}
	}

	return out
}

// spellings lists the distinct spellings of the keys of es, in document order.
func spellings(es []entry) []string {
	var out []string

	for _, e := range es {
		if !slices.Contains(out, e.key) {
			out = append(out, e.key)
		}
	}

	return out
}

// asMapping returns n if it is a mapping, and nil otherwise.
func asMapping(n *yamllib.Node) *yamllib.Node {
	if n != nil && n.Kind == yamllib.MappingNode {
		return n
	}

	return nil
}

// kind names a node for an error message.
func kind(n *yamllib.Node) string {
	switch n.Kind {
	case yamllib.MappingNode:
		return "a mapping"
	case yamllib.SequenceNode:
		return "a sequence"
	}

	return "a single value"
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

func unmarshalDoc(doc *yamllib.Node, path []string, dst any, ks *keyset) error {
	elem, err := structOf(dst)
	if err != nil {
		return err
	}

	// A missing level is not an error, but the struct is still walked, with
	// nothing to read, so that a field that can never be set is reported.
	keys := config.Keys(path)
	node := doc

	ks.add(keys)

	for i, key := range keys {
		found := find(entries(node), key)

		if len(found) > 1 {
			where := "at the top of the document"
			if i > 0 {
				where = "under " + strings.Join(keys[:i], ".")
			}

			return fmt.Errorf("yaml: %s, %w", where, ambiguousError(spellings(found)))
		}

		node = nil

		if len(found) == 1 && !isNull(found[0].value) {
			if node = asMapping(found[0].value); node == nil {
				return lineError(found[0].value.Line, strings.Join(keys[:i+1], ".")+" must be a mapping, not "+kind(found[0].value))
			}
		}
	}

	return errors.Join(unmarshalMap(node, ks, nil, keys, "", elem)...)
}

// describe lists where dst would be read from, without a document.
func describe(path []string, dst any) ([]config.FieldKey, error) {
	elem, err := structOf(dst)
	if err != nil {
		return nil, err
	}

	var (
		keys   []config.FieldKey
		ignore keyset
	)

	// A throwaway keyset, so that describing doesn't count as asking.
	errs := unmarshalMap(nil, &ignore, func(field, key string) {
		keys = append(keys, config.FieldKey{Field: field, Key: key})
	}, config.Keys(path), "", elem)

	keys, err = config.Annotate("yaml", dst, keys)

	return keys, errors.Join(append(errs, err)...)
}

// unmarshalMap loads the struct rv from the mapping m, which sits at the document
// path keys, and whose Go field names are reached through goPrefix. It returns
// every problem it finds: mistakes in the struct first, then problems with
// values. Fields that are fine are still set. leaf, if not nil, is told each
// field that holds a value and its path in the document.
func unmarshalMap(m *yamllib.Node, ks *keyset, leaf func(field, key string), keys []string, goPrefix string, rv reflect.Value) []error {
	fields, problems := config.Fields(rv, "yaml", parseTag)

	var errs []error

	for _, p := range problems {
		key := ""
		if p.Name != "" {
			key = docPath(keys, p.Name)

			// A field with a mistake in it still owns its key.
			ks.add(append(slices.Clone(keys), p.Name))
		}

		errs = append(errs, &config.FieldError{Provider: "yaml", Field: goPath(goPrefix, p.GoName), Key: key, Err: p.Err})
	}

	es := entries(m)

	// Keys are matched without regard to case, so two fields whose names differ
	// only in case read the same one, and only one of them can be right.
	claimed := map[string]string{}

	for _, f := range fields {
		field := goPath(goPrefix, f.GoName)

		if other, ok := claimed[strings.ToLower(f.Name)]; ok {
			errs = append(errs, &config.FieldError{
				Provider: "yaml",
				Field:    field,
				Key:      docPath(keys, f.Name),
				Err:      fmt.Errorf("key %s is also read by field %s; rename one of them", docPath(keys, f.Name), other),
			})

			continue
		}

		claimed[strings.ToLower(f.Name)] = field

		ks.add(append(slices.Clone(keys), f.Name))

		found := find(es, f.Name)

		// A key that is there more than once has no value to use. Nothing is
		// loaded from it, and the other fields are unaffected.
		if len(found) > 1 {
			if !f.Group && leaf != nil {
				leaf(field, docPath(keys, f.Name))
			}

			errs = append(errs, &config.FieldError{Provider: "yaml", Field: field, Key: docPath(keys, f.Name), Err: ambiguousError(spellings(found))})

			continue
		}

		var val *yamllib.Node
		if len(found) == 1 {
			val = found[0].value
		}

		if f.Group {
			var child *yamllib.Node

			if val != nil && !isNull(val) {
				if child = asMapping(val); child == nil {
					errs = append(errs, &config.FieldError{Provider: "yaml", Field: field, Key: docPath(keys, f.Name), Err: fmt.Errorf("expected a mapping, got %s", kind(val))})

					continue
				}
			}

			errs = append(errs, unmarshalMap(child, ks, leaf, append(slices.Clone(keys), f.Name), field, f.Value)...)

			continue
		}

		if leaf != nil {
			leaf(field, docPath(keys, f.Name))
		}

		// An absent key, and null, leave the field as it is.
		if val == nil || isNull(val) {
			continue
		}

		// Keys of a map are case sensitive, so only the same key twice is ambiguous.
		if f.Value.Kind() == reflect.Map {
			if repeated := repeatedKeys(val); len(repeated) > 0 {
				errs = append(errs, &config.FieldError{Provider: "yaml", Field: field, Key: docPath(keys, f.Name), Err: ambiguousError(repeated)})

				continue
			}
		}

		if err := setValue(f.Value, val); err != nil {
			errs = append(errs, &config.FieldError{Provider: "yaml", Field: field, Key: docPath(keys, f.Name), Err: err})
		}
	}

	return errs
}

// repeatedKeys lists the keys of the mapping n that are there more than once,
// exactly, in order.
func repeatedKeys(n *yamllib.Node) []string {
	if n.Kind != yamllib.MappingNode {
		return nil
	}

	seen := map[string]int{}

	var out []string

	for _, e := range entries(n) {
		if seen[e.key]++; seen[e.key] == 2 {
			out = append(out, e.key)
		}
	}

	return out
}

// ambiguousError is the error for a key that appears more than once, naming the
// spellings used and not the values.
func ambiguousError(spellings []string) error {
	quoted := make([]string, len(spellings))

	for i, s := range spellings {
		quoted[i] = strconv.Quote(s)
	}

	return fmt.Errorf("%s: %w", strings.Join(quoted, ", "), config.ErrAmbiguous)
}

// docPath is the path to a key in the document, such as svc.tls.cert_file.
func docPath(keys []string, name string) string {
	return strings.Join(append(slices.Clone(keys), name), ".")
}

func goPath(prefix, name string) string {
	if prefix == "" {
		return name
	}

	return prefix + "." + name
}

// parseTag reads a yaml tag: the name before any options, which are ignored.
// "-" skips the field. A tag with no name takes the field's common name (see
// [config.Fields]), and without one is a problem, as a field that is silently not
// read is a mistake.
func parseTag(tag string) (config.Tag, error) {
	name, _, _ := strings.Cut(tag, ",")

	if name == "-" {
		return config.Tag{Skip: true}, nil
	}

	return config.Tag{Name: name}, nil
}

var (
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
	urlType         = reflect.TypeFor[url.URL]()
)

// isLeaf reports whether a value of type rt is set from one scalar.
func isLeaf(rt reflect.Type) bool {
	if rt == urlType || rt.Implements(textUnmarshaler) || reflect.PointerTo(rt).Implements(textUnmarshaler) {
		return true
	}

	switch rt.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	}

	return false
}

// setValue sets rv, a field of a type [config.Fields] accepted, from a node.
func setValue(rv reflect.Value, n *yamllib.Node) error {
	rt := rv.Type()

	if isLeaf(rt) {
		return setScalar(rv, n)
	}

	switch rt.Kind() {
	case reflect.Pointer:
		// A new value, as the pointer may be shared.
		value := reflect.New(rt.Elem())

		if err := setValue(value.Elem(), n); err != nil {
			return err
		}

		rv.Set(value)
	case reflect.Slice:
		if n.Kind != yamllib.SequenceNode {
			return fmt.Errorf("expected a sequence, got %s", kind(n))
		}

		out := reflect.MakeSlice(rt, len(n.Content), len(n.Content))

		for i, item := range n.Content {
			if err := setValue(out.Index(i), item); err != nil {
				return fmt.Errorf("item %d: %w", i, err)
			}
		}

		rv.Set(out)
	case reflect.Map:
		if n.Kind != yamllib.MappingNode {
			return fmt.Errorf("expected a mapping, got %s", kind(n))
		}

		es := entries(n)

		// In key order, so that which entry an error is about doesn't depend on
		// anything but the document.
		slices.SortStableFunc(es, func(a, b entry) int { return strings.Compare(a.key, b.key) })

		out := reflect.MakeMapWithSize(rt, len(es))

		for _, e := range es {
			key, value := reflect.New(rt.Key()).Elem(), reflect.New(rt.Elem()).Elem()

			// The key is text from the document, so if it isn't a valid key for the
			// map it isn't repeated.
			if err := config.SetString(key, e.key); err != nil {
				return fmt.Errorf("a key: %w", err)
			}

			if err := setValue(value, e.value); err != nil {
				return fmt.Errorf("key %q: %w", e.key, err)
			}

			out.SetMapIndex(key, value)
		}

		rv.Set(out)
	}

	return nil
}

// setScalar sets rv from the text of a scalar as written, not as YAML would
// read it, and so as the other providers read the same text. A bool is exactly
// true or false.
func setScalar(rv reflect.Value, n *yamllib.Node) error {
	if n.Kind != yamllib.ScalarNode {
		return fmt.Errorf("expected a single value, got %s", kind(n))
	}

	if isNull(n) {
		return errors.New("expected a value, got null")
	}

	if rv.Kind() == reflect.Bool {
		switch n.Value {
		case "true":
			rv.SetBool(true)
		case "false":
			rv.SetBool(false)
		default:
			return fmt.Errorf("not a valid bool: %w", strconv.ErrSyntax)
		}

		return nil
	}

	return config.SetString(rv, n.Value)
}
