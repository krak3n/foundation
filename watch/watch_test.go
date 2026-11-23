package watch_test

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"unicode"
	"unicode/utf8"

	"go.krak3n.dev/foundation/watch"
)

// An event says what to do: restart or shut down. When more than one is waiting the
// stronger wins, so a shutdown is never lost behind a restart, and a shutdown is the
// end: nothing comes after it.

func TestKindsHaveNames(t *testing.T) {
	if got := fmt.Sprint(watch.Restart); got != "restart" {
		t.Errorf("Restart is %q", got)
	}

	if got := fmt.Sprint(watch.Shutdown); got != "shutdown" {
		t.Errorf("Shutdown is %q", got)
	}

	// The zero value is not a kind, so an event that was never filled in can't
	// mean anything. Odd values are shown without being trusted.
	for _, k := range []watch.Kind{0, 3, -1, 1 << 30} {
		s := fmt.Sprint(k)

		if s == "restart" || s == "shutdown" || s == "stop" || len(s) > 40 {
			t.Errorf("Kind(%d) is %q", int(k), s)
		}
	}

	if watch.Restart == 0 || watch.Shutdown == 0 || watch.Restart == watch.Shutdown {
		t.Error("the kinds must be distinct and not the zero value")
	}

	if watch.Shutdown <= watch.Restart {
		t.Error("Shutdown is the stronger kind and sorts above Restart")
	}
}

// The values are part of what a trigger written elsewhere sends, so renaming a
// kind must not move them: zero stays "not a kind", and the stronger is the higher.
func TestKindValuesDoNotMove(t *testing.T) {
	if watch.Restart != 1 || watch.Shutdown != 2 {
		t.Errorf("Restart = %d, Shutdown = %d, want 1 and 2", int(watch.Restart), int(watch.Shutdown))
	}
}

// Whoever writes a trigger of their own can ask whether a kind is one.
func TestKindValid(t *testing.T) {
	for k, want := range map[watch.Kind]bool{
		watch.Restart: true, watch.Shutdown: true, 0: false, -1: false, 3: false, 1 << 30: false,
	} {
		if got := k.Valid(); got != want {
			t.Errorf("Kind(%d).Valid() = %v, want %v", int(k), got, want)
		}
	}
}

func TestManualDeliversWhatIsFired(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr, fire := watch.Manual()

		events, cancel := start(t, tr)
		defer cancel()

		fire(watch.Restart, "config changed")
		synctest.Wait()

		got, closed := drain(events)

		if closed || len(got) != 1 || got[0].Kind != watch.Restart || got[0].Reason != "config changed" {
			t.Fatalf("got %+v, closed %v", got, closed)
		}

		// And again, which is a second event, not a repeat of the first.
		fire(watch.Restart, "again")
		synctest.Wait()

		if got, _ := drain(events); len(got) != 1 || got[0].Reason != "again" {
			t.Fatalf("got %+v", got)
		}
	})
}

// A burst is one event, however long: the watcher never blocks on a slow reader
// and never builds a queue.
func TestManualCoalescesABurst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr, fire := watch.Manual()

		events, cancel := start(t, tr)
		defer cancel()

		for range 1000 {
			fire(watch.Restart, "burst")
		}

		synctest.Wait()

		if got, _ := drain(events); len(got) != 1 || got[0].Kind != watch.Restart {
			t.Fatalf("got %d events, want one: %+v", len(got), got)
		}
	})
}

func TestAShutdownWinsOverARestartWhateverTheOrder(t *testing.T) {
	for name, order := range map[string][]watch.Kind{
		"restart then shutdown":      {watch.Restart, watch.Shutdown},
		"shutdown then restart":      {watch.Shutdown, watch.Restart},
		"restarts around a shutdown": {watch.Restart, watch.Shutdown, watch.Restart},
		"shutdowns and restarts":     {watch.Shutdown, watch.Restart, watch.Shutdown, watch.Restart},
		"a restart before a burst":   {watch.Restart, watch.Restart, watch.Shutdown},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tr, fire := watch.Manual()

				events, cancel := start(t, tr)
				defer cancel()

				for _, k := range order {
					fire(k, k.String())
				}

				synctest.Wait()

				got, closed := drain(events)

				if len(got) != 1 || got[0].Kind != watch.Shutdown {
					t.Fatalf("got %v, want a single shutdown", kinds(got))
				}

				if !closed {
					t.Error("the channel should be closed after a shutdown")
				}
			})
		})
	}
}

