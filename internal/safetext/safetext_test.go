package safetext_test

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"go.krak3n.dev/foundation/internal/safetext"
)

func TestEscapeLeavesOrdinaryTextAlone(t *testing.T) {
	for _, s := range []string{
		"",
		"svc.http.server",
		"SVC_CAFÉ_日本",
		"a path/with spaces and-dashes_1.json",
		"emoji 🙂 and punctuation: ,;()[]{}",
	} {
		if got := safetext.Escape(s, false); got != s {
			t.Errorf("Escape(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestEscapeShowsWhatMisleadsAReader(t *testing.T) {
	tests := map[string]struct {
		in   string
		want string
	}{
		"a newline":                {"a\nb", `a\nb`},
		"a carriage return":        {"a\r\nb", `a\r\nb`},
		"a tab":                    {"a\tb", `a\tb`},
		"an escape sequence":       {"\x1b[31mred", `\x1b[31mred`},
		"a NUL":                    {"a\x00b", `a\x00b`},
		"DEL":                      {"a\x7fb", `a\x7fb`},
		"a C1 control":             {"a\u009bb", `a\u009bb`},
		"a right-to-left override": {"a\u202eb", `a\u202eb`},
		"a bidi isolate":           {"a\u2066b", `a\u2066b`},
		"a zero width space":       {"a\u200bb", `a\u200bb`},
		"a byte order mark":        {"a\ufeffb", `a\ufeffb`},
		"a line separator":         {"a\u2028b", `a\u2028b`},
		"a paragraph separator":    {"a\u2029b", `a\u2029b`},
		"a no-break space":         {"a\u00a0b", `a\u00a0b`},
		"invalid UTF-8":            {"a\xffb", "a\ufffdb"},
		"a lone surrogate":         {"a\xed\xa0\x80b", "a\ufffdb"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := safetext.Escape(tt.in, false); got != tt.want {
				t.Fatalf("Escape(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// A help text has lines, and nothing else may start one.
func TestEscapeKeepsNewlinesOnlyWhenAsked(t *testing.T) {
	in := "first\nsecond\u2028third\r\nfourth"

	if got, want := safetext.Escape(in, true), "first\nsecond\\u2028third\\r\nfourth"; got != want {
		t.Errorf("keeping newlines: Escape = %q, want %q", got, want)
	}

	if got := safetext.Escape(in, false); strings.Contains(got, "\n") {
		t.Errorf("not keeping newlines: Escape = %q, which has one", got)
	}
}

// What is escaped is escaped once: the result is plain text that needs nothing
// more, and escaping it again must not change it, or a log line would grow every
// time it passed through.
func TestEscapeIsIdempotent(t *testing.T) {
	in := "a\nb\u202ec\xffd\x1b[0m"

	once := safetext.Escape(in, false)

	if twice := safetext.Escape(once, false); twice != once {
		t.Fatalf("Escape twice = %q, once = %q", twice, once)
	}
}

// A hostile string can't make the result grow without bound: every character is
// at most ten bytes (\U0010ffff is ten).
func TestEscapeGrowthIsBounded(t *testing.T) {
	in := strings.Repeat("\x00\u202e\xff", 1000)

	if got := safetext.Escape(in, false); len(got) > 10*utf8.RuneCountInString(strings.ToValidUTF8(in, "\ufffd")) {
		t.Fatalf("Escape grew %d bytes into %d", len(in), len(got))
	}
}

func FuzzEscape(f *testing.F) {
	for _, s := range []string{"", "a", "a\nb", "\x1b[2J", "\u202e", "\u2028", "\xff", "\xed\xa0\x80", "日本", "\u0085", "a\x00b"} {
		f.Add(s, true)
		f.Add(s, false)
	}

	f.Fuzz(func(t *testing.T, s string, keepNewlines bool) {
		got := safetext.Escape(s, keepNewlines)

		if !utf8.ValidString(got) {
			t.Fatalf("Escape(%q) = %q is not valid UTF-8", s, got)
		}

		for _, r := range got {
			switch {
			case r == '\n' && keepNewlines:
			case r == ' ':
			case !unicode.IsGraphic(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Zs):
				t.Fatalf("Escape(%q) = %q still has %U", s, got, r)
			}
		}

		if again := safetext.Escape(got, keepNewlines); again != got {
			t.Fatalf("Escape(%q) = %q, but escaping that again gives %q", s, got, again)
		}

		if limit := 10*(utf8.RuneCountInString(s)+len(s)) + 10; len(got) > limit {
			t.Fatalf("Escape(%q) grew to %d bytes", s, len(got))
		}
	})
}

const cutMark = "...(cut)"

func TestClipKeepsWhatFitsAndEscapesIt(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"", ""},
		{"boom", "boom"},
		{"héllo 日本語", "héllo 日本語"},
		{"a\nb\x1b[2J", `a\nb\x1b[2J`},
		{strings.Repeat("a", 64), strings.Repeat("a", 64)}, // exactly the limit
	} {
		if got := safetext.Clip(tt.in, 64); got != tt.want {
			t.Errorf("Clip(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestClipCutsWhatIsTooLongAndSaysSo(t *testing.T) {
	got := safetext.Clip(strings.Repeat("a", 65), 64)

	if want := strings.Repeat("a", 64) + cutMark; got != want {
		t.Errorf("Clip = %q, want %q", got, want)
	}
}

// The cut is made before anything is escaped, at a character boundary, so a huge
// input costs no more than a small one and nothing is left half a character.
func TestClipCutsAtACharacterBoundary(t *testing.T) {
	// Each is three bytes, so a limit of 64 falls inside the 22nd.
	got := safetext.Clip(strings.Repeat("日", 100), 64)

	if want := strings.Repeat("日", 21) + cutMark; got != want {
		t.Errorf("Clip = %q, want %q", got, want)
	}

	if !utf8.ValidString(got) {
		t.Errorf("Clip = %q is not valid UTF-8", got)
	}
}

func TestClipEscapesWhatItKeepsOfAHostileInput(t *testing.T) {
	in := "start\nlevel=ERROR forged\x1b[2J\u202e" + strings.Repeat("x", 1000)

	got := safetext.Clip(in, 64)

	if got != safetext.Escape(got, false) {
		t.Errorf("Clip(%q) = %q still has what Escape would change", in, got)
	}

	for _, want := range []string{`\n`, `\x1b`, `\u202e`} {
		if !strings.Contains(got, want) {
			t.Errorf("Clip = %q does not show %s", got, want)
		}
	}
}

func TestClipOfALimitThatLeavesNothing(t *testing.T) {
	for _, limit := range []int{0, -1, -1 << 40} {
		if got := safetext.Clip("abc", limit); got != cutMark {
			t.Errorf("Clip(abc, %d) = %q, want just the mark", limit, got)
		}

		if got := safetext.Clip("", limit); got != "" {
			t.Errorf("Clip(empty, %d) = %q, want empty", limit, got)
		}
	}
}

func FuzzClip(f *testing.F) {
	for _, s := range []string{"", "a", "a\nb", "\x1b[2J", "\u202e", "\u2028", "\xff", "\xed\xa0\x80", "日本語日本語", "a\x00b"} {
		f.Add(s, 0)
		f.Add(s, 3)
		f.Add(s, 64)
	}

	f.Fuzz(func(t *testing.T, s string, limit int) {
		got := safetext.Clip(s, limit)

		if !utf8.ValidString(got) {
			t.Fatalf("Clip(%q, %d) = %q is not valid UTF-8", s, limit, got)
		}

		if again := safetext.Escape(got, false); again != got {
			t.Fatalf("Clip(%q, %d) = %q is not escaped: Escape gives %q", s, limit, got, again)
		}

		for _, r := range got {
			if r == '\n' || unicode.IsControl(r) {
				t.Fatalf("Clip(%q, %d) = %q still has %U", s, limit, got, r)
			}
		}

		// Cut before escaping, so the result is bounded by the limit and not by the
		// input: at most ten bytes for each of limit bytes, and the mark.
		if limit < 0 {
			limit = 0
		}

		if bound := 10*limit + len(cutMark); len(got) > bound {
			t.Fatalf("Clip(%q, %d) is %d bytes, more than %d", s, limit, len(got), bound)
		}
	})
}
