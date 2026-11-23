package foundation_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"unicode"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/foundationtest"
)

// A provider that is given config.WithRestartOnChange says that the program is to
// restart when what it reads changes, and the framework does the rest: it starts
// the watch before the root function runs (so before the first Load, and nothing
// between reading a file and watching it can be missed), a change goes through the
// restart machinery (so a bad edit is rejected by the candidate and the instance
// that is running is left alone), and nothing about the files is trusted: they are
// looked at, never read, until the new instance loads them. The program calls
// nothing, and a provider that didn't ask is never watched.
//
// Files are real and the clock is the fake one of a synctest bubble, so the
// time between an edit and the program noticing is exact. An edit sets the time
// of the file, since a rewrite in the same second could look like no change.

// cfgDoc is what the programs below read.
type cfgDoc struct {
	Addr string `config:"addr"`
}

// canaryPath is a name that must not appear anywhere it should not.
const canaryPath = "canary-secret-config-path-9d2e"

// editedAt is the time version n of a file is given.
func editedAt(n int) time.Time {
	return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC).Add(time.Duration(n) * time.Hour)
}

// edit writes the file and gives it the time of version n. It is safe from any
// goroutine of the test.
func edit(t *testing.T, path, data string, n int) {
	t.Helper()

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Error(err)

		return
	}

	if err := os.Chtimes(path, editedAt(n), editedAt(n)); err != nil {
		t.Error(err)
	}
}

// editAfter edits the file once d has passed on the fake clock. It is a little
// later than d: a watch polls on whole seconds, and an edit made at the very
// instant of a poll can be seen half done (written, not yet stamped), which is two
// changes and so two restarts.
func editAfter(t *testing.T, d time.Duration, path, data string, n int) {
	t.Helper()

	go func() {
		time.Sleep(d + time.Millisecond)

		edit(t, path, data, n)
	}()
}

func addrDoc(addr string) string { return `{"svc": {"addr": "` + addr + `"}}` }

// cfgProgram is a program that loads configuration, and records what each instance
// read. A candidate is a dry run, which loads and checks and then stops, as the
// contract says, and is counted on its own.
type cfgProgram struct {
	mu         sync.Mutex
	seen       []string
	candidates atomic.Int32
}

func (c *cfgProgram) fn(body func(f *foundation.F, n int, cfg cfgDoc)) foundation.Func {
	return func(f *foundation.F) {
		var cfg cfgDoc

		f.Load(&cfg)

		if f.DryRun() {
			c.candidates.Add(1)

			return
		}

		c.mu.Lock()
		c.seen = append(c.seen, cfg.Addr)
		n := len(c.seen)
		c.mu.Unlock()

		body(f, n, cfg)
	}
}

func (c *cfgProgram) read() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.seen...)
}

func equalStrings(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }

// asked is the option that gives the provider a restart on change.
var asked = config.WithRestartOnChange()

// --- what a change does ---

func TestAnEditToAFileThatAsksToRestartRestartsTheProgram(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		path := filepath.Join(t.TempDir(), "svc.json")

		edit(t, path, addrDoc(":1"), 1)
		editAfter(t, 5*time.Second, path, addrDoc(":2"), 2)

		var c cfgProgram

		foundation.Run("svc", c.fn(func(f *foundation.F, n int, _ cfgDoc) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			until(f)
		}), quick(foundation.RestartPolicy{}), foundation.WithDocumentConfig(json.File(path, asked)))

		if got := c.read(); !equalStrings(got, []string{":1", ":2"}) {
			t.Fatalf("the instances read %v, want [:1 :2]", got)
		}

		assertExitCodes(t, exit)

		restarting := entriesNamed(recorder, "restarting")
		if len(restarting) != 1 || restarting[0].attrs["reason"] != "file changed: "+path {
			t.Errorf("want one restart that names the file, got %+v", restarting)
		}
	})
}