// A shutdown is the end of the watch: it releases what it holds and closes the
// channel, and a restart that arrives afterwards is dropped, never delivered.
func TestShutdownIsTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr, fire := watch.Manual()

		events, cancel := start(t, tr)
		defer cancel()

		fire(watch.Shutdown, "shutting down")
		synctest.Wait()

		got, closed := drain(events)

		if len(got) != 1 || got[0].Kind != watch.Shutdown || !closed {
			t.Fatalf("got %+v, closed %v", got, closed)
		}

		// Firing after the end is harmless: it doesn't panic, block or deliver.
		fire(watch.Restart, "too late")
		fire(watch.Shutdown, "too late")
		synctest.Wait()

		if got, closed := drain(events); len(got) != 0 || !closed {
			t.Fatalf("got %+v, closed %v after the end", got, closed)
		}
	})
}

// A kind that isn't one is never delivered: the zero value, and anything else.
func TestManualDropsAnInvalidKind(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr, fire := watch.Manual()

		events, cancel := start(t, tr)
		defer cancel()

		for _, k := range []watch.Kind{0, -1, 3, 99} {
			fire(k, "not a kind")
		}

		synctest.Wait()

		if got, closed := drain(events); len(got) != 0 || closed {
			t.Fatalf("got %+v, closed %v", got, closed)
		}

		// The watch still works.
		fire(watch.Restart, "fine")
		synctest.Wait()

		if got, _ := drain(events); len(got) != 1 {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestCancellingClosesTheChannel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr, fire := watch.Manual()

		events, cancel := start(t, tr)

		cancel()
		synctest.Wait()

		if _, closed := drain(events); !closed {
			t.Fatal("the channel should be closed once the context is done")
		}

		fire(watch.Restart, "after")
		synctest.Wait()
	})
}

// Every Watch of a trigger is its own: each gets what is fired while it watches.
func TestManualFansOutToEveryWatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr, fire := watch.Manual()

		a, cancelA := start(t, tr)
		defer cancelA()

		b, cancelB := start(t, tr)

		fire(watch.Restart, "x")
		synctest.Wait()

		if got, _ := drain(a); len(got) != 1 {
			t.Errorf("first watch got %+v", got)
		}

		if got, _ := drain(b); len(got) != 1 {
			t.Errorf("second watch got %+v", got)
		}

		// One that has stopped watching doesn't hold up the other.
		cancelB()
		synctest.Wait()

		fire(watch.Restart, "y")
		synctest.Wait()

		if got, _ := drain(a); len(got) != 1 {
			t.Errorf("first watch got %+v", got)
		}
	})
}

// A reason is for a log, so it is cut short, at a character boundary.
func TestReasonsAreBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr, fire := watch.Manual()

		events, cancel := start(t, tr)
		defer cancel()

		// Multi-byte characters, so that a cut can land inside one.
		fire(watch.Restart, strings.Repeat("é", 10000))
		synctest.Wait()

		got, _ := drain(events)
		if len(got) != 1 {
			t.Fatalf("got %+v", got)
		}

		if len(got[0].Reason) > watch.MaxReason || !utf8.ValidString(got[0].Reason) {
			t.Fatalf("a reason of %d bytes, valid UTF-8 %v", len(got[0].Reason), utf8.ValidString(got[0].Reason))
		}
	})
}

// Fired from many goroutines at once (run with the race detector): a shutdown that
// was fired is what the watcher ends with, and it is the only thing left.
func TestManualIsSafeForConcurrentUse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr, fire := watch.Manual()

		events, cancel := start(t, tr)
		defer cancel()

		var wg sync.WaitGroup

		for i := range 50 {
			wg.Go(func() {
				for range 20 {
					fire(watch.Restart, "r")
				}

				if i == 25 {
					fire(watch.Shutdown, "s")
				}
			})
		}

		wg.Wait()
		synctest.Wait()

		got, closed := drain(events)

		if len(got) != 1 || got[0].Kind != watch.Shutdown || !closed {
			t.Fatalf("got %v, closed %v, want a single shutdown", kinds(got), closed)
		}
	})
}

// afterFuncContext is a context of its own, not one wrapped around another (the
// context package looks through a wrapper and registers on what is inside), that
// counts what is registered on it with context.AfterFunc and what is released
// again: how a watch can be seen to keep a registration after it is over.
type afterFuncContext struct {
	done chan struct{}

	registered, released atomic.Int32
}

