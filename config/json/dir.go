package json

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/internal/config/confdir"
	"go.krak3n.dev/foundation/internal/config/envname"
	"go.krak3n.dev/foundation/internal/config/safefile"
)

// Dir returns a [DirProvider] that reads every JSON file (a name ending in .json)
// in the directory at path, the way a conf.d directory is read: in the order of
// their names, byte by byte, so that 00-base.json comes before 10-site.json, with
// each file overriding the ones before it. A later file can set a field to false
// or zero. The path must not be empty: an empty path is an error from Unmarshal
// and Check (use [DirFromEnv] for one that may not be given). A directory that
// doesn't exist is skipped unless [config.WithRequired] is given; an empty one is fine.
//
// Only files named like documents are read. Hidden files, backups (name~, #name#),
// other extensions and subdirectories are ignored, and are not even opened.
// Anything that matches but can't be used is an error, not a file to skip: a
// symbolic link that points nowhere, a named pipe, a file that can't be read, more
// than 256 files or 32 MiB in all, and each file over the limit of [File]. A
// provider that is refused reads nothing, so the service never runs on some of
// the files only. Like [File] it refuses a directory or a file that the group or
// others can write, unless [config.WithoutPermissionCheck] is given.
func Dir(path string, opts ...config.FileOption) *DirProvider {
	return newDir(path, "", opts)
}

// DirFromEnv returns a [DirProvider] that reads the directory whose path is in the
// environment variable name, which the program gives in code. It follows the
// rules of [FileFromEnv]: the variable is read once, here; unset or empty means no
// directory, or an error naming the variable with [config.WithRequired]; errors say
// $NAME and the name of the file in it, never the path the variable holds; and the
// name is checked as an alias of the env provider is.
func DirFromEnv(name string, opts ...config.FileOption) *DirProvider {
	p := newDir("", name, opts)

	if err := envname.Check(name); err != nil {
		p.err = fmt.Errorf("json: %w", err)

		return p
	}

	p.path, _ = os.LookupEnv(name)

	return p
}

func newDir(path, envName string, opts []config.FileOption) *DirProvider {
	o := config.ResolveFile(opts...)

	return &DirProvider{path: path, envName: envName, required: o.Required, noModes: o.NoPermissionCheck, restart: o.RestartOnChange}
}

// DirProvider reads configuration from the JSON files in a directory, which is
// read each time it is asked for.
type DirProvider struct {
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

// DocumentLayer marks the provider as one for the document layer.
func (p *DirProvider) DocumentLayer() {}

// Path is the directory the provider reads, which is empty when the variable that
// names it is not set.
func (p *DirProvider) Path() string {
	return p.path
}

// dirExtensions are the endings of the files the provider reads.
func dirExtensions() []string {
	return []string{".json"}
}

// RestartAsked says whether the provider was asked to restart the program when
// what it reads changes, whether or not there is anything to read.
func (p *DirProvider) RestartAsked() bool {
	return p.restart
}

// RestartSources says what the provider reads, when it was asked to restart the
// program when that changes ([config.WithRestartOnChange]): the directory, with the
// endings of the files in it that are documents, labelled with the variable ($NAME)
// for a path from the environment. There is nothing to say when it was not asked,
// or when there is no directory.
func (p *DirProvider) RestartSources() []config.Source {
	if !p.restart || p.path == "" {
		return nil
	}

	label := ""
	if p.envName != "" {
		label = "$" + p.envName
	}

	return []config.Source{{Path: p.path, Label: label, Dir: true, Extensions: dirExtensions()}}
}

// source is how the directory, or a file in it, is named in an error: by its path,
// or by the variable it came from, which is never shown by value.
func (p *DirProvider) source(name string) string {
	base := p.path
	if p.envName != "" {
		base = "$" + p.envName
	}

	if name == "" {
		return base
	}

	return base + "/" + name
}

// list returns the files to read, in order, or nil if there is no directory.
func (p *DirProvider) list() ([]string, error) {
	switch {
	case p.err != nil:
		return nil, p.err
	case p.path == "" && p.envName == "":
		return nil, errors.New("json: the directory path is empty")
	case p.path == "" && p.required:
		return nil, fmt.Errorf("json: $%s is not set, and the directory it names is required", p.envName)
	case p.path == "":
		return nil, nil
	}

	names, err := confdir.List(p.path, !p.noModes, dirExtensions()...)
	if err != nil {
		if errors.Is(err, confdir.ErrNotFound) && !p.required {
			return nil, nil
		}

		return nil, fmt.Errorf("json: reading %s: %w", p.source(""), err)
	}

	return names, nil
}

// Unmarshal reads the files in order, navigates to the subtree matching path in
// each and populates dst with the values found. A problem in one file is reported
// with its name and the others are still read.
func (p *DirProvider) Unmarshal(path []string, dst any) error {
	names, err := p.list()
	if err != nil {
		return err
	}

	var errs []error

	for _, name := range names {
		data, err := safefile.ReadConfigFile(filepath.Join(p.path, name), maxDocument, !p.noModes)
		if err != nil {
			errs = append(errs, readError(p.source(name), err))

			continue
		}

		if err := unmarshalData(data, path, dst, &p.keys); err != nil {
			errs = append(errs, inFile(err, p.source(name)))
		}
	}

	return errors.Join(errs...)
}

// Describe lists the paths in the documents the provider would read for dst at
// path, with the field each is for, in declaration order. It reads nothing, so the
// directory need not exist, and doesn't count as asking for the keys. See
// [config.Describer].
func (p *DirProvider) Describe(path []string, dst any) ([]config.FieldKey, error) {
	if p.err != nil {
		return nil, p.err
	}

	return describe(path, dst)
}

// Check reads the files again and lists the keys under root, the path of the root
// function, that nothing has asked for, as a [*config.FieldError] wrapping
// [config.ErrUnknown] for each, with the file in the message. A key that is read in
// one file is known in all of them. A missing directory has nothing to report. See
// [RawProvider.Check].
func (p *DirProvider) Check(root []string) error {
	names, err := p.list()
	if err != nil {
		return err
	}

	var errs []error

	for _, name := range names {
		data, err := safefile.ReadConfigFile(filepath.Join(p.path, name), maxDocument, !p.noModes)
		if err != nil {
			errs = append(errs, readError(p.source(name), err))

			continue
		}

		if err := check(data, root, &p.keys, p.source(name)); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// inFile says which file an error is about, for the problems that don't already
// name a field and a key.
func inFile(err error, source string) error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		errs := joined.Unwrap()
		out := make([]error, len(errs))

		for i, e := range errs {
			out[i] = inFile(e, source)
		}

		return errors.Join(out...)
	}

	var fieldErr *config.FieldError
	if errors.As(err, &fieldErr) {
		return err
	}

	return fmt.Errorf("%w (in %s)", err, source)
}
