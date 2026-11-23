package watch_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"go.krak3n.dev/foundation/watch"
)

// Dir watches the files in one directory. Hidden files (a leading dot) and
// subdirectories are not part of it; which of the others count is the caller's
// to say, with extensions and a filter. It knows nothing of any provider. Like Files
// it only ever stats.

func dirOf(dir string, opts ...watch.Option) watch.Trigger {
	return watch.Dir(dir, opts...)
}

func TestDirDetectsChangesToTheFilesInIt(t *testing.T) {
	tests := map[string]struct {
		setup  func(t *testing.T, dir string)
		mutate func(t *testing.T, dir string)
	}{
		"a file is added": {
			setup:  func(t *testing.T, dir string) {},
			mutate: func(t *testing.T, dir string) { write(t, filepath.Join(dir, "10-site.json"), "{}") },
		},
		"a file is removed": {
			setup: func(t *testing.T, dir string) { write(t, filepath.Join(dir, "10-site.json"), "{}") },
			mutate: func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, "10-site.json")); err != nil {
					t.Fatal(err)
				}
			},
		},
		"a file changes size": {
			setup:  func(t *testing.T, dir string) { write(t, filepath.Join(dir, "10-site.json"), "{}") },
			mutate: func(t *testing.T, dir string) { write(t, filepath.Join(dir, "10-site.json"), `{"a": 1}`) },
		},
		"a file changes time": {
			setup:  func(t *testing.T, dir string) { write(t, filepath.Join(dir, "10-site.json"), "{}") },
			mutate: func(t *testing.T, dir string) { setTime(t, filepath.Join(dir, "10-site.json"), later) },
		},
		"a file is renamed": {
			setup: func(t *testing.T, dir string) { write(t, filepath.Join(dir, "10-site.json"), "{}") },
			mutate: func(t *testing.T, dir string) {
				if err := os.Rename(filepath.Join(dir, "10-site.json"), filepath.Join(dir, "20-site.json")); err != nil {
					t.Fatal(err)
				}
			},
		},
		"a file is replaced by one that looks the same": {
			setup: func(t *testing.T, dir string) { write(t, filepath.Join(dir, "10-site.json"), "{}") },
			mutate: func(t *testing.T, dir string) {
				tmp := filepath.Join(dir, ".10-site.json.tmp")

				write(t, tmp, "[]")

				if err := os.Rename(tmp, filepath.Join(dir, "10-site.json")); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dir := t.TempDir()

				tt.setup(t, dir)

				events, cancel := start(t, dirOf(dir, watch.WithExtensions(".json")))
				defer cancel()

				tt.mutate(t, dir)

				settle(2 * watch.DefaultInterval)

				got, _ := drain(events)

				if len(got) != 1 || got[0].Kind != watch.Restart || got[0].Reason == "" {
					t.Fatalf("got %+v, want one restart with a reason", got)
				}

				plain(t, got[0])
			})
		})
	}
}

// The default sees every file that isn't hidden (a leading dot) and every kind of
// leftover: this package knows nothing of what any provider reads, so it can see
// more than a provider does, which costs a spurious event that the candidate
// absorbs, and never less, which would miss an edit. A directory is not entered.
func TestDirWithoutOptionsSeesEveryVisibleFile(t *testing.T) {
	names := []string{
		"10-site.json", "backup~", "#10-site.json#", "10-site.json.bak", "README",
		"10-site.json.swp",
		// A name that could forge a log line is a file like any other here, and is
		// never logged.
		"a\n.json", "a\x1b[2J.json", "\xff.json", "a\u202e.json",
	}

	for _, name := range names {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dir := t.TempDir()

				events, cancel := start(t, dirOf(dir))
				defer cancel()

				if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
					t.Skip("this file system won't have the name")
				}

				settle(2 * watch.DefaultInterval)

				got, _ := drain(events)

				if len(got) != 1 {
					t.Fatalf("got %+v, want one event", got)
				}

				plain(t, got[0])
			})
		})
	}
}

func TestDirIgnoresHiddenFilesAndDirectories(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()

		events, cancel := start(t, dirOf(dir))
		defer cancel()

		for round := range 3 {
			write(t, filepath.Join(dir, ".hidden"), strings.Repeat("x", round+1))
			write(t, filepath.Join(dir, ".10-site.json.swp"), strings.Repeat("x", round+1))

			sub := filepath.Join(dir, "sub.json")
			if err := os.MkdirAll(sub, 0o700); err != nil {
				t.Fatal(err)
			}

			write(t, filepath.Join(sub, "inner.json"), strings.Repeat("y", round+1))

			if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
				t.Fatal(err)
			}

			settle(2 * watch.DefaultInterval)
		}

		if got, closed := drain(events); len(got) != 0 || closed {
			t.Fatalf("got %+v, closed %v: nothing here is a visible file", got, closed)
		}
	})
}