func (c *afterFuncContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *afterFuncContext) Done() <-chan struct{}       { return c.done }
func (c *afterFuncContext) Err() error                  { return nil }
func (c *afterFuncContext) Value(any) any               { return nil }

func (c *afterFuncContext) AfterFunc(func()) func() bool {
	c.registered.Add(1)

	return func() bool {
		c.released.Add(1)

		return true
	}
}

// A watch that ended with a shutdown has no more use for the context, which may live
// as long as the process: what it registered there is let go, or every watch that
// is started and stopped leaves something behind.
func TestAShutdownReleasesWhatTheWatchRegisteredOnTheContext(t *testing.T) {
	tr, fire := watch.Manual()

	ctx := &afterFuncContext{done: make(chan struct{})}

	events, err := tr.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}

	fire(watch.Shutdown, "now")

	if got, closed := drain(events); len(got) != 1 || !closed {
		t.Fatalf("got %+v, closed %v", got, closed)
	}

	if ctx.registered.Load() == 0 {
		t.Fatal("the test is not measuring anything: nothing was registered on the context")
	}

	if reg, rel := ctx.registered.Load(), ctx.released.Load(); reg != rel {
		t.Fatalf("%d registered on the context and %d released after the shutdown", reg, rel)
	}
}

// Whatever order things are fired and read in, a shutdown that was fired is delivered,
// nothing follows it, and no restart is delivered that wasn't fired.
func FuzzManualCoalescing(f *testing.F) {
	for _, seed := range [][]byte{{}, {0}, {1}, {0, 1}, {1, 0}, {0, 2, 0, 2}, {1, 2, 0, 2}, {0, 0, 0, 1, 2, 2}} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, ops []byte) {
		synctest.Test(t, func(t *testing.T) {
			tr, fire := watch.Manual()

			events, cancel := start(t, tr)
			defer cancel()

			var (
				restarts, shutdowns int
				delivered           []watch.Event
				closed              bool
			)

			for _, op := range ops {
				switch op % 3 {
				case 0:
					fire(watch.Restart, "r")

					if !closed {
						restarts++
					}
				case 1:
					fire(watch.Shutdown, "s")

					if !closed {
						shutdowns++
					}
				case 2:
					synctest.Wait()

					got, c := drain(events)

					delivered = append(delivered, got...)
					closed = closed || c
				}
			}

			synctest.Wait()

			got, c := drain(events)

			delivered = append(delivered, got...)
			closed = closed || c

			for i, ev := range delivered {
				switch ev.Kind {
				case watch.Restart:
					if restarts == 0 {
						t.Fatalf("a restart that was never fired: %v", kinds(delivered))
					}
				case watch.Shutdown:
					if i != len(delivered)-1 {
						t.Fatalf("an event after a shutdown: %v", kinds(delivered))
					}
				default:
					t.Fatalf("an invalid kind %v delivered", ev.Kind)
				}
			}

			if shutdowns > 0 && (len(delivered) == 0 || delivered[len(delivered)-1].Kind != watch.Shutdown || !closed) {
				t.Fatalf("a shutdown was fired but not delivered: %v, closed %v", kinds(delivered), closed)
			}
		})
	})
}

// Where a thing to watch came from is sometimes not for logs: a path that the
// operator put in an environment variable is a configuration value, and the
// program names it by the variable ($SVC_CONFIG). A label is the name a reason
// uses instead of the path, so what asked for the watch can say what it is called
// without the package knowing why. The label is the caller's own text, in code;
// it is still checked, because a bad one is better found when the watch starts than
// in a log.

// canaryDir is a directory whose name must never show up where a label was given.
const canaryDir = "canary-secret-path-4f7c"

func TestALabelNamesAFileInsteadOfItsPath(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), canaryDir+".json")

		write(t, path, "one")

		events, cancel := start(t, watch.Files([]string{path}, watch.WithLabel("$SVC_CONFIG")))
		defer cancel()

		write(t, path, "two")
		setTime(t, path, later)

		settle(2 * time.Second)

		got, _ := drain(events)
		if len(got) != 1 {
			t.Fatalf("got %d events, want 1: %+v", len(got), got)
		}

		if got[0].Kind != watch.Restart || got[0].Reason != "file changed: $SVC_CONFIG" {
			t.Errorf("the reason is %q, want the label in place of the path", got[0].Reason)
		}

		if strings.Contains(got[0].Reason, canaryDir) {
			t.Errorf("the reason has the path in it: %q", got[0].Reason)
		}
	})
}