// Not asking is not being watched, whatever else the provider is told.
func TestAFileThatDidNotAskIsNotWatched(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		path := filepath.Join(t.TempDir(), "svc.json")

		edit(t, path, addrDoc(":1"), 1)
		editAfter(t, 5*time.Second, path, addrDoc(":2"), 2)

		var c cfgProgram

		foundation.Run("svc", c.fn(func(f *foundation.F, n int, _ cfgDoc) {
			time.Sleep(time.Minute)

			f.Shutdown("done")
		}), quick(foundation.RestartPolicy{}), foundation.WithDocumentConfig(json.File(path, config.WithRequired())))

		if got := c.read(); !equalStrings(got, []string{":1"}) {
			t.Fatalf("the instances read %v, want one instance", got)
		}

		assertExitCodes(t, exit)

		for _, msg := range []string{"restarting", "watching configuration", "no configuration to watch"} {
			if got := entriesNamed(recorder, msg); len(got) != 0 {
				t.Errorf("%q was logged: %+v", msg, got)
			}
		}
	})
}

// A bad edit is a mistake in a file, not a reason to stop what is running: the
// candidate loads it, finds the problem and the instance that is running is left
// alone, with one error saying why. The next edit gets its own turn.
func TestABadEditIsRejectedAndTheInstanceKeepsRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		path := filepath.Join(t.TempDir(), "svc.json")

		edit(t, path, addrDoc(":1"), 1)
		editAfter(t, 5*time.Second, path, `{"svc": `, 2)
		editAfter(t, 30*time.Second, path, addrDoc(":3"), 3)

		var (
			c        cfgProgram
			rejected int
			alive    error
		)

		foundation.Run("svc", c.fn(func(f *foundation.F, n int, _ cfgDoc) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			// Well after the bad edit and before the fix.
			time.Sleep(20 * time.Second)

			rejected = len(entriesNamed(recorder, "restart rejected"))
			alive = f.Context().Err()

			until(f)
		}), quick(foundation.RestartPolicy{}), foundation.WithDocumentConfig(json.File(path, asked)))

		if rejected != 1 {
			t.Errorf("%d rejections before the fix, want 1", rejected)
		}

		if alive != nil {
			t.Errorf("the instance was stopped by a bad edit: %v", alive)
		}

		if got := c.read(); !equalStrings(got, []string{":1", ":3"}) {
			t.Fatalf("the instances read %v, want [:1 :3]: the fix is a restart", got)
		}

		assertExitCodes(t, exit)

		if got := entriesNamed(recorder, "restarting"); len(got) != 1 {
			t.Errorf("%d restarts, want 1", len(got))
		}
	})
}

// A file that did not exist is a state like any other: it appearing is a change,
// and the next instance reads it.
func TestAnOptionalFileThatAppearsRestartsTheProgram(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		path := filepath.Join(t.TempDir(), "svc.json")

		editAfter(t, 5*time.Second, path, addrDoc(":2"), 2)

		var c cfgProgram

		foundation.Run("svc", c.fn(func(f *foundation.F, n int, _ cfgDoc) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			until(f)
		}), quick(foundation.RestartPolicy{}), foundation.WithDocumentConfig(json.File(path, asked)))

		if got := c.read(); !equalStrings(got, []string{"", ":2"}) {
			t.Fatalf("the instances read %q, want [\"\" \":2\"]", got)
		}

		assertExitCodes(t, exit)
	})
}

// A file that must exist and is removed is a change as well; the candidate loads
// without it, finds it missing and the instance is left alone.
func TestARequiredFileThatIsRemovedIsRejected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		path := filepath.Join(t.TempDir(), "svc.json")

		edit(t, path, addrDoc(":1"), 1)

		go func() {
			// Not at a whole second, when the poller also looks: which came first
			// would be luck.
			time.Sleep(5*time.Second + time.Millisecond)

			if err := os.Remove(path); err != nil {
				t.Error(err)
			}
		}()

		var c cfgProgram

		foundation.Run("svc", c.fn(func(f *foundation.F, n int, _ cfgDoc) {
			time.Sleep(30 * time.Second)

			f.Shutdown("done")
		}), quick(foundation.RestartPolicy{}), foundation.WithDocumentConfig(json.File(path, asked, config.WithRequired())))

		if got := c.read(); len(got) != 1 {
			t.Fatalf("the instances read %v, want only the first", got)
		}

		assertExitCodes(t, exit)

		if got := entriesNamed(recorder, "restart rejected"); len(got) != 1 {
			t.Errorf("%d rejections, want 1", len(got))
		}
	})
}

