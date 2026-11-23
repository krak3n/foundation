// Package json provides [config.Provider] implementations that read
// configuration from JSON sources.
//
// The providers parse JSON into a nested map and navigate to the subtree
// matching the keys of the path (see [config.Keys], case-insensitive). Names are
// split into keys on anything that isn't a letter, digit or underscore, so the path
// ["app", "http.server"] means app > http > server, just as the env provider
// reads APP_HTTP_SERVER. Fields are
// mapped using the "json" struct tag:
//
//	type Config struct {
//	    Addr string `json:"addr"`
//	}
//
// With path ["app", "http", "server"] and JSON:
//
//	{
//	    "app": {
//	        "http": {
//	            "server": {
//	                "addr": ":8080"
//	            }
//	        }
//	    }
//	}
//
// Only fields present in the JSON subtree are set; other fields are left
// untouched, preserving defaults or values from earlier providers.
//
// Supported field types are strings, bools, integers, floats, time.Duration
// (a string such as "30s", or a number of nanoseconds), url.URL, anything
// implementing encoding.TextUnmarshaler (such as time.Time and net.IP, from a
// string), pointers to those, slices of them (JSON arrays), and maps of them
// (JSON objects). Numbers are read exactly, and a value that doesn't fit its
// field, such as 300 for an int8 or 1.5 for an int, is an error. Integers given
// as strings are base 10. A struct with a tag is a nested object, and an
// embedded struct without a tag has its fields loaded as if they were declared
// in the parent. A tagged field of any other type is an error.
package json

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/internal/config/decode"
	"go.krak3n.dev/foundation/internal/config/envname"
	"go.krak3n.dev/foundation/internal/config/safefile"
)

// maxDocument is the largest configuration file that is read. One larger is a
// mistake, and is refused rather than read and decoded in full.
const maxDocument = 8 << 20

// readError describes a failure to read the file called source, which is its path
// or, for a path from the environment, the variable ($NAME). The error names it
// itself, so only the cause is kept.
func readError(source string, err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}

	return fmt.Errorf("json: reading %s: %w", source, err)
}

// File returns a [FileProvider] that reads from a JSON file at path, which must
// not be empty: an empty path is an error from Unmarshal and Check, not a file to
// skip (use [FileFromEnv] for a path that may not be given). If the file does not exist, [FileProvider.Unmarshal] silently returns nil.
// Any other error is returned by Unmarshal. The path must be a regular file of
// at most 8 MiB, to which symbolic links are followed; a named pipe or a device,
// which could block or never end, is an error. A file that the group or others can
// write, or a directory that holds it that they can, is refused (see
// [config.WithoutPermissionCheck]).
func File(path string, opts ...config.FileOption) *FileProvider {
	p := newFile(config.ResolveFile(opts...))
	p.path = path

	return p
}

// FileFromEnv returns a [FileProvider] that reads the JSON file whose path is in
// the environment variable name, which the program gives in code. It is how
// whoever runs the program chooses the file; the format is still JSON.
//
// The variable is read once, here. If it is unset or empty there is no file: the
// provider reads nothing and [FileProvider.Path] is empty, unless [config.WithRequired]
// is given, when that is an error from Unmarshal and Check naming the variable.
// Errors about the file name the variable ($NAME), never its value, as a path
// from the environment is not for logs. The name has the rules of an alias in the
// env provider (capitals, digits and underscores, at most 128 bytes, not a
// variable the system reads or one that holds credentials); a name that breaks
// them is an error from Unmarshal and Check. Otherwise the file is read as one
// given to [File] is.
func FileFromEnv(name string, opts ...config.FileOption) *FileProvider {
	p := newFile(config.ResolveFile(opts...))
	p.envName = name

	if err := envname.Check(name); err != nil {
		p.err = fmt.Errorf("json: %w", err)

		return p
	}

	p.path, _ = os.LookupEnv(name)

	return p
}

// Raw returns a [RawProvider] that reads from data.
func Raw(data []byte) *RawProvider {
	return &RawProvider{data: data}
}

// FileProvider reads configuration from a JSON file. The file is opened lazily
// in [FileProvider.Unmarshal].
type FileProvider struct {
	path     string
	required bool
	noModes  bool
	restart  bool

	// envName is the variable the path came from, if it did, and err a mistake
	// found when the provider was made.
	envName string
	err     error

	keys keyset
}

// newFile is a provider with the settings its options asked for.
func newFile(s config.FileSettings) *FileProvider {
	return &FileProvider{required: s.Required, noModes: s.NoPermissionCheck, restart: s.RestartOnChange}
}

// DocumentLayer marks the provider as one for the document layer.
func (p *FileProvider) DocumentLayer() {}

