//go:build unix

package watch_test

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"go.krak3n.dev/foundation/watch"
)

// A link is followed to what it points at, so pointing it somewhere else is a
// change even if the two files look the same.
func TestFilesSeesALinkPointedElsewhere(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()

		a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")

		write(t, a, "aaaa")
		write(t, b, "bbbb")

		link := filepath.Join(dir, "config.json")

		if err := os.Symlink(a, link); err != nil {
			t.Fatal(err)
		}

		events, cancel := start(t, filesOf(link))
		defer cancel()

		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}

		if err := os.Symlink(b, link); err != nil {
			t.Fatal(err)
		}

		settle(2 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 1 {
			t.Fatalf("got %+v, want one", got)
		}
	})
}

// A write that puts the size and the modification time back looks the same to
// stat, but not to the time the file's status changed, which a write can't set. It
// runs in real time: the file system's own clock is what is being tested.
func TestFilesSeesAWriteThatRestoresTheTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	write(t, path, "aaaa")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := watch.Files([]string{path}, watch.WithInterval(watch.MinInterval)).Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Enough for the file system's clock to have moved on.
	time.Sleep(50 * time.Millisecond)

	if err := os.WriteFile(path, []byte("bbbb"), 0o600); err != nil {
		t.Fatal(err)
	}

	setTime(t, path, epoch)

	select {
	case ev := <-events:
		if ev.Kind != watch.Restart {
			t.Fatalf("got %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a write that restored the size and the time was not seen")
	}
}

// A parent that can't be looked into is a change, because what is in it is no
// longer known; it is not a crash, and not silence.
func TestFilesTreatsAnUnreadableParentAsAChange(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can look into anything")
	}

	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")

		write(t, path, "aaaa")

		events, cancel := start(t, filesOf(path))
		defer cancel()

		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		settle(2 * watch.DefaultInterval)

		got, _ := drain(events)
		if len(got) != 1 {
			t.Fatalf("got %+v, want one", got)
		}

		plain(t, got[0])

		// It stays unreadable: that is not news every poll.
		settle(10 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 0 {
			t.Fatalf("got %+v while nothing changed", got)
		}
	})
}

// Opening a named pipe waits for a writer. Nothing is opened, so a pipe where a
// file should be, or in a directory, is looked at and does not hold anything up.
func TestAFIFOIsLookedAtNotOpened(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe.json")

	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("can't make a FIFO here: %v", err)
	}

	for name, trigger := range map[string]watch.Trigger{
		"files": watch.Files([]string{fifo}, watch.WithInterval(watch.MinInterval)),
		"dir":   watch.Dir(dir, watch.WithInterval(watch.MinInterval)),
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			type result struct {
				events <-chan watch.Event
				err    error
			}

			done := make(chan result, 1)

			go func() {
				events, err := trigger.Watch(ctx)
				done <- result{events, err}
			}()

			select {
			case r := <-done:
				if r.err != nil {
					t.Fatal(r.err)
				}

				// Let it poll a few times.
				select {
				case ev := <-r.events:
					t.Fatalf("got %+v for a pipe that didn't change", ev)
				case <-time.After(500 * time.Millisecond):
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Watch blocked on a named pipe")
			}
		})
	}
}

// In a directory, a link to a file is part of it, and a link that points nowhere
// is a state of its own that ends when it is mended.
func TestDirSeesLinksChangeAndMend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		elsewhere := t.TempDir()

		target := filepath.Join(elsewhere, "target.json")
		link := filepath.Join(dir, "10-site.json")

		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}

		events, cancel := start(t, dirOf(dir, watch.WithExtensions(".json")))
		defer cancel()

		settle(5 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 0 {
			t.Fatalf("got %+v while the link stayed broken", got)
		}

		write(t, target, "{}")

		settle(2 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 1 {
			t.Fatalf("got %+v when the link was mended", got)
		}
	})
}

// A link to a directory with a file's name is a directory, which isn't entered.
func TestDirDoesNotEnterALinkToADirectory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		other := t.TempDir()

		if err := os.Symlink(other, filepath.Join(dir, "linked.json")); err != nil {
			t.Fatal(err)
		}

		events, cancel := start(t, dirOf(dir, watch.WithExtensions(".json")))
		defer cancel()

		write(t, filepath.Join(other, "inner.json"), "{}")

		settle(5 * watch.DefaultInterval)

		if got, _ := drain(events); len(got) != 0 {
			t.Fatalf("got %+v for a file in another directory", got)
		}
	})
}

// Opening a named pipe waits for a writer. A directory that is replaced by one
// after the watch started must not hold the watcher up: it is a change, and the
// watch still stops when it is told to.
func TestADirectoryReplacedByAFIFODoesNotBlockTheWatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conf.d")

	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := watch.Dir(path, watch.WithInterval(watch.MinInterval)).Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("can't make a FIFO here: %v", err)
	}

	select {
	case ev, ok := <-events:
		if !ok || ev.Kind != watch.Restart {
			t.Fatalf("got %+v, closed %v", ev, !ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watch did not see that the directory was gone: it is stuck opening a pipe")
	}

	cancel()

	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("got an event after the watch was cancelled")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling did nothing: the watcher is stuck on the pipe")
	}
}
