package foundation_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/foundationtest"
)

// The text of a failure can carry what an outsider chose: a panic with a job's
// name in it, an error that quotes the input it could not decode. It is escaped
// before it is logged, whatever the log handler does with it, and cut short; the
// stack of a panic, which is the runtime's and has no such text in it, is kept as
// it is.

const maxFailText = 8 << 10 // the longest the text of a failure is logged, in bytes

// hostileText is a message with what an attacker would put in one, and a canary.
var hostileText = map[string]struct {
	text string
	// shows is how it must read once escaped.
	shows []string
}{
	"a newline that forges a line": {"first\nlevel=ERROR msg=forged canary", []string{`first\nlevel=ERROR msg=forged canary`}},
	"an escape sequence":           {"\x1b[2J\x1b[H canary", []string{`\x1b[2J\x1b[H canary`}},
	"a bidi override":              {"admin \u202eelif canary", []string{`\u202e`, "canary"}},
	"a line separator":             {"a\u2028b canary", []string{`a\u2028b canary`}},
	"a NUL":                        {"a\x00b canary", []string{`a\x00b canary`}},
	"a carriage return":            {"a\rb canary", []string{`a\rb canary`}},
	"invalid UTF-8":                {"a\xff\xfeb canary", []string{"a\ufffdb canary"}},
}

// loggedFailure runs fn as the root function and returns what the failure said.
func loggedFailure(t *testing.T, msg string, fn foundation.Func, opts ...foundation.Option) logged {
	t.Helper()

	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("svc", fn, opts...)

	assertExitCodes(t, exit, 1)

	got := entriesNamed(recorder, msg)
	if len(got) != 1 {
		t.Fatalf("%d %q lines were logged, want 1: %v", len(got), msg, loggedEntries(recorder))
	}

	return got[0]
}

// shown is s cut short enough to put in a failure message.
func shown(s string) string {
	if len(s) > 80 {
		return fmt.Sprintf("%q... (%d bytes)", s[:80], len(s))
	}

	return fmt.Sprintf("%q", s)
}

// cleanText checks that nothing in a logged value could mislead a reader. It says
// so once, as a huge value would otherwise be reported for every character.
func cleanText(t *testing.T, what, s string) {
	t.Helper()

	if !utf8.ValidString(s) {
		t.Errorf("%s %s is not valid UTF-8", what, shown(s))
	}

	for _, r := range s {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			t.Errorf("%s %s has %U in it", what, shown(s), r)

			return
		}
	}
}

func TestTheTextOfAFailureIsEscapedBeforeItIsLogged(t *testing.T) {
	for name, tt := range hostileText {
		t.Run(name, func(t *testing.T) {
			l := loggedFailure(t, "shutting down due to failure", func(f *foundation.F) {
				f.Fail(errors.New(tt.text))
			})

			cleanText(t, "err", l.attrs["err"])

			for _, want := range tt.shows {
				if !strings.Contains(l.attrs["err"], want) {
					t.Errorf("err = %s does not read %q", shown(l.attrs["err"]), want)
				}
			}
		})
	}
}

// A failure while the program is already shutting down says so in a line of its
// own, and its text is escaped as well.
func TestTheTextOfAFailureDuringShutdownIsEscaped(t *testing.T) {
	for name, tt := range hostileText {
		t.Run(name, func(t *testing.T) {
			l := loggedFailure(t, "failure during shutdown", func(f *foundation.F) {
				f.Run("late", func(late *foundation.F) {
					late.Parallel()

					<-late.Context().Done()

					late.Fail(errors.New(tt.text))
				})

				f.Fail(errors.New("first"))
			})

			cleanText(t, "err", l.attrs["err"])

			for _, want := range tt.shows {
				if !strings.Contains(l.attrs["err"], want) {
					t.Errorf("err = %s does not read %q", shown(l.attrs["err"]), want)
				}
			}
		})
	}
}

type panicValue struct{ text string }

func (p panicValue) String() string { return p.text }

func (p panicValue) Error() string { return p.text }

func TestTheTextOfAPanicIsEscapedAndItsStackIsKept(t *testing.T) {
	for name, tt := range hostileText {
		for kind, value := range map[string]any{
			"a string":        tt.text,
			"an error":        errors.New(tt.text),
			"a stringer":      panicValue{tt.text},
			"a wrapped error": fmt.Errorf("job failed: %w", errors.New(tt.text)),
		} {
			t.Run(name+" as "+kind, func(t *testing.T) {
				l := loggedFailure(t, "shutting down due to failure", func(f *foundation.F) {
					panic(value)
				})

				cleanText(t, "err", l.attrs["err"])

				if !strings.HasPrefix(l.attrs["err"], "panic: ") {
					t.Errorf("err = %q does not say it was a panic", l.attrs["err"])
				}

				for _, want := range tt.shows {
					if !strings.Contains(l.attrs["err"], want) {
						t.Errorf("err = %s does not read %q", shown(l.attrs["err"]), want)
					}
				}

				// The stack is the runtime's: it has lines and tabs, and is not touched.
				stack := l.attrs["stack"]

				if !strings.Contains(stack, "goroutine ") || !strings.Contains(stack, "\n\t") {
					t.Errorf("the stack was lost or flattened: %q", stack)
				}
			})
		}
	}
}