// --- directories ---

func TestADirectoryIsWatchedForTheDocumentsItReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		dir := t.TempDir()
		a := filepath.Join(dir, "a.json")

		edit(t, a, addrDoc(":1"), 1)

		// What editors and operators leave in a directory of configuration is not
		// configuration: none of it is read, so none of it is a reason to restart,
		// and a name made to forge a line is one of the things that is not read.
		go func() {
			time.Sleep(5 * time.Second)

			for i, name := range []string{
				"a.json~", "#a.json#", ".a.json.swp", ".hidden.json", "README", "notes.txt",
				"x.JSON", "a\nforged: line.json", "a\x1b[2J.json", "a\u202e.json",
			} {
				edit(t, filepath.Join(dir, name), addrDoc(":bad"), 10+i)
			}
		}()

		var (
			c            cfgProgram
			restartsHere int
		)

		foundation.Run("svc", c.fn(func(f *foundation.F, n int, _ cfgDoc) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			time.Sleep(30 * time.Second)

			restartsHere = len(entriesNamed(recorder, "restarting"))

			// Now a document, which is.
			edit(t, a, addrDoc(":2"), 2)

			until(f)
		}), quick(foundation.RestartPolicy{}), foundation.WithDocumentConfig(json.Dir(dir, asked)))

		if restartsHere != 0 {
			t.Errorf("%d restarts for files that are not documents", restartsHere)
		}

		if got := c.read(); !equalStrings(got, []string{":1", ":2"}) {
			t.Fatalf("the instances read %v, want [:1 :2]", got)
		}

		assertExitCodes(t, exit)

		restarting := entriesNamed(recorder, "restarting")
		if len(restarting) != 1 || !strings.HasSuffix(restarting[0].attrs["reason"], "(a.json)") {
			t.Errorf("want one restart naming a.json, got %+v", restarting)
		}
	})
}

// --- a change at the wrong moment ---

// The watch is started before the root function runs, so a file that changes
// before the first Load is not missed: it is seen, however it was done, and the
// program restarts to be sure it is running on what is there. Were the watch
// started after the load, the change would be in its baseline and nothing would
// ever say that the file was read before it was written.
func TestAChangeBeforeTheFirstLoadIsNotMissed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		path := filepath.Join(t.TempDir(), "svc.json")

		edit(t, path, addrDoc(":1"), 1)

		var (
			instances atomic.Int32
			edited    atomic.Bool
		)

		foundation.Run("svc", func(f *foundation.F) {
			if f.DryRun() {
				return
			}

			n := instances.Add(1)

			// The first thing the program does, before it loads anything.
			if edited.CompareAndSwap(false, true) {
				edit(t, path, addrDoc(":2"), 2)
			}

			var cfg cfgDoc

			f.Load(&cfg)

			if n > 1 {
				f.Shutdown("done")

				return
			}

			select {
			case <-f.Context().Done():
			case <-time.After(time.Minute):
				f.Shutdown("the change before the load was missed")
			}
		}, quick(foundation.RestartPolicy{}), foundation.WithDocumentConfig(json.File(path, asked)))

		if got := instances.Load(); got != 2 {
			t.Fatalf("%d instances ran, want 2: a change before the first load was missed", got)
		}

		assertExitCodes(t, exit)
	})
}

// --- abuse ---