// An extension is matched exactly, at the end of the name, and nothing else is
// known about names.
func TestDirWithExtensionsSeesOnlyTheseNames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()

		events, cancel := start(t, dirOf(dir, watch.WithExtensions(".json", ".yml")))
		defer cancel()

		for _, name := range []string{"README", "notes.txt", "99-other.JSON", "site.json.bak", ".hidden.json"} {
			write(t, filepath.Join(dir, name), "x")
		}

		settle(2 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 0 {
			t.Fatalf("got %+v for names with another extension", got)
		}

		write(t, filepath.Join(dir, "10-site.json"), "{}")

		settle(2 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 1 {
			t.Fatalf("got %+v for a .json file", got)
		}

		write(t, filepath.Join(dir, "20-site.yml"), "a: 1")

		settle(2 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 1 {
			t.Fatalf("got %+v for a .yml file", got)
		}
	})
}

// WithFilter is how a caller says which names are its own, such as the files a
// provider reads. It narrows what is already seen, together with extensions.
func TestDirWithFilterNarrowsWhatIsSeen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()

		only := func(name string) bool { return strings.HasPrefix(name, "10-") }

		events, cancel := start(t, dirOf(dir, watch.WithExtensions(".json"), watch.WithFilter(only)))
		defer cancel()

		write(t, filepath.Join(dir, "20-site.json"), "{}") // filtered out
		write(t, filepath.Join(dir, "10-site.txt"), "{}")  // wrong extension

		settle(2 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 0 {
			t.Fatalf("got %+v for names the filter or the extensions refuse", got)
		}

		write(t, filepath.Join(dir, "10-site.json"), "{}")

		settle(2 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 1 {
			t.Fatalf("got %+v for a name both accept", got)
		}
	})
}

// The filter is given names, never paths, from one listing of one directory, and
// no more of them than the limit: it can't be made to run without bound by what is
// in the directory.
func TestDirFilterSeesNamesNeverPathsAndBoundedMany(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()

		for i := range watch.MaxDirFiles + 50 {
			write(t, filepath.Join(dir, fmt.Sprintf("f%05d", i)), "x")
		}

		var calls, longest int

		filter := func(name string) bool {
			calls++

			if strings.ContainsRune(name, filepath.Separator) {
				t.Errorf("the filter was given a path: %q", name)
			}

			longest = max(longest, len(name))

			return true
		}

		_, cancel := start(t, dirOf(dir, watch.WithFilter(filter)))
		defer cancel()

		settle(3 * watch.DefaultInterval)

		// The baseline and a few polls, each bounded by the limit and not by what is in
		// the directory.
		if polls := 5; calls > polls*(watch.MaxDirFiles+50) {
			t.Errorf("the filter ran %d times", calls)
		}

		if longest > 255 {
			t.Errorf("a name of %d bytes", longest)
		}
	})
}

// A filter that panics is a mistake in the caller's code, and it can't be allowed
// to take the process down or to be quiet: the watch ends, which the framework
// reports, and nothing is delivered after it.
func TestDirFilterThatPanicsEndsTheWatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()

		boom := func(name string) bool {
			if name == "trip" {
				panic("a filter that panics")
			}

			return true
		}

		events, cancel := start(t, dirOf(dir, watch.WithFilter(boom)))
		defer cancel()

		write(t, filepath.Join(dir, "trip"), "x")

		settle(3 * watch.DefaultInterval)

		got, closed := drain(events)

		if !closed {
			t.Fatalf("got %+v, and the watch did not end", got)
		}

		for _, ev := range got {
			plain(t, ev)
		}
	})
}

// A directory that isn't there, and one with nothing in it, hold the same
// configuration, so going from one to the other is not a change, and a file
// appearing is.
func TestDirThatDoesNotExistYet(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "conf.d")

		events, cancel := start(t, dirOf(dir, watch.WithExtensions(".json")))
		defer cancel()

		settle(5 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 0 {
			t.Fatalf("got %+v for a directory that is not there", got)
		}

		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}

		settle(2 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 0 {
			t.Fatalf("got %+v for an empty directory", got)
		}

		write(t, filepath.Join(dir, "00-base.json"), "{}")

		settle(2 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 1 {
			t.Fatalf("got %+v for a file in it", got)
		}
	})
}

