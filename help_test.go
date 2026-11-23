package foundation_test

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/flags"
)

// --help is a dry run that describes instead of reading: every function loads its
// configuration, but nothing is read, checked or started, and what the functions
// say about themselves is printed once at the end.

const helpDefault = "default-LEAK-3b1c"

type helpRoot struct {
	Addr string `env:"ADDR" json:"addr" flag:"addr" help:"Listen address."`
}

type helpAPI struct {
	Workers int `env:"WORKERS" json:"workers" flag:"workers,short=w" help:"Number of workers."`
}

type helpCache struct {
	Size int `flag:"size" help:"Entries."`
}

type helpServer struct {
	Port int `flag:"port,short=p" help:"Listen port."`
}

type helpMail struct {
	To string `flag:"to"`
}

// helpTree is a service with every kind of function: flags at the root, a nested
// function, a dotted name that makes an implied group, a function with no flags
// of its own above one that has, and one with nothing to show.
func helpTree(f *foundation.F) {
	f.Help("Runs the shop.\n\nSecond paragraph.")

	root := helpRoot{Addr: helpDefault}

	f.Load(&root)

	f.Run("api", func(f *foundation.F) {
		f.Help("Runs the API.\nMore detail follows.")

		f.Load(&helpAPI{Workers: 4})

		f.Run("cache", func(f *foundation.F) {
			f.Help("Cache settings.")

			f.Load(&helpCache{})
		})
	})

	f.Run("http.server", func(f *foundation.F) {
		f.Help("HTTP server.")

		f.Load(&helpServer{Port: 8080})
	})

	f.Run("jobs", func(f *foundation.F) {
		f.Help("Background jobs.")

		f.Run("mail", func(f *foundation.F) { f.Load(&helpMail{}) })
	})

	f.Run("internal", func(f *foundation.F) {
		f.Help("Not shown.")

		f.Load(&struct {
			X int `env:"X"`
		}{})
	})
}

// runHelp runs fn with the arguments, collecting what is printed.
func runHelp(t *testing.T, args []string, fn foundation.Func, opts ...foundation.Option) (string, argsRun) {
	t.Helper()

	var out bytes.Buffer

	res := runArgs(t, args, fn, append([]foundation.Option{foundation.WithHelpOutput(&out)}, opts...)...)

	return out.String(), res
}

// The rows of help are written here from the rules, so the tests say what the
// layout is: a flag is its indent, "-x, " or as many spaces, then --name and the
// type unless it is a bool; a function is "--name" indented by the markers above
// it; descriptions line up two spaces after the longest left side.
func flagRow(indent int, short, long, typ, desc string) [2]string {
	left := strings.Repeat(" ", indent)

	if short != "" {
		left += "-" + short + ", "
	} else {
		left += "    "
	}

	left += "--" + long

	if typ != "" {
		left += " " + typ
	}

	return [2]string{left, desc}
}

func fnRow(indent int, name, desc string) [2]string {
	return [2]string{strings.Repeat(" ", indent) + "--" + name, desc}
}

func table(rows ...[2]string) string {
	width := 0

	for _, r := range rows {
		width = max(width, len(r[0]))
	}

	var b strings.Builder

	for _, r := range rows {
		if r[1] == "" {
			b.WriteString(r[0] + "\n")

			continue
		}

		b.WriteString(r[0] + strings.Repeat(" ", width-len(r[0])+2) + r[1] + "\n")
	}

	return b.String()
}