// Whoever can write the file can change it as often as they like. The restart
// policy is what bounds that, as it does for any source: only Limit restarts begin
// in a window, a candidate that is run counts as an attempt whether or not it
// passes, and the one that is due when the limit is reached is held, not dropped.
func TestAFloodOfEditsIsBoundedByTheRestartPolicy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		path := filepath.Join(t.TempDir(), "svc.json")

		edit(t, path, addrDoc(":0"), 1)

		go func() {
			for i := 2; i < 70; i++ {
				time.Sleep(300 * time.Millisecond)

				edit(t, path, addrDoc(fmt.Sprintf(":%d", i)), i)
			}
		}()

		deadline := time.Now().Add(45 * time.Second)

		var c cfgProgram

		foundation.Run("svc", c.fn(func(f *foundation.F, n int, _ cfgDoc) {
			select {
			case <-f.Context().Done():
			case <-time.After(time.Until(deadline)):
				f.Shutdown("done")
			}
		}), quick(foundation.RestartPolicy{Limit: 3}), foundation.WithDocumentConfig(json.File(path, asked)))

		assertExitCodes(t, exit)

		if got := len(entriesNamed(recorder, "restarting")); got > 3 {
			t.Errorf("%d restarts began in a window with a limit of 3", got)
		}

		if got := c.candidates.Load(); got > 3 {
			t.Errorf("%d candidates were run in a window with a limit of 3", got)
		}

		if got := len(entriesNamed(recorder, "restart held")); got == 0 {
			t.Error("a restart that was due at the limit should have been held, with a log saying so")
		}
	})
}

// --- what is said, and what is not ---

// The path of a file that came from the environment is a configuration value. It
// is named by the variable, in every reason and every log line, and a canary in the
// path is looked for in all of them.
func TestAnEnvironmentPathIsNeverLogged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), canaryPath)

	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "svc.json")

	t.Setenv("SVC_CONFIG", path)

	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		edit(t, path, addrDoc(":1"), 1)
		editAfter(t, 5*time.Second, path, addrDoc(":2"), 2)

		var c cfgProgram

		foundation.Run("svc", c.fn(func(f *foundation.F, n int, _ cfgDoc) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			until(f)
		}), quick(foundation.RestartPolicy{}), foundation.WithDocumentConfig(json.FileFromEnv("SVC_CONFIG", asked)))

		if got := c.read(); !equalStrings(got, []string{":1", ":2"}) {
			t.Fatalf("the instances read %v, want [:1 :2]", got)
		}

		assertExitCodes(t, exit)

		for _, l := range loggedEntries(recorder) {
			if strings.Contains(l.msg, canaryPath) {
				t.Errorf("a message has the path in it: %q", l.msg)
			}

			for k, v := range l.attrs {
				if strings.Contains(v, canaryPath) || strings.Contains(k, canaryPath) {
					t.Errorf("%q has the path in %s=%q", l.msg, k, v)
				}
			}
		}

		restarting := entriesNamed(recorder, "restarting")
		if len(restarting) != 1 || restarting[0].attrs["reason"] != "file changed: $SVC_CONFIG" {
			t.Errorf("want the restart to name the variable, got %+v", restarting)
		}

		started := entriesNamed(recorder, "watching configuration")
		if len(started) == 0 || !strings.Contains(started[0].attrs["names"], "$SVC_CONFIG") {
			t.Errorf("want the start of the watch to name the variable, got %+v", started)
		}
	})
}

// Saying what is watched is an Info line, once an instance: how many, and the names
// they are known by.
func TestTheWatchSaysWhatItIsWatching(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		dir := t.TempDir()
		file := filepath.Join(dir, "svc.json")

		foundation.Run("svc", func(f *foundation.F) {
			f.Shutdown("done")
		}, foundation.WithDocumentConfig(json.File(file, asked), json.Dir(dir, asked), json.File(file+"x")))

		started := entriesNamed(recorder, "watching configuration")
		if len(started) != 1 {
			t.Fatalf("want one line, got %+v", started)
		}

		if started[0].attrs["sources"] != "2" {
			t.Errorf("sources = %q, want 2: only the providers that asked", started[0].attrs["sources"])
		}
	})
}

// A provider that asked, and has nothing to watch because the variable that names
// its file isn't set, is not a mistake: there is just no file. Nothing is started,
// and the log says so once.
func TestAProviderWithNothingToWatchStartsNoWatch(t *testing.T) {
	t.Setenv("SVC_CONFIG", "")

	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		var c cfgProgram

		foundation.Run("svc", c.fn(func(f *foundation.F, n int, _ cfgDoc) {
			f.Shutdown("done")
		}), foundation.WithDocumentConfig(json.FileFromEnv("SVC_CONFIG", asked)))

		assertExitCodes(t, exit)

		if got := entriesNamed(recorder, "no configuration to watch"); len(got) != 1 || got[0].level.String() != "INFO" {
			t.Errorf("want one info line, got %+v", got)
		}

		if got := entriesNamed(recorder, "watching configuration"); len(got) != 0 {
			t.Errorf("a watch was started: %+v", got)
		}
	})
}