// More files than a directory of configuration may hold is one state, not an
// event for every file: the transition is news, and the files that follow in the
// state are not.
func TestDirOverTheLimitIsAStableState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()

		name := func(i int) string { return filepath.Join(dir, fmt.Sprintf("%04d.json", i)) }

		for i := range watch.MaxDirFiles {
			write(t, name(i), "{}")
		}

		events, cancel := start(t, dirOf(dir, watch.WithExtensions(".json")))
		defer cancel()

		settle(5 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 0 {
			t.Fatalf("got %+v at the limit", got)
		}

		write(t, name(watch.MaxDirFiles), "{}")

		settle(2 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 1 {
			t.Fatalf("got %+v for going over the limit", got)
		}

		write(t, name(watch.MaxDirFiles+1), "{}")
		write(t, name(watch.MaxDirFiles+2), "{}")

		settle(5 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 0 {
			t.Fatalf("got %+v for more files while over the limit", got)
		}

		for i := watch.MaxDirFiles; i < watch.MaxDirFiles+3; i++ {
			if err := os.Remove(name(i)); err != nil {
				t.Fatal(err)
			}
		}

		settle(2 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 1 {
			t.Fatalf("got %+v for coming back under the limit", got)
		}
	})
}

func TestDirRefusesWhatIsNotAWatch(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file.json")

	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		trigger watch.Trigger
		ok      bool
	}{
		"an empty path":                      {watch.Dir(""), false},
		"a NUL in the path":                  {watch.Dir("a\x00b"), false},
		"a file, not a directory":            {watch.Dir(file), false},
		"a directory that is missing":        {watch.Dir(filepath.Join(t.TempDir(), "missing")), true},
		"an extension without its dot":       {watch.Dir(t.TempDir(), watch.WithExtensions("json")), false},
		"an empty extension":                 {watch.Dir(t.TempDir(), watch.WithExtensions("")), false},
		"no extensions given":                {watch.Dir(t.TempDir(), watch.WithExtensions()), false},
		"an extension with a slash":          {watch.Dir(t.TempDir(), watch.WithExtensions(".a/b")), false},
		"several extensions":                 {watch.Dir(t.TempDir(), watch.WithExtensions(".yaml", ".yml")), true},
		"a double extension":                 {watch.Dir(t.TempDir(), watch.WithExtensions(".tar.gz")), true},
		"only dots":                          {watch.Dir(t.TempDir(), watch.WithExtensions("..")), false},
		"a hidden looking extension":         {watch.Dir(t.TempDir(), watch.WithExtensions("..json")), false},
		"a control character":                {watch.Dir(t.TempDir(), watch.WithExtensions(".js\non")), false},
		"an extension of the longest length": {watch.Dir(t.TempDir(), watch.WithExtensions("."+strings.Repeat("a", 15))), true},
		"an extension one byte too long":     {watch.Dir(t.TempDir(), watch.WithExtensions("."+strings.Repeat("a", 16))), false},
		"an interval under the floor":        {watch.Dir(t.TempDir(), watch.WithInterval(1)), false},
		"a nil filter":                       {watch.Dir(t.TempDir(), watch.WithFilter(nil)), false},
		"a filter":                           {watch.Dir(t.TempDir(), watch.WithFilter(func(string) bool { return true })), true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				_, err := tt.trigger.Watch(ctx)

				if tt.ok != (err == nil) {
					t.Fatalf("Watch error = %v, want ok %v", err, tt.ok)
				}
			})
		})
	}
}

func TestCancellingStopsDir(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		events, cancel := start(t, dirOf(t.TempDir()))

		cancel()
		synctest.Wait()

		if _, closed := drain(events); !closed {
			t.Fatal("the channel should be closed")
		}
	})
}

// A watched directory that is replaced by a file is a change, not a crash, and
// the watch carries on.
func TestDirReplacedByAFileIsAChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := t.TempDir()
		path := filepath.Join(base, "conf.d")

		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}

		write(t, filepath.Join(path, "00-base.json"), "{}")

		events, cancel := start(t, dirOf(path, watch.WithExtensions(".json")))
		defer cancel()

		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}

		write(t, path, "not a directory")

		settle(2 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 1 {
			t.Fatalf("got %+v, want one", got)
		}

		// It is a state like any other: that it stays is not news.
		settle(10 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 0 {
			t.Fatalf("got %+v while nothing changed", got)
		}

		// And when the directory is back, that is news again.
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}

		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}

		write(t, filepath.Join(path, "00-base.json"), "{}")

		settle(2 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 1 {
			t.Fatalf("got %+v when the directory came back", got)
		}
	})
}
