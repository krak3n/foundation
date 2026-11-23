package watch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// listBatch is how many entries of a directory are read at a time.
	listBatch = 256

	// maxEntries is the most entries of a directory that are looked at: past it the
	// directory is over the limit without being looked at further.
	maxEntries = 1 << 16

	// maxCandidates is the most names that match in a directory that are looked at
	// with stat: a few times the limit is already over it.
	maxCandidates = 4 * MaxDirFiles
)

// state is what is known of one file at one poll. Two states are the same file
// unchanged if every part is the same.
type state struct {
	// err says why the file couldn't be looked at, as a reason without its path;
	// an unreadable file is a state of its own and stays one without being news.
	err string

	exists bool
	mode   fs.FileMode
	size   int64
	mtime  int64
	ctime  int64

	// info is kept to tell one file from another that looks the same.
	info fs.FileInfo
}

func (a state) same(b state) bool {
	if a.err != b.err || a.exists != b.exists || a.mode != b.mode || a.size != b.size || a.mtime != b.mtime || a.ctime != b.ctime {
		return false
	}

	if a.info != nil && b.info != nil {
		return os.SameFile(a.info, b.info)
	}

	return true
}

// stat looks at the path, following links. A path that isn't there is a state; so
// is one that can't be looked at.
func stat(path string) state {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return state{}
		}

		return state{err: why(err)}
	}

	return of(info)
}

func of(info fs.FileInfo) state {
	return state{
		exists: true,
		mode:   info.Mode(),
		size:   info.Size(),
		mtime:  info.ModTime().UnixNano(),
		ctime:  ctimeOf(info),
		info:   info,
	}
}

// why says what was wrong without saying where: the path is not part of the
// reason a file is in the state it is.
func why(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}

	return err.Error()
}

// poll runs take every interval until ctx is done and posts a restart when what it
// returns differs from the last time. The first take is made before poll returns,
// so a change after that is seen. It ends the emitter when it ends. A take that
// panics (it may run a caller's function) is an error from the first, and the end of
// the watch after that, never a crash.
func poll[S any](ctx context.Context, interval time.Duration, o *Emitter, take func() S, diff func(prev, cur S) (string, bool)) error {
	prev, ok := attempt(take)
	if !ok {
		return errors.New("watch: looking for a change panicked")
	}

	go func() {
		defer o.End()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cur, ok := attempt(take)
				if !ok {
					return
				}

				if reason, changed := diff(prev, cur); changed {
					o.Post(Event{Kind: Restart, Reason: reason})
				}

				prev = cur
			}
		}
	}()

	return nil
}

// attempt calls take, and says whether it returned instead of panicking.
func attempt[S any](take func() S) (s S, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()

	return take(), true
}

// Files returns a trigger that polls the files at paths and sends a [Restart] when
// one changes. A file that doesn't exist is a state like any other: it appearing
// is a change. Links are followed. Files are only looked at with stat, never
// opened or read (on Windows, telling one file from another that looks the same
// may open it for its attributes, without asking to read it).
//
// At least one path and at most [MaxPaths] are needed, and none may be empty or
// have a NUL in it. The reason names the first file that changed, as it was given.
func Files(paths []string, opts ...Option) Trigger {
	return &filesTrigger{paths: append([]string(nil), paths...), settings: newSettings(opts)}
}

type filesTrigger struct {
	paths    []string
	settings settings
}

func (f *filesTrigger) Watch(ctx context.Context) (<-chan Event, error) {
	switch {
	case len(f.paths) == 0:
		return nil, errors.New("watch: Files was given no paths")
	case len(f.paths) > MaxPaths:
		return nil, fmt.Errorf("watch: Files was given %d paths, the limit is %d", len(f.paths), MaxPaths)
	}

	if err := f.settings.checkLabel(); err != nil {
		return nil, err
	}

	if f.settings.labelSet && len(f.paths) != 1 {
		return nil, errors.New("watch: a label names one path, and Files was given more")
	}

	for i, p := range f.paths {
		if p == "" || strings.ContainsRune(p, 0) {
			return nil, fmt.Errorf("watch: path %d is empty or has a NUL in it", i+1)
		}
	}

	interval, err := f.settings.pollInterval()
	if err != nil {
		return nil, err
	}

	o := NewEmitter(nil)

	err = poll(ctx, interval, o,
		func() []state {
			states := make([]state, len(f.paths))

			for i, p := range f.paths {
				states[i] = stat(p)
			}

			return states
		},
		func(prev, cur []state) (string, bool) {
			first, n := -1, 0

			for i := range cur {
				if !prev[i].same(cur[i]) {
					if first < 0 {
						first = i
					}

					n++
				}
			}

			if first < 0 {
				return "", false
			}

			// The path is shown as the program gave it, which is its own text. A path
			// that came from the environment must not be: whoever watches one gives
			// it a label instead (see the restart consumer), as errors say $NAME.
			reason := "file changed: " + f.settings.named(f.paths[first])
			if n > 1 {
				reason += fmt.Sprintf(" (and %d more)", n-1)
			}

			return reason, true
		},
	)

	if err != nil {
		return nil, err
	}

	return o.out, nil
}

// Dir returns a trigger that polls the files in the directory at path, and sends a
// [Restart] when one is added, removed or changed. Every entry whose name does not
// start with a dot counts, and subdirectories (and links to them) are not entered.
// This package knows nothing of what any reader of the directory reads, so it sees
// at least as much as they do, which can cost an event they don't need and never
// misses an edit. [WithExtensions] and [WithFilter] narrow it to the caller's own
// files, such as the ones a provider reads. Names in a reason are made safe to log.
//
// A directory that doesn't exist and one with nothing in it are the same, so one
// appearing is not a change, and a file in it is. More than [MaxDirFiles] files is
// one state of its own. The path must not be empty or have a NUL in it, and must
// not be a file. The directory is listed, and the files in it are only looked at
// with stat, never opened or read.
func Dir(path string, opts ...Option) Trigger {
	return &dirTrigger{path: path, settings: newSettings(opts)}
}

