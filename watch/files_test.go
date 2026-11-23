package watch_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"go.krak3n.dev/foundation/watch"
)

// Files polls the state of the files it is given, and never opens them: a file is
// only ever looked at with stat, so a named pipe or a device at a watched path
// can't block it, and nothing is read.

func filesOf(paths ...string) watch.Trigger { return watch.Files(paths) }

func TestFilesReportsNothingWithoutAChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		present := filepath.Join(dir, "present.json")

		write(t, present, "aaaa")

		events, cancel := start(t, filesOf(present, filepath.Join(dir, "absent.json")))
		defer cancel()

		settle(10 * watch.DefaultInterval)

		if got, closed := drain(events); len(got) != 0 || closed {
			t.Fatalf("got %+v, closed %v", got, closed)
		}
	})
}

// Watch returns once it has looked at the files, so a change made the moment it
// returns is not missed: whoever starts the thing that reads them starts the
// watch first.
func TestAChangeRightAfterWatchReturnsIsSeen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")

		write(t, path, "aaaa")

		events, cancel := start(t, filesOf(path))
		defer cancel()

		write(t, path, "bbbbbbbb")

		settle(2 * watch.DefaultInterval)

		got, _ := drain(events)

		if len(got) != 1 || got[0].Kind != watch.Restart {
			t.Fatalf("got %+v, want one restart", got)
		}
	})
}

func TestFilesDetectsChanges(t *testing.T) {
	tests := map[string]struct {
		setup  func(t *testing.T, dir, path string)
		mutate func(t *testing.T, dir, path string)
	}{
		"it grows": {
			setup:  func(t *testing.T, dir, path string) { write(t, path, "aaaa") },
			mutate: func(t *testing.T, dir, path string) { write(t, path, "aaaaaa") },
		},
		"it is emptied": {
			setup:  func(t *testing.T, dir, path string) { write(t, path, "aaaa") },
			mutate: func(t *testing.T, dir, path string) { write(t, path, "") },
		},
		"its time changes": {
			setup:  func(t *testing.T, dir, path string) { write(t, path, "aaaa") },
			mutate: func(t *testing.T, dir, path string) { setTime(t, path, later) },
		},
		"it is created": {
			setup:  func(t *testing.T, dir, path string) {},
			mutate: func(t *testing.T, dir, path string) { write(t, path, "aaaa") },
		},
		"it is deleted": {
			setup: func(t *testing.T, dir, path string) { write(t, path, "aaaa") },
			mutate: func(t *testing.T, dir, path string) {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			},
		},
		// What an editor, or a deployment, does: write elsewhere and rename over. The
		// new file has the same size and time, so only that it is another file says so.
		"it is replaced by an identical looking file": {
			setup: func(t *testing.T, dir, path string) { write(t, path, "aaaa") },
			mutate: func(t *testing.T, dir, path string) {
				tmp := path + ".new"

				write(t, tmp, "bbbb")

				if err := os.Rename(tmp, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		"it becomes a directory": {
			setup: func(t *testing.T, dir, path string) { write(t, path, "aaaa") },
			mutate: func(t *testing.T, dir, path string) {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}

				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "config.json")

				tt.setup(t, dir, path)

				events, cancel := start(t, filesOf(path))
				defer cancel()

				tt.mutate(t, dir, path)

				settle(2 * watch.DefaultInterval)

				got, _ := drain(events)

				if len(got) != 1 || got[0].Kind != watch.Restart || got[0].Reason == "" {
					t.Fatalf("got %+v, want one restart with a reason", got)
				}

				plain(t, got[0])

				if !strings.Contains(got[0].Reason, path) {
					t.Errorf("the reason should say which file: %q", got[0].Reason)
				}
			})
		})
	}
}

// A change is one event, not one for every poll after it.
func TestAChangeIsReportedOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")

		write(t, path, "aaaa")

		events, cancel := start(t, filesOf(path))
		defer cancel()

		write(t, path, "bbbbbbbb")

		settle(20 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 1 {
			t.Fatalf("got %d events for one change: %+v", len(got), got)
		}
	})
}