func TestTheTextOfOrdinaryFailuresIsLoggedAsItIs(t *testing.T) {
	for _, text := range []string{
		"boom",
		"héllo 日本語 🙂",
		"two  spaces and 'quotes' and \"double\"",
		"key=value; other: thing [x] {y}",
		"/path/to/file.json: no such file",
	} {
		t.Run(text, func(t *testing.T) {
			l := loggedFailure(t, "shutting down due to failure", func(f *foundation.F) {
				f.Fail(errors.New(text))
			})

			if l.attrs["err"] != text {
				t.Errorf("err = %q, want %q", l.attrs["err"], text)
			}
		})
	}
}

func TestAHugeFailureIsCutShort(t *testing.T) {
	tests := map[string]string{
		"a megabyte of text":                    strings.Repeat("a", 1<<20),
		"multibyte text cut inside a character": strings.Repeat("日", 5000),
		"a megabyte of control characters":      strings.Repeat("\x1b", 1<<20),
	}

	for name, text := range tests {
		t.Run(name, func(t *testing.T) {
			l := loggedFailure(t, "shutting down due to failure", func(f *foundation.F) {
				f.Fail(errors.New(text))
			})

			got := l.attrs["err"]

			cleanText(t, "err", got)

			if !strings.HasSuffix(got, "...(cut)") {
				t.Errorf("err does not say it was cut: ...%q", got[max(0, len(got)-40):])
			}

			// Cut before it is escaped, so the most it can be is ten bytes a byte.
			if limit := 10*maxFailText + len("...(cut)"); len(got) > limit {
				t.Errorf("err is %d bytes, more than %d", len(got), limit)
			}
		})
	}

	t.Run("plain text is cut at the limit", func(t *testing.T) {
		l := loggedFailure(t, "shutting down due to failure", func(f *foundation.F) {
			f.Fail(errors.New(strings.Repeat("a", 1<<20)))
		})

		if want := strings.Repeat("a", maxFailText) + "...(cut)"; l.attrs["err"] != want {
			t.Errorf("err is %d bytes, want the first %d and the mark", len(l.attrs["err"]), maxFailText)
		}
	})

	t.Run("a message that fits is not marked", func(t *testing.T) {
		text := strings.Repeat("a", maxFailText)

		l := loggedFailure(t, "shutting down due to failure", func(f *foundation.F) {
			f.Fail(errors.New(text))
		})

		if l.attrs["err"] != text {
			t.Errorf("a message of exactly the limit was changed: %d bytes", len(l.attrs["err"]))
		}
	})
}

// Every startup mistake is said at once (see the options tests), on one line, so
// escaping the text of the failure leaves them readable.
func TestStartupMistakesAreSaidOnOneLine(t *testing.T) {
	res := runStartup(t,
		foundation.WithDocumentConfig(),
		foundation.WithStrictIgnore(""),
	)

	refusesStartup(t, res, "invalid configuration options", "invalid WithStrictIgnore")

	if strings.ContainsRune(res.failure, '\n') || strings.Contains(res.failure, `\n`) {
		t.Errorf("the failure %q runs over more than one line", res.failure)
	}

	if !strings.Contains(res.failure, "; ") {
		t.Errorf("the failure %q does not separate the mistakes", res.failure)
	}
}

// The name of a function can be data (a worker named after an id from
// configuration), so it is escaped in the fn attribute of what it logs. The name
// itself, and the path, are the program's own and are left as they are.
func TestTheNameOfAFunctionIsEscapedWhereItIsLogged(t *testing.T) {
	const name = "worker\nlevel=ERROR forged\x1b[2J\u202e canary"

	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	var (
		path   []string
		joined string
	)

	foundation.Run("svc", func(f *foundation.F) {
		f.Run(name, func(c *foundation.F) {
			path, joined = c.Path(), c.String()

			c.Log().Info("hello")
		})
	})

	assertExitCodes(t, exit)

	if len(path) != 2 || path[1] != name || joined != "svc/"+name {
		t.Errorf("the name was changed: path %q, string %q", path, joined)
	}

	hello := entriesNamed(recorder, "hello")
	if len(hello) != 1 {
		t.Fatalf("%d hello lines were logged", len(hello))
	}

	fn := hello[0].attrs["fn"]

	cleanText(t, "fn", fn)

	for _, want := range []string{`worker\nlevel=ERROR forged`, `\x1b[2J`, `\u202e canary`} {
		if !strings.Contains(fn, want) {
			t.Errorf("fn = %q does not read %q", fn, want)
		}
	}
}