// Path is the file the provider reads.
func (p *FileProvider) Path() string {
	return p.path
}

// RestartAsked says whether the provider was asked to restart the program when
// what it reads changes, whether or not there is anything to read.
func (p *FileProvider) RestartAsked() bool {
	return p.restart
}

// RestartSources says what the provider reads, when it was asked to restart the
// program when that changes ([config.WithRestartOnChange]): the file, or for a path
// from the environment the file labelled with the variable ($NAME). There is
// nothing to say when it was not asked, or when there is no file.
func (p *FileProvider) RestartSources() []config.Source {
	if !p.restart || p.path == "" {
		return nil
	}

	return []config.Source{{Path: p.path, Label: p.label()}}
}

// label is what a log calls the path in place of it, when it came from the
// environment.
func (p *FileProvider) label() string {
	if p.envName == "" {
		return ""
	}

	return "$" + p.envName
}

// source is how the file is named in an error: its path, or the variable it came
// from, which is never shown by value.
func (p *FileProvider) source() string {
	if p.envName != "" {
		return "$" + p.envName
	}

	return p.path
}

// skip reports whether there is no file to read, and an error if the provider
// can't be used: a bad variable name, an empty path, or a required file that was
// not named.
func (p *FileProvider) skip() (bool, error) {
	switch {
	case p.err != nil:
		return false, p.err
	case p.path != "":
		return false, nil
	case p.envName == "":
		return false, errors.New("json: the file path is empty")
	case p.required:
		return false, fmt.Errorf("json: $%s is not set, and the file it names is required", p.envName)
	}

	return true, nil
}

// DocumentLayer marks the provider as one for the document layer.
func (p *RawProvider) DocumentLayer() {}

// Unmarshal opens the file, navigates to the subtree matching path, and
// populates dst with the values found.
func (p *FileProvider) Unmarshal(path []string, dst any) error {
	if skip, err := p.skip(); skip || err != nil {
		return err
	}

	data, err := safefile.ReadConfigFile(p.path, maxDocument, !p.noModes)
	if err != nil {
		if !p.required && errors.Is(err, fs.ErrNotExist) {
			return nil
		}

		return readError(p.source(), err)
	}

	return unmarshalData(data, path, dst, &p.keys)
}

// Describe lists the paths in the document the provider would read for dst at
// path, with the field each is for, in declaration order. It reads nothing, so
// the file need not exist, and doesn't count as asking for the keys. See
// [config.Describer].
func (p *FileProvider) Describe(path []string, dst any) ([]config.FieldKey, error) {
	if p.err != nil {
		return nil, p.err
	}

	return describe(path, dst)
}

// Check reads the file again and lists the keys under root, the path of the root
// function, that nothing has asked for, as a [*config.FieldError] wrapping
// [config.ErrUnknown] for each, named by their path in the document and with the
// file in the message. A missing file has nothing to report. See
// [RawProvider.Check].
func (p *FileProvider) Check(root []string) error {
	if skip, err := p.skip(); skip || err != nil {
		return err
	}

	data, err := safefile.ReadConfigFile(p.path, maxDocument, !p.noModes)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}

		return readError(p.source(), err)
	}

	return check(data, root, &p.keys, p.source())
}

// RawProvider reads configuration from raw JSON bytes.
type RawProvider struct {
	data []byte

	keys keyset
}

// Unmarshal navigates to the subtree matching path and populates dst with the
// values found.
func (p *RawProvider) Unmarshal(path []string, dst any) error {
	return unmarshalData(p.data, path, dst, &p.keys)
}

// Describe lists the paths in the document the provider would read for dst at
// path, with the field each is for, in declaration order. It reads nothing, so
// the data need not be valid, and doesn't count as asking for the keys. See
// [config.Describer].
func (p *RawProvider) Describe(path []string, dst any) ([]config.FieldKey, error) {
	return describe(path, dst)
}

// Check lists the keys under root, the path of the root function, that nothing
// has asked for, as a [*config.FieldError] wrapping [config.ErrUnknown] for each,
// named by their path in the document, sorted and joined with [errors.Join]. It
// is a typo detector: "adrr" is not read by anything that reads "addr". An
// object nothing asked for is reported once, not once for every key inside it.
//
// Call it once every function has loaded its configuration, as
// [go.krak3n.dev/foundation.WithStrictConfig] does: keys that belong to a
// function that hasn't loaded yet look unknown.
func (p *RawProvider) Check(root []string) error {
	return check(p.data, root, &p.keys, "")
}

// check lists what is under root in data that keys doesn't know.
func check(data []byte, root []string, keys *keyset, source string) error {
	var doc map[string]any

	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("json: %w", err)
	}

	return unknownErrors(keys.unknown(doc, config.Keys(root)), source)
}