// A burst of writes between two polls is one change, and a reader that is slow
// never holds the watcher up or makes it queue: what is waiting is one event.
func TestFilesCoalescesAndNeverBlocksOnASlowReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")

		write(t, path, "a")

		events, cancel := start(t, filesOf(path))
		defer cancel()

		for i := 1; i <= 50; i++ {
			write(t, path, strings.Repeat("a", i+1))
		}

		settle(2 * watch.DefaultInterval)

		// Ten more changes, with nobody reading.
		for i := range 10 {
			write(t, path, strings.Repeat("b", 100+i))

			settle(watch.DefaultInterval)
		}

		if got, _ := drain(events); len(got) != 1 {
			t.Fatalf("got %d events waiting, want the one: %+v", len(got), got)
		}

		// The watcher is still going.
		write(t, path, "short")

		settle(2 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 1 {
			t.Fatalf("got %+v after the reader caught up", got)
		}
	})
}

func TestFilesHonoursTheInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")

		write(t, path, "a")

		events, cancel := start(t, watch.Files([]string{path}, watch.WithInterval(10*time.Second)))
		defer cancel()

		write(t, path, "bb")

		settle(5 * time.Second)

		if got, _ := drain(events); len(got) != 0 {
			t.Fatalf("got %+v before the interval", got)
		}

		settle(10 * time.Second)

		if got, _ := drain(events); len(got) != 1 {
			t.Fatalf("got %+v after the interval", got)
		}
	})
}

// Every Watch has its own baseline: a change before it started is not news to it.
func TestEachWatchTakesItsOwnBaseline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")

		write(t, path, "a")

		tr := filesOf(path)

		first, cancelFirst := start(t, tr)
		defer cancelFirst()

		write(t, path, "bb")

		second, cancelSecond := start(t, tr)
		defer cancelSecond()

		settle(2 * watch.DefaultInterval)

		if got, _ := drain(first); len(got) != 1 {
			t.Errorf("the first watch should see the change: %+v", got)
		}

		if got, _ := drain(second); len(got) != 0 {
			t.Errorf("the second watch began after it: %+v", got)
		}
	})
}

func TestCancellingStopsFiles(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")

		write(t, path, "a")

		events, cancel := start(t, filesOf(path))

		cancel()
		synctest.Wait()

		if _, closed := drain(events); !closed {
			t.Fatal("the channel should be closed")
		}

		// The bubble only ends if the polling goroutine has: that is the leak check.
	})
}

func TestFilesRefusesWhatIsNotAWatch(t *testing.T) {
	many := func(n int) []string {
		paths := make([]string, n)
		for i := range paths {
			paths[i] = filepath.Join(os.TempDir(), "watch-limit", fmt.Sprintf("f%d", i))
		}

		return paths
	}

	tests := map[string]struct {
		trigger watch.Trigger
		ok      bool
	}{
		"no paths":                {watch.Files(nil), false},
		"an empty path":           {watch.Files([]string{"a.json", ""}), false},
		"a NUL in a path":         {watch.Files([]string{"a\x00b"}), false},
		"the most paths":          {watch.Files(many(watch.MaxPaths)), true},
		"one more than the most":  {watch.Files(many(watch.MaxPaths + 1)), false},
		"the shortest interval":   {watch.Files([]string{"a"}, watch.WithInterval(watch.MinInterval)), true},
		"under the shortest":      {watch.Files([]string{"a"}, watch.WithInterval(watch.MinInterval-1)), false},
		"a zero interval":         {watch.Files([]string{"a"}, watch.WithInterval(0)), false},
		"a negative interval":     {watch.Files([]string{"a"}, watch.WithInterval(-time.Second)), false},
		"a very long interval":    {watch.Files([]string{"a"}, watch.WithInterval(24*time.Hour)), true},
		"an interval that wraps":  {watch.Files([]string{"a"}, watch.WithInterval(time.Duration(1<<62))), false},
		"a nil option is ignored": {watch.Files([]string{"a"}, nil), true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				events, err := tt.trigger.Watch(ctx)

				if tt.ok != (err == nil) {
					t.Fatalf("Watch error = %v, want ok %v", err, tt.ok)
				}

				if err != nil && events != nil {
					t.Error("a watch that failed to start should return no channel")
				}

				if err != nil && len(err.Error()) > 300 {
					t.Errorf("an error of %d bytes", len(err.Error()))
				}
			})
		})
	}
}

// A reason is bounded however long the path is.
func TestFilesReasonsAreBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), strings.Repeat("é", 100))

		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Skipf("can't make a long name here: %v", err)
		}

		path := filepath.Join(dir, strings.Repeat("é", 100)+".json")

		write(t, path, "a")

		events, cancel := start(t, filesOf(path))
		defer cancel()

		write(t, path, "bb")

		settle(2 * watch.DefaultInterval)

		got, _ := drain(events)
		if len(got) != 1 {
			t.Fatalf("got %+v", got)
		}

		plain(t, got[0])
	})
}
