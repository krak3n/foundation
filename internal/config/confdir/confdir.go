// Package confdir lists the documents in a directory of configuration, the way a
// conf.d directory is read: the files of one format, in name order, and nothing
// else. The file providers share it, so they select and refuse the same things.
//
// A directory holds more than configuration (editor swap files, backups, a
// README), and whoever can write to it can change the service, so the rules are
// strict: a name has to say it is a document of the format, a name that looks like
// a leftover or can't be shown safely is never read, anything that matches but
// can't be read is an error rather than a file to skip, and the number of files and
// their size are bounded.
package confdir

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.krak3n.dev/foundation/internal/config/safefile"
)

const (
	// MaxFiles is the most documents a directory can hold. More is an error, as
	// reading some of them would run the service on less than was put there.
	MaxFiles = 256

	// MaxTotalBytes is the most the documents of a directory can add up to.
	MaxTotalBytes = 32 << 20

	// maxEntries is the most entries of any kind that are looked at.
	maxEntries = 1 << 16

	// maxProblems is the most problems reported for one directory.
	maxProblems = 16

	// maxName is the longest name of a file that is read, in bytes.
	maxName = 255
)

var (
	// ErrNotFound is the cause of the error for a directory that doesn't exist.
	ErrNotFound = errors.New("directory does not exist")

	// ErrNotDir is the cause of the error for a path that is not a directory.
	ErrNotDir = errors.New("not a directory")
)

// Match reports whether a file called name is a document with one of the
// extensions, which are matched exactly, with the dot. It is not if the name is not
// [Visible].
func Match(name string, exts ...string) bool {
	if !Visible(name) {
		return false
	}

	for _, ext := range exts {
		if ext != "" && strings.HasSuffix(name, ext) {
			return true
		}
	}

	return false
}

// Visible reports whether a file called name could be part of a directory of
// configuration at all, whatever its extension. It is not if the name is a
// leftover or hidden (starting with a dot, ending in ~, or wrapped in #), if it
// has a separator, if it is longer than 255 bytes, or if it isn't printable text:
// a control character, a bidirectional override or invalid UTF-8 could forge a log
// line or hide what a name says.
func Visible(name string) bool {
	if name == "" || len(name) > maxName || !utf8.ValidString(name) {
		return false
	}

	switch {
	case strings.HasPrefix(name, "."), strings.HasSuffix(name, "~"):
		return false
	case len(name) > 1 && strings.HasPrefix(name, "#") && strings.HasSuffix(name, "#"):
		return false
	case strings.ContainsAny(name, `/\`):
		return false
	}

	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return false
		}
	}

	return true
}

// List returns the names of the documents in the directory dir, which are the
// regular files (symbolic links are followed) whose names [Match] the extensions,
// in the order of their names, byte by byte. Subdirectories are not entered and
// links to them are skipped.
//
// Anything that matches and can't be used is an error, and nothing is returned:
// a link that points nowhere, a file that isn't a regular file (a named pipe would
// block whoever opened it), more than [MaxFiles] files or [MaxTotalBytes] bytes
// of them, and, if modes is true, a directory or a file the group or others can
// write (see [safefile.TooOpen]). Errors name files by their own name and never
// by the path of the directory, which may come from the environment. A directory
// that doesn't exist is [ErrNotFound], and a path that is not one [ErrNotDir].
func List(dir string, modes bool, exts ...string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}

		return nil, bare(err)
	}

	if !info.IsDir() {
		return nil, ErrNotDir
	}

	if modes {
		if err := safefile.CheckMode(dir, info.Mode()); err != nil {
			return nil, bare(err)
		}
	}

	names, err := matching(dir, exts)
	if err != nil {
		return nil, err
	}

	var (
		files  []string
		total  int64
		errs   []error
		report = func(name string, err error) {
			if len(errs) < maxProblems {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
			}
		}
	)

	for _, name := range names {
		fi, err := os.Stat(filepath.Join(dir, name))

		switch {
		case err != nil:
			report(name, errors.New(reason(err)))
		case fi.IsDir():
			// Not entered.
		case !fi.Mode().IsRegular():
			report(name, safefile.ErrNotRegular)
		default:
			if modes {
				if err := safefile.CheckMode(name, fi.Mode()); err != nil {
					report(name, bare(err))

					continue
				}
			}

			files = append(files, name)
			total += fi.Size()
		}
	}

	if len(errs) > 0 {
		if len(errs) == maxProblems {
			errs = append(errs, errors.New("and possibly more"))
		}

		return nil, errors.Join(errs...)
	}

	if len(files) > MaxFiles {
		return nil, fmt.Errorf("more than %d files; the limit is %d", MaxFiles, MaxFiles)
	}

	if total > MaxTotalBytes {
		return nil, fmt.Errorf("the files add up to more than %d MiB", MaxTotalBytes>>20)
	}

	return files, nil
}

// matching returns the entries of dir whose names match, sorted byte by byte. It
// reads the directory in batches and gives up past [maxEntries], so a directory of
// millions of entries can't use the memory of the process.
func matching(dir string, exts []string) ([]string, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, bare(err)
	}

	defer f.Close()

	var (
		names []string
		seen  int
	)

	for {
		entries, err := f.ReadDir(256)

		for _, e := range entries {
			if seen++; seen > maxEntries {
				return nil, fmt.Errorf("more than %d entries", maxEntries)
			}

			if Match(e.Name(), exts...) {
				names = append(names, e.Name())
			}
		}

		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, bare(err)
		}
	}

	sort.Strings(names)

	return names, nil
}

// bare drops the path from an error from the file system, which names the
// directory, keeping the cause.
func bare(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}

	return err
}

// reason says why a file could not be looked at, without its path.
func reason(err error) string {
	return bare(err).Error()
}