func unmarshalData(data []byte, path []string, dst any, ks *keyset) error {
	var root map[string]any

	// Numbers are kept as text, so that integers beyond 2^53 are exact and a
	// fraction can be told apart from a whole number.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	if err := dec.Decode(&root); err != nil {
		return fmt.Errorf("json: %w", err)
	}

	// A second document after the first would be ignored, and could say anything.
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("json: unexpected data after the document")
	}

	elem, err := structOf(dst)
	if err != nil {
		return err
	}

	dups := scanDuplicates(data)

	// A missing level is not an error, but the struct is still walked, with
	// nothing to read, so that a field that can never be set is reported.
	keys := config.Keys(path)
	node := root

	ks.add(keys)

	for i, key := range keys {
		if spellings := dups.ambiguous(keys[:i], key); spellings != nil {
			where := "at the top of the document"
			if i > 0 {
				where = "under " + strings.Join(keys[:i], ".")
			}

			return fmt.Errorf("json: %s, %w", where, ambiguousError(spellings))
		}

		child, _ := getInsensitive(node, key)
		node, _ = child.(map[string]any)
	}

	return errors.Join(unmarshalMap(node, ks, dups, nil, keys, "", elem)...)
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
	errs := unmarshalMap(nil, &ignore, nil, func(field, key string) {
		keys = append(keys, config.FieldKey{Field: field, Key: key})
	}, config.Keys(path), "", elem)

	keys, err = config.Annotate("json", dst, keys)

	return keys, errors.Join(append(errs, err)...)
}

// getInsensitive performs a case-insensitive lookup in a map.
func getInsensitive(m map[string]any, key string) (any, bool) {
	lower := strings.ToLower(key)

	for k, v := range m {
		if strings.ToLower(k) == lower {
			return v, true
		}
	}

	return nil, false
}