func TestALabelNamesADirectoryInsteadOfItsPath(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), canaryDir)

		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}

		write(t, filepath.Join(dir, "a.json"), "one")

		events, cancel := start(t, watch.Dir(dir, watch.WithLabel("$SVC_CONFIG_DIR")))
		defer cancel()

		write(t, filepath.Join(dir, "a.json"), "two")
		setTime(t, filepath.Join(dir, "a.json"), later)

		settle(2 * time.Second)

		got, _ := drain(events)
		if len(got) != 1 {
			t.Fatalf("got %d events, want 1: %+v", len(got), got)
		}

		if want := "directory changed: $SVC_CONFIG_DIR (a.json)"; got[0].Reason != want {
			t.Errorf("the reason is %q, want %q", got[0].Reason, want)
		}
	})
}

// A directory with more files than are looked at has reasons of its own, and they
// are the other places the path was written.
func TestALabelReplacesThePathInEveryDirectoryReason(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), canaryDir)

		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}

		write(t, filepath.Join(dir, "a.json"), "one")

		events, cancel := start(t, watch.Dir(dir, watch.WithLabel("$SVC_CONFIG_DIR")))
		defer cancel()

		// Over the limit, and back under it again.
		for i := range watch.MaxDirFiles + 1 {
			write(t, filepath.Join(dir, "f"+strings.Repeat("0", 4)+string(rune('a'+i%26))+string(rune('a'+i/26%26))+".json"), "x")
		}

		settle(2 * time.Second)

		over, _ := drain(events)

		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}

		for _, e := range entries[1:] {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				t.Fatal(err)
			}
		}

		settle(2 * time.Second)

		back, _ := drain(events)

		reasons := append(over, back...)
		if len(reasons) < 2 {
			t.Fatalf("expected a reason for going over the limit and one for coming back: %+v", reasons)
		}

		for _, ev := range reasons {
			if strings.Contains(ev.Reason, canaryDir) {
				t.Errorf("the reason has the path in it: %q", ev.Reason)
			}

			if !strings.Contains(ev.Reason, "$SVC_CONFIG_DIR") {
				t.Errorf("the reason does not say what it is: %q", ev.Reason)
			}
		}
	})
}

// With no label nothing is different: the reason names the path as it was given.
func TestWithoutALabelTheReasonNamesThePath(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "svc.json")

		write(t, path, "one")

		events, cancel := start(t, watch.Files([]string{path}))
		defer cancel()

		write(t, path, "two")
		setTime(t, path, later)

		settle(2 * time.Second)

		got, _ := drain(events)
		if len(got) != 1 || got[0].Reason != "file changed: "+path {
			t.Errorf("got %+v, want the path in the reason", got)
		}
	})
}

// A watch that is made of others keeps what they say.
func TestALabelSurvivesAny(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, canaryDir+".json")

		write(t, path, "one")

		events, cancel := start(t, watch.Any(
			watch.Files([]string{path}, watch.WithLabel("$SVC_CONFIG")),
			watch.Dir(dir, watch.WithLabel("$SVC_CONFIG_DIR")),
		))
		defer cancel()

		write(t, path, "two")
		setTime(t, path, later)

		settle(2 * time.Second)

		got, _ := drain(events)
		if len(got) == 0 {
			t.Fatal("no event")
		}

		for _, ev := range got {
			if strings.Contains(ev.Reason, dir) || strings.Contains(ev.Reason, canaryDir) {
				t.Errorf("the reason has the path in it: %q", ev.Reason)
			}
		}
	})
}