type dirTrigger struct {
	path     string
	settings settings
}

// dirState is what is known of a directory at one poll.
type dirState struct {
	err     string
	over    bool
	entries []dirEntry
}

// dirEntry is one file in a directory, in order of name.
type dirEntry struct {
	name string
	state
}

func (d *dirTrigger) Watch(ctx context.Context) (<-chan Event, error) {
	if d.path == "" || strings.ContainsRune(d.path, 0) {
		return nil, errors.New("watch: the directory path is empty or has a NUL in it")
	}

	if err := d.settings.checkLabel(); err != nil {
		return nil, err
	}

	if err := d.settings.extensions(); err != nil {
		return nil, err
	}

	interval, err := d.settings.pollInterval()
	if err != nil {
		return nil, err
	}

	if info, err := os.Stat(d.path); err == nil && !info.IsDir() {
		return nil, errors.New("watch: the path is not a directory")
	}

	o := NewEmitter(nil)

	name := d.settings.named(d.path)

	err = poll(ctx, interval, o, d.take, func(prev, cur dirState) (string, bool) {
		switch {
		case prev.err != cur.err:
			return "directory changed: " + name, true
		case prev.over != cur.over && cur.over:
			return "too many files in " + name, true
		case prev.over != cur.over:
			return "directory changed: " + name + " (back under the limit)", true
		case cur.over:
			// Over the limit is one state, whatever is in it.
			return "", false
		}

		if file, changed := firstDifference(prev, cur); changed {
			return "directory changed: " + name + " (" + shown(file) + ")", true
		}

		return "", false
	})

	if err != nil {
		return nil, err
	}

	return o.out, nil
}

// take looks at the directory.
func (d *dirTrigger) take() dirState {
	// Look before opening: opening a named pipe waits for a writer, and a directory
	// that was replaced by one must be a change, not a watcher that never comes back.
	// What is at the path can still change between this and the open, which is the
	// one window left; a file or a device that gets there is opened and then found
	// not to be a directory.
	info, err := os.Stat(d.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return dirState{}
		}

		return dirState{err: why(err)}
	}

	if !info.IsDir() {
		return dirState{err: "not a directory"}
	}

	f, err := os.Open(d.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return dirState{}
		}

		return dirState{err: why(err)}
	}

	defer f.Close()

	if opened, err := f.Stat(); err != nil || !opened.IsDir() {
		return dirState{err: "not a directory"}
	}

	var (
		names []string
		seen  int
	)

	for {
		entries, err := f.ReadDir(listBatch)

		for _, e := range entries {
			// A directory of millions of entries is not looked at in full.
			if seen++; seen > maxEntries {
				return dirState{over: true}
			}

			if d.matches(e.Name()) {
				names = append(names, e.Name())
			}
		}

		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return dirState{err: why(err)}
		}
	}

	// Past a few times the limit there is no need to look at each.
	if len(names) > maxCandidates {
		return dirState{over: true}
	}

	sort.Strings(names)

	st := dirState{}

	for _, name := range names {
		s := d.statEntry(name)

		// Not entered: a directory, or a link to one.
		if s.exists && s.mode.IsDir() {
			continue
		}

		st.entries = append(st.entries, dirEntry{name: name, state: s})
	}

	if len(st.entries) > MaxDirFiles {
		return dirState{over: true}
	}

	return st
}

// matches says whether a listed name is part of the watch: not hidden, one of the
// extensions if there are any, and accepted by the filter if there is one. Nothing
// else is known about names, so a caller that reads fewer files than this sees gets
// an event it doesn't need, never the reverse.
func (d *dirTrigger) matches(name string) bool {
	if strings.HasPrefix(name, ".") {
		return false
	}

	if d.settings.extsSet {
		ok := false

		for _, ext := range d.settings.exts {
			if strings.HasSuffix(name, ext) {
				ok = true

				break
			}
		}

		if !ok {
			return false
		}
	}

	return d.settings.filter == nil || d.settings.filter(name)
}

// shown is a name made fit for a log: anything that isn't valid, printable text is
// replaced, so a name can't forge a line or hide in one.
func shown(name string) string {
	return strings.Map(func(r rune) rune {
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			return '?'
		}

		return r
	}, name)
}

// statEntry looks at an entry that was listed: if it can't be followed it is a
// link that points nowhere, which is a state of its own until it is mended.
func (d *dirTrigger) statEntry(name string) state {
	info, err := os.Stat(filepath.Join(d.path, name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return state{err: "link points nowhere"}
		}

		return state{err: why(err)}
	}

	return of(info)
}

// firstDifference names the first file, in order of name, that was added, removed
// or changed.
func firstDifference(prev, cur dirState) (string, bool) {
	i, j := 0, 0

	for i < len(prev.entries) || j < len(cur.entries) {
		switch {
		case j >= len(cur.entries) || (i < len(prev.entries) && prev.entries[i].name < cur.entries[j].name):
			return prev.entries[i].name, true
		case i >= len(prev.entries) || cur.entries[j].name < prev.entries[i].name:
			return cur.entries[j].name, true
		case !prev.entries[i].same(cur.entries[j].state):
			return cur.entries[j].name, true
		}

		i++
		j++
	}

	return "", false
}