func TestHelpPrintsTheTree(t *testing.T) {
	out, res := runHelp(t, []string{"--help"}, helpTree)

	if len(res.exit) != 0 {
		t.Fatalf("exit codes %v, problems %v", res.exit, argsProblems(res.rec))
	}

	want := "Runs the shop.\n\nSecond paragraph.\n\n" + table(
		flagRow(2, "h", "help", "", "Show this help"),
		flagRow(2, "", "dry-run", "", "Check configuration and start nothing"),
		flagRow(2, "", "addr", "string", "Listen address."),
		fnRow(0, "api", "Runs the API."),
		flagRow(4, "w", "workers", "int", "Number of workers."),
		fnRow(2, "cache", "Cache settings."),
		flagRow(6, "", "size", "int", "Entries."),
		fnRow(0, "http", ""),
		fnRow(2, "server", "HTTP server."),
		flagRow(6, "p", "port", "int", "Listen port."),
		fnRow(0, "jobs", "Background jobs."),
		fnRow(2, "mail", ""),
		flagRow(6, "", "to", "string", ""),
	)

	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestHelpNarrowsToAFunction(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			// The function's whole text, then what it has, and what is under it.
			"function", []string{"--api", "--help"},
			"Runs the API.\nMore detail follows.\n\n" + table(
				flagRow(2, "w", "workers", "int", "Number of workers."),
				fnRow(0, "cache", "Cache settings."),
				flagRow(4, "", "size", "int", "Entries."),
			),
		},
		{
			"short flag, after its flags", []string{"--api", "-w", "2", "-h"},
			"Runs the API.\nMore detail follows.\n\n" + table(
				flagRow(2, "w", "workers", "int", "Number of workers."),
				fnRow(0, "cache", "Cache settings."),
				flagRow(4, "", "size", "int", "Entries."),
			),
		},
		{
			"sub command", []string{"--api", "--cache", "--help"},
			"Cache settings.\n\n" + table(
				flagRow(2, "", "size", "int", "Entries."),
			),
		},
		{
			// http is not a function, only the first word of one, so it has no text.
			"implied group", []string{"--http", "--help"},
			table(
				fnRow(0, "server", "HTTP server."),
				flagRow(4, "p", "port", "int", "Listen port."),
			),
		},
		{
			"dotted name", []string{"--http", "--server", "--help"},
			"HTTP server.\n\n" + table(
				flagRow(2, "p", "port", "int", "Listen port."),
			),
		},
		{
			// A function with no flags of its own is shown for what is under it.
			"function with no flags", []string{"--jobs", "--help"},
			"Background jobs.\n\n" + table(
				fnRow(0, "mail", ""),
				flagRow(4, "", "to", "string", ""),
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, res := runHelp(t, tt.args, helpTree)

			if len(res.exit) != 0 {
				t.Fatalf("exit codes %v, problems %v", res.exit, argsProblems(res.rec))
			}

			if out != tt.want {
				t.Fatalf("got:\n%s\nwant:\n%s", out, tt.want)
			}
		})
	}
}

// -h and --help are one thing, and --dry-run, which is given first, can go with
// either.
func TestHelpSpellings(t *testing.T) {
	full, _ := runHelp(t, []string{"--help"}, helpTree)

	for _, args := range [][]string{{"-h"}, {"--dry-run", "--help"}, {"--help", "--dry-run"}, {"--dry-run", "-h"}} {
		out, res := runHelp(t, args, helpTree)

		if len(res.exit) != 0 || out != full {
			t.Errorf("%q: exit %v, output differs:\n%s", args, res.exit, out)
		}
	}
}

// Nothing starts: it is a dry run, so what would listen skips, and the process
// exits normally.
func TestHelpStartsNothing(t *testing.T) {
	var started, sawDryRun bool

	out, res := runHelp(t, []string{"--help"}, func(f *foundation.F) {
		sawDryRun = f.DryRun()

		f.Run("server", func(f *foundation.F) {
			f.Load(&helpServer{})

			if f.DryRun() {
				f.Skip("help")
			}

			started = true
		})
	})

	if started || !sawDryRun || len(res.exit) != 0 || !res.ran {
		t.Fatalf("started %v, DryRun() %v, exit %v, ran %v", started, sawDryRun, res.exit, res.ran)
	}

	if out == "" {
		t.Fatal("nothing was printed")
	}
}

// A function that doesn't skip is caught as it is in a dry run, rather than left
// running with the help printed over it.
func TestHelpFailsIfSomethingKeepsRunning(t *testing.T) {
	var buf bytes.Buffer

	hang := make(chan struct{})
	defer close(hang)

	res := runArgs(t, []string{"--help"}, func(f *foundation.F) {
		f.Run("server", func(f *foundation.F) {
			f.SetTimeout(50 * time.Millisecond)

			<-hang
		})
	}, foundation.WithHelpOutput(&buf), foundation.WithDryRunTimeout(100*time.Millisecond))

	if !slices.Equal(res.exit, []int{1}) || buf.Len() != 0 {
		t.Fatalf("exit %v, printed %q", res.exit, buf.String())
	}
}

// What the functions say is printed from their structs, and nothing is read: not
// the environment, not the file, and nothing is checked, so help works when the
// configuration is broken or empty, which is when it is most needed.
type helpStrict struct {
	Port int `env:"PORT" flag:"port" config:"required" help:"Listen port."`
}

func (helpStrict) Validate() error { return errors.New("never valid") }