func TestALabelThatIsNotOneStopsTheWatch(t *testing.T) {
	long := strings.Repeat("a", watch.MaxLabel+1)

	tests := map[string]string{
		"empty":             "",
		"too long":          long,
		"a newline":         "$A\nforged: line",
		"a carriage return": "$A\rforged",
		"an escape":         "$A\x1b[2J",
		"NUL":               "$A\x00",
		"DEL":               "$A\x7f",
		"invalid UTF-8":     "$A\xff",
		"a bidi override":   "$A\u202e",
		"a line separator":  "$A ",
	}

	dir := t.TempDir()
	file := filepath.Join(dir, "svc.json")

	write(t, file, "x")

	for name, label := range tests {
		t.Run(name, func(t *testing.T) {
			for what, tr := range map[string]watch.Trigger{
				"Files": watch.Files([]string{file}, watch.WithLabel(label)),
				"Dir":   watch.Dir(dir, watch.WithLabel(label)),
			} {
				events, err := tr.Watch(t.Context())
				if err == nil {
					t.Errorf("%s: a label of %q was accepted", what, label)

					continue
				}

				if events != nil {
					t.Errorf("%s: a channel came back with the error", what)
				}

				// The reason it is wrong is given without the label, which may be
				// what was wrong.
				if label != "" && strings.Contains(err.Error(), label) {
					t.Errorf("%s: the error repeats the label: %v", what, err)
				}

				if len(err.Error()) > 200 {
					t.Errorf("%s: an error of %d bytes", what, len(err.Error()))
				}

				for _, r := range err.Error() {
					if unicode.IsControl(r) {
						t.Errorf("%s: the error contains %q", what, r)
					}
				}
			}
		})
	}
}

func TestALabelOfTheLongestAllowedLengthIsAccepted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "svc.json")

		write(t, path, "one")

		label := strings.Repeat("a", watch.MaxLabel)

		events, cancel := start(t, watch.Files([]string{path}, watch.WithLabel(label)))
		defer cancel()

		write(t, path, "two")
		setTime(t, path, later)

		settle(2 * time.Second)

		got, _ := drain(events)
		if len(got) != 1 || !strings.Contains(got[0].Reason, label) {
			t.Errorf("got %+v", got)
		}

		if len(got[0].Reason) > watch.MaxReason {
			t.Errorf("a reason of %d bytes, the limit is %d", len(got[0].Reason), watch.MaxReason)
		}
	})
}

// One label names one thing. With several paths it could not say which changed,
// so it is refused rather than made to mean something else.
func TestALabelNeedsExactlyOnePath(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")

	write(t, a, "x")
	write(t, b, "x")

	if _, err := watch.Files([]string{a, b}, watch.WithLabel("$SVC_CONFIG")).Watch(t.Context()); err == nil {
		t.Error("a label for two paths was accepted")
	}
}

// The errors of a watch that has a label say nothing of the path either.
func TestAnErrorDoesNotRepeatThePathOfALabelledWatch(t *testing.T) {
	file := filepath.Join(t.TempDir(), canaryDir)

	write(t, file, "x")

	tests := map[string]watch.Trigger{
		"a directory that is a file": watch.Dir(file, watch.WithLabel("$SVC_CONFIG_DIR")),
		"a NUL in the path":          watch.Files([]string{file + "\x00"}, watch.WithLabel("$SVC_CONFIG")),
		"an interval that is wrong":  watch.Files([]string{file}, watch.WithLabel("$SVC_CONFIG"), watch.WithInterval(time.Nanosecond)),
	}

	for name, tr := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := tr.Watch(t.Context())
			if err == nil {
				t.Fatal("expected an error")
			}

			if strings.Contains(err.Error(), canaryDir) {
				t.Errorf("the error has the path in it: %v", err)
			}
		})
	}
}

// Whatever label is given, the watch either refuses it or only ever shows it as
// safe text: the checks that keep a reason fit for a log hold for the label too.
func FuzzLabel(f *testing.F) {
	for _, seed := range []string{"", "$SVC_CONFIG", "a\nb", "\x1b[0m", "\xff", "é", strings.Repeat("x", 129), "\u202e", "a b"} {
		f.Add(seed)
	}

	path := filepath.Join(f.TempDir(), "svc.json")

	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, label string) {
		ctx := t.Context()

		events, err := watch.Files([]string{path}, watch.WithLabel(label)).Watch(ctx)
		if err != nil {
			if strings.Contains(err.Error(), canaryDir) {
				t.Fatalf("the error has a path in it: %v", err)
			}

			return
		}

		_ = events

		if label == "" || len(label) > watch.MaxLabel || !utf8.ValidString(label) {
			t.Fatalf("a label of %q was accepted", label)
		}

		for _, r := range label {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
				t.Fatalf("a label with %q in it was accepted", r)
			}
		}
	})
}