// unmarshalMap loads the struct rv from the object m, which sits at the document
// path keys, and whose Go field names are reached through goPrefix. It returns
// every problem it finds: mistakes in the struct first, then problems with
// values. Fields that are fine are still set. leaf, if not nil, is told each
// field that holds a value and its path in the document.
func unmarshalMap(m map[string]any, ks *keyset, dups *duplicates, leaf func(field, key string), keys []string, goPrefix string, rv reflect.Value) []error {
	fields, problems := decode.CommonFields(rv, "json", parseTag)

	var errs []error

	for _, p := range problems {
		key := ""
		if p.Name != "" {
			key = docPath(keys, p.Name)

			// A field with a mistake in it still owns its key.
			ks.add(append(slices.Clone(keys), p.Name))
		}

		errs = append(errs, &config.FieldError{Provider: "json", Field: goPath(goPrefix, p.GoName), Key: key, Err: p.Err})
	}

	// Keys are matched without regard to case, so two fields whose names differ
	// only in case read the same one, and only one of them can be right.
	claimed := map[string]string{}

	for _, f := range fields {
		field := goPath(goPrefix, f.GoName)

		if other, ok := claimed[strings.ToLower(f.Name)]; ok {
			errs = append(errs, &config.FieldError{
				Provider: "json",
				Field:    field,
				Key:      docPath(keys, f.Name),
				Err:      fmt.Errorf("key %s is also read by field %s; rename one of them", docPath(keys, f.Name), other),
			})

			continue
		}

		claimed[strings.ToLower(f.Name)] = field

		ks.add(append(slices.Clone(keys), f.Name))

		// A key that is there more than once has no value to use. Nothing is
		// loaded from it, and the other fields are unaffected.
		if spellings := dups.ambiguous(keys, f.Name); spellings != nil {
			if !f.Group && leaf != nil {
				leaf(field, docPath(keys, f.Name))
			}

			errs = append(errs, &config.FieldError{Provider: "json", Field: field, Key: docPath(keys, f.Name), Err: ambiguousError(spellings)})

			continue
		}

		val, ok := getInsensitive(m, f.Name)

		if f.Group {
			child, _ := val.(map[string]any)

			errs = append(errs, unmarshalMap(child, ks, dups, leaf, append(slices.Clone(keys), f.Name), field, f.Value)...)

			continue
		}

		if leaf != nil {
			leaf(field, docPath(keys, f.Name))
		}

		// Keys of a map are case sensitive, so only the same key twice is ambiguous.
		if f.Value.Kind() == reflect.Map {
			if repeated := dups.repeated(append(slices.Clone(keys), f.Name)); len(repeated) > 0 {
				errs = append(errs, &config.FieldError{Provider: "json", Field: field, Key: docPath(keys, f.Name), Err: ambiguousError(repeated)})

				continue
			}
		}

		// An absent key, and null, leave the field as it is.
		if !ok || val == nil {
			continue
		}

		if err := setValue(f.Value, val); err != nil {
			errs = append(errs, &config.FieldError{Provider: "json", Field: field, Key: docPath(keys, f.Name), Err: err})
		}
	}

	return errs
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

// parseTag reads a json tag: the name before any options, which are the standard
// ones such as omitempty and are ignored. "-" skips the field. A tag with no name
// takes the field's common name (see [config.Fields]), and without one is a
// problem, as a field that is silently not read is a mistake.
func parseTag(tag string) (decode.Tag, error) {
	name, _, _ := strings.Cut(tag, ",")

	if name == "-" {
		return decode.Tag{Skip: true}, nil
	}

	return decode.Tag{Name: name}, nil
}

// setValue sets rv, a field of a type [decode.Check] accepts, from a value
// decoded from JSON: a string, json.Number, bool, []any or map[string]any.
func setValue(rv reflect.Value, val any) error {
	rt := rv.Type()

	if category := decode.Classify(rt); category != decode.None {
		return setLeaf(rv, category, val)
	}

	switch rt.Kind() {
	case reflect.Pointer:
		// A new value, as the pointer may be shared.
		value := reflect.New(rt.Elem())

		if err := setValue(value.Elem(), val); err != nil {
			return err
		}

		rv.Set(value)
	case reflect.Slice:
		items, ok := val.([]any)
		if !ok {
			return fmt.Errorf("expected an array, got %s", kind(val))
		}

		out := reflect.MakeSlice(rt, len(items), len(items))

		for i, item := range items {
			if err := setValue(out.Index(i), item); err != nil {
				return fmt.Errorf("item %d: %w", i, err)
			}
		}

		rv.Set(out)
	case reflect.Map:
		object, ok := val.(map[string]any)
		if !ok {
			return fmt.Errorf("expected an object, got %s", kind(val))
		}

		out := reflect.MakeMapWithSize(rt, len(object))

		// In key order, so that which entry an error is about doesn't depend on
		// the order Go ranges over the map.
		for _, k := range slices.Sorted(maps.Keys(object)) {
			item := object[k]

			key, value := reflect.New(rt.Key()).Elem(), reflect.New(rt.Elem()).Elem()

			// The key is text from the document, so if it isn't a valid key for the
			// map it isn't repeated.
			if err := decode.SetLeaf(key, k); err != nil {
				return fmt.Errorf("a key: %w", err)
			}

			if err := setValue(value, item); err != nil {
				return fmt.Errorf("key %q: %w", k, err)
			}

			out.SetMapIndex(key, value)
		}

		rv.Set(out)
	}

	return nil
}

// setLeaf sets a single value. Strings may hold anything a leaf can be parsed
// from (a duration, a base 10 number, a timestamp), and numbers are parsed from
// their exact text. A number is a duration in nanoseconds.
func setLeaf(rv reflect.Value, category decode.Category, val any) error {
	switch v := val.(type) {
	case string:
		if category == decode.Bool {
			return fmt.Errorf("expected a bool, got a string")
		}

		return decode.SetLeaf(rv, v)
	case json.Number:
		switch category {
		case decode.Int, decode.Uint, decode.Float:
			return decode.SetLeaf(rv, v.String())
		case decode.Duration:
			n, err := strconv.ParseInt(v.String(), 10, 64)
			if err != nil {
				return errors.New("not a valid duration: a number must be a whole number of nanoseconds")
			}

			rv.SetInt(n)

			return nil
		}

		return fmt.Errorf("expected a string, got a number")
	case bool:
		if category == decode.Bool {
			rv.SetBool(v)

			return nil
		}

		return fmt.Errorf("expected a string or number, got a bool")
	}

	return fmt.Errorf("expected a single value, got %s", kind(val))
}

// kind names a decoded JSON value for an error message.
func kind(val any) string {
	switch val.(type) {
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	case string:
		return "a string"
	case json.Number:
		return "a number"
	case bool:
		return "a bool"
	}

	return fmt.Sprintf("%T", val)
}

// InvalidUnmarshalError describes an invalid argument passed to Unmarshal.
type InvalidUnmarshalError struct {
	Type reflect.Type
}

func (e *InvalidUnmarshalError) Error() string {
	if e.Type == nil {
		return "json: Unmarshal(nil)"
	}

	if e.Type.Kind() != reflect.Pointer {
		return "json: Unmarshal(non-pointer " + e.Type.String() + ")"
	}

	return "json: Unmarshal(" + e.Type.String() + ")"
}