func TestHelpReadsNoProvider(t *testing.T) {
	var called bool

	spy := config.ProviderFunc(func([]string, any) error {
		called = true

		panic("a provider was read")
	})

	var cfg helpRoot

	out, res := runHelp(t, []string{"--help"}, func(f *foundation.F) {
		cfg = helpRoot{Addr: helpDefault}

		f.Load(&cfg)
	}, foundation.WithConfigProviders(
		spy,
		envOf(map[string]string{"SVC_ADDR": canaryValue}),
		flags.New(flags.WithArgs([]string{"--help"})),
	))

	if called || len(res.exit) != 0 {
		t.Fatalf("called %v, exit %v, problems %v", called, res.exit, argsProblems(res.rec))
	}

	// What the providers hold is not what help says.
	if cfg.Addr != helpDefault {
		t.Fatalf("Addr = %q, want the default left alone", cfg.Addr)
	}

	assertNoValue(t, out+logText(res.rec), canaryValue, helpDefault)
}

func TestHelpWorksWithBrokenConfiguration(t *testing.T) {
	out, res := runHelp(t, []string{"--help"}, func(f *foundation.F) {
		// Required and not set, and a Validate that always fails.
		f.Load(&helpStrict{})
	}, foundation.WithConfigProviders(
		envOf(map[string]string{"SVC_PORT": "not-a-number"}),
		flags.New(flags.WithArgs([]string{"--help"})),
	), foundation.WithStrictConfig())

	if len(res.exit) != 0 || !strings.Contains(out, "--port int") {
		t.Fatalf("exit %v, problems %v, output:\n%s", res.exit, argsProblems(res.rec), out)
	}
}

// Defaults and values are never printed, whatever they are.
func TestHelpPrintsNoDefaultsAndNoValues(t *testing.T) {
	out, _ := runHelp(t, []string{"--help"}, helpTree)

	for _, leak := range []string{helpDefault, "8080", "default"} {
		if strings.Contains(out, leak) {
			t.Errorf("output contains %q:\n%s", leak, out)
		}
	}
}

// Code after Load still runs, with the defaults: help can't know which code was
// meant to start something. That is the dry run rule, and the reason to skip.
func TestHelpRunsCodeAfterLoadWithDefaults(t *testing.T) {
	var after string

	_, res := runHelp(t, []string{"--help"}, func(f *foundation.F) {
		cfg := helpRoot{Addr: helpDefault}

		f.Load(&cfg)

		after = cfg.Addr
	})

	if after != helpDefault || len(res.exit) != 0 {
		t.Fatalf("after = %q, exit %v", after, res.exit)
	}
}

// Mistakes in the arguments win over help: the same ones as in a real run, with
// nothing printed.
func TestArgumentMistakesWinOverHelp(t *testing.T) {
	tests := [][]string{
		{"--help", "--nope"},
		{"--nope", "--help"},
		{"--api", "--help", "--nope"},
		{"--api", "--workers=x", "--help"},
		{"--help", "stray"},
		{"--api", "--cache", "--cache", "--help"},
		{"--help", "--help"},
		{"--help=true"},
		{"-h=x"},
		{"--help", "--api", "--workers"},
	}

	for _, args := range tests {
		out, res := runHelp(t, args, helpTree)

		if !slices.Equal(res.exit, []int{1}) || out != "" {
			t.Errorf("%q: exit %v, printed %q", args, res.exit, out)
		}

		assertNoValue(t, logText(res.rec), "stray")
	}
}

// The words are reserved wherever they are given, so what --help means doesn't
// depend on where it is.
func TestAFunctionCannotClaimHelp(t *testing.T) {
	out, res := runHelp(t, []string{"--help"}, func(f *foundation.F) {
		f.Run("api", func(f *foundation.F) {
			f.Load(&struct {
				X bool `flag:"help"`
			}{})
		})
	})

	if !slices.Equal(res.exit, []int{1}) || out != "" {
		t.Fatalf("exit %v, printed %q", res.exit, out)
	}
}

// Help starts as a mode before anything runs, so it is not something a file or
// the environment can switch on or off.
func TestHelpComesOnlyFromTheArguments(t *testing.T) {
	var dry bool

	out, res := runHelp(t, nil, func(f *foundation.F) {
		dry = f.DryRun()

		f.Load(&helpRoot{})
	}, foundation.WithConfigProviders(
		envOf(map[string]string{"SVC_HELP": "true", "HELP": "true", "SVC_ADDR": ":1"}),
		flags.New(flags.WithArgs(nil)),
	))

	if dry || out != "" || len(res.exit) != 0 {
		t.Fatalf("DryRun() = %v, printed %q, exit %v", dry, out, res.exit)
	}
}

// With the providers chosen the arguments still say what the run is.
func TestHelpWorksWithChosenProviders(t *testing.T) {
	out, res := runHelp(t, []string{"--help"}, helpTree, foundation.WithConfigProviders(envOf(nil)))

	if len(res.exit) != 0 || !strings.Contains(out, "Runs the shop.") {
		t.Fatalf("exit %v, output %q", res.exit, out)
	}
}