// --- where it is not started ---

func TestNothingIsWatchedInADryRunOrHelp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "svc.json")

	edit(t, path, addrDoc(":1"), 1)

	t.Run("dry run", func(t *testing.T) {
		codes, recorder := dryRun(t, func(f *foundation.F) {
			var cfg cfgDoc

			f.Load(&cfg)
		}, foundation.WithDocumentConfig(json.File(path, asked)))

		if len(codes) != 0 {
			t.Errorf("exit codes %v", codes)
		}

		if got := entriesNamed(recorder, "watching configuration"); len(got) != 0 {
			t.Errorf("a watch was started in a dry run: %+v", got)
		}
	})

	t.Run("help", func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		var out bytes.Buffer

		foundation.Run("svc", func(f *foundation.F) {
			var cfg cfgDoc

			f.Load(&cfg)
		}, foundation.WithDocumentConfig(json.File(path, asked)), foundation.WithArgs([]string{"--help"}), foundation.WithHelpOutput(&out))

		assertExitCodes(t, exit)

		if got := entriesNamed(recorder, "watching configuration"); len(got) != 0 {
			t.Errorf("a watch was started for help: %+v", got)
		}
	})
}

// A candidate is a dry run beside the live instance, so it starts no watch of its
// own: one for each instance that runs, and none for the checks.
func TestACandidateStartsNoWatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		recorder := foundationtest.NewLogRecorder()
		recorder.Install(t)

		path := filepath.Join(t.TempDir(), "svc.json")

		edit(t, path, addrDoc(":1"), 1)
		editAfter(t, 5*time.Second, path, addrDoc(":2"), 2)

		var c cfgProgram

		foundation.Run("svc", c.fn(func(f *foundation.F, n int, _ cfgDoc) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			until(f)
		}), quick(foundation.RestartPolicy{}), foundation.WithDocumentConfig(json.File(path, asked)))

		assertExitCodes(t, exit)

		if got := c.candidates.Load(); got != 1 {
			t.Fatalf("%d candidates ran, want 1", got)
		}

		if got := len(entriesNamed(recorder, "watching configuration")); got != 2 {
			t.Errorf("%d watches were started for 2 instances", got)
		}
	})
}

// A program that is shut down while it watches ends cleanly: the watch is stopped
// with the rest, and the bubble ending is the check that nothing was left running.
func TestAShutdownWhileWatchingEndsCleanly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		dir := t.TempDir()

		foundation.Run("svc", func(f *foundation.F) {
			f.Run("later", func(f *foundation.F) {
				f.Parallel()

				time.Sleep(10 * time.Second)

				f.Shutdown("done")
			})
		}, foundation.WithDocumentConfig(json.Dir(dir, asked), json.File(filepath.Join(dir, "a.json"), asked)))

		assertExitCodes(t, exit)
	})
}

// --- the stack ---

// The explicit whole stack watches the providers that asked too, and what is not a
// file (bytes in memory) is part of it without a word.
func TestAnExplicitStackIsWatchedToo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		path := filepath.Join(t.TempDir(), "svc.json")

		edit(t, path, addrDoc(":1"), 1)
		editAfter(t, 5*time.Second, path, addrDoc(":2"), 2)

		var c cfgProgram

		foundation.Run("svc", c.fn(func(f *foundation.F, n int, _ cfgDoc) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			until(f)
		}), quick(foundation.RestartPolicy{}), foundation.WithConfigProviders(json.Raw([]byte(`{}`)), json.File(path, asked)))

		if got := c.read(); !equalStrings(got, []string{":1", ":2"}) {
			t.Fatalf("the instances read %v, want [:1 :2]", got)
		}

		assertExitCodes(t, exit)
	})
}