// Which signals mean shut down, and which restart, is the program's decision, made
// through package interrupt. This package says when something changed, and has no
// opinion about signals: it neither exports a trigger for them nor reaches for the
// package that handles them, so nothing in it can name SIGINT or SIGTERM, and there
// is no second way to ask for a shutdown that skips interrupt's rules.
func TestWatchHasNothingToDoWithSignals(t *testing.T) {
	for name, file := range sources(t, 0) {
		for _, imp := range file.Imports {
			if imp.Path.Value == `"os/signal"` {
				t.Errorf("%s imports os/signal", name)
			}
		}

		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncDecl:
				if n.Recv == nil && strings.Contains(strings.ToLower(n.Name.Name), "signal") {
					t.Errorf("%s declares %s", name, n.Name.Name)
				}
			case *ast.SelectorExpr:
				if id, ok := n.X.(*ast.Ident); ok && id.Name == "syscall" && (n.Sel.Name == "SIGINT" || n.Sel.Name == "SIGTERM") {
					t.Errorf("%s names syscall.%s", name, n.Sel.Name)
				}
			}

			return true
		})
	}

	if _, err := os.Stat("../internal/signalhook"); err == nil {
		t.Error("internal/signalhook should not exist: there is nothing in watch to bypass")
	}
}

// The package knows nothing of the rest of the framework: it imports the standard
// library and nothing else, so it can be understood, and a trigger written, without
// reading anything outside it. In particular it does not reach into the packages
// that read configuration to learn what a configuration file is.
func TestWatchImportsOnlyTheStandardLibrary(t *testing.T) {
	for name, file := range sources(t, parser.ImportsOnly) {
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)

			// The standard library has no dot in the first element of a path.
			if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
				t.Errorf("%s imports %s", name, path)
			}
		}
	}
}

// sources parses the files of the package that are not tests, whatever their build
// constraints (the package is checked for every platform at once).
func sources(t *testing.T, mode parser.Mode) map[string]*ast.File {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	fset := token.NewFileSet()
	files := map[string]*ast.File{}

	for _, e := range entries {
		name := e.Name()

		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, mode)
		if err != nil {
			t.Fatal(err)
		}

		files[name] = file
	}

	if len(files) == 0 {
		t.Fatal("no source files found")
	}

	return files
}

// Most tests run in a synctest bubble: the polling interval is on the fake clock,
// so a test lets seconds pass at once, while the files are real. Times of files
// are set explicitly, so what changed is never down to the clock of the machine.

// epoch is the time files are given, and later the time they are changed to.
var (
	epoch = time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	later = epoch.Add(time.Hour)
)

// start watches with tr until the returned function is called, and fails the test
// if the watch can't start. Every bubble must cancel before it ends: a goroutine
// that doesn't stop is reported as a deadlock, which is the leak check.
func start(t *testing.T, tr watch.Trigger) (<-chan watch.Event, context.CancelFunc) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	events, err := tr.Watch(ctx)
	if err != nil {
		cancel()
		t.Fatalf("Watch: %v", err)
	}

	return events, cancel
}

// settle lets d pass on the fake clock with every goroutine in the bubble run.
func settle(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

// drain returns what is waiting on the channel, without waiting, and whether it
// has been closed.
func drain(ch <-chan watch.Event) (events []watch.Event, closed bool) {
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events, true
			}

			events = append(events, ev)
		default:
			return events, false
		}
	}
}

// write writes a file of 0600 and gives it the time epoch.
func write(t *testing.T, path, data string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	setTime(t, path, epoch)
}

func setTime(t *testing.T, path string, mtime time.Time) {
	t.Helper()

	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// plain asserts that what a reason says is safe to log: bounded, UTF-8, and no
// control characters.
func plain(t *testing.T, ev watch.Event) {
	t.Helper()

	if len(ev.Reason) > watch.MaxReason {
		t.Errorf("a reason of %d bytes, the limit is %d", len(ev.Reason), watch.MaxReason)
	}

	if !utf8.ValidString(ev.Reason) {
		t.Errorf("reason %q is not UTF-8", ev.Reason)
	}

	for _, r := range ev.Reason {
		if r < 0x20 || r == 0x7f {
			t.Errorf("reason %q has a control character", ev.Reason)

			break
		}
	}
}

// kinds returns the kinds of the events.
func kinds(events []watch.Event) []watch.Kind {
	out := make([]watch.Kind, len(events))

	for i, ev := range events {
		out[i] = ev.Kind
	}

	return out
}