// Quiet, so the output is only help, but a warning or an error is still logged.
func TestHelpLogsOnlyWarningsAndErrors(t *testing.T) {
	_, res := runHelp(t, []string{"--help"}, func(f *foundation.F) {
		f.Log().Info("hello")
		f.Log().Warn("careful")

		f.Run("a", func(f *foundation.F) { f.Skip("help") })
	})

	var warned bool

	for _, l := range loggedEntries(res.rec) {
		if l.level < slog.LevelWarn {
			t.Errorf("logged %q at %v", l.msg, l.level)
		}

		warned = warned || l.msg == "careful"
	}

	if !warned {
		t.Fatal("the warning was dropped")
	}
}

// Order is the order of f.Run, however the functions that run together get to
// their Load.
func TestHelpIsInTheOrderOfRun(t *testing.T) {
	delays := []time.Duration{30 * time.Millisecond, 0, 15 * time.Millisecond}
	names := []string{"slow", "quick", "middle"}

	out, res := runHelp(t, []string{"--help"}, func(f *foundation.F) {
		for i, name := range names {
			f.Run(name, func(f *foundation.F) {
				f.Parallel()

				time.Sleep(delays[i])

				f.Load(&helpMail{})
			})
		}
	})

	if len(res.exit) != 0 {
		t.Fatalf("exit %v, problems %v", res.exit, argsProblems(res.rec))
	}

	var got []string

	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "--") {
			got = append(got, strings.Fields(line)[0])
		}
	}

	if !slices.Equal(got, []string{"--slow", "--quick", "--middle"}) {
		t.Fatalf("got %q in:\n%s", got, out)
	}
}

// Outside help nothing about help is kept, checked or printed.
func TestHelpTextDoesNothingInANormalRun(t *testing.T) {
	var out bytes.Buffer

	res := runArgs(t, nil, func(f *foundation.F) {
		f.Help(strings.Repeat("x", 1<<20))
		f.Help("the last call wins")

		f.Load(&helpRoot{})
	}, foundation.WithHelpOutput(&out))

	if len(res.exit) != 0 || out.Len() != 0 {
		t.Fatalf("exit %v, printed %q", res.exit, out.String())
	}
}

func TestTheLastHelpCallWins(t *testing.T) {
	out, _ := runHelp(t, []string{"--help"}, func(f *foundation.F) {
		f.Help("first")
		f.Help("second")

		f.Load(&helpRoot{})
	})

	if !strings.HasPrefix(out, "second\n") || strings.Contains(out, "first") {
		t.Fatalf("got %q", out)
	}
}

// What is printed is escaped, whoever wrote it: a function from a dependency, a
// tag, a name. A terminal reading it must not be able to be driven by it, or
// lines forged.
type helpHostile struct {
	X string `flag:"x" help:"tag \x1b[2J\r\nforged: tag"`
}

func TestHelpIsEscaped(t *testing.T) {
	out, res := runHelp(t, []string{"--help"}, func(f *foundation.F) {
		f.Help("title \x1b]0;pwned\a\u202e\nsecond line \x00 \u0085")

		f.Load(&helpHostile{})

		f.Run("api", func(f *foundation.F) {
			f.Help("first \x1b[31mred\x1b[0m\rforged: line\nmore")

			f.Load(&helpAPI{})
		})
	})

	if len(res.exit) != 0 {
		t.Fatalf("exit %v, problems %v", res.exit, argsProblems(res.rec))
	}

	assertPrintable(t, out)

	for _, want := range []string{`\x1b`, `\r`, `red`} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// A newline in a tag or in the line of a function is one line, not two.
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "forged") {
			t.Errorf("a forged line: %q", line)
		}
	}
}

// Limits fail closed: help is for people to read, so a text too long to is a
// mistake in the program, said loudly, and not cut short.
func TestHelpTextIsBounded(t *testing.T) {
	const max = 16 << 10

	tests := []struct {
		name string
		n    int
		fail bool
	}{
		{"at the limit", max, false},
		{"over the limit", max + 1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, res := runHelp(t, []string{"--help"}, func(f *foundation.F) {
				f.Help(strings.Repeat("x", tt.n))

				f.Load(&helpRoot{})
			})

			if tt.fail {
				if !slices.Equal(res.exit, []int{1}) || out != "" {
					t.Fatalf("exit %v, printed %d bytes", res.exit, len(out))
				}

				return
			}

			if len(res.exit) != 0 || out == "" {
				t.Fatalf("exit %v, printed %d bytes", res.exit, len(out))
			}
		})
	}
}

