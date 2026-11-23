// Package ignore matches the keys that strict configuration should not report as
// unknown, against patterns a developer wrote in code.
//
// A pattern is a key, with "*" standing for any run of characters, including
// none and including the dots and underscores that separate the parts of a key:
// "SVC_LEGACY_*", "*._comment". Nothing else is special, and the whole key must
// match. The language is this small on purpose: a pattern that switches the
// check off, or matches far more than was meant, has to be hard to write by
// accident, so there are no character classes, no escapes and no regular
// expressions, and a pattern that would match everything is refused.
package ignore

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// MaxPatterns is the most patterns there can be.
	MaxPatterns = 64

	// MaxPatternLength is the longest a pattern can be, in bytes.
	MaxPatternLength = 256

	// echoed is how much of a rejected pattern an error repeats.
	echoed = 40
)

// The reasons a set of patterns is refused. Test for them with [errors.Is].
var (
	ErrEmpty      = errors.New("is empty")
	ErrTooBroad   = errors.New("would match every key")
	ErrCharacters = errors.New("has a control character or is not valid UTF-8")
	ErrTooLong    = errors.New("is too long")
	ErrTooMany    = errors.New("too many patterns")
)

// A Matcher reports whether a key matches any of a set of patterns. It is
// immutable, so safe for concurrent use. The zero value, and a nil *Matcher,
// match nothing.
type Matcher struct {
	patterns []string
	parts    [][]string
}

// Compile checks the patterns and prepares them for matching. It returns the
// first problem, and no matcher, so that a set which is partly wrong is never
// partly applied.
func Compile(patterns []string) (*Matcher, error) {
	if len(patterns) > MaxPatterns {
		return nil, fmt.Errorf("ignore: %w: %d, at most %d", ErrTooMany, len(patterns), MaxPatterns)
	}

	m := &Matcher{patterns: append([]string(nil), patterns...)}

	for _, p := range patterns {
		if err := check(p); err != nil {
			return nil, fmt.Errorf("ignore: pattern %s %w", show(p), err)
		}

		m.parts = append(m.parts, strings.Split(p, "*"))
	}

	return m, nil
}

func check(p string) error {
	switch {
	case p == "":
		return ErrEmpty
	case len(p) > MaxPatternLength:
		return ErrTooLong
	case !utf8.ValidString(p) || strings.ContainsFunc(p, isControl):
		return ErrCharacters
	case strings.Trim(p, "*") == "":
		return ErrTooBroad
	}

	return nil
}

func isControl(r rune) bool {
	return r < ' ' || r == 0x7f
}

// show quotes a pattern for an error: escaped, so that it can't carry control
// characters into a log, and cut short, so that it can't fill one.
func show(p string) string {
	if len(p) > echoed {
		p = p[:echoed]

		for !utf8.ValidString(p) {
			p = p[:len(p)-1]
		}

		return strconv.QuoteToASCII(p + "...")
	}

	return strconv.QuoteToASCII(p)
}

// Patterns returns the patterns, as they were given.
func (m *Matcher) Patterns() []string {
	if m == nil {
		return nil
	}

	return append([]string(nil), m.patterns...)
}

// Match reports whether key matches any pattern.
func (m *Matcher) Match(key string) bool {
	if m == nil {
		return false
	}

	for _, parts := range m.parts {
		if matches(parts, key) {
			return true
		}
	}

	return false
}

// matches reports whether key is made of parts, the pieces of a pattern between
// its stars, in order: the first at the start, the last at the end, and the ones
// between somewhere in the middle. Each is taken at its first occurrence, which
// is always the best choice because it leaves the most of the key for the rest,
// so no backtracking is needed and the work is linear in the key.
func matches(parts []string, key string) bool {
	if len(parts) == 1 {
		return key == parts[0]
	}

	first, last := parts[0], parts[len(parts)-1]

	if len(key) < len(first)+len(last) || !strings.HasPrefix(key, first) || !strings.HasSuffix(key, last) {
		return false
	}

	rest := key[len(first) : len(key)-len(last)]

	for _, part := range parts[1 : len(parts)-1] {
		if part == "" {
			continue
		}

		i := strings.Index(rest, part)
		if i < 0 {
			return false
		}

		rest = rest[i+len(part):]
	}

	return true
}
