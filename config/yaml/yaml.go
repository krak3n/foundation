// Package yaml provides [config.Provider] implementations that read
// configuration from YAML sources.
//
// It reads names the way the JSON provider does: the keys of the path (see
// [config.Keys]) are nested mappings, matched without regard to case, so the path
// ["app", "http.server"] is app > http > server, just as the environment provider
// reads APP_HTTP_SERVER. A field is read by the name in its config tag, or by
// the name in a yaml tag of its own, which wins for this provider alone
// (yaml:"-" skips the field):
//
//	type Config struct {
//	    Addr string `config:"addr"`
//	}
//
// With path ["app", "http", "server"] and the document:
//
//	app:
//	  http:
//	    server:
//	      addr: ":8080"
//
// Only fields present in the document are set; the others keep their defaults or
// the values of earlier providers. An unquoted null, ~ or an empty value counts
// as not present.
//
// # What is accepted
//
// A document is a single mapping, in block or flow style, with the subset of YAML
// that says what it means. These are refused, wherever they are in the file even
// in parts nothing reads, because each lets a file say something other than what
// a reviewer sees, or costs more than it should:
//
//   - anchors and aliases (&a, *a), which also allow a few lines to expand to
//     gigabytes,
//   - merge keys (<<) and explicit tags (!!binary, !custom),
//   - complex keys (a key that is not a scalar), and
//   - more than one document in the file.
//
// A key that is read and appears more than once, exactly or differing only in
// case, is a [config.ErrAmbiguous] problem for the field, since which one a
// parser keeps is an accident. Duplicates in keys nobody reads are fine. The
// limits are 8 MiB for a file, 64 levels of nesting and 100,000 nodes.
//
// # Values
//
// A scalar is read as the text it was written as, and never as what YAML 1.1
// would have made of it, so no, on, 0755 and 1_000 are strings where a string is
// wanted, and are exactly what they say elsewhere. Numbers and durations are
// parsed as the other providers parse them (integers in base 10, a duration
// such as 30s), a bool is exactly true or false, a sequence is a slice and a
// mapping is a map. Supported types are those of [config.SetString].
//
// # Errors
//
// An error says the line and what kind of mistake it is, and never repeats what
// the file says: a document is configuration, which may hold a password in the
// wrong place. Every problem with a value is a [*config.FieldError] naming the
// field and the key.
package yaml

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	yamllib "go.yaml.in/yaml/v3"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/internal/config/envname"
	"go.krak3n.dev/foundation/internal/config/safefile"
)

// File returns a [FileProvider] that reads from a YAML file at path, which must
// not be empty: an empty path is an error from Unmarshal and Check, not a file to
// skip (use [FileFromEnv] for a path that may not be given). If the file
// does not exist, [FileProvider.Unmarshal] silently returns nil; any other error
// is returned. The path must be a regular file of at most 8 MiB, to which
// symbolic links are followed; a named pipe or a device, which could block or
// never end, is an error. A file that the group or others can write, or a
// directory that holds it that they can, is refused (see
// [config.WithoutPermissionCheck]).
func File(path string, opts ...config.FileOption) *FileProvider {
	p := newFile(config.ResolveFile(opts...))
	p.path = path

	return p
}

// FileFromEnv returns a [FileProvider] that reads the YAML file whose path is in
// the environment variable name, which the program gives in code. It is how
// whoever runs the program chooses the file; the format is still YAML.
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
		p.err = fmt.Errorf("yaml: %w", err)

		return p
	}

	p.path, _ = os.LookupEnv(name)

	return p
}

// Raw returns a [RawProvider] that reads from data.
func Raw(data []byte) *RawProvider {
	return &RawProvider{data: data}
}

// FileProvider reads configuration from a YAML file, which is read each time it
// is asked for.
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
		return false, errors.New("yaml: the file path is empty")
	case p.required:
		return false, fmt.Errorf("yaml: $%s is not set, and the file it names is required", p.envName)
	}

	return true, nil
}

// DocumentLayer marks the provider as one for the document layer.
func (p *RawProvider) DocumentLayer() {}

// read parses the file, or returns a nil document if it is missing and not
// required.
func (p *FileProvider) read() (*yamllib.Node, error) {
	if skip, err := p.skip(); skip || err != nil {
		return nil, err
	}

	data, err := safefile.ReadConfigFile(p.path, maxDocument, !p.noModes)
	if err != nil {
		if !p.required && errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}

		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			err = pathErr.Err
		}

		return nil, fmt.Errorf("yaml: reading %s: %w", p.source(), err)
	}

	doc, err := parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w (in %s)", err, p.source())
	}

	return doc, nil
}

// Unmarshal reads the file, navigates to the mapping matching path and populates
// dst with the values found.
func (p *FileProvider) Unmarshal(path []string, dst any) error {
	doc, err := p.read()
	if err != nil {
		return err
	}

	return unmarshalDoc(doc, path, dst, &p.keys)
}

// Describe lists the keys in the document the provider would read for dst at
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
	doc, err := p.read()
	if err != nil {
		return err
	}

	return unknownErrors(p.keys.unknown(doc, config.Keys(root)), p.source())
}

// RawProvider reads configuration from YAML bytes.
type RawProvider struct {
	data []byte

	keys keyset
}

// Unmarshal navigates to the mapping matching path and populates dst with the
// values found.
func (p *RawProvider) Unmarshal(path []string, dst any) error {
	doc, err := parse(p.data)
	if err != nil {
		return err
	}

	return unmarshalDoc(doc, path, dst, &p.keys)
}

// Describe lists the keys in the document the provider would read for dst at
// path, with the field each is for, in declaration order. It reads nothing, so
// the data need not be valid, and doesn't count as asking for the keys. See
// [config.Describer].
func (p *RawProvider) Describe(path []string, dst any) ([]config.FieldKey, error) {
	return describe(path, dst)
}

// Check lists the keys under root, the path of the root function, that nothing
// has asked for, as a [*config.FieldError] wrapping [config.ErrUnknown] for each,
// named by their path in the document, sorted at each level. It is a typo
// detector: "adrr" is not read by anything that reads "addr". A mapping nothing
// asked for is reported once, not once for every key inside it.
//
// Call it once every function has loaded its configuration, as
// [go.krak3n.dev/foundation.WithStrictConfig] does: keys that belong to a
// function that hasn't loaded yet look unknown.
func (p *RawProvider) Check(root []string) error {
	doc, err := parse(p.data)
	if err != nil {
		return err
	}

	return unknownErrors(p.keys.unknown(doc, config.Keys(root)), "")
}
