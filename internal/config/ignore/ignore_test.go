package ignore_test

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.krak3n.dev/foundation/internal/config/ignore"
)

func compile(t *testing.T, patterns ...string) *ignore.Matcher {
	t.Helper()

	m, err := ignore.Compile(patterns)
	if err != nil {
		t.Fatalf("Compile(%q): %v", patterns, err)
	}

	return m
}

func TestMatch(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		pattern, key string
		want         bool
	}{
		// Exact names.
		{"SVC_LEGACY_FLAG", "SVC_LEGACY_FLAG", true},
		{"SVC_LEGACY_FLAG", "SVC_LEGACY_FLAG2", false},
		{"SVC_LEGACY_FLAG", "XSVC_LEGACY_FLAG", false},
		{"svc._comment", "svc._comment", true},

		// The whole key must match, not part of it.
		{"_*", "x_y", false},
		{"_*", "_y", true},
		{"*_y", "_y_z", false},

		// * at the start, in the middle and at the end.
		{"SVC_LEGACY_*", "SVC_LEGACY_FLAG", true},
		{"SVC_LEGACY_*", "SVC_LEGACY_", true},
		{"SVC_LEGACY_*", "SVC_LEGACY", false},
		{"*._comment", "svc._comment", true},
		{"*._comment", "svc.http._comment", true},
		{"*._comment", "svc._comments", false},
		{"svc.*.note", "svc.http.note", true},
		{"svc.*.note", "svc.note", false},

		// * matches any run of characters, including none and the dots of a path.
		{"a*b", "ab", true},
		{"a*b", "a.x.y.b", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "aXbYbZc", true},
		{"a*b*c", "aXbYbZ", false},
		{"a**b", "axb", true},

		// Matching is case sensitive: more is reported, never less.
		{"svc._COMMENT", "svc._comment", false},
		{"SVC_*", "svc_x", false},

		// Nothing else is special.
		{"a?b", "axb", false},
		{"a?b", "a?b", true},
		{"[ab]", "a", false},
		{"[ab]", "[ab]", true},
		{`a\*`, "a*", false},
		{`a\*`, `a\x`, true},
		{"a.c", "abc", false},
		{"(x)", "(x)", true},

		// A key can be anything, including text an attacker chose.
		{"SVC_*", "SVC_\n", true},
		{"SVC_*", "SVC_\x1b[2J", true},
		{"SVC_*", "SVC_日本", true},
		{"a*", "", false},
	} {
		t.Run(fmt.Sprintf("%q matches %q", tc.pattern, tc.key), func(t *testing.T) {
			t.Parallel()

			if got := compile(t, tc.pattern).Match(tc.key); got != tc.want {
				t.Fatalf("Match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchesAnyPattern(t *testing.T) {
	t.Parallel()

	m := compile(t, "SVC_LEGACY_*", "*._comment", "SVC_EXACT")

	for key, want := range map[string]bool{
		"SVC_LEGACY_A": true,
		"svc._comment": true,
		"SVC_EXACT":    true,
		"SVC_ADRR":     false,
		"":             false,
	} {
		if got := m.Match(key); got != want {
			t.Errorf("Match(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestNoPatternsMatchNothing(t *testing.T) {
	t.Parallel()

	for _, m := range []*ignore.Matcher{compile(t), compile(t, []string{}...)} {
		if m.Match("SVC_X") || m.Match("") {
			t.Fatal("no patterns should ignore nothing")
		}

		if len(m.Patterns()) != 0 {
			t.Fatalf("Patterns = %v, want none", m.Patterns())
		}
	}

	var nilMatcher *ignore.Matcher

	if nilMatcher.Match("SVC_X") {
		t.Fatal("a nil matcher should ignore nothing")
	}
}

func TestDuplicatePatternsAreFine(t *testing.T) {
	t.Parallel()

	m := compile(t, "SVC_A_*", "SVC_A_*")

	if !m.Match("SVC_A_1") {
		t.Fatal("expected a match")
	}
}

func TestPatternsAreReportedAsGiven(t *testing.T) {
	t.Parallel()

	given := []string{"SVC_LEGACY_*", "*._comment"}
	m := compile(t, given...)

	got := m.Patterns()

	if strings.Join(got, ",") != strings.Join(given, ",") {
		t.Fatalf("Patterns = %v, want %v", got, given)
	}

	// The caller's slice and the returned one are copies.
	got[0] = "changed"
	given[1] = "changed"

	if m.Patterns()[0] != "SVC_LEGACY_*" || m.Patterns()[1] != "*._comment" {
		t.Fatalf("Patterns changed under the matcher: %v", m.Patterns())
	}

	if !m.Match("svc._comment") || m.Match("changed") {
		t.Fatal("the matcher must not follow changes to the slices")
	}
}

// A pattern that would switch the check off, or that can't mean anything, stops
// startup instead of being ignored.
func TestCompileRejectsBadPatterns(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		patterns []string
		want     error
	}{
		"empty":              {[]string{""}, ignore.ErrEmpty},
		"a bare star":        {[]string{"*"}, ignore.ErrTooBroad},
		"only stars":         {[]string{"****"}, ignore.ErrTooBroad},
		"one bad of several": {[]string{"SVC_A_*", "*"}, ignore.ErrTooBroad},
		"newline":            {[]string{"SVC_\nX"}, ignore.ErrCharacters},
		"escape sequence":    {[]string{"SVC_\x1b[2J"}, ignore.ErrCharacters},
		"nul":                {[]string{"SVC_\x00"}, ignore.ErrCharacters},
		"delete":             {[]string{"SVC_\x7f"}, ignore.ErrCharacters},
		"invalid utf-8":      {[]string{"SVC_\xff\xfe"}, ignore.ErrCharacters},
		"too long":           {[]string{strings.Repeat("a", ignore.MaxPatternLength+1)}, ignore.ErrTooLong},
		"too many":           {manyPatterns(ignore.MaxPatterns + 1), ignore.ErrTooMany},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			m, err := ignore.Compile(tc.patterns)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want it to be %v", err, tc.want)
			}

			if m != nil {
				t.Fatal("a rejected set must not give a matcher that ignores part of it")
			}
		})
	}
}

func TestCompileAcceptsTheLimits(t *testing.T) {
	t.Parallel()

	compile(t, strings.Repeat("a", ignore.MaxPatternLength))
	compile(t, manyPatterns(ignore.MaxPatterns)...)
}

// The error names the pattern so it can be fixed, but a pattern is code, not
// data: even so, control characters in it must not reach a log as they are.
func TestCompileErrorsAreSafeToLog(t *testing.T) {
	t.Parallel()

	_, err := ignore.Compile([]string{"SVC_\n[2J\x1b"})
	if err == nil {
		t.Fatal("expected an error")
	}

	if strings.ContainsFunc(err.Error(), func(r rune) bool { return r < ' ' || r == 0x7f }) {
		t.Fatalf("the error contains a control character: %q", err.Error())
	}

	if !strings.Contains(err.Error(), "SVC_") {
		t.Fatalf("the error should name the pattern, got %q", err.Error())
	}

	long := strings.Repeat("a", 10_000)

	_, err = ignore.Compile([]string{long})
	if err == nil || len(err.Error()) > 1_000 {
		t.Fatalf("an over-long pattern must not be echoed in full, error is %d bytes", len(fmt.Sprint(err)))
	}
}

// A hostile key is as long as the environment or document allows and full of the
// characters that make a naive matcher slow. Matching stays quick for every
// pattern allowed.
func TestMatchIsFastOnHostileKeys(t *testing.T) {
	t.Parallel()

	patterns := manyPatterns(ignore.MaxPatterns - 1)
	patterns = append(patterns, strings.Repeat("a*", ignore.MaxPatternLength/2-1)+"b")

	m := compile(t, patterns...)

	key := strings.Repeat("a", 1<<20)

	start := time.Now()

	for range 20 {
		if m.Match(key) {
			t.Fatal("unexpected match")
		}
	}

	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("matching took %v", elapsed)
	}
}

func manyPatterns(n int) []string {
	out := make([]string, n)

	for i := range out {
		out[i] = fmt.Sprintf("SVC_GROUP_%d_*", i)
	}

	return out
}

// reference is the matcher written the obvious way, as a regular expression.
func reference(pattern string) *regexp.Regexp {
	var b strings.Builder

	b.WriteString(`(?s)\A`)

	for i, part := range strings.Split(pattern, "*") {
		if i > 0 {
			b.WriteString(`.*`)
		}

		b.WriteString(regexp.QuoteMeta(part))
	}

	b.WriteString(`\z`)

	return regexp.MustCompile(b.String())
}

// The matcher agrees with the regular expression it stands for, whatever the
// pattern and key. Anything Compile refuses is skipped; it must never panic.
func FuzzMatch(f *testing.F) {
	for _, seed := range [][2]string{
		{"SVC_LEGACY_*", "SVC_LEGACY_FLAG"},
		{"*._comment", "svc.http._comment"},
		{"a*b*c", "aXbYbZc"},
		{"a**b", "ab"},
		{"x*", ""},
		{"日本*", "日本語"},
		{`a\*`, `a\x`},
	} {
		f.Add(seed[0], seed[1])
	}

	f.Fuzz(func(t *testing.T, pattern, key string) {
		m, err := ignore.Compile([]string{pattern})
		if err != nil {
			return
		}

		if !utf8.ValidString(key) {
			m.Match(key) // must not panic

			return
		}

		if got, want := m.Match(key), reference(pattern).MatchString(key); got != want {
			t.Fatalf("Match(%q, %q) = %v, the regular expression says %v", pattern, key, got, want)
		}
	})
}