func TestADocumentThatIsNotAFileDoesNotGetInTheWay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		path := filepath.Join(t.TempDir(), "svc.json")

		edit(t, path, addrDoc(":1"), 1)
		editAfter(t, 5*time.Second, path, addrDoc(":2"), 2)

		var c cfgProgram

		foundation.Run("svc", c.fn(func(f *foundation.F, n int, _ cfgDoc) {
			if n > 1 {
				f.Shutdown("done")

				return
			}

			until(f)
		}), quick(foundation.RestartPolicy{}), foundation.WithDocumentConfig(json.Raw([]byte(`{"svc": {"addr": ":0"}}`)), json.File(path, asked)))

		if got := c.read(); !equalStrings(got, []string{":1", ":2"}) {
			t.Fatalf("the instances read %v, want [:1 :2]", got)
		}

		assertExitCodes(t, exit)
	})
}

// --- mistakes stop startup ---

// restartDoc is a document provider written the way a third party would write one,
// that says what it reads as it likes: the framework takes nothing it says on trust.
type restartDoc struct {
	sources []config.Source
	panics  bool
}

func (restartDoc) DocumentLayer()                {}
func (restartDoc) Unmarshal([]string, any) error { return nil }

func (d restartDoc) RestartSources() []config.Source {
	if d.panics {
		panic("canary panic value " + canaryPath)
	}

	return d.sources
}

// startup runs a program with the providers and says whether it started, how it
// ended, and what was said when it didn't.
func startup(t *testing.T, providers []config.DocumentLayer, opts ...foundation.Option) (started bool, codes []int, failure string, all []logged) {
	t.Helper()

	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("svc", func(f *foundation.F) {
		started = true
	}, append([]foundation.Option{foundation.WithDocumentConfig(providers...)}, opts...)...)

	all = loggedEntries(recorder)

	for _, l := range all {
		if l.msg == "shutting down due to failure" {
			failure = l.attrs["err"]
		}
	}

	return started, exit.Codes(), failure, all
}

func TestWhatAProviderSaysItReadsIsCheckedBeforeAnythingStarts(t *testing.T) {
	many := make([]config.Source, 65)
	for i := range many {
		many[i] = config.Source{Path: fmt.Sprintf("/etc/%s-%d.json", canaryPath, i)}
	}

	longPath := "/" + canaryPath + strings.Repeat("a", 4100)

	tests := map[string]restartDoc{
		"more sources than can be watched":    {sources: many},
		"an empty path":                       {sources: []config.Source{{Path: ""}}},
		"a NUL in the path":                   {sources: []config.Source{{Path: "/etc/" + canaryPath + "\x00.json"}}},
		"a path that is too long":             {sources: []config.Source{{Path: longPath}}},
		"a newline in the label":              {sources: []config.Source{{Path: "/etc/x.json", Label: "$A\nforged " + canaryPath}}},
		"an escape in the label":              {sources: []config.Source{{Path: "/etc/x.json", Label: "\x1b[2J" + canaryPath}}},
		"a label that is too long":            {sources: []config.Source{{Path: "/etc/x.json", Label: canaryPath + strings.Repeat("a", 200)}}},
		"a directory with no extensions":      {sources: []config.Source{{Path: "/etc/" + canaryPath, Dir: true}}},
		"an extension with no dot":            {sources: []config.Source{{Path: "/etc/" + canaryPath, Dir: true, Extensions: []string{"json"}}}},
		"an extension that is only a dot":     {sources: []config.Source{{Path: "/etc/" + canaryPath, Dir: true, Extensions: []string{"."}}}},
		"a control character in an extension": {sources: []config.Source{{Path: "/etc/" + canaryPath, Dir: true, Extensions: []string{".js\non"}}}},
		"an extension that is too long":       {sources: []config.Source{{Path: "/etc/" + canaryPath, Dir: true, Extensions: []string{"." + strings.Repeat("a", 40)}}}},
		"too many extensions":                 {sources: []config.Source{{Path: "/etc/" + canaryPath, Dir: true, Extensions: []string{".a", ".b", ".c", ".d", ".e", ".f", ".g", ".h", ".i"}}}},
		"a provider that panics":              {panics: true},
	}

	for name, doc := range tests {
		for mode, opts := range map[string][]foundation.Option{
			"a run":     nil,
			"a dry run": {foundation.WithDryRun(true)},
		} {
			t.Run(name+" in "+mode, func(t *testing.T) {
				started, codes, failure, all := startup(t, []config.DocumentLayer{doc}, opts...)

				if started {
					t.Error("the root function ran")
				}

				if len(codes) != 1 || codes[0] != 1 {
					t.Errorf("exit codes = %v, want [1]", codes)
				}

				if !strings.Contains(failure, "invalid configuration options") {
					t.Errorf("the failure is %q", failure)
				}

				if len(failure) > 300 {
					t.Errorf("a failure of %d bytes", len(failure))
				}

				for _, r := range failure {
					if unicode.IsControl(r) {
						t.Errorf("the failure contains %q", r)
					}
				}

				for _, l := range all {
					if strings.Contains(l.msg, canaryPath) {
						t.Errorf("a message has what the provider said in it: %q", l.msg)
					}

					for k, v := range l.attrs {
						if strings.Contains(v, canaryPath) {
							t.Errorf("%q has what the provider said in %s=%q", l.msg, k, v)
						}
					}
				}
			})
		}
	}
}