func TestAHelpTagIsBoundedInTheRun(t *testing.T) {
	out, res := runHelp(t, []string{"--help"}, func(f *foundation.F) {
		f.Load(bigTag(1025))
	})

	if !slices.Equal(res.exit, []int{1}) || out != "" {
		t.Fatalf("exit %v, printed %d bytes", res.exit, len(out))
	}

	out, res = runHelp(t, []string{"--help"}, func(f *foundation.F) {
		f.Load(bigTag(1024))
	})

	if len(res.exit) != 0 || out == "" {
		t.Fatalf("exit %v, printed %d bytes", res.exit, len(out))
	}
}

// The whole of the output is bounded too, since a tree can be made of many
// functions that are each within bounds.
func TestHelpOutputIsBounded(t *testing.T) {
	line := strings.Repeat("y", 16<<10)

	out, res := runHelp(t, []string{"--help"}, func(f *foundation.F) {
		for i := range 100 {
			f.Run("f"+strconv.Itoa(1000+i), func(f *foundation.F) {
				f.Help(line)

				f.Load(&helpMail{})
			})
		}
	})

	if !slices.Equal(res.exit, []int{1}) || out != "" {
		t.Fatalf("exit %v, printed %d bytes", res.exit, len(out))
	}
}

// Help that couldn't be printed is not a success.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestHelpThatCannotBeWrittenFails(t *testing.T) {
	res := runArgs(t, []string{"--help"}, helpTree, foundation.WithHelpOutput(failingWriter{}))

	if !slices.Equal(res.exit, []int{1}) {
		t.Fatalf("exit %v", res.exit)
	}
}

// By default help goes to standard output, looked up when it is printed.
func TestHelpGoesToStandardOutputByDefault(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	old := os.Stdout
	os.Stdout = w

	t.Cleanup(func() { os.Stdout = old })

	res := runArgs(t, []string{"--help"}, helpTree)

	os.Stdout = old

	_ = w.Close()

	out, _ := io.ReadAll(r)

	if len(res.exit) != 0 || !strings.Contains(string(out), "Runs the shop.") {
		t.Fatalf("exit %v, standard output %q", res.exit, out)
	}
}

// Each function is a Load of its own, and they can run together.
func TestHelpWithManyFunctionsLoadingTogether(t *testing.T) {
	out, res := runHelp(t, []string{"--help"}, func(f *foundation.F) {
		for i := range 50 {
			f.Run("f"+strconv.Itoa(1000+i), func(f *foundation.F) {
				f.Parallel()

				f.Load(&helpMail{})
			})
		}
	})

	if len(res.exit) != 0 || strings.Count(out, "--to string") != 50 {
		t.Fatalf("exit %v, %d entries", res.exit, strings.Count(out, "--to string"))
	}
}

func FuzzHelpText(f *testing.F) {
	for _, seed := range []string{"", "plain", "a\nb", "\x1b[31m", "\u202e", "\x00", "\xff\xfe", "line\r\nline", strings.Repeat("é", 100)} {
		f.Add(seed, seed)
	}

	f.Fuzz(func(t *testing.T, text, field string) {
		out, _ := runHelp(t, []string{"--help"}, func(f *foundation.F) {
			f.Help(text)

			f.Run("api", func(f *foundation.F) {
				f.Help(field)

				f.Load(&helpAPI{})
			})
		})

		assertPrintable(t, out)
	})
}

// bigTag is a pointer to a struct with a flag whose help tag is n bytes long.
func bigTag(n int) any {
	typ := reflect.StructOf([]reflect.StructField{{
		Name: "X",
		Type: reflect.TypeOf(""),
		Tag:  reflect.StructTag(`flag:"x" help:"` + strings.Repeat("h", n) + `"`),
	}})

	return reflect.New(typ).Interface()
}

const canaryValue = "hunter2-LEAK-9f3a"

func assertNoValue(t *testing.T, got string, values ...string) {
	t.Helper()

	for _, v := range values {
		if strings.Contains(got, v) {
			t.Errorf("%q appears in %q", v, got)
		}
	}
}

// assertPrintable fails if s has a control character other than the newline that
// ends a line, or isn't valid text.
func assertPrintable(t *testing.T, s string) {
	t.Helper()

	if !utf8.ValidString(s) {
		t.Errorf("output is not valid UTF-8: %q", s)
	}

	for _, r := range s {
		if r != '\n' && (unicode.IsControl(r) || r == '\u202e' || r == '\u2028' || r == '\u2029') {
			t.Errorf("output holds %U: %q", r, s)

			return
		}
	}
}