// The limit is the limit: that many are fine.
func TestTheMostSourcesThatCanBeWatchedIsAccepted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exit := installExit(t)

		dir := t.TempDir()

		sources := make([]config.Source, 64)
		for i := range sources {
			sources[i] = config.Source{Path: filepath.Join(dir, fmt.Sprintf("%d.json", i))}
		}

		started := false

		foundation.Run("svc", func(f *foundation.F) {
			started = true

			f.Shutdown("done")
		}, foundation.WithDocumentConfig(restartDoc{sources: sources}))

		if !started {
			t.Error("the root function did not run")
		}

		assertExitCodes(t, exit)
	})
}

// --- Load ---

// A provider that asked to restart the program but is given only to Load is not in
// the stack, so nothing would watch it: that is refused instead of being quietly
// ignored.
func TestLoadRefusesAProviderThatAskedToRestartButIsNotInTheStack(t *testing.T) {
	path := filepath.Join(t.TempDir(), canaryPath+".json")

	edit(t, path, addrDoc(":1"), 1)

	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var finished bool

	foundation.Run("svc", func(f *foundation.F) {
		var cfg cfgDoc

		f.Load(&cfg, json.File(path, asked))

		finished = true
	})

	if finished {
		t.Error("Load carried on")
	}

	assertExitCodes(t, exit, 1)

	var failure string

	for _, l := range loggedEntries(recorder) {
		if l.msg == "shutting down due to failure" {
			failure = l.attrs["err"]
		}

		if strings.Contains(l.msg, canaryPath) {
			t.Errorf("a message has the path in it: %q", l.msg)
		}

		for k, v := range l.attrs {
			if strings.Contains(v, canaryPath) {
				t.Errorf("%q has the path in %s=%q", l.msg, k, v)
			}
		}
	}

	if !strings.Contains(failure, "restart") {
		t.Errorf("the failure should say it is about restarting: %q", failure)
	}
}

func TestLoadAcceptsAProviderThatIsInTheStack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "svc.json")

	edit(t, path, addrDoc(":1"), 1)

	exit := installExit(t)

	var got cfgDoc

	p := json.File(path, asked)

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&got, p)

		f.Shutdown("done")
	}, foundation.WithDocumentConfig(p))

	if got.Addr != ":1" {
		t.Errorf("the provider was not read: %+v", got)
	}

	assertExitCodes(t, exit)
}

func TestLoadAcceptsAProviderThatDidNotAsk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "svc.json")

	edit(t, path, addrDoc(":1"), 1)

	exit := installExit(t)

	var got cfgDoc

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(&got, json.File(path))
	})

	if got.Addr != ":1" {
		t.Errorf("the provider was not read: %+v", got)
	}

	assertExitCodes(t, exit)
}
